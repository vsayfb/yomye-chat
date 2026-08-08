package conversation

import (
	"context"
	"testing"
	"time"

	"github.com/vsayfb/gig-platform-chat-service/internal/messagecursor"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestSendDirectMessageCreatesAndReusesConversation(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	policy := &fakeCreationPolicy{allowed: true}
	service := NewService(repo, policy)
	ctx := context.Background()

	first, created, err := service.SendDirectMessage(ctx, "user-b", "user-a", "hello", nil)
	if err != nil {
		t.Fatalf("first SendDirectMessage() error = %v", err)
	}
	if !created {
		t.Fatal("first SendDirectMessage() did not create a conversation")
	}

	second, created, err := service.SendDirectMessage(ctx, "user-a", "user-b", "hi", nil)
	if err != nil {
		t.Fatalf("second SendDirectMessage() error = %v", err)
	}
	if created {
		t.Fatal("second SendDirectMessage() created a duplicate conversation")
	}
	if first.ConversationID != second.ConversationID {
		t.Fatalf("conversation IDs differ: %s != %s", first.ConversationID, second.ConversationID)
	}
	if len(repo.conversations) != 1 {
		t.Fatalf("conversation count = %d, want 1", len(repo.conversations))
	}
	if len(repo.messages) != 2 {
		t.Fatalf("message count = %d, want 2", len(repo.messages))
	}
	if policy.calls != 1 {
		t.Fatalf("creation policy calls = %d, want 1", policy.calls)
	}
}

func TestSendDirectMessageRejectsOptedOutTargetBeforeCreation(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	policy := &fakeCreationPolicy{allowed: false}
	service := NewService(repo, policy)

	_, _, err := service.SendDirectMessage(
		context.Background(),
		"user-a",
		"user-b",
		"hello",
		nil,
	)
	if err != ErrDirectMessagesDisabled {
		t.Fatalf("SendDirectMessage() error = %v, want ErrDirectMessagesDisabled", err)
	}
	if len(repo.conversations) != 0 || len(repo.messages) != 0 {
		t.Fatal("opted-out target caused content to be persisted")
	}
}

func TestGetRequiresActiveMembership(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	item := &Conversation{
		ID:   bson.NewObjectID(),
		Type: TypeDirect,
		Members: []Member{
			{UserID: "user-a", Role: RoleMember},
			{UserID: "user-b", Role: RoleMember},
		},
	}
	repo.conversations[item.ID] = item

	_, err := NewService(repo, &fakeCreationPolicy{allowed: true}).
		Get(context.Background(), item.ID, "outsider")
	if err != ErrForbidden {
		t.Fatalf("Get() error = %v, want ErrForbidden", err)
	}
}

type fakeCreationPolicy struct {
	allowed bool
	err     error
	calls   int
}

func (p *fakeCreationPolicy) AllowsSocialDirectMessages(
	context.Context,
	string,
) (bool, error) {
	p.calls++
	return p.allowed, p.err
}

func TestOtherDirectMember(t *testing.T) {
	t.Parallel()

	item := &Conversation{
		Type: TypeDirect,
		Members: []Member{
			{UserID: "user-a", Role: RoleMember},
			{UserID: "user-b", Role: RoleMember},
		},
	}

	got, err := OtherDirectMember(item, "user-a")
	if err != nil {
		t.Fatalf("OtherDirectMember() error = %v", err)
	}
	if got != "user-b" {
		t.Fatalf("OtherDirectMember() = %q, want user-b", got)
	}
}

func TestOtherDirectMemberRejectsNonMember(t *testing.T) {
	t.Parallel()

	item := &Conversation{
		Type: TypeDirect,
		Members: []Member{
			{UserID: "user-a", Role: RoleMember},
			{UserID: "user-b", Role: RoleMember},
		},
	}

	if _, err := OtherDirectMember(item, "outsider"); err != ErrForbidden {
		t.Fatalf("OtherDirectMember() error = %v, want ErrForbidden", err)
	}
}

type fakeRepository struct {
	conversations map[bson.ObjectID]*Conversation
	byDirectKey   map[string]*Conversation
	messages      []*Message
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{
		conversations: make(map[bson.ObjectID]*Conversation),
		byDirectKey:   make(map[string]*Conversation),
	}
}

func (r *fakeRepository) EnsureIndexes(context.Context) error { return nil }

func (r *fakeRepository) FindDirectByKey(
	_ context.Context,
	key string,
) (*Conversation, error) {
	return r.byDirectKey[key], nil
}

func (r *fakeRepository) FindByID(
	_ context.Context,
	id bson.ObjectID,
) (*Conversation, error) {
	return r.conversations[id], nil
}

func (r *fakeRepository) ListForUser(
	_ context.Context,
	userID string,
) ([]Conversation, error) {
	var result []Conversation
	for _, item := range r.conversations {
		if isActiveMember(item, userID) {
			result = append(result, *item)
		}
	}
	return result, nil
}

func (r *fakeRepository) Create(_ context.Context, item *Conversation) error {
	if item.ID.IsZero() {
		item.ID = bson.NewObjectID()
	}
	r.conversations[item.ID] = item
	r.byDirectKey[item.DirectKey] = item
	return nil
}

func (r *fakeRepository) UpdateLastMessage(
	_ context.Context,
	id bson.ObjectID,
	content string,
	sentAt time.Time,
) error {
	item := r.conversations[id]
	item.LastMessage = content
	item.LastMessageAt = sentAt
	return nil
}

func (r *fakeRepository) CreateMessage(_ context.Context, message *Message) error {
	if message.ID.IsZero() {
		message.ID = bson.NewObjectID()
	}
	r.messages = append(r.messages, message)
	return nil
}

func (r *fakeRepository) ListMessages(
	_ context.Context,
	conversationID bson.ObjectID,
	limit int,
	_ *messagecursor.Cursor,
) ([]Message, error) {
	var result []Message
	for _, message := range r.messages {
		if message.ConversationID == conversationID {
			result = append(result, *message)
		}
	}
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}
