//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/vsayfb/gig-platform-chat-service/internal/application"
	"github.com/vsayfb/gig-platform-chat-service/internal/attachment"
	"github.com/vsayfb/gig-platform-chat-service/internal/conversation"
	"github.com/vsayfb/gig-platform-chat-service/internal/readiness"
)

func TestApplicationApproveRejectRaceHasSingleWinner(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{})
	initiator := uuid.NewString()
	target := uuid.NewString()
	app := env.seedApplication(t, initiator, target, uuid.NewString(), application.ApplicationStatusPending)
	token := signedAccessToken(t, target, "target")

	endpoints := []string{
		env.server.URL + "/applications/" + app.ID.Hex() + "/approve",
		env.server.URL + "/applications/" + app.ID.Hex() + "/reject",
	}
	start := make(chan struct{})
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for _, endpoint := range endpoints {
		wg.Add(1)
		go func(endpoint string) {
			defer wg.Done()
			<-start
			req := authenticatedRequest(t, http.MethodPost, endpoint, token)
			statuses <- doStatus(t, req)
		}(endpoint)
	}
	close(start)
	wg.Wait()
	close(statuses)

	got := make([]int, 0, 2)
	for status := range statuses {
		got = append(got, status)
	}
	sort.Ints(got)
	want := []int{http.StatusNoContent, http.StatusConflict}
	sort.Ints(want)
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("approve/reject statuses = %v, want one 204 and one 409", got)
	}

	persisted, err := env.appRepo.FindApplicationByID(context.Background(), app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != application.ApplicationStatusAccepted && persisted.Status != application.ApplicationStatusRejected {
		t.Fatalf("final application status = %q, want accepted or rejected", persisted.Status)
	}

	// A third transition must remain rejected rather than overwriting the winner.
	req := authenticatedRequest(t, http.MethodPost, endpoints[0], token)
	if status := doStatus(t, req); status != http.StatusConflict {
		t.Fatalf("third transition status = %d, want 409", status)
	}
}

func TestOnlyApplicationTargetCanTransitionStatus(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{})
	initiator := uuid.NewString()
	target := uuid.NewString()
	app := env.seedApplication(t, initiator, target, uuid.NewString(), application.ApplicationStatusPending)

	req := authenticatedRequest(t, http.MethodPost,
		env.server.URL+"/applications/"+app.ID.Hex()+"/approve",
		signedAccessToken(t, initiator, "initiator"))
	if status := doStatus(t, req); status != http.StatusForbidden {
		t.Fatalf("initiator approve status = %d, want 403", status)
	}

	persisted, err := env.appRepo.FindApplicationByID(context.Background(), app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != application.ApplicationStatusPending {
		t.Fatalf("unauthorized transition mutated status to %q", persisted.Status)
	}
}

type listEnvelope[T any] struct {
	Status int `json:"status"`
	Data   []T `json:"data"`
}

func TestStableDirectMessagePaginationAndAuthorization(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{})
	alice := uuid.NewString()
	bob := uuid.NewString()
	stranger := uuid.NewString()
	direct := env.seedDirectConversation(t, alice, bob)

	sharedTime := time.Date(2026, 8, 8, 10, 11, 12, 345000000, time.UTC)
	ids := make([]bson.ObjectID, 7)
	docs := make([]any, 0, len(ids))
	for i := range ids {
		ids[i] = bson.NewObjectID()
		docs = append(docs, conversation.Message{
			ID: ids[i], ConversationID: direct.ID, SenderID: alice,
			ClientMessageID: "page-" + ids[i].Hex(), Content: ids[i].Hex(), SentAt: sharedTime,
		})
	}
	if _, err := env.db.Collection(conversation.MessagesCollection).InsertMany(context.Background(), docs); err != nil {
		t.Fatalf("seed direct messages: %v", err)
	}

	first := getDirectMessages(t, env, alice, direct.ID.Hex(), url.Values{"limit": []string{"3"}})
	if len(first) != 3 {
		t.Fatalf("first page length = %d, want 3", len(first))
	}
	if !(first[0].ID.Hex() > first[1].ID.Hex() && first[1].ID.Hex() > first[2].ID.Hex()) {
		t.Fatalf("first page not sorted by _id descending for equal timestamps: %v %v %v", first[0].ID, first[1].ID, first[2].ID)
	}

	cursor := first[len(first)-1]
	second := getDirectMessages(t, env, alice, direct.ID.Hex(), url.Values{
		"limit":     []string{"3"},
		"before":    []string{cursor.SentAt.Format(time.RFC3339Nano)},
		"before_id": []string{cursor.ID.Hex()},
	})
	if len(second) != 3 {
		t.Fatalf("second page length = %d, want 3", len(second))
	}
	seen := map[bson.ObjectID]struct{}{}
	for _, item := range first {
		seen[item.ID] = struct{}{}
	}
	for _, item := range second {
		if _, exists := seen[item.ID]; exists {
			t.Fatalf("message %s appeared on both pages", item.ID.Hex())
		}
		seen[item.ID] = struct{}{}
	}
	last := second[len(second)-1]
	third := getDirectMessages(t, env, alice, direct.ID.Hex(), url.Values{
		"limit":     []string{"3"},
		"before":    []string{last.SentAt.Format(time.RFC3339Nano)},
		"before_id": []string{last.ID.Hex()},
	})
	if len(third) != 1 {
		t.Fatalf("third page length = %d, want 1", len(third))
	}
	if _, exists := seen[third[0].ID]; exists {
		t.Fatalf("message %s was duplicated on final page", third[0].ID.Hex())
	}
	seen[third[0].ID] = struct{}{}
	if len(seen) != len(ids) {
		t.Fatalf("pagination returned %d unique messages, want %d", len(seen), len(ids))
	}

	req := authenticatedRequest(t, http.MethodGet,
		env.server.URL+"/conversations/"+direct.ID.Hex()+"/messages",
		signedAccessToken(t, stranger, "stranger"))
	if status := doStatus(t, req); status != http.StatusForbidden {
		t.Fatalf("stranger message history status = %d, want 403", status)
	}

	// Cursor components form one opaque boundary and must never be accepted
	// independently; doing so would reintroduce skip/duplication ambiguity.
	for _, query := range []string{
		"before=" + url.QueryEscape(sharedTime.Format(time.RFC3339Nano)),
		"before_id=" + bson.NewObjectID().Hex(),
	} {
		req := authenticatedRequest(t, http.MethodGet,
			env.server.URL+"/conversations/"+direct.ID.Hex()+"/messages?"+query,
			signedAccessToken(t, alice, "alice"))
		if status := doStatus(t, req); status != http.StatusBadRequest {
			t.Fatalf("partial cursor %q status = %d, want 400", query, status)
		}
	}
}

