//go:build integration

package integration_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"go.mongodb.org/mongo-driver/v2/bson"
	"google.golang.org/grpc/codes"

	"github.com/vsayfb/gig-platform-chat-service/internal/application"
	"github.com/vsayfb/gig-platform-chat-service/internal/conversation"
)

func TestWebSocketTicketAuthenticationSecurity(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{allowedOrigins: []string{allowedWebOrigin}})
	sender := uuid.NewString()
	receiver := uuid.NewString()
	direct := env.seedDirectConversation(t, sender, receiver)

	t.Run("rejects invalid Core access tokens", func(t *testing.T) {
		cases := []struct {
			name  string
			token string
		}{
			{
				name: "wrong issuer",
				token: signedAccessTokenWith(t, sender, "sender", "wrong-core", accessTokenUse,
					time.Now().Add(time.Minute), gojwt.SigningMethodHS256, integrationJWTSecret),
			},
			{
				name: "wrong token use",
				token: signedAccessTokenWith(t, sender, "sender", coreIssuer, "refresh",
					time.Now().Add(time.Minute), gojwt.SigningMethodHS256, integrationJWTSecret),
			},
			{
				name: "wrong signing secret",
				token: signedAccessTokenWith(t, sender, "sender", coreIssuer, accessTokenUse,
					time.Now().Add(time.Minute), gojwt.SigningMethodHS256, "wrong-secret"),
			},
			{
				name: "expired",
				token: signedAccessTokenWith(t, sender, "sender", coreIssuer, accessTokenUse,
					time.Now().Add(-time.Minute), gojwt.SigningMethodHS256, integrationJWTSecret),
			},
			{
				name: "wrong algorithm",
				token: signedAccessTokenWith(t, sender, "sender", coreIssuer, accessTokenUse,
					time.Now().Add(time.Minute), gojwt.SigningMethodHS384, integrationJWTSecret),
			},
			{
				name: "invalid subject",
				token: signedAccessTokenWith(t, "not-a-uuid", "sender", coreIssuer, accessTokenUse,
					time.Now().Add(time.Minute), gojwt.SigningMethodHS256, integrationJWTSecret),
			},
			{
				name: "nil UUID subject",
				token: signedAccessTokenWith(t, uuid.Nil.String(), "sender", coreIssuer, accessTokenUse,
					time.Now().Add(time.Minute), gojwt.SigningMethodHS256, integrationJWTSecret),
			},
			{name: "opaque Core refresh token", token: strings.Repeat("a1", 32)},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, statusCode := env.issueTicket(t, tc.token, allowedWebOrigin)
				if statusCode != http.StatusUnauthorized {
					t.Fatalf("ticket status = %d, want 401", statusCode)
				}
			})
		}
	})

	t.Run("rejects unexpected browser origin without consuming ticket", func(t *testing.T) {
		token := signedAccessToken(t, sender, "sender")
		ticket, statusCode := env.issueTicket(t, token, allowedWebOrigin)
		if statusCode != http.StatusCreated {
			t.Fatalf("ticket status = %d", statusCode)
		}

		_, resp, err := dialWebSocket(t, env.server.URL, "/ws/direct", url.Values{
			"ticket": []string{ticket}, "conversationID": []string{direct.ID.Hex()},
		}, "https://evil.example")
		if err == nil {
			t.Fatal("unexpected browser origin was accepted")
		}
		if resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("unexpected-origin handshake = %s, want HTTP 403", describeDialError(err, resp))
		}

		conn, resp, err := dialWebSocket(t, env.server.URL, "/ws/direct", url.Values{
			"ticket": []string{ticket}, "conversationID": []string{direct.ID.Hex()},
		}, allowedWebOrigin)
		if err != nil {
			t.Fatalf("valid origin could not use ticket after rejected origin: %s", describeDialError(err, resp))
		}
		_ = conn.Close()
	})

	t.Run("native clients without Origin are accepted", func(t *testing.T) {
		token := signedAccessToken(t, sender, "sender")
		ticket, statusCode := env.issueTicket(t, token, "")
		if statusCode != http.StatusCreated {
			t.Fatalf("native ticket status = %d, want 201", statusCode)
		}
		conn, resp, err := dialWebSocket(t, env.server.URL, "/ws/direct", url.Values{
			"ticket": []string{ticket}, "conversationID": []string{direct.ID.Hex()},
		}, "")
		if err != nil {
			t.Fatalf("native websocket without Origin rejected: %s", describeDialError(err, resp))
		}
		_ = conn.Close()
	})

	t.Run("ticket is one time", func(t *testing.T) {
		ticket := env.mustIssueTicket(t, signedAccessToken(t, sender, "sender"))
		conn := connectDirect(t, env, ticket, direct.ID.Hex())
		_ = conn.Close()

		_, resp, err := dialWebSocket(t, env.server.URL, "/ws/direct", url.Values{
			"ticket": []string{ticket}, "conversationID": []string{direct.ID.Hex()},
		}, allowedWebOrigin)
		if err == nil {
			t.Fatal("consumed ticket was accepted twice")
		}
		if resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("reused ticket handshake = %s, want HTTP 401", describeDialError(err, resp))
		}
	})

	t.Run("legacy JWT query authentication is rejected", func(t *testing.T) {
		token := signedAccessToken(t, sender, "sender")
		_, resp, err := dialWebSocket(t, env.server.URL, "/ws/direct", url.Values{
			"token": []string{token}, "conversationID": []string{direct.ID.Hex()},
		}, allowedWebOrigin)
		if err == nil {
			t.Fatal("legacy JWT query authentication was accepted")
		}
		if resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("legacy token handshake = %s, want HTTP 401", describeDialError(err, resp))
		}
	})
}

