package application

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/vsayfb/gig-platform-chat-service/internal/attachment"
	"github.com/vsayfb/gig-platform-chat-service/internal/messagecursor"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

var (
	ErrForbidden                 = errors.New("Forbidden")
	ErrApplicationPending        = errors.New("Application is in pending status.")
	ErrApplicationMustBeApproved = errors.New("Application must be approved by employer.")
	ErrApplicationRejected       = errors.New("Application has rejected by employer.")
	ErrApplicationArchived       = errors.New("Application has been archived.")
	ErrInvalidApplicationStatus  = errors.New("Unknown application status.")
	ErrApplicationNotFound       = errors.New("Application not found.")
)

type Service interface {
	// Applications
	ListApplications(ctx context.Context, userID string) ([]ApplicationSummary, error)
	GetApplication(ctx context.Context, threadID bson.ObjectID, userID string) (*Application, error)

	ApproveApplication(ctx context.Context, threadID bson.ObjectID, approverID string) error
	RejectApplication(ctx context.Context, threadID bson.ObjectID, approverID string) error

	ListMessages(
		ctx context.Context,
		userID string,
		threadID bson.ObjectID,
		limit int,
		cursor *messagecursor.Cursor,
	) ([]Message, error)

	// Messages
	// GetMessages(ctx context.Context, threadID bson.ObjectID, cursor *Cursor) ([]Message, error)
	FindMessageByClientMessageID(
		ctx context.Context,
		senderID string,
		clientMessageID string,
	) (*Message, error)

	// SendMessage returns the persisted message and whether sending it
	// created a brand-new thread (application) — i.e. this was the very
	// first message between these participants for this gig.
	SendMessage(
		ctx context.Context,
		senderID string,
		targetID string,
		gigID string,
		clientMessageID string,
		content string,
		attachments []attachment.Ref,
	) (msg *Message, isNewThread bool, replayed bool, err error)
}

type applicationService struct {
	repo Repository
}

func NewApplicationService(repo Repository) *applicationService {
	return &applicationService{
		repo: repo,
	}
}

// GetApplication returns the application identified by the given thread ID.
func (s *applicationService) GetApplication(
	ctx context.Context,
	threadID bson.ObjectID,
	userID string,
) (*Application, error) {

	app, err := s.repo.FindApplicationByID(ctx, threadID)

	if err != nil {
		return nil, err
	}

	if app == nil {
		return nil, ErrApplicationNotFound
	}

	if app.InitiatorID != userID &&
		app.TargetID != userID {
		return nil, ErrForbidden
	}

	return app, nil
}

// ListApplications returns all applications the user participates in.
func (s *applicationService) ListApplications(
	ctx context.Context,
	userID string,
) ([]ApplicationSummary, error) {

	apps, err := s.repo.ListApplications(ctx, userID)

	if err != nil {
		return nil, err
	}

	summaries := make([]ApplicationSummary, 0, len(apps))

	for _, app := range apps {
		summaries = append(summaries, ApplicationSummary{
			ThreadID: app.ID,
			GigID:    app.GigID,
		})
	}

	return summaries, nil
}

// ApproveApplication approves a pending application.
// Only the target user may approve an application.
func (s *applicationService) ApproveApplication(
	ctx context.Context,
	threadID bson.ObjectID,
	approverID string,
) error {
	return s.transitionPendingApplication(
		ctx,
		threadID,
		approverID,
		ApplicationStatusAccepted,
	)
}

// RejectApplication rejects a pending application.
// Only the target user may reject an application.
func (s *applicationService) RejectApplication(
	ctx context.Context,
	threadID bson.ObjectID,
	rejectorID string,
) error {
	return s.transitionPendingApplication(
		ctx,
		threadID,
		rejectorID,
		ApplicationStatusRejected,
	)
}

func (s *applicationService) transitionPendingApplication(
	ctx context.Context,
	threadID bson.ObjectID,
	targetID string,
	to ApplicationStatus,
) error {
	transitioned, err := s.repo.TransitionApplicationStatus(
		ctx,
		threadID,
		targetID,
		ApplicationStatusPending,
		to,
	)
	if err != nil {
		return err
	}
	if transitioned {
		return nil
	}

	// The compare-and-set did not match. Re-read only to classify the failure
	// for the existing HTTP contract; the state transition itself is already
	// race-safe because it happened in the conditional update above.
	app, err := s.repo.FindApplicationByID(ctx, threadID)
	if err != nil {
		return err
	}
	if app == nil {
		return ErrApplicationNotFound
	}
	if app.TargetID != targetID {
		return ErrForbidden
	}
	return ErrInvalidApplicationStatus
}

func (s *applicationService) ListMessages(
	ctx context.Context,
	userID string,
	threadID bson.ObjectID,
	limit int,
	cursor *messagecursor.Cursor,
) ([]Message, error) {

	app, err := s.repo.FindApplicationByID(ctx, threadID)
	if err != nil {
		return nil, err
	}

	if app == nil {
		return nil, ErrApplicationNotFound
	}

	if app.InitiatorID != userID &&
		app.TargetID != userID {
		return nil, ErrForbidden
	}

	return s.repo.ListMessages(
		ctx,
		threadID,
		limit,
		cursor,
	)
}

