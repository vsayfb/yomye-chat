package ws

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/vsayfb/gig-platform-chat-service/internal/application"
	"github.com/vsayfb/gig-platform-chat-service/internal/attachment"
	"github.com/vsayfb/gig-platform-chat-service/internal/conversation"
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
