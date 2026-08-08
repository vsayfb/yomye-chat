//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/vsayfb/gig-platform-chat-service/hub"
	"github.com/vsayfb/gig-platform-chat-service/internal/attachment"
	"github.com/vsayfb/gig-platform-chat-service/internal/conversation"
	"github.com/vsayfb/gig-platform-chat-service/internal/ws"
	"github.com/vsayfb/gig-platform-chat-service/internal/wsticket"
	"github.com/vsayfb/gig-platform-chat-service/pkg/database"
	chatjwt "github.com/vsayfb/gig-platform-chat-service/pkg/jwt"
	"github.com/vsayfb/gig-platform-chat-service/pkg/middleware"
)

const (
	integrationJWTSecret = "integration-jwt-secret"
	coreIssuer           = "yevmiye-core"
	accessTokenUse       = "access"
)

type ticketEnvelope struct {
	Status int `json:"status"`
	Data   struct {
		Ticket string `json:"ticket"`
	} `json:"data"`
}

type acceptedMessage struct {
	Type            string `json:"type"`
	ClientMessageID string `json:"client_message_id"`
	MessageID       string `json:"message_id"`
	ConversationID  string `json:"conversation_id"`
	SentAt          string `json:"sent_at"`
}

type deliveredDirectMessage struct {
	Type           string `json:"type"`
	ConversationID string `json:"conversation_id"`
	SenderID       string `json:"sender_id"`
	Content        string `json:"content"`
}

func TestDirectMessageRetryPersistsAndDeliversExactlyOnce(t *testing.T) {
	mongoURI := os.Getenv("CHAT_INTEGRATION_MONGO_URI")
	if mongoURI == "" {
		t.Fatal("CHAT_INTEGRATION_MONGO_URI is required; run `make test-integration`")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dbName := "chat_integration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	mongoClient, db, err := database.NewMongoDB(ctx, mongoURI, dbName)
	if err != nil {
		t.Fatalf("connect integration MongoDB: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_ = db.Drop(cleanupCtx)
		_ = mongoClient.Disconnect(cleanupCtx)
	})

	conversationRepo := conversation.NewRepository(db)
	if err := conversationRepo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("ensure conversation indexes: %v", err)
	}

	attachmentRepo := attachment.NewRepository(db)
	if err := attachmentRepo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("ensure attachment indexes: %v", err)
	}

	// This scenario resumes an existing direct conversation, so the Core user
	// preference boundary is not involved. No Chat repository/service is mocked.
	conversationSvc := conversation.NewService(conversationRepo, nil)
	attachmentSvc := attachment.NewService(attachmentRepo, nil, mongoClient, nil)

	senderID := uuid.NewString()
	receiverID := uuid.NewString()
	participants := []string{senderID, receiverID}
	sort.Strings(participants)
	now := time.Now().UTC()
	directConversation := &conversation.Conversation{
		Type:      conversation.TypeDirect,
		DirectKey: strings.Join(participants, "\x00"),
		CreatedBy: senderID,
		Members: []conversation.Member{
			{UserID: senderID, Role: conversation.RoleMember, JoinedAt: now},
			{UserID: receiverID, Role: conversation.RoleMember, JoinedAt: now},
		},
		LastMessageAt: now,
	}
	if err := conversationRepo.Create(ctx, directConversation); err != nil {
		t.Fatalf("seed direct conversation: %v", err)
	}

	h := hub.New()
	tickets := wsticket.NewStore(wsticket.DefaultTTL)
	wsHandler := ws.NewWSHandler(
		h,
		tickets,
		nil,
		conversationSvc,
		attachmentSvc,
		nil,
		nil,
		nil,
		true,
		nil,
	)

	jwtSvc := chatjwt.New(integrationJWTSecret)
	router := chi.NewRouter()
	auth := middleware.Auth(jwtSvc)
	router.With(auth).Post("/ws/ticket", wsHandler.IssueTicket)
	router.Get("/ws/direct", wsHandler.ServeDirectWS)

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	t.Cleanup(func() {
		h.Shutdown(websocket.CloseGoingAway, "integration_test_shutdown")
		waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer waitCancel()
		_ = h.Wait(waitCtx)
	})

	receiverConn := connectDirect(
		t,
		server.URL,
		issueTicket(t, server.URL, signedAccessToken(t, receiverID, "receiver")),
		directConversation.ID.Hex(),
	)
	t.Cleanup(func() { _ = receiverConn.Close() })
	waitConnected(t, h, receiverID, true)

	senderConn := connectDirect(
		t,
		server.URL,
		issueTicket(t, server.URL, signedAccessToken(t, senderID, "sender")),
		directConversation.ID.Hex(),
	)
	waitConnected(t, h, senderID, true)

	const clientMessageID = "integration-message-1"
	const content = "persist me once"

	if err := senderConn.WriteJSON(map[string]any{
		"client_message_id": clientMessageID,
		"content":           content,
	}); err != nil {
		t.Fatalf("send direct message: %v", err)
	}

	firstAck := readAccepted(t, senderConn)
	if firstAck.ClientMessageID != clientMessageID ||
		firstAck.ConversationID != directConversation.ID.Hex() {
		t.Fatalf("unexpected first acknowledgement: %+v", firstAck)
	}

	var delivered deliveredDirectMessage
	if err := receiverConn.ReadJSON(&delivered); err != nil {
		t.Fatalf("read recipient delivery: %v", err)
	}
	if delivered.Type != "message" || delivered.SenderID != senderID ||
		delivered.ConversationID != directConversation.ID.Hex() || delivered.Content != content {
		t.Fatalf("unexpected delivered message: %+v", delivered)
	}

	assertMessageCount(t, db, senderID, clientMessageID, 1)

	if err := senderConn.Close(); err != nil {
		t.Fatalf("close sender WebSocket: %v", err)
	}
	waitConnected(t, h, senderID, false)

	retryConn := connectDirect(
		t,
		server.URL,
		issueTicket(t, server.URL, signedAccessToken(t, senderID, "sender")),
		directConversation.ID.Hex(),
	)
	t.Cleanup(func() { _ = retryConn.Close() })
	waitConnected(t, h, senderID, true)

	if err := retryConn.WriteJSON(map[string]any{
		"client_message_id": clientMessageID,
		"content":           content,
	}); err != nil {
		t.Fatalf("retry direct message: %v", err)
	}

	retryAck := readAccepted(t, retryConn)
	if retryAck.MessageID != firstAck.MessageID ||
		retryAck.ConversationID != firstAck.ConversationID ||
		retryAck.ClientMessageID != firstAck.ClientMessageID {
		t.Fatalf("retry acknowledgement changed persisted identity: first=%+v retry=%+v", firstAck, retryAck)
	}

	assertMessageCount(t, db, senderID, clientMessageID, 1)

	if err := receiverConn.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatalf("set recipient read deadline: %v", err)
	}
	var duplicate deliveredDirectMessage
	err = receiverConn.ReadJSON(&duplicate)
	if err == nil {
		t.Fatalf("idempotent retry was delivered twice: %+v", duplicate)
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("recipient connection failed while checking duplicate delivery: %v", err)
	}
}

