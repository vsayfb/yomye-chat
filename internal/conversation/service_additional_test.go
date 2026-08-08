package conversation

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestSendDirectMessageRejectsInvalidParticipantsBeforePolicyOrPersistence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		senderID string
		targetID string
	}{
		{name: "missing sender", targetID: "user-b"},
		{name: "missing target", senderID: "user-a"},
		{name: "self message", senderID: "user-a", targetID: "user-a"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := newFakeRepository()
			policy := &fakeCreationPolicy{allowed: true}
			service := NewService(repo, policy)

			_, _, _, err := service.SendDirectMessage(
				context.Background(), tt.senderID, tt.targetID, "client-1", "hello", nil,
			)
			if !errors.Is(err, ErrInvalidDirectConversation) {
				t.Fatalf("SendDirectMessage() error = %v, want ErrInvalidDirectConversation", err)
			}
			if policy.calls != 0 || len(repo.conversations) != 0 || len(repo.messages) != 0 {
				t.Fatal("invalid participants reached policy or persistence")
			}
		})
	}
}

func TestListMessagesAuthorizesBeforeReadingMessages(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	id := bson.NewObjectID()
	repo.conversations[id] = &Conversation{
		ID: id, Type: TypeDirect,
		Members: []Member{{UserID: "user-a"}, {UserID: "user-b"}},
	}
	service := NewService(repo, &fakeCreationPolicy{allowed: true})

	if _, err := service.ListMessages(context.Background(), "outsider", id, 50, nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ListMessages() error = %v, want ErrForbidden", err)
	}
	if repo.listMessagesCalls != 0 {
		t.Fatal("message repository was queried before membership authorization")
	}
}

func TestDirectConversationExistsFailsClosedForCorruptMembership(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		item *Conversation
	}{
		{
			name: "wrong type",
			item: &Conversation{Type: Type("group"), Members: []Member{{UserID: "user-a"}, {UserID: "user-b"}}},
		},
		{
			name: "missing participant",
			item: &Conversation{Type: TypeDirect, Members: []Member{{UserID: "user-a"}}},
		},
		{
			name: "participant left",
			item: func() *Conversation {
				left := timePointerForConversation()
				return &Conversation{Type: TypeDirect, Members: []Member{{UserID: "user-a"}, {UserID: "user-b", LeftAt: &left}}}
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := newFakeRepository()
			repo.byDirectKey[directKey("user-a", "user-b")] = tt.item
			exists, err := NewService(repo, &fakeCreationPolicy{allowed: true}).DirectConversationExists(
				context.Background(), "user-a", "user-b",
			)
			if exists || !errors.Is(err, ErrForbidden) {
				t.Fatalf("DirectConversationExists() exists=%v err=%v, want forbidden", exists, err)
			}
		})
	}
}

func TestDirectKeyIsCanonicalAndBoundarySafe(t *testing.T) {
	t.Parallel()

	if directKey("user-b", "user-a") != directKey("user-a", "user-b") {
		t.Fatal("directKey depends on participant order")
	}
	if directKey("ab", "c") == directKey("a", "bc") {
		t.Fatal("directKey is ambiguous across participant boundaries")
	}
}

func TestListReturnsOnlySummaryFieldsFromRepository(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	id := bson.NewObjectID()
	repo.conversations[id] = &Conversation{
		ID: id, Type: TypeDirect, LastMessage: "hello",
		Members: []Member{{UserID: "user-a"}, {UserID: "user-b"}},
	}

	items, err := NewService(repo, &fakeCreationPolicy{allowed: true}).List(context.Background(), "user-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != id || items[0].LastMessage != "hello" {
		t.Fatalf("List() = %#v", items)
	}
}

func timePointerForConversation() time.Time {
	return time.Unix(1, 0).UTC()
}
