//go:build integration

package integration_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/vsayfb/gig-platform-chat-service/internal/application"
	"github.com/vsayfb/gig-platform-chat-service/internal/attachment"
	"github.com/vsayfb/gig-platform-chat-service/internal/conversation"
)

func TestDirectMessageRetryPersistsAndDeliversExactlyOnce(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{})

	senderID := uuid.NewString()
	receiverID := uuid.NewString()
	direct := env.seedDirectConversation(t, senderID, receiverID)

	receiverConn := connectDirect(
		t, env,
		env.mustIssueTicket(t, signedAccessToken(t, receiverID, "receiver")),
		direct.ID.Hex(),
	)
	t.Cleanup(func() { _ = receiverConn.Close() })
	waitConnected(t, env.hub, receiverID, true)

	senderConn := connectDirect(
		t, env,
		env.mustIssueTicket(t, signedAccessToken(t, senderID, "sender")),
		direct.ID.Hex(),
	)
	waitConnected(t, env.hub, senderID, true)

	const clientMessageID = "integration-message-1"
	const content = "persist me once"
	if err := senderConn.WriteJSON(map[string]any{
		"client_message_id": clientMessageID,
		"content":           content,
	}); err != nil {
		t.Fatalf("send direct message: %v", err)
	}

	firstAck := readAccepted(t, senderConn)
	if firstAck.ClientMessageID != clientMessageID || firstAck.ConversationID != direct.ID.Hex() {
		t.Fatalf("unexpected first acknowledgement: %+v", firstAck)
	}

	delivered := readDirectDelivery(t, receiverConn)
	if delivered.Type != "message" || delivered.SenderID != senderID ||
		delivered.ConversationID != direct.ID.Hex() || delivered.Content != content {
		t.Fatalf("unexpected delivered message: %+v", delivered)
	}

	assertCollectionCount(t, env.db.Collection(conversation.MessagesCollection), bson.M{
		"sender_id": senderID, "client_message_id": clientMessageID,
	}, 1)

	_ = senderConn.Close()
	waitConnected(t, env.hub, senderID, false)

	retryConn := connectDirect(
		t, env,
		env.mustIssueTicket(t, signedAccessToken(t, senderID, "sender")),
		direct.ID.Hex(),
	)
	t.Cleanup(func() { _ = retryConn.Close() })

	if err := retryConn.WriteJSON(map[string]any{
		"client_message_id": clientMessageID,
		"content":           content,
	}); err != nil {
		t.Fatalf("retry direct message: %v", err)
	}

	retryAck := readAccepted(t, retryConn)
	if retryAck.MessageID != firstAck.MessageID || retryAck.ConversationID != firstAck.ConversationID {
		t.Fatalf("retry changed persisted identity: first=%+v retry=%+v", firstAck, retryAck)
	}
	assertCollectionCount(t, env.db.Collection(conversation.MessagesCollection), bson.M{
		"sender_id": senderID, "client_message_id": clientMessageID,
	}, 1)
	assertNoWebSocketMessage(t, receiverConn, 300*time.Millisecond)
}

