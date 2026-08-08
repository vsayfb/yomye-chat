package application

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vsayfb/gig-platform-chat-service/internal/messagecursor"
	"go.mongodb.org/mongo-driver/v2/bson"
)

type transitionRepository struct {
	mu  sync.Mutex
	app *Application
}

func (r *transitionRepository) EnsureIndexes(context.Context) error { return nil }

func (r *transitionRepository) FindApplicationByParticipants(
	context.Context,
	string,
	string,
	string,
) (*Application, error) {
	return nil, nil
}

func (r *transitionRepository) FindApplicationByID(
	_ context.Context,
	threadID bson.ObjectID,
) (*Application, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.app == nil || r.app.ID != threadID {
		return nil, nil
	}
	copy := *r.app
	return &copy, nil
}

func (r *transitionRepository) ListApplications(context.Context, string) ([]Application, error) {
	return nil, nil
}

func (r *transitionRepository) CreateApplication(context.Context, *Application) error  { return nil }
func (r *transitionRepository) DeleteApplication(context.Context, bson.ObjectID) error { return nil }

func (r *transitionRepository) TransitionApplicationStatus(
	_ context.Context,
	threadID bson.ObjectID,
	targetID string,
	from ApplicationStatus,
	to ApplicationStatus,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.app == nil ||
		r.app.ID != threadID ||
		r.app.TargetID != targetID ||
		r.app.Status != from {
		return false, nil
	}
	r.app.Status = to
	return true, nil
}

func (r *transitionRepository) UpdateLastMessage(
	context.Context,
	bson.ObjectID,
	string,
	time.Time,
) error {
	return nil
}

func (r *transitionRepository) CreateMessage(context.Context, *Message) error { return nil }

func (r *transitionRepository) ListMessages(
	context.Context,
	bson.ObjectID,
	int,
	*messagecursor.Cursor,
) ([]Message, error) {
	return nil, nil
}

func TestApplicationTransitionIsCompareAndSet(t *testing.T) {
	t.Parallel()

	threadID := bson.NewObjectID()
	repo := &transitionRepository{app: &Application{
		ID:       threadID,
		TargetID: "target",
		Status:   ApplicationStatusPending,
	}}
	service := NewApplicationService(repo)

	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		results <- service.ApproveApplication(context.Background(), threadID, "target")
	}()
	go func() {
		<-start
		results <- service.RejectApplication(context.Background(), threadID, "target")
	}()
	close(start)

	var succeeded, conflicted int
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrInvalidApplicationStatus):
			conflicted++
		default:
			t.Fatalf("unexpected transition error: %v", err)
		}
	}

	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent transitions: success=%d conflict=%d", succeeded, conflicted)
	}
}

func TestApplicationTransitionChecksTargetInAtomicPredicate(t *testing.T) {
	t.Parallel()

	threadID := bson.NewObjectID()
	repo := &transitionRepository{app: &Application{
		ID:       threadID,
		TargetID: "target",
		Status:   ApplicationStatusPending,
	}}
	service := NewApplicationService(repo)

	err := service.ApproveApplication(context.Background(), threadID, "other-user")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("ApproveApplication() error = %v, want ErrForbidden", err)
	}

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if repo.app.Status != ApplicationStatusPending {
		t.Fatalf("forbidden transition changed status to %q", repo.app.Status)
	}
}