func TestStableApplicationMessagePagination(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{})
	initiator := uuid.NewString()
	target := uuid.NewString()
	app := env.seedApplication(t, initiator, target, uuid.NewString(), application.ApplicationStatusAccepted)

	sharedTime := time.Date(2026, 8, 8, 10, 11, 12, 345000000, time.UTC)
	docs := make([]any, 0, 6)
	for i := 0; i < 6; i++ {
		id := bson.NewObjectID()
		docs = append(docs, application.Message{
			ID: id, ThreadID: app.ID, SenderID: initiator,
			ClientMessageID: "app-page-" + id.Hex(), Content: id.Hex(), SentAt: sharedTime,
		})
	}
	if _, err := env.db.Collection(application.MessagesCollection).InsertMany(context.Background(), docs); err != nil {
		t.Fatalf("seed application messages: %v", err)
	}

	first := getApplicationMessages(t, env, target, app.ID.Hex(), url.Values{"limit": []string{"2"}})
	cursor := first[len(first)-1]
	second := getApplicationMessages(t, env, target, app.ID.Hex(), url.Values{
		"limit": []string{"2"}, "before": []string{cursor.SentAt.Format(time.RFC3339Nano)}, "before_id": []string{cursor.ID.Hex()},
	})
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("application pagination lengths = %d,%d want 2,2", len(first), len(second))
	}
	seen := map[bson.ObjectID]struct{}{}
	for _, page := range [][]application.Message{first, second} {
		for _, item := range page {
			if _, exists := seen[item.ID]; exists {
				t.Fatalf("application pagination duplicated %s", item.ID.Hex())
			}
			seen[item.ID] = struct{}{}
		}
	}
	last := second[len(second)-1]
	third := getApplicationMessages(t, env, target, app.ID.Hex(), url.Values{
		"limit": []string{"2"}, "before": []string{last.SentAt.Format(time.RFC3339Nano)}, "before_id": []string{last.ID.Hex()},
	})
	if len(third) != 2 {
		t.Fatalf("third application page length = %d, want 2", len(third))
	}
	for _, item := range third {
		if _, exists := seen[item.ID]; exists {
			t.Fatalf("application pagination duplicated %s", item.ID.Hex())
		}
		seen[item.ID] = struct{}{}
	}
	if len(seen) != 6 {
		t.Fatalf("application pagination returned %d unique messages, want 6", len(seen))
	}
}