func TestConcurrentDuplicateDirectSendPersistsAndDeliversOnce(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{})

	senderID := uuid.NewString()
	receiverID := uuid.NewString()
	direct := env.seedDirectConversation(t, senderID, receiverID)

	receiverConn := connectDirect(t, env, env.mustIssueTicket(t, signedAccessToken(t, receiverID, "receiver")), direct.ID.Hex())
	t.Cleanup(func() { _ = receiverConn.Close() })

	sender1 := connectDirect(t, env, env.mustIssueTicket(t, signedAccessToken(t, senderID, "sender")), direct.ID.Hex())
	sender2 := connectDirect(t, env, env.mustIssueTicket(t, signedAccessToken(t, senderID, "sender")), direct.ID.Hex())
	t.Cleanup(func() { _ = sender1.Close() })
	t.Cleanup(func() { _ = sender2.Close() })

	const clientMessageID = "concurrent-same-message"
	payload := map[string]any{"client_message_id": clientMessageID, "content": "only once"}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, conn := range []*websocket.Conn{sender1, sender2} {
		wg.Add(1)
		go func(c *websocket.Conn) {
			defer wg.Done()
			<-start
			errs <- c.WriteJSON(payload)
		}(conn)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent send failed: %v", err)
		}
	}

	ack1 := readAccepted(t, sender1)
	ack2 := readAccepted(t, sender2)
	if ack1.MessageID != ack2.MessageID {
		t.Fatalf("concurrent duplicate sends produced different messages: %s != %s", ack1.MessageID, ack2.MessageID)
	}

	_ = readDirectDelivery(t, receiverConn)
	assertNoWebSocketMessage(t, receiverConn, 300*time.Millisecond)
	assertCollectionCount(t, env.db.Collection(conversation.MessagesCollection), bson.M{
		"sender_id": senderID, "client_message_id": clientMessageID,
	}, 1)
}

func TestDirectMessageRouteIsolationAcrossOpenChats(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{})

	alice := uuid.NewString()
	bob := uuid.NewString()
	carol := uuid.NewString()
	ab := env.seedDirectConversation(t, alice, bob)
	ac := env.seedDirectConversation(t, alice, carol)

	aliceBob := connectDirect(t, env, env.mustIssueTicket(t, signedAccessToken(t, alice, "alice")), ab.ID.Hex())
	aliceCarol := connectDirect(t, env, env.mustIssueTicket(t, signedAccessToken(t, alice, "alice")), ac.ID.Hex())
	bobConn := connectDirect(t, env, env.mustIssueTicket(t, signedAccessToken(t, bob, "bob")), ab.ID.Hex())
	t.Cleanup(func() { _ = aliceBob.Close() })
	t.Cleanup(func() { _ = aliceCarol.Close() })
	t.Cleanup(func() { _ = bobConn.Close() })

	if err := bobConn.WriteJSON(map[string]any{
		"client_message_id": "route-isolation",
		"content":           "for alice's bob chat only",
	}); err != nil {
		t.Fatal(err)
	}
	_ = readAccepted(t, bobConn)

	delivered := readDirectDelivery(t, aliceBob)
	if delivered.SenderID != bob || delivered.ConversationID != ab.ID.Hex() {
		t.Fatalf("message delivered to wrong route: %+v", delivered)
	}
	assertNoWebSocketMessage(t, aliceCarol, 300*time.Millisecond)
}

func TestApplicationFirstMessageRetryCreatesOneThreadAndOneMessage(t *testing.T) {
	core := &coreBehavior{AllowDirectMessages: true}
	env := newTestEnv(t, testEnvOptions{core: core})

	initiator := uuid.NewString()
	target := uuid.NewString()
	gigID := uuid.NewString()

	targetConn := connectApplicationParticipants(
		t, env,
		env.mustIssueTicket(t, signedAccessToken(t, target, "target")),
		initiator,
		gigID,
	)
	t.Cleanup(func() { _ = targetConn.Close() })

	senderConn := connectApplicationParticipants(
		t, env,
		env.mustIssueTicket(t, signedAccessToken(t, initiator, "initiator")),
		target,
		gigID,
	)

	const clientMessageID = "application-first-message"
	if err := senderConn.WriteJSON(map[string]any{
		"client_message_id": clientMessageID,
		"content":           "I would like to apply",
	}); err != nil {
		t.Fatal(err)
	}
	firstAck := readAccepted(t, senderConn)
	if firstAck.ThreadID == "" {
		t.Fatalf("first application ack had no thread ID: %+v", firstAck)
	}

	delivered := readApplicationDelivery(t, targetConn)
	if delivered.ThreadID != firstAck.ThreadID || delivered.SenderID != initiator {
		t.Fatalf("unexpected application delivery: %+v", delivered)
	}

	assertCollectionCount(t, env.db.Collection(application.ApplicationsCollection), bson.M{
		"_id": mustObjectID(t, firstAck.ThreadID),
	}, 1)
	assertCollectionCount(t, env.db.Collection(application.MessagesCollection), bson.M{
		"sender_id": initiator, "client_message_id": clientMessageID,
	}, 1)

	_ = senderConn.Close()
	retryConn := connectApplicationThread(
		t, env,
		env.mustIssueTicket(t, signedAccessToken(t, initiator, "initiator")),
		firstAck.ThreadID,
	)
	t.Cleanup(func() { _ = retryConn.Close() })

	if err := retryConn.WriteJSON(map[string]any{
		"client_message_id": clientMessageID,
		"content":           "I would like to apply",
	}); err != nil {
		t.Fatal(err)
	}
	retryAck := readAccepted(t, retryConn)
	if retryAck.MessageID != firstAck.MessageID || retryAck.ThreadID != firstAck.ThreadID {
		t.Fatalf("application retry changed persisted identity: first=%+v retry=%+v", firstAck, retryAck)
	}
	assertCollectionCount(t, env.db.Collection(application.ApplicationsCollection), bson.M{}, 1)
	assertCollectionCount(t, env.db.Collection(application.MessagesCollection), bson.M{}, 1)
	assertNoWebSocketMessage(t, targetConn, 300*time.Millisecond)
}

