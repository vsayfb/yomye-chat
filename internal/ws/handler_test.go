package ws

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gojwt "github.com/golang-jwt/jwt/v5"
	"github.com/vsayfb/gig-platform-chat-service/internal/application"
	"github.com/vsayfb/gig-platform-chat-service/internal/attachment"
	"github.com/vsayfb/gig-platform-chat-service/internal/conversation"
	"github.com/vsayfb/gig-platform-chat-service/internal/wsticket"
	chatjwt "github.com/vsayfb/gig-platform-chat-service/pkg/jwt"
	"github.com/vsayfb/gig-platform-chat-service/pkg/middleware"
)

func TestNotificationPreviewDoesNotExposeMascotToken(t *testing.T) {
	t.Parallel()

	preview := notificationPreview("Hello :yovmiye_happy:", 100)
	if strings.Contains(preview, ":yovmiye_") {
		t.Fatalf("notificationPreview() exposed raw mascot token: %q", preview)
	}
	if preview != "Hello [Yövmiye emojisi]" {
		t.Fatalf("notificationPreview() = %q", preview)
	}
}

func TestApplicationMessageErrorMappings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err  error
		code string
	}{
		{application.ErrApplicationPending, "application_pending"},
		{application.ErrApplicationMustBeApproved, "application_must_be_approved"},
		{application.ErrApplicationRejected, "application_rejected"},
		{application.ErrApplicationArchived, "application_archived"},
		{application.ErrForbidden, "forbidden"},
		{application.ErrInvalidApplicationStatus, "invalid_application_status"},
		{application.ErrApplicationNotFound, "application_not_found"},
		{errors.New("database unavailable"), "send_failed"},
	}

	for _, test := range tests {
		code, message := applicationMessageError(test.err)
		if code != test.code {
			t.Errorf("applicationMessageError(%v) code = %q, want %q", test.err, code, test.code)
		}
		if message == "" {
			t.Errorf("applicationMessageError(%v) returned an empty message", test.err)
		}
	}
}

func TestDirectMessageErrorMappings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err  error
		code string
	}{
		{conversation.ErrDirectMessagesDisabled, "direct_messages_disabled"},
		{conversation.ErrForbidden, "forbidden"},
		{conversation.ErrNotFound, "conversation_not_found"},
		{conversation.ErrInvalidDirectConversation, "invalid_conversation"},
		{errors.New("database unavailable"), "send_failed"},
	}

	for _, test := range tests {
		code, message := directMessageError(test.err)
		if code != test.code {
			t.Errorf("directMessageError(%v) code = %q, want %q", test.err, code, test.code)
		}
		if message == "" {
			t.Errorf("directMessageError(%v) returned an empty message", test.err)
		}
	}
}