func TestDevelopmentOriginPolicyAllowsAnyBrowserOrigin(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{allowAnyOrigin: true})
	sender := uuid.NewString()
	receiver := uuid.NewString()
	direct := env.seedDirectConversation(t, sender, receiver)

	ticket, statusCode := env.issueTicket(
		t, signedAccessToken(t, sender, "sender"), "http://localhost:5173",
	)
	if statusCode != http.StatusCreated {
		t.Fatalf("development ticket status = %d, want 201", statusCode)
	}
	conn, resp, err := dialWebSocket(t, env.server.URL, "/ws/direct", url.Values{
		"ticket": []string{ticket}, "conversationID": []string{direct.ID.Hex()},
	}, "http://arbitrary.localhost:9999")
	if err != nil {
		t.Fatalf("development arbitrary origin was rejected: %s", describeDialError(err, resp))
	}
	_ = conn.Close()
}

func TestWebSocketTicketExpirationAndAccessTokenLifetime(t *testing.T) {
	t.Run("unused ticket expires", func(t *testing.T) {
		env := newTestEnv(t, testEnvOptions{ticketTTL: 50 * time.Millisecond})
		sender := uuid.NewString()
		receiver := uuid.NewString()
		direct := env.seedDirectConversation(t, sender, receiver)

		ticket := env.mustIssueTicket(t, signedAccessToken(t, sender, "sender"))
		time.Sleep(100 * time.Millisecond)
		_, resp, err := dialWebSocket(t, env.server.URL, "/ws/direct", url.Values{
			"ticket": []string{ticket}, "conversationID": []string{direct.ID.Hex()},
		}, allowedWebOrigin)
		if err == nil {
			t.Fatal("expired ticket was accepted")
		}
		if resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expired ticket handshake = %s, want HTTP 401", describeDialError(err, resp))
		}
	})

	t.Run("socket closes when Core access token expires", func(t *testing.T) {
		env := newTestEnv(t, testEnvOptions{})
		sender := uuid.NewString()
		receiver := uuid.NewString()
		direct := env.seedDirectConversation(t, sender, receiver)

		token := signedAccessTokenWith(t, sender, "sender", coreIssuer, accessTokenUse,
			time.Now().Add(1200*time.Millisecond), gojwt.SigningMethodHS256, integrationJWTSecret)
		conn := connectDirect(t, env, env.mustIssueTicket(t, token), direct.ID.Hex())
		defer conn.Close()

		_ = conn.SetReadDeadline(time.Now().Add(4 * time.Second))
		_, _, err := conn.ReadMessage()
		if err == nil {
			t.Fatal("websocket remained open after access token expiration")
		}
		var closeErr *websocket.CloseError
		if !errors.As(err, &closeErr) || closeErr.Code != 4001 || closeErr.Text != "access_token_expired" {
			t.Fatalf("token expiry close = %v, want code=4001 reason=access_token_expired", err)
		}
	})
}