func TestAttachmentCommitIsTransactionalAndRetrySafe(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{})

	sender := uuid.NewString()
	receiver := uuid.NewString()
	direct := env.seedDirectConversation(t, sender, receiver)

	expires := time.Now().UTC().Add(time.Hour)
	item := &attachment.Attachment{
		Scope:        attachment.ScopeDirect,
		ScopeID:      direct.ID,
		UploaderID:   sender,
		ObjectKey:    "integration/object",
		OriginalName: "photo.png",
		ContentType:  "image/png",
		SizeBytes:    123,
		Status:       attachment.StatusReady,
		ExpiresAt:    &expires,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	if err := env.attachRepo.Create(context.Background(), item); err != nil {
		t.Fatalf("seed ready attachment: %v", err)
	}

	receiverConn := connectDirect(t, env, env.mustIssueTicket(t, signedAccessToken(t, receiver, "receiver")), direct.ID.Hex())
	senderConn := connectDirect(t, env, env.mustIssueTicket(t, signedAccessToken(t, sender, "sender")), direct.ID.Hex())
	t.Cleanup(func() { _ = receiverConn.Close() })
	t.Cleanup(func() { _ = senderConn.Close() })

	const clientMessageID = "attachment-message"
	payload := map[string]any{
		"client_message_id": clientMessageID,
		"content":           "attachment",
		"attachment_ids":    []string{item.ID.Hex()},
	}
	if err := senderConn.WriteJSON(payload); err != nil {
		t.Fatal(err)
	}
	ack := readAccepted(t, senderConn)

	delivered := readDirectDelivery(t, receiverConn)
	if len(delivered.Attachments) != 1 || delivered.Attachments[0].ID != item.ID {
		t.Fatalf("attachment snapshot missing from delivery: %+v", delivered.Attachments)
	}

	persistedAttachment, err := env.attachRepo.FindByID(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persistedAttachment == nil || persistedAttachment.Status != attachment.StatusAttached ||
		persistedAttachment.MessageID == nil || persistedAttachment.MessageID.Hex() != ack.MessageID {
		t.Fatalf("attachment was not atomically attached to message: %+v", persistedAttachment)
	}

	_ = senderConn.Close()
	retryConn := connectDirect(t, env, env.mustIssueTicket(t, signedAccessToken(t, sender, "sender")), direct.ID.Hex())
	t.Cleanup(func() { _ = retryConn.Close() })
	if err := retryConn.WriteJSON(payload); err != nil {
		t.Fatal(err)
	}
	retryAck := readAccepted(t, retryConn)
	if retryAck.MessageID != ack.MessageID {
		t.Fatalf("attachment retry created a new message: first=%s retry=%s", ack.MessageID, retryAck.MessageID)
	}
	assertCollectionCount(t, env.db.Collection(conversation.MessagesCollection), bson.M{
		"sender_id": sender, "client_message_id": clientMessageID,
	}, 1)
}

func TestFirstDirectMessageMayAtomicallyCreateConversationWithAttachment(t *testing.T) {
	core := &coreBehavior{AllowDirectMessages: true}
	env := newTestEnv(t, testEnvOptions{core: core})

	sender := uuid.NewString()
	target := uuid.NewString()
	expiresAt := time.Now().UTC().Add(time.Hour)
	item := &attachment.Attachment{
		Scope: attachment.ScopeDirect, UploaderID: sender,
		Provisional: true, TargetID: target,
		ObjectKey: "chat/pending/direct/test/image.png", OriginalName: "image.png",
		ContentType: "image/png", SizeBytes: 12, Status: attachment.StatusReady,
		ExpiresAt: &expiresAt, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := env.attachRepo.Create(context.Background(), item); err != nil {
		t.Fatalf("seed provisional direct attachment: %v", err)
	}

	conn, resp, err := dialWebSocket(t, env.server.URL, "/ws/direct", url.Values{
		"ticket":   []string{env.mustIssueTicket(t, signedAccessToken(t, sender, "sender"))},
		"targetID": []string{target},
	}, allowedWebOrigin)
	if err != nil {
		t.Fatalf("connect direct first-contact WebSocket: %s", describeDialError(err, resp))
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{
		"client_message_id": "direct-first-attachment",
		"attachment_ids":    []string{item.ID.Hex()},
	}); err != nil {
		t.Fatal(err)
	}
	ack := readAccepted(t, conn)
	if ack.ConversationID == "" {
		t.Fatalf("first attachment acknowledgement had no conversation ID: %+v", ack)
	}

	persisted, err := env.attachRepo.FindByID(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted == nil || persisted.Status != attachment.StatusAttached ||
		persisted.ScopeID.Hex() != ack.ConversationID || persisted.Provisional ||
		persisted.TargetID != "" || persisted.MessageID == nil ||
		persisted.MessageID.Hex() != ack.MessageID {
		t.Fatalf("provisional direct attachment was not atomically bound: %+v", persisted)
	}
	assertCollectionCount(t, env.db.Collection(conversation.MessagesCollection), bson.M{
		"sender_id": sender, "client_message_id": "direct-first-attachment",
	}, 1)
}

func TestFirstApplicationMessageMayAtomicallyCreateThreadWithAttachment(t *testing.T) {
	core := &coreBehavior{}
	env := newTestEnv(t, testEnvOptions{core: core})

	initiator := uuid.NewString()
	target := uuid.NewString()
	gigID := uuid.NewString()
	expiresAt := time.Now().UTC().Add(time.Hour)
	item := &attachment.Attachment{
		Scope: attachment.ScopeApplication, UploaderID: initiator,
		Provisional: true, TargetID: target, GigID: gigID,
		ObjectKey: "chat/pending/application/test/image.png", OriginalName: "image.png",
		ContentType: "image/png", SizeBytes: 12, Status: attachment.StatusReady,
		ExpiresAt: &expiresAt, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := env.attachRepo.Create(context.Background(), item); err != nil {
		t.Fatalf("seed provisional application attachment: %v", err)
	}

	conn := connectApplicationParticipants(
		t, env,
		env.mustIssueTicket(t, signedAccessToken(t, initiator, "initiator")),
		target, gigID,
	)
	defer conn.Close()
	if err := conn.WriteJSON(map[string]any{
		"client_message_id": "application-first-attachment",
		"attachment_ids":    []string{item.ID.Hex()},
	}); err != nil {
		t.Fatal(err)
	}
	ack := readAccepted(t, conn)
	if ack.ThreadID == "" {
		t.Fatalf("first attachment acknowledgement had no thread ID: %+v", ack)
	}

	persisted, err := env.attachRepo.FindByID(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted == nil || persisted.Status != attachment.StatusAttached ||
		persisted.ScopeID.Hex() != ack.ThreadID || persisted.Provisional ||
		persisted.TargetID != "" || persisted.GigID != "" ||
		persisted.MessageID == nil || persisted.MessageID.Hex() != ack.MessageID {
		t.Fatalf("provisional application attachment was not atomically bound: %+v", persisted)
	}
	assertCollectionCount(t, env.db.Collection(application.MessagesCollection), bson.M{
		"sender_id": initiator, "client_message_id": "application-first-attachment",
	}, 1)
}

func TestUnavailableAttachmentDoesNotPersistMessage(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{})
	sender := uuid.NewString()
	receiver := uuid.NewString()
	direct := env.seedDirectConversation(t, sender, receiver)

	expired := time.Now().UTC().Add(-time.Minute)
	item := &attachment.Attachment{
		Scope: attachment.ScopeDirect, ScopeID: direct.ID, UploaderID: sender,
		ObjectKey: "integration/expired", OriginalName: "expired.png",
		ContentType: "image/png", SizeBytes: 12, Status: attachment.StatusReady,
		ExpiresAt: &expired, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := env.attachRepo.Create(context.Background(), item); err != nil {
		t.Fatal(err)
	}

	senderConn := connectDirect(t, env, env.mustIssueTicket(t, signedAccessToken(t, sender, "sender")), direct.ID.Hex())
	t.Cleanup(func() { _ = senderConn.Close() })
	if err := senderConn.WriteJSON(map[string]any{
		"client_message_id": "expired-attachment",
		"content":           "should fail",
		"attachment_ids":    []string{item.ID.Hex()},
	}); err != nil {
		t.Fatal(err)
	}
	wsErr := readWSError(t, senderConn)
	if wsErr.Code != "attachment_unavailable" {
		t.Fatalf("error code = %q, want attachment_unavailable", wsErr.Code)
	}
	assertCollectionCount(t, env.db.Collection(conversation.MessagesCollection), bson.M{
		"sender_id": sender, "client_message_id": "expired-attachment",
	}, 0)
}

func TestMessageValidationAndFrameLimitPreventPersistence(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{})
	sender := uuid.NewString()
	receiver := uuid.NewString()
	direct := env.seedDirectConversation(t, sender, receiver)

	conn := connectDirect(t, env, env.mustIssueTicket(t, signedAccessToken(t, sender, "sender")), direct.ID.Hex())
	t.Cleanup(func() { _ = conn.Close() })

	if err := conn.WriteJSON(map[string]any{"content": "missing id"}); err != nil {
		t.Fatal(err)
	}
	if got := readWSError(t, conn); got.Code != "invalid_client_message_id" {
		t.Fatalf("missing id error = %q", got.Code)
	}

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"client_message_id":`)); err != nil {
		t.Fatal(err)
	}
	if got := readWSError(t, conn); got.Code != "invalid_message" {
		t.Fatalf("malformed JSON error = %q", got.Code)
	}

	if err := conn.WriteJSON(map[string]any{"client_message_id": "empty-message", "content": ""}); err != nil {
		t.Fatal(err)
	}
	if got := readWSError(t, conn); got.Code != "empty_message" {
		t.Fatalf("empty message error = %q", got.Code)
	}

	longID := "too-long-logical-message"
	content := make([]byte, 4001)
	for i := range content {
		content[i] = 'a'
	}
	if err := conn.WriteJSON(map[string]any{"client_message_id": longID, "content": string(content)}); err != nil {
		t.Fatal(err)
	}
	if got := readWSError(t, conn); got.Code != "message_too_long" {
		t.Fatalf("long content error = %q", got.Code)
	}
	assertCollectionCount(t, env.db.Collection(conversation.MessagesCollection), bson.M{}, 0)

	oversized := make([]byte, 70<<10)
	for i := range oversized {
		oversized[i] = 'x'
	}
	raw := append([]byte(`{"client_message_id":"oversized-frame","content":"`), oversized...)
	raw = append(raw, []byte(`"}`)...)
	if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
		t.Fatalf("write oversized websocket frame: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("oversized websocket frame did not close the connection")
	}
	assertCollectionCount(t, env.db.Collection(conversation.MessagesCollection), bson.M{
		"client_message_id": "oversized-frame",
	}, 0)
}

func TestSQSFailureDoesNotRollbackDurableDirectMessage(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{sqsStatus: http.StatusInternalServerError})
	sender := uuid.NewString()
	receiver := uuid.NewString()
	direct := env.seedDirectConversation(t, sender, receiver)

	// Receiver intentionally stays offline so the durable message path attempts
	// the best-effort SQS notification after acknowledging persistence.
	conn := connectDirect(t, env, env.mustIssueTicket(t, signedAccessToken(t, sender, "sender")), direct.ID.Hex())
	t.Cleanup(func() { _ = conn.Close() })

	if err := conn.WriteJSON(map[string]any{
		"client_message_id": "sqs-failure-message",
		"content":           "durable despite notification failure",
	}); err != nil {
		t.Fatal(err)
	}
	ack := readAccepted(t, conn)
	if ack.MessageID == "" {
		t.Fatal("message was not acknowledged before notification failure")
	}
	assertCollectionCount(t, env.db.Collection(conversation.MessagesCollection), bson.M{
		"sender_id": sender, "client_message_id": "sqs-failure-message",
	}, 1)
}

func TestShutdownClosesActiveWebSocketAndDrainsHub(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{})
	sender := uuid.NewString()
	receiver := uuid.NewString()
	direct := env.seedDirectConversation(t, sender, receiver)

	conn := connectDirect(t, env, env.mustIssueTicket(t, signedAccessToken(t, sender, "sender")), direct.ID.Hex())
	waitConnected(t, env.hub, sender, true)

	env.hub.Shutdown(websocket.CloseGoingAway, "server_shutdown")
	waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := env.hub.Wait(waitCtx); err != nil {
		t.Fatalf("hub did not drain: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("active websocket remained open after shutdown")
	}
	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) || closeErr.Code != websocket.CloseGoingAway {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			t.Fatalf("websocket did not close during shutdown: %v", err)
		}
		t.Fatalf("shutdown close error = %v, want code %d", err, websocket.CloseGoingAway)
	}
}

func TestApplicationStatusGuardsPreventUnauthorizedPersistence(t *testing.T) {
	tests := []struct {
		name       string
		status     application.ApplicationStatus
		senderRole string
		wantCode   string
	}{
		{
			name:       "initiator cannot send again while pending",
			status:     application.ApplicationStatusPending,
			senderRole: "initiator",
			wantCode:   "application_pending",
		},
		{
			name:       "target cannot message before approving pending application",
			status:     application.ApplicationStatusPending,
			senderRole: "target",
			wantCode:   "application_must_be_approved",
		},
		{
			name:       "rejected application cannot accept messages",
			status:     application.ApplicationStatusRejected,
			senderRole: "initiator",
			wantCode:   "application_rejected",
		},
		{
			name:       "archived application cannot accept messages",
			status:     application.ApplicationStatusArchived,
			senderRole: "target",
			wantCode:   "application_archived",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t, testEnvOptions{})
			initiator := uuid.NewString()
			target := uuid.NewString()
			app := env.seedApplication(t, initiator, target, uuid.NewString(), tc.status)

			sender := initiator
			if tc.senderRole == "target" {
				sender = target
			}
			conn := connectApplicationThread(
				t, env,
				env.mustIssueTicket(t, signedAccessToken(t, sender, tc.senderRole)),
				app.ID.Hex(),
			)
			defer conn.Close()

			clientID := "status-guard-" + tc.senderRole
			if err := conn.WriteJSON(map[string]any{
				"client_message_id": clientID,
				"content":           "must not persist",
			}); err != nil {
				t.Fatal(err)
			}
			got := readWSError(t, conn)
			if got.Code != tc.wantCode {
				t.Fatalf("status guard error = %q, want %q", got.Code, tc.wantCode)
			}
			assertCollectionCount(t, env.db.Collection(application.MessagesCollection), bson.M{
				"sender_id": sender, "client_message_id": clientID,
			}, 0)
		})
	}
}