func (s *applicationService) FindMessageByClientMessageID(
	ctx context.Context,
	senderID string,
	clientMessageID string,
) (*Message, error) {
	return s.repo.FindMessageByClientMessageID(ctx, senderID, clientMessageID)
}

// SendMessage sends a message within the context of an application.
//
// If no application exists between the sender and target for the specified gig,
// a new application is created in the Pending state and the message becomes the
// initial application message.
//
// Once an application exists, message delivery is governed by the application's
// current status:
//
//   - Pending:
//     The initiator cannot send additional messages until the target explicitly
//     approves the application. The target must approve the application before
//     either participant can exchange messages.
//
//   - Accepted:
//     Both participants may exchange messages freely.
//
//   - Rejected:
//     No further messages are permitted.
//
//   - Archived:
//     No further messages are permitted.
//
// The method updates the application's last message metadata whenever a message
// is successfully persisted.
func (s *applicationService) SendMessage(
	ctx context.Context,
	senderID string,
	targetID string,
	gigID string,
	clientMessageID string,
	content string,
	attachments []attachment.Ref,
) (*Message, bool, bool, error) {

	if existing, err := s.repo.FindMessageByClientMessageID(
		ctx, senderID, clientMessageID,
	); err != nil {
		return nil, false, false, err
	} else if existing != nil {
		return existing, false, true, nil
	}

	app, err := s.repo.FindApplicationByParticipants(
		ctx,
		gigID,
		senderID,
		targetID,
	)

	if err != nil {
		return nil, false, false, err
	}

	// First message creates the application.
	if app == nil {
		now := time.Now().UTC()

		app = &Application{
			GigID:           gigID,
			ConversationKey: conversationKey(gigID, senderID, targetID),
			InitiatorID:     senderID,
			TargetID:        targetID,
			Status:          ApplicationStatusPending,
			LastMessage:     content,
			LastMessageAt:   now,
		}

		if err := s.repo.CreateApplication(ctx, app); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				return s.SendMessage(
					ctx, senderID, targetID, gigID, clientMessageID, content, attachments,
				)
			}
			return nil, false, false, err
		}

		msg, replayed, err := s.persistMessage(
			ctx, app.ID, senderID, clientMessageID, content, attachments,
		)
		if err != nil {
			if cleanupErr := s.repo.DeleteApplication(ctx, app.ID); cleanupErr != nil {
				slog.ErrorContext(ctx, "failed to clean up incomplete application",
					"thread_id", app.ID.Hex(), "error", cleanupErr)
			}
		}

		return msg, !replayed, replayed, err
	}

	isInitiator := app.InitiatorID == senderID
	isTarget := app.TargetID == senderID

	if !isInitiator && !isTarget {
		return nil, false, false, ErrForbidden
	}

	switch app.Status {

	case ApplicationStatusPending:
		if isInitiator {
			return nil, false, false, ErrApplicationPending
		}

		// Target must approve before Applicationting.
		return nil, false, false, ErrApplicationMustBeApproved

	case ApplicationStatusAccepted:
		msg, replayed, err := s.persistMessage(
			ctx, app.ID, senderID, clientMessageID, content, attachments,
		)

		return msg, false, replayed, err

	case ApplicationStatusRejected:
		return nil, false, false, ErrApplicationRejected

	case ApplicationStatusArchived:
		return nil, false, false, ErrApplicationArchived

	default:
		return nil, false, false, ErrInvalidApplicationStatus
	}
}

func (s *applicationService) persistMessage(
	ctx context.Context,
	threadID bson.ObjectID,
	senderID string,
	clientMessageID string,
	content string,
	attachments []attachment.Ref,
) (*Message, bool, error) {

	now := time.Now().UTC()

	msg := &Message{
		ThreadID:        threadID,
		SenderID:        senderID,
		ClientMessageID: clientMessageID,
		Content:         content,
		Attachments:     attachments,
		IsRead:          false,
		SentAt:          now,
	}

	persisted, created, err := s.repo.CreateOrGetMessage(ctx, msg)
	if err != nil {
		return nil, false, err
	}
	if !created {
		return persisted, true, nil
	}

	if err := s.repo.UpdateLastMessage(
		ctx,
		threadID,
		content,
		persisted.SentAt,
	); err != nil {
		// The message is already durable. Report success to prevent clients from
		// retrying and creating a duplicate; list ordering can be repaired later.
		slog.ErrorContext(ctx, "failed to update application message metadata",
			"thread_id", threadID.Hex(), "error", err)
	}

	return persisted, false, nil
}

func conversationKey(gigID, userA, userB string) string {
	users := []string{userA, userB}
	sort.Strings(users)
	return strings.Join([]string{gigID, users[0], users[1]}, "\x00")
}