func TestCoreProtocolBoundary(t *testing.T) {
	t.Run("direct first contact respects Core privacy preference", func(t *testing.T) {
		core := &coreBehavior{AllowDirectMessages: false}
		env := newTestEnv(t, testEnvOptions{core: core})
		sender := uuid.NewString()
		target := uuid.NewString()

		ticket := env.mustIssueTicket(t, signedAccessToken(t, sender, "sender"))
		_, resp, err := dialWebSocket(t, env.server.URL, "/ws/direct", url.Values{
			"ticket": []string{ticket}, "targetID": []string{target},
		}, allowedWebOrigin)
		if err == nil {
			t.Fatal("direct conversation opened despite disabled Core preference")
		}
		if resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("disabled direct preference handshake = %s, want HTTP 403", describeDialError(err, resp))
		}
		assertCollectionCount(t, env.db.Collection(conversation.ConversationsCollection), bson.M{}, 0)
	})

	t.Run("direct privacy lookup fails closed on missing Core user", func(t *testing.T) {
		core := &coreBehavior{MissingUsers: true, AllowDirectMessages: true}
		env := newTestEnv(t, testEnvOptions{core: core})
		sender := uuid.NewString()
		target := uuid.NewString()
		ticket := env.mustIssueTicket(t, signedAccessToken(t, sender, "sender"))

		_, resp, err := dialWebSocket(t, env.server.URL, "/ws/direct", url.Values{
			"ticket": []string{ticket}, "targetID": []string{target},
		}, allowedWebOrigin)
		if err == nil {
			t.Fatal("direct conversation opened with missing Core target")
		}
		if resp == nil || resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("missing Core direct user handshake = %s, want HTTP 500", describeDialError(err, resp))
		}
		assertCollectionCount(t, env.db.Collection(conversation.ConversationsCollection), bson.M{}, 0)
	})

	t.Run("direct privacy lookup fails closed on mismatched Core user", func(t *testing.T) {
		core := &coreBehavior{MismatchedUsersID: uuid.NewString(), AllowDirectMessages: true}
		env := newTestEnv(t, testEnvOptions{core: core})
		sender := uuid.NewString()
		target := uuid.NewString()
		ticket := env.mustIssueTicket(t, signedAccessToken(t, sender, "sender"))

		_, resp, err := dialWebSocket(t, env.server.URL, "/ws/direct", url.Values{
			"ticket": []string{ticket}, "targetID": []string{target},
		}, allowedWebOrigin)
		if err == nil {
			t.Fatal("direct conversation opened with mismatched Core target")
		}
		if resp == nil || resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("mismatched Core direct user handshake = %s, want HTTP 500", describeDialError(err, resp))
		}
		assertCollectionCount(t, env.db.Collection(conversation.ConversationsCollection), bson.M{}, 0)
	})

	t.Run("Core timeout is bounded before WebSocket upgrade", func(t *testing.T) {
		core := &coreBehavior{Delay: 5 * time.Second, AllowDirectMessages: true}
		env := newTestEnv(t, testEnvOptions{core: core})
		sender := uuid.NewString()
		target := uuid.NewString()
		gigID := uuid.NewString()
		ticket := env.mustIssueTicket(t, signedAccessToken(t, sender, "sender"))

		started := time.Now()
		_, resp, err := dialWebSocket(t, env.server.URL, "/ws", url.Values{
			"ticket": []string{ticket}, "targetID": []string{target}, "gigID": []string{gigID},
		}, allowedWebOrigin)
		elapsed := time.Since(started)
		if err == nil {
			t.Fatal("WebSocket upgraded while Core was timing out")
		}
		if resp == nil || resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("Core timeout handshake = %s, want HTTP 500", describeDialError(err, resp))
		}
		if elapsed > 4*time.Second {
			t.Fatalf("Core failure was not bounded by Chat timeout: %s", elapsed)
		}
		assertCollectionCount(t, env.db.Collection(application.ApplicationsCollection), bson.M{}, 0)
	})

	t.Run("Core unavailable fails closed", func(t *testing.T) {
		core := &coreBehavior{ErrorCode: codes.Unavailable}
		env := newTestEnv(t, testEnvOptions{core: core})
		sender := uuid.NewString()
		target := uuid.NewString()
		gigID := uuid.NewString()
		ticket := env.mustIssueTicket(t, signedAccessToken(t, sender, "sender"))

		_, resp, err := dialWebSocket(t, env.server.URL, "/ws", url.Values{
			"ticket": []string{ticket}, "targetID": []string{target}, "gigID": []string{gigID},
		}, allowedWebOrigin)
		if err == nil {
			t.Fatal("WebSocket upgraded while Core was unavailable")
		}
		if resp == nil || resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("Core unavailable handshake = %s, want HTTP 500", describeDialError(err, resp))
		}
	})

	t.Run("mismatched Core user response fails closed", func(t *testing.T) {
		core := &coreBehavior{AllowDirectMessages: true, MismatchedUserID: uuid.NewString()}
		env := newTestEnv(t, testEnvOptions{core: core})
		sender := uuid.NewString()
		target := uuid.NewString()
		gigID := uuid.NewString()
		ticket := env.mustIssueTicket(t, signedAccessToken(t, sender, "sender"))

		_, resp, err := dialWebSocket(t, env.server.URL, "/ws", url.Values{
			"ticket": []string{ticket}, "targetID": []string{target}, "gigID": []string{gigID},
		}, allowedWebOrigin)
		if err == nil {
			t.Fatal("WebSocket accepted mismatched Core user identity")
		}
		if resp == nil || resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("mismatched Core user handshake = %s, want HTTP 500", describeDialError(err, resp))
		}
		assertCollectionCount(t, env.db.Collection(application.ApplicationsCollection), bson.M{}, 0)
	})

	t.Run("missing Core gig fails closed", func(t *testing.T) {
		core := &coreBehavior{AllowDirectMessages: true, MissingGig: true}
		env := newTestEnv(t, testEnvOptions{core: core})
		sender := uuid.NewString()
		target := uuid.NewString()
		gigID := uuid.NewString()
		ticket := env.mustIssueTicket(t, signedAccessToken(t, sender, "sender"))

		_, resp, err := dialWebSocket(t, env.server.URL, "/ws", url.Values{
			"ticket": []string{ticket}, "targetID": []string{target}, "gigID": []string{gigID},
		}, allowedWebOrigin)
		if err == nil {
			t.Fatal("WebSocket accepted missing Core gig")
		}
		if resp == nil || resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("missing Core gig handshake = %s, want HTTP 500", describeDialError(err, resp))
		}
	})

	t.Run("missing Core user fails closed without panic", func(t *testing.T) {
		core := &coreBehavior{MalformedUser: true}
		env := newTestEnv(t, testEnvOptions{core: core})
		sender := uuid.NewString()
		target := uuid.NewString()
		gigID := uuid.NewString()
		ticket := env.mustIssueTicket(t, signedAccessToken(t, sender, "sender"))

		_, resp, err := dialWebSocket(t, env.server.URL, "/ws", url.Values{
			"ticket": []string{ticket}, "targetID": []string{target}, "gigID": []string{gigID},
		}, allowedWebOrigin)
		if err == nil {
			t.Fatal("WebSocket accepted missing Core user")
		}
		if resp == nil || resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("missing Core user handshake = %s, want HTTP 500", describeDialError(err, resp))
		}
	})
}