func TestAttachmentScopeAndOwnershipAreEnforcedDuringMessageCommit(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{})
	sender := uuid.NewString()
	receiver := uuid.NewString()
	otherUser := uuid.NewString()
	direct := env.seedDirectConversation(t, sender, receiver)
	otherDirect := env.seedDirectConversation(t, sender, otherUser)

	expires := time.Now().UTC().Add(time.Hour)
	wrongScope := &attachment.Attachment{
		Scope: attachment.ScopeDirect, ScopeID: otherDirect.ID, UploaderID: sender,
		ObjectKey: "integration/wrong-scope", OriginalName: "scope.png",
		ContentType: "image/png", SizeBytes: 10, Status: attachment.StatusReady,
		ExpiresAt: &expires, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	wrongOwner := &attachment.Attachment{
		Scope: attachment.ScopeDirect, ScopeID: direct.ID, UploaderID: receiver,
		ObjectKey: "integration/wrong-owner", OriginalName: "owner.png",
		ContentType: "image/png", SizeBytes: 10, Status: attachment.StatusReady,
		ExpiresAt: &expires, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	for _, item := range []*attachment.Attachment{wrongScope, wrongOwner} {
		if err := env.attachRepo.Create(context.Background(), item); err != nil {
			t.Fatal(err)
		}
	}

	conn := connectDirect(t, env, env.mustIssueTicket(t, signedAccessToken(t, sender, "sender")), direct.ID.Hex())
	defer conn.Close()

	for name, id := range map[string]bson.ObjectID{
		"cross-conversation": wrongScope.ID,
		"wrong-uploader":     wrongOwner.ID,
	} {
		clientID := "attachment-" + name
		if err := conn.WriteJSON(map[string]any{
			"client_message_id": clientID,
			"content":           "must fail",
			"attachment_ids":    []string{id.Hex()},
		}); err != nil {
			t.Fatal(err)
		}
		if got := readWSError(t, conn); got.Code != "attachment_unavailable" {
			t.Fatalf("%s attachment error = %q, want attachment_unavailable", name, got.Code)
		}
		assertCollectionCount(t, env.db.Collection(conversation.MessagesCollection), bson.M{
			"sender_id": sender, "client_message_id": clientID,
		}, 0)
	}
}

func TestConcurrentDuplicateAcceptedApplicationSendPersistsAndDeliversOnce(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{})
	initiator := uuid.NewString()
	target := uuid.NewString()
	app := env.seedApplication(t, initiator, target, uuid.NewString(), application.ApplicationStatusAccepted)

	targetConn := connectApplicationThread(
		t, env,
		env.mustIssueTicket(t, signedAccessToken(t, target, "target")),
		app.ID.Hex(),
	)
	sender1 := connectApplicationThread(
		t, env,
		env.mustIssueTicket(t, signedAccessToken(t, initiator, "initiator")),
		app.ID.Hex(),
	)
	sender2 := connectApplicationThread(
		t, env,
		env.mustIssueTicket(t, signedAccessToken(t, initiator, "initiator")),
		app.ID.Hex(),
	)
	defer targetConn.Close()
	defer sender1.Close()
	defer sender2.Close()

	payload := map[string]any{
		"client_message_id": "concurrent-application-retry",
		"content":           "persist once",
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, conn := range []*websocket.Conn{sender1, sender2} {
		wg.Add(1)
		go func(c *websocket.Conn) {
			defer wg.Done()
			<-start
			errs <- c.WriteJSON(payload)
		}(conn)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent application send failed: %v", err)
		}
	}

	ack1 := readAccepted(t, sender1)
	ack2 := readAccepted(t, sender2)
	if ack1.MessageID != ack2.MessageID || ack1.ThreadID != app.ID.Hex() || ack2.ThreadID != app.ID.Hex() {
		t.Fatalf("concurrent application retry diverged: first=%+v second=%+v", ack1, ack2)
	}

	_ = readApplicationDelivery(t, targetConn)
	assertNoWebSocketMessage(t, targetConn, 300*time.Millisecond)
	assertCollectionCount(t, env.db.Collection(application.MessagesCollection), bson.M{
		"sender_id": initiator, "client_message_id": "concurrent-application-retry",
	}, 1)
}

