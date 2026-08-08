package application

import (
	"context"
	"testing"
	"time"

	"github.com/vsayfb/gig-platform-chat-service/internal/messagecursor"
	"go.mongodb.org/mongo-driver/v2/bson"
)

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

func TestInitialApplicationMessageRetryReturnsPersistedMessage(t *testing.T) {
	t.Parallel()

	repo := newIdempotencyRepository()
	service := NewApplicationService(repo)
	ctx := context.Background()

	first, newThread, replayed, err := service.SendMessage(
		ctx,
		"worker",
		"employer",
		"gig-1",
		"client-retry",
		"hello",
		nil,
	)
	if err != nil {
		t.Fatalf("first SendMessage() error = %v", err)
	}
	if !newThread || replayed {
		t.Fatalf("first send flags: newThread=%v replayed=%v", newThread, replayed)
	}

	second, newThread, replayed, err := service.SendMessage(
		ctx,
		"worker",
		"employer",
		"gig-1",
		"client-retry",
		"hello",
		nil,
	)
	if err != nil {
		t.Fatalf("retry SendMessage() error = %v", err)
	}
	if newThread || !replayed {
		t.Fatalf("retry flags: newThread=%v replayed=%v", newThread, replayed)
	}
	if first.ID != second.ID {
		t.Fatalf("retry message ID = %s, want %s", second.ID, first.ID)
	}
	if len(repo.messages) != 1 {
		t.Fatalf("message count = %d, want 1", len(repo.messages))
	}
	if repo.app == nil || repo.app.Status != ApplicationStatusPending {
		t.Fatalf("application state = %#v, want pending", repo.app)
	}
}

type idempotencyRepository struct {
	app               *Application
	applications      []Application
	messages          map[string]*Message
	listedMessages    []Message
	listMessagesCalls int
	updateErr         error
	createMessageErr  error
	deleteCalls       int
}

func newIdempotencyRepository() *idempotencyRepository {
	return &idempotencyRepository{messages: make(map[string]*Message)}
}

func (r *idempotencyRepository) EnsureIndexes(context.Context) error { return nil }

func (r *idempotencyRepository) FindApplicationByParticipants(
	_ context.Context,
	gigID string,
	userA string,
	userB string,
) (*Application, error) {
	if r.app == nil || r.app.GigID != gigID {
		return nil, nil
	}
	matches := (r.app.InitiatorID == userA && r.app.TargetID == userB) ||
		(r.app.InitiatorID == userB && r.app.TargetID == userA)
	if !matches {
		return nil, nil
	}
	copy := *r.app
	return &copy, nil
}

func (r *idempotencyRepository) FindApplicationByID(
	_ context.Context,
	threadID bson.ObjectID,
) (*Application, error) {
	if r.app == nil || r.app.ID != threadID {
		return nil, nil
	}
	copy := *r.app
	return &copy, nil
}

func (r *idempotencyRepository) ListApplications(context.Context, string) ([]Application, error) {
	return r.applications, nil
}

func (r *idempotencyRepository) CreateApplication(_ context.Context, app *Application) error {
	if app.ID.IsZero() {
		app.ID = bson.NewObjectID()
	}
	copy := *app
	r.app = &copy
	return nil
}

func (r *idempotencyRepository) DeleteApplication(_ context.Context, threadID bson.ObjectID) error {
	r.deleteCalls++
	if r.app != nil && r.app.ID == threadID {
		r.app = nil
	}
	return nil
}

func (r *idempotencyRepository) TransitionApplicationStatus(
	context.Context,
	bson.ObjectID,
	string,
	ApplicationStatus,
	ApplicationStatus,
) (bool, error) {
	return false, nil
}

func (r *idempotencyRepository) UpdateLastMessage(
	_ context.Context,
	threadID bson.ObjectID,
	content string,
	sentAt time.Time,
) error {
	if r.updateErr != nil {
		return r.updateErr
	}
	if r.app != nil && r.app.ID == threadID {
		r.app.LastMessage = content
		r.app.LastMessageAt = sentAt
	}
	return nil
}

func (r *idempotencyRepository) FindMessageByClientMessageID(
	_ context.Context,
	senderID string,
	clientMessageID string,
) (*Message, error) {
	return r.messages[senderID+"\x00"+clientMessageID], nil
}

func (r *idempotencyRepository) CreateOrGetMessage(
	_ context.Context,
	message *Message,
) (*Message, bool, error) {
	if r.createMessageErr != nil {
		return nil, false, r.createMessageErr
	}
	key := message.SenderID + "\x00" + message.ClientMessageID
	if existing := r.messages[key]; existing != nil {
		return existing, false, nil
	}
	if message.ID.IsZero() {
		message.ID = bson.NewObjectID()
	}
	r.messages[key] = message
	return message, true, nil
}

func (r *idempotencyRepository) ListMessages(
	context.Context,
	bson.ObjectID,
	int,
	*messagecursor.Cursor,
) ([]Message, error) {
	r.listMessagesCalls++
	return r.listedMessages, nil
}