func TestApplicationThreadCannotBeResumedByNonParticipant(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{})
	initiator := uuid.NewString()
	target := uuid.NewString()
	stranger := uuid.NewString()
	app := env.seedApplication(t, initiator, target, uuid.NewString(), application.ApplicationStatusAccepted)

	ticket := env.mustIssueTicket(t, signedAccessToken(t, stranger, "stranger"))
	_, resp, err := dialWebSocket(t, env.server.URL, "/ws", url.Values{
		"ticket": []string{ticket}, "threadID": []string{app.ID.Hex()},
	}, allowedWebOrigin)
	if err == nil {
		t.Fatal("non-participant resumed private application thread")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-participant thread resume = %s, want HTTP 403", describeDialError(err, resp))
	}
}

func TestDirectFirstContactCreatesConversationWhenCoreAllowsIt(t *testing.T) {
	core := &coreBehavior{AllowDirectMessages: true}
	env := newTestEnv(t, testEnvOptions{core: core})
	sender := uuid.NewString()
	target := uuid.NewString()

	ticket := env.mustIssueTicket(t, signedAccessToken(t, sender, "sender"))
	conn, resp, err := dialWebSocket(t, env.server.URL, "/ws/direct", url.Values{
		"ticket": []string{ticket}, "targetID": []string{target},
	}, allowedWebOrigin)
	if err != nil {
		t.Fatalf("direct first-contact handshake failed: %s", describeDialError(err, resp))
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]any{
		"client_message_id": "direct-first-contact",
		"content":           "hello",
	}); err != nil {
		t.Fatal(err)
	}
	ack := readAccepted(t, conn)
	if ack.ConversationID == "" {
		t.Fatalf("first direct message did not create conversation: %+v", ack)
	}
	assertCollectionCount(t, env.db.Collection(conversation.ConversationsCollection), bson.M{"_id": mustObjectID(t, ack.ConversationID)}, 1)
	assertCollectionCount(t, env.db.Collection(conversation.MessagesCollection), bson.M{"client_message_id": "direct-first-contact"}, 1)
}