func getDirectMessages(t *testing.T, env *testEnv, userID, conversationID string, q url.Values) []conversation.Message {
	t.Helper()
	endpoint := env.server.URL + "/conversations/" + conversationID + "/messages?" + q.Encode()
	req := authenticatedRequest(t, http.MethodGet, endpoint, signedAccessToken(t, userID, "user"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list direct messages status = %d", resp.StatusCode)
	}
	var envelope listEnvelope[conversation.Message]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func getApplicationMessages(t *testing.T, env *testEnv, userID, threadID string, q url.Values) []application.Message {
	t.Helper()
	endpoint := env.server.URL + "/applications/" + threadID + "/messages?" + q.Encode()
	req := authenticatedRequest(t, http.MethodGet, endpoint, signedAccessToken(t, userID, "user"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list application messages status = %d", resp.StatusCode)
	}
	var envelope listEnvelope[application.Message]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func TestCriticalMongoIndexesExist(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{})

	checks := []struct {
		collection *mongo.Collection
		names      []string
	}{
		{env.db.Collection(application.ApplicationsCollection), []string{"unique_conversation", "initiator_applications", "target_applications"}},
		{env.db.Collection(application.MessagesCollection), []string{"application_messages_v2", "application_message_idempotency"}},
		{env.db.Collection(conversation.ConversationsCollection), []string{"unique_direct_conversation", "member_conversations"}},
		{env.db.Collection(conversation.MessagesCollection), []string{"conversation_messages_v2", "conversation_message_idempotency"}},
		{env.db.Collection(attachment.Collection), []string{"scope_attachments", "attachment_expiry", "attachment_deletion_retry"}},
	}

	for _, check := range checks {
		names := listIndexNames(t, check.collection)
		for _, want := range check.names {
			if _, ok := names[want]; !ok {
				t.Fatalf("collection %s missing critical index %q; got %v", check.collection.Name(), want, names)
			}
		}
	}
}

func listIndexNames(t *testing.T, collection *mongo.Collection) map[string]struct{} {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cursor, err := collection.Indexes().List(ctx)
	if err != nil {
		t.Fatalf("list indexes for %s: %v", collection.Name(), err)
	}
	defer cursor.Close(ctx)

	names := map[string]struct{}{}
	for cursor.Next(ctx) {
		var doc struct {
			Name string `bson:"name"`
		}
		if err := cursor.Decode(&doc); err != nil {
			t.Fatal(err)
		}
		names[doc.Name] = struct{}{}
	}
	if err := cursor.Err(); err != nil {
		t.Fatal(err)
	}
	return names
}

func TestReadinessUsesMongoAndDrainingOnly(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{})

	resp, err := http.Get(env.server.URL + "/ready")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ready status with Mongo available = %d, want 200", resp.StatusCode)
	}

	env.ready.StartDraining()
	resp, err = http.Get(env.server.URL + "/ready")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("ready status while draining = %d, want 503", resp.StatusCode)
	}

	unreachable, err := mongo.Connect(options.Client().ApplyURI("mongodb://127.0.0.1:1").SetServerSelectionTimeout(100 * time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer unreachable.Disconnect(context.Background())
	unavailableHandler := readiness.NewHandler(unreachable)
	router := chi.NewRouter()
	router.Get("/ready", unavailableHandler.ServeHTTP)
	unavailableServer := httptest.NewServer(router)
	defer unavailableServer.Close()

	started := time.Now()
	resp, err = http.Get(unavailableServer.URL + "/ready")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("ready status with Mongo unavailable = %d, want 503", resp.StatusCode)
	}
	if time.Since(started) > 2500*time.Millisecond {
		t.Fatalf("readiness Mongo failure exceeded bounded timeout: %s", time.Since(started))
	}
}

func TestConcurrentFirstApplicationCreatesSingleThread(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{})
	initiator := uuid.NewString()
	target := uuid.NewString()
	gigID := uuid.NewString()

	type result struct {
		msg       *application.Message
		newThread bool
		replayed  bool
		err       error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, clientID := range []string{"first-app-a", "first-app-b"} {
		clientID := clientID
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			msg, newThread, replayed, err := env.appSvc.SendMessage(
				context.Background(), initiator, target, gigID, clientID, clientID, nil,
			)
			results <- result{msg: msg, newThread: newThread, replayed: replayed, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	pendingFailures := 0
	for result := range results {
		switch {
		case result.err == nil:
			successes++
			if result.msg == nil {
				t.Fatal("successful first application send returned nil message")
			}
		case errors.Is(result.err, application.ErrApplicationPending):
			pendingFailures++
		default:
			t.Fatalf("unexpected concurrent first-application result: %+v", result)
		}
	}
	if successes != 1 || pendingFailures != 1 {
		t.Fatalf("concurrent first application results: success=%d pending=%d, want 1/1", successes, pendingFailures)
	}
	assertCollectionCount(t, env.db.Collection(application.ApplicationsCollection), bson.M{}, 1)
	assertCollectionCount(t, env.db.Collection(application.MessagesCollection), bson.M{}, 1)
}

func TestPrivateHTTPRoutesRequireCoreAccessToken(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{})
	alice := uuid.NewString()
	bob := uuid.NewString()
	direct := env.seedDirectConversation(t, alice, bob)

	endpoints := []string{
		env.server.URL + "/conversations/" + direct.ID.Hex(),
		env.server.URL + "/conversations/" + direct.ID.Hex() + "/messages",
		env.server.URL + "/ws/ticket",
	}
	for _, endpoint := range endpoints {
		method := http.MethodGet
		if strings.HasSuffix(endpoint, "/ws/ticket") {
			method = http.MethodPost
		}
		req, err := http.NewRequest(method, endpoint, nil)
		if err != nil {
			t.Fatal(err)
		}
		if status := doStatus(t, req); status != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s %s status = %d, want 401", method, endpoint, status)
		}
	}
}
