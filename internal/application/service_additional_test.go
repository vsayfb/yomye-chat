package application

import (
	"context"
	"errors"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestGetApplicationRequiresParticipant(t *testing.T) {
	t.Parallel()

	repo := newIdempotencyRepository()
	threadID := bson.NewObjectID()
	repo.app = &Application{ID: threadID, InitiatorID: "worker", TargetID: "employer"}
	service := NewApplicationService(repo)

	if _, err := service.GetApplication(context.Background(), threadID, "outsider"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("GetApplication() error = %v, want ErrForbidden", err)
	}
	got, err := service.GetApplication(context.Background(), threadID, "worker")
	if err != nil || got.ID != threadID {
		t.Fatalf("GetApplication() = %#v, err=%v", got, err)
	}
	if _, err := service.GetApplication(context.Background(), bson.NewObjectID(), "worker"); !errors.Is(err, ErrApplicationNotFound) {
		t.Fatalf("missing GetApplication() error = %v, want ErrApplicationNotFound", err)
	}
}

func TestApplicationExistsUsesGigAndUnorderedParticipants(t *testing.T) {
	t.Parallel()

	repo := newIdempotencyRepository()
	repo.app = &Application{
		GigID: "gig-1", InitiatorID: "worker", TargetID: "employer",
	}
	service := NewApplicationService(repo)

	exists, err := service.ApplicationExists(
		context.Background(), "gig-1", "employer", "worker",
	)
	if err != nil || !exists {
		t.Fatalf("ApplicationExists() exists=%t err=%v, want true", exists, err)
	}
	exists, err = service.ApplicationExists(
		context.Background(), "gig-2", "worker", "employer",
	)
	if err != nil || exists {
		t.Fatalf("ApplicationExists() exists=%t err=%v, want false", exists, err)
	}
}

func TestListMessagesAuthorizesBeforeRepositoryRead(t *testing.T) {
	t.Parallel()

	repo := newIdempotencyRepository()
	threadID := bson.NewObjectID()
	repo.app = &Application{ID: threadID, InitiatorID: "worker", TargetID: "employer"}
	service := NewApplicationService(repo)

	if _, err := service.ListMessages(context.Background(), "outsider", threadID, 50, nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ListMessages() error = %v, want ErrForbidden", err)
	}
	if repo.listMessagesCalls != 0 {
		t.Fatal("message repository was queried before participant authorization")
	}
}

func TestListApplicationsProjectsPrivateThreadSummary(t *testing.T) {
	t.Parallel()

	repo := newIdempotencyRepository()
	firstID := bson.NewObjectID()
	secondID := bson.NewObjectID()
	repo.applications = []Application{
		{ID: firstID, GigID: "gig-1", LastMessage: "private text"},
		{ID: secondID, GigID: "gig-2", LastMessage: "another private text"},
	}

	got, err := NewApplicationService(repo).ListApplications(context.Background(), "worker")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ThreadID != firstID || got[0].GigID != "gig-1" || got[1].ThreadID != secondID {
		t.Fatalf("ListApplications() = %#v", got)
	}
}

func TestSendMessageEnforcesExistingApplicationStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   ApplicationStatus
		senderID string
		targetID string
		wantErr  error
	}{
		{name: "pending initiator", status: ApplicationStatusPending, senderID: "worker", targetID: "employer", wantErr: ErrApplicationPending},
		{name: "pending target", status: ApplicationStatusPending, senderID: "employer", targetID: "worker", wantErr: ErrApplicationMustBeApproved},
		{name: "rejected", status: ApplicationStatusRejected, senderID: "worker", targetID: "employer", wantErr: ErrApplicationRejected},
		{name: "archived", status: ApplicationStatusArchived, senderID: "worker", targetID: "employer", wantErr: ErrApplicationArchived},
		{name: "unknown", status: ApplicationStatus("BROKEN"), senderID: "worker", targetID: "employer", wantErr: ErrInvalidApplicationStatus},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := newIdempotencyRepository()
			repo.app = &Application{
				ID: bson.NewObjectID(), GigID: "gig-1",
				InitiatorID: "worker", TargetID: "employer", Status: tt.status,
			}
			_, _, _, err := NewApplicationService(repo).SendMessage(
				context.Background(), tt.senderID, tt.targetID, "gig-1", "client-1", "hello", nil,
			)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("SendMessage() error = %v, want %v", err, tt.wantErr)
			}
			if len(repo.messages) != 0 {
				t.Fatal("status-rejected message was persisted")
			}
		})
	}
}

func TestInitialMessagePersistenceFailureRemovesIncompleteApplication(t *testing.T) {
	t.Parallel()

	repo := newIdempotencyRepository()
	repo.createMessageErr = errors.New("persist failed")

	_, _, _, err := NewApplicationService(repo).SendMessage(
		context.Background(), "worker", "employer", "gig-1", "client-1", "hello", nil,
	)
	if err == nil {
		t.Fatal("SendMessage() unexpectedly succeeded")
	}
	if repo.deleteCalls != 1 || repo.app != nil {
		t.Fatalf("incomplete application cleanup deleteCalls=%d app=%#v", repo.deleteCalls, repo.app)
	}
}

func TestDurableMessageSucceedsEvenWhenLastMessageMetadataUpdateFails(t *testing.T) {
	t.Parallel()

	repo := newIdempotencyRepository()
	repo.app = &Application{
		ID: bson.NewObjectID(), GigID: "gig-1",
		InitiatorID: "worker", TargetID: "employer", Status: ApplicationStatusAccepted,
	}
	repo.updateErr = errors.New("metadata update failed")

	msg, newThread, replayed, err := NewApplicationService(repo).SendMessage(
		context.Background(), "worker", "employer", "gig-1", "client-1", "hello", nil,
	)
	if err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}
	if msg == nil || msg.ID.IsZero() || newThread || replayed {
		t.Fatalf("SendMessage() msg=%#v newThread=%v replayed=%v", msg, newThread, replayed)
	}
	if len(repo.messages) != 1 {
		t.Fatalf("durable message count = %d, want 1", len(repo.messages))
	}
}

func TestTransitionClassifiesMissingApplication(t *testing.T) {
	t.Parallel()

	repo := &transitionRepository{}
	err := NewApplicationService(repo).ApproveApplication(context.Background(), bson.NewObjectID(), "target")
	if !errors.Is(err, ErrApplicationNotFound) {
		t.Fatalf("ApproveApplication() error = %v, want ErrApplicationNotFound", err)
	}
}
