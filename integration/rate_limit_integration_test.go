//go:build integration

package integration_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/vsayfb/gig-platform-chat-service/config"
	"github.com/vsayfb/gig-platform-chat-service/internal/application"
	"github.com/vsayfb/gig-platform-chat-service/internal/conversation"
)

func TestTicketRateLimitIsPerAuthenticatedUser(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{rateLimits: &config.RateLimitConfig{
		HTTPReadPerMinute: 300, HTTPReadBurst: 60,
		HTTPWritePerMinute: 60, HTTPWriteBurst: 15,
		WSTicketPerMinute: 1, WSTicketBurst: 1,
		WSMessagePerSecond: 5, WSMessageBurst: 10,
	}})

	aliceToken := signedAccessToken(t, uuid.NewString(), "alice")
	first, status := env.issueTicket(t, aliceToken, allowedWebOrigin)
	if status != http.StatusCreated || first == "" {
		t.Fatalf("first ticket = %q status=%d, want created", first, status)
	}

	req, err := http.NewRequest(http.MethodPost, env.server.URL+"/ws/ticket", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+aliceToken)
	req.Header.Set("Origin", allowedWebOrigin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second ticket status = %d, want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("ticket rate-limit response missing Retry-After")
	}

	bobTicket, bobStatus := env.issueTicket(t, signedAccessToken(t, uuid.NewString(), "bob"), allowedWebOrigin)
	if bobStatus != http.StatusCreated || bobTicket == "" {
		t.Fatalf("bob ticket = %q status=%d, want independent bucket", bobTicket, bobStatus)
	}
}

func TestHTTPReadAndWriteRateLimitsAreIndependent(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{rateLimits: &config.RateLimitConfig{
		HTTPReadPerMinute: 1, HTTPReadBurst: 1,
		HTTPWritePerMinute: 1, HTTPWriteBurst: 1,
		WSTicketPerMinute: 20, WSTicketBurst: 5,
		WSMessagePerSecond: 5, WSMessageBurst: 10,
	}})

	initiator := uuid.NewString()
	target := uuid.NewString()
	app := env.seedApplication(t, initiator, target, uuid.NewString(), application.ApplicationStatusPending)
	targetToken := signedAccessToken(t, target, "target")

	getURL := env.server.URL + "/applications/" + app.ID.Hex()
	if status := doStatus(t, authenticatedRequest(t, http.MethodGet, getURL, targetToken)); status != http.StatusOK {
		t.Fatalf("first read status = %d, want 200", status)
	}
	if status := doStatus(t, authenticatedRequest(t, http.MethodGet, getURL, targetToken)); status != http.StatusTooManyRequests {
		t.Fatalf("second read status = %d, want 429", status)
	}

	approveURL := env.server.URL + "/applications/" + app.ID.Hex() + "/approve"
	if status := doStatus(t, authenticatedRequest(t, http.MethodPost, approveURL, targetToken)); status != http.StatusNoContent {
		t.Fatalf("write after exhausted read bucket = %d, want 204", status)
	}

	rejectURL := env.server.URL + "/applications/" + app.ID.Hex() + "/reject"
	if status := doStatus(t, authenticatedRequest(t, http.MethodPost, rejectURL, targetToken)); status != http.StatusTooManyRequests {
		t.Fatalf("second write status = %d, want 429 before handler", status)
	}

	// A different authenticated user gets a distinct read bucket.
	if status := doStatus(t, authenticatedRequest(t, http.MethodGet, getURL,
		signedAccessToken(t, initiator, "initiator"))); status != http.StatusOK {
		t.Fatalf("initiator read status = %d, want independent 200", status)
	}

	// Readiness is operational control-plane traffic and is intentionally not rate limited.
	for i := 0; i < 3; i++ {
		resp, err := http.Get(env.server.URL + "/ready")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("readiness request %d status = %d, want 200", i, resp.StatusCode)
		}
	}
}

func TestWebSocketMessageRateLimitPreventsPersistence(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{rateLimits: &config.RateLimitConfig{
		HTTPReadPerMinute: 300, HTTPReadBurst: 60,
		HTTPWritePerMinute: 60, HTTPWriteBurst: 15,
		WSTicketPerMinute: 20, WSTicketBurst: 5,
		WSMessagePerSecond: 1, WSMessageBurst: 1,
	}})

	sender := uuid.NewString()
	receiver := uuid.NewString()
	direct := env.seedDirectConversation(t, sender, receiver)
	conn := connectDirect(t, env, env.mustIssueTicket(t, signedAccessToken(t, sender, "sender")), direct.ID.Hex())
	t.Cleanup(func() { _ = conn.Close() })

	if err := conn.WriteJSON(map[string]any{
		"client_message_id": "rate-first",
		"content":           "first",
	}); err != nil {
		t.Fatal(err)
	}
	_ = readAccepted(t, conn)

	if err := conn.WriteJSON(map[string]any{
		"client_message_id": "rate-second",
		"content":           "second",
	}); err != nil {
		t.Fatal(err)
	}
	rateErr := readWSError(t, conn)
	if rateErr.Code != "rate_limited" || rateErr.RetryAfterMS <= 0 {
		t.Fatalf("rate-limit websocket error = %+v", rateErr)
	}

	count, err := env.db.Collection(conversation.MessagesCollection).CountDocuments(
		context.Background(), bson.M{"sender_id": sender, "client_message_id": "rate-second"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rate-limited WebSocket message persisted %d documents, want 0", count)
	}
}