func TestOfflineIdempotentRetryPublishesNotificationOnce(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{})
	sender := uuid.NewString()
	receiver := uuid.NewString()
	direct := env.seedDirectConversation(t, sender, receiver)

	const clientID = "offline-notification-idempotency"
	conn := connectDirect(t, env, env.mustIssueTicket(t, signedAccessToken(t, sender, "sender")), direct.ID.Hex())
	if err := conn.WriteJSON(map[string]any{
		"client_message_id": clientID,
		"content":           "notify only once",
	}); err != nil {
		t.Fatal(err)
	}
	first := readAccepted(t, conn)
	_ = conn.Close()

	deadline := time.Now().Add(2 * time.Second)
	for env.sqsRequests.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := env.sqsRequests.Load(); got != 1 {
		t.Fatalf("first offline send produced %d SQS requests, want 1", got)
	}

	retry := connectDirect(t, env, env.mustIssueTicket(t, signedAccessToken(t, sender, "sender")), direct.ID.Hex())
	defer retry.Close()
	if err := retry.WriteJSON(map[string]any{
		"client_message_id": clientID,
		"content":           "notify only once",
	}); err != nil {
		t.Fatal(err)
	}
	second := readAccepted(t, retry)
	if second.MessageID != first.MessageID {
		t.Fatalf("offline retry changed durable message identity: %s != %s", first.MessageID, second.MessageID)
	}
	time.Sleep(150 * time.Millisecond)
	if got := env.sqsRequests.Load(); got != 1 {
		t.Fatalf("idempotent retry produced duplicate SQS notification requests: %d", got)
	}
	assertCollectionCount(t, env.db.Collection(conversation.MessagesCollection), bson.M{
		"sender_id": sender, "client_message_id": clientID,
	}, 1)
}