func issueTicket(t *testing.T, baseURL, token string) string {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, baseURL+"/ws/ticket", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("issue WebSocket ticket: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("ticket status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}

	var envelope ticketEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode ticket response: %v", err)
	}
	if envelope.Data.Ticket == "" {
		t.Fatal("ticket response was empty")
	}
	return envelope.Data.Ticket
}

func connectDirect(t *testing.T, baseURL, ticket, conversationID string) *websocket.Conn {
	t.Helper()

	endpoint := "ws" + strings.TrimPrefix(baseURL, "http") + "/ws/direct"
	query := url.Values{
		"ticket":         []string{ticket},
		"conversationID": []string{conversationID},
	}

	conn, resp, err := websocket.DefaultDialer.Dial(endpoint+"?"+query.Encode(), nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("connect direct WebSocket: %v (HTTP %d)", err, resp.StatusCode)
		}
		t.Fatalf("connect direct WebSocket: %v", err)
	}
	return conn
}

func readAccepted(t *testing.T, conn *websocket.Conn) acceptedMessage {
	t.Helper()

	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var accepted acceptedMessage
	if err := conn.ReadJSON(&accepted); err != nil {
		t.Fatalf("read message.accepted: %v", err)
	}
	if accepted.Type != "message.accepted" || accepted.MessageID == "" {
		t.Fatalf("unexpected acknowledgement: %+v", accepted)
	}
	return accepted
}

func waitConnected(t *testing.T, h *hub.Hub, userID string, want bool) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h.IsConnected(userID) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("hub connected(%s) = %t, want %t", userID, h.IsConnected(userID), want)
}

func assertMessageCount(
	t *testing.T,
	db *mongo.Database,
	senderID string,
	clientMessageID string,
	want int64,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	count, err := db.Collection(conversation.MessagesCollection).CountDocuments(ctx, bson.M{
		"sender_id":         senderID,
		"client_message_id": clientMessageID,
	})
	if err != nil {
		t.Fatalf("count persisted messages: %v", err)
	}
	if count != want {
		t.Fatalf("persisted message count = %d, want %d", count, want)
	}
}

func signedAccessToken(t *testing.T, subject, name string) string {
	t.Helper()

	claims := chatjwt.Claims{
		Name:     name,
		TokenUse: accessTokenUse,
		RegisteredClaims: gojwt.RegisteredClaims{
			Issuer:    coreIssuer,
			Subject:   subject,
			ExpiresAt: gojwt.NewNumericDate(time.Now().Add(10 * time.Minute)),
		},
	}
	token, err := gojwt.NewWithClaims(gojwt.SigningMethodHS256, claims).
		SignedString([]byte(integrationJWTSecret))
	if err != nil {
		t.Fatalf("sign access token: %v", err)
	}
	return token
}
