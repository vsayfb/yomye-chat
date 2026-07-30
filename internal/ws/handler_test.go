package ws

import (
	"strings"
	"testing"
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