func TestApplicationAndDirectChannelsDoNotCrossDeliver(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{})
	alice := uuid.NewString()
	bob := uuid.NewString()
	direct := env.seedDirectConversation(t, alice, bob)
	app := env.seedApplication(t, bob, alice, uuid.NewString(), application.ApplicationStatusAccepted)

	aliceDirect := connectDirect(t, env, env.mustIssueTicket(t, signedAccessToken(t, alice, "alice")), direct.ID.Hex())
	aliceApplication := connectApplicationThread(t, env, env.mustIssueTicket(t, signedAccessToken(t, alice, "alice")), app.ID.Hex())
	bobDirect := connectDirect(t, env, env.mustIssueTicket(t, signedAccessToken(t, bob, "bob")), direct.ID.Hex())
	defer aliceDirect.Close()
	defer aliceApplication.Close()
	defer bobDirect.Close()

	if err := bobDirect.WriteJSON(map[string]any{
		"client_message_id": "channel-isolation",
		"content":           "direct only",
	}); err != nil {
		t.Fatal(err)
	}
	_ = readAccepted(t, bobDirect)
	got := readDirectDelivery(t, aliceDirect)
	if got.ConversationID != direct.ID.Hex() || got.SenderID != bob {
		t.Fatalf("unexpected direct delivery: %+v", got)
	}
	assertNoWebSocketMessage(t, aliceApplication, 300*time.Millisecond)
}