func TestAcceptedPayloadCorrelatesClientMessage(t *testing.T) {
	t.Parallel()

	payload, err := json.Marshal(outgoingAccepted{
		Type:            "message.accepted",
		ClientMessageID: "client-123",
		MessageID:       "message-456",
		ConversationID:  "conversation-789",
		SentAt:          "2026-07-29T12:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}

	text := string(payload)
	for _, expected := range []string{
		`"type":"message.accepted"`,
		`"client_message_id":"client-123"`,
		`"message_id":"message-456"`,
		`"conversation_id":"conversation-789"`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("accepted payload %s does not contain %s", text, expected)
		}
	}
}

func TestParseAttachmentIDs(t *testing.T) {
	t.Parallel()

	first := "64b64c8e4f1a2b3c4d5e6f70"
	second := "64b64c8e4f1a2b3c4d5e6f71"
	ids, err := parseAttachmentIDs([]string{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0].Hex() != first || ids[1].Hex() != second {
		t.Fatalf("parseAttachmentIDs() = %v", ids)
	}

	if _, err := parseAttachmentIDs([]string{first, first}); !errors.Is(err, attachment.ErrDuplicate) {
		t.Fatalf("duplicate error = %v, want ErrDuplicate", err)
	}
	if _, err := parseAttachmentIDs([]string{first, second, first}); !errors.Is(err, attachment.ErrTooMany) {
		t.Fatalf("limit error = %v, want ErrTooMany", err)
	}
	if _, err := parseAttachmentIDs([]string{"not-an-id"}); !errors.Is(err, attachment.ErrInvalidInput) {
		t.Fatalf("invalid ID error = %v, want ErrInvalidInput", err)
	}
}

func TestNotificationPreviewTruncatesAfterReplacingMascotToken(t *testing.T) {
	t.Parallel()

	preview := notificationPreview(":yovmiye_happy: says hello", 20)
	if got := len([]rune(preview)); got > 20 {
		t.Fatalf("notificationPreview() rune length = %d, want <= 20", got)
	}
	if strings.Contains(preview, ":yovmiye_") {
		t.Fatalf("notificationPreview() exposed raw mascot token: %q", preview)
	}
}

func TestValidateMessageContentEnforcesLogicalLength(t *testing.T) {
	t.Parallel()

	if code, _, ok := validateMessageContent(strings.Repeat("a", maxMessageLogicalLength)); !ok || code != "" {
		t.Fatalf("max-length message rejected: code=%q ok=%v", code, ok)
	}

	code, message, ok := validateMessageContent(strings.Repeat("a", maxMessageLogicalLength+1))
	if ok {
		t.Fatal("over-limit message was accepted")
	}
	if code != "message_too_long" {
		t.Fatalf("over-limit code = %q, want message_too_long", code)
	}
	if message == "" {
		t.Fatal("over-limit message is empty")
	}
}

func TestValidateMessageContentStillValidatesMascotTokens(t *testing.T) {
	t.Parallel()

	code, _, ok := validateMessageContent("hello :yovmiye_unknown:")
	if ok {
		t.Fatal("unsupported mascot token was accepted")
	}
	if code != "invalid_emoji" {
		t.Fatalf("invalid mascot code = %q, want invalid_emoji", code)
	}
}

func TestValidateClientMessageID(t *testing.T) {
	t.Parallel()

	if code, _, ok := validateClientMessageID("client-123"); !ok || code != "" {
		t.Fatalf("valid client_message_id rejected: code=%q ok=%v", code, ok)
	}

	if code, _, ok := validateClientMessageID(""); ok || code != "invalid_client_message_id" {
		t.Fatalf("empty client_message_id: code=%q ok=%v", code, ok)
	}

	tooLong := strings.Repeat("a", maxClientMessageIDLength+1)
	if code, _, ok := validateClientMessageID(tooLong); ok || code != "invalid_client_message_id" {
		t.Fatalf("oversized client_message_id: code=%q ok=%v", code, ok)
	}
}

func TestOriginPolicyAllowsAnyOriginInDevelopment(t *testing.T) {
	t.Parallel()

	h := &WSHandler{allowAnyOrigin: true}
	r := httptest.NewRequest("GET", "http://chat/ws", nil)
	r.Header.Set("Origin", "https://untrusted.example")
	if !h.originAllowed(r) {
		t.Fatal("development origin policy rejected an origin")
	}
}

func TestOriginPolicyEnforcesConfiguredBrowserOrigins(t *testing.T) {
	t.Parallel()

	h := &WSHandler{allowedOrigins: map[string]struct{}{
		"https://staging.yevmiye.com": {},
	}}

	allowed := httptest.NewRequest("GET", "http://chat/ws", nil)
	allowed.Header.Set("Origin", "https://staging.yevmiye.com")
	if !h.originAllowed(allowed) {
		t.Fatal("configured browser origin was rejected")
	}

	rejected := httptest.NewRequest("GET", "http://chat/ws", nil)
	rejected.Header.Set("Origin", "https://evil.example")
	if h.originAllowed(rejected) {
		t.Fatal("unconfigured browser origin was accepted")
	}

	native := httptest.NewRequest("GET", "http://chat/ws", nil)
	if !h.originAllowed(native) {
		t.Fatal("native request without Origin was rejected")
	}
}

func TestAuthenticateTicketConsumesTicketOnce(t *testing.T) {
	t.Parallel()

	store := wsticket.NewStore(time.Minute)
	claims := &chatjwt.Claims{RegisteredClaims: gojwt.RegisteredClaims{
		Subject:   "9e2a9df0-ea28-433a-8b8b-920cb86eb62d",
		ExpiresAt: gojwt.NewNumericDate(time.Now().Add(time.Hour)),
	}}
	ticket, _, err := store.Issue(claims)
	if err != nil {
		t.Fatal(err)
	}

	h := &WSHandler{allowAnyOrigin: true, tickets: store}
	request := httptest.NewRequest("GET", "http://chat/ws?ticket="+ticket, nil)
	response := httptest.NewRecorder()
	got, ok := h.authenticateTicket(response, request)
	if !ok || got.Subject != claims.Subject {
		t.Fatalf("authenticateTicket() = (%+v, %v)", got, ok)
	}

	response = httptest.NewRecorder()
	if _, ok := h.authenticateTicket(response, request); ok {
		t.Fatal("consumed ticket was accepted twice")
	}
	if response.Code != 401 {
		t.Fatalf("second ticket use status = %d, want 401", response.Code)
	}
}

func TestAuthenticateTicketRejectsLegacyJWTQueryParameter(t *testing.T) {
	t.Parallel()

	h := &WSHandler{allowAnyOrigin: true, tickets: wsticket.NewStore(time.Minute)}
	request := httptest.NewRequest("GET", "http://chat/ws?token=legacy-jwt", nil)
	response := httptest.NewRecorder()
	if _, ok := h.authenticateTicket(response, request); ok {
		t.Fatal("legacy token query parameter was accepted")
	}
	if response.Code != 401 {
		t.Fatalf("legacy token status = %d, want 401", response.Code)
	}
}

func TestIssueTicketReturnsNoStoreOneTimeCredential(t *testing.T) {
	t.Parallel()

	store := wsticket.NewStore(time.Minute)
	h := &WSHandler{allowAnyOrigin: true, tickets: store}
	claims := &chatjwt.Claims{RegisteredClaims: gojwt.RegisteredClaims{
		Subject:   "9e2a9df0-ea28-433a-8b8b-920cb86eb62d",
		ExpiresAt: gojwt.NewNumericDate(time.Now().Add(time.Hour)),
	}}
	ctx := context.WithValue(context.Background(), middleware.ClaimsKey, claims)
	request := httptest.NewRequest("POST", "http://chat/ws/ticket", nil).WithContext(ctx)
	response := httptest.NewRecorder()

	h.IssueTicket(response, request)
	if response.Code != 201 {
		t.Fatalf("IssueTicket() status = %d, want 201", response.Code)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}

	var payload struct {
		Data ticketResponse `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data.Ticket) != 64 || payload.Data.ExpiresAt == "" {
		t.Fatalf("ticket response = %+v", payload.Data)
	}
	if _, err := store.Consume(payload.Data.Ticket); err != nil {
		t.Fatalf("issued ticket was not consumable: %v", err)
	}
}