func TestCoreFailureDoesNotConsumeUnrelatedMongoState(t *testing.T) {
	// This test guards against future resolver changes accidentally mutating
	// persistence before external identity/gig validation completes.
	core := &coreBehavior{ErrorCode: codes.Unavailable}
	env := newTestEnv(t, testEnvOptions{core: core})
	existingA := uuid.NewString()
	existingB := uuid.NewString()
	env.seedDirectConversation(t, existingA, existingB)

	sender := uuid.NewString()
	target := uuid.NewString()
	ticket := env.mustIssueTicket(t, signedAccessToken(t, sender, "sender"))
	_, _, _ = dialWebSocket(t, env.server.URL, "/ws", url.Values{
		"ticket": []string{ticket}, "targetID": []string{target}, "gigID": []string{uuid.NewString()},
	}, allowedWebOrigin)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	count, err := env.db.Collection(conversation.ConversationsCollection).CountDocuments(ctx, bson.M{})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("Core failure mutated unrelated Mongo state: conversation count=%d", count)
	}
}

func TestDirectConversationCannotBeResumedByNonParticipant(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{})
	memberA := uuid.NewString()
	memberB := uuid.NewString()
	stranger := uuid.NewString()
	direct := env.seedDirectConversation(t, memberA, memberB)

	ticket := env.mustIssueTicket(t, signedAccessToken(t, stranger, "stranger"))
	_, resp, err := dialWebSocket(t, env.server.URL, "/ws/direct", url.Values{
		"ticket": []string{ticket}, "conversationID": []string{direct.ID.Hex()},
	}, allowedWebOrigin)
	if err == nil {
		t.Fatal("non-participant resumed private direct conversation")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-participant direct resume = %s, want HTTP 403", describeDialError(err, resp))
	}
	assertCollectionCount(t, env.db.Collection(conversation.MessagesCollection), bson.M{}, 0)
}
