package application

import "testing"

func TestConversationKeyIsIndependentOfParticipantOrder(t *testing.T) {
	t.Parallel()

	forward := conversationKey("gig-1", "user-a", "user-b")
	reverse := conversationKey("gig-1", "user-b", "user-a")

	if forward != reverse {
		t.Fatalf("conversation key depends on participant order: %q != %q", forward, reverse)
	}
}

func TestConversationKeyIncludesGig(t *testing.T) {
	t.Parallel()

	first := conversationKey("gig-1", "user-a", "user-b")
	second := conversationKey("gig-2", "user-a", "user-b")

	if first == second {
		t.Fatal("conversation key must distinguish gigs")
	}
}
