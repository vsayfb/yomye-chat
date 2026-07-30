package conversation

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

var (
	ErrNotFound                  = errors.New("conversation not found")
	ErrForbidden                 = errors.New("conversation access forbidden")
	ErrInvalidDirectConversation = errors.New("invalid direct conversation")
	ErrDirectMessagesDisabled    = errors.New("user does not allow social direct messages")
)

type CreationPolicy interface {
	AllowsSocialDirectMessages(ctx context.Context, targetID string) (bool, error)
}

type Service interface {
	List(ctx context.Context, userID string) ([]Summary, error)
	Get(ctx context.Context, id bson.ObjectID, userID string) (*Conversation, error)
	ListMessages(
		ctx context.Context,
		userID string,
		id bson.ObjectID,
		limit int,
		before *time.Time,
	) ([]Message, error)
	DirectConversationExists(ctx context.Context, userA, userB string) (bool, error)
	SendDirectMessage(
		ctx context.Context,
		senderID string,
		targetID string,
		content string,
	) (*Message, bool, error)
}

type service struct {
	repo   Repository
	policy CreationPolicy
}

func NewService(repo Repository, policy CreationPolicy) Service {
	return &service{repo: repo, policy: policy}
}

func (s *service) List(ctx context.Context, userID string) ([]Summary, error) {
	conversations, err := s.repo.ListForUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	result := make([]Summary, 0, len(conversations))
	for _, conversation := range conversations {
		result = append(result, Summary{
			ID:            conversation.ID,
			Type:          conversation.Type,
			Members:       conversation.Members,
			LastMessage:   conversation.LastMessage,
			LastMessageAt: conversation.LastMessageAt,
		})
	}
	return result, nil
}

func (s *service) Get(
	ctx context.Context,
	id bson.ObjectID,
	userID string,
) (*Conversation, error) {
	conversation, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if conversation == nil {
		return nil, ErrNotFound
	}
	if !isActiveMember(conversation, userID) {
		return nil, ErrForbidden
	}
	return conversation, nil
}

func (s *service) ListMessages(
	ctx context.Context,
	userID string,
	id bson.ObjectID,
	limit int,
	before *time.Time,
) ([]Message, error) {
	if _, err := s.Get(ctx, id, userID); err != nil {
		return nil, err
	}
	return s.repo.ListMessages(ctx, id, limit, before)
}

func (s *service) DirectConversationExists(
	ctx context.Context,
	userA string,
	userB string,
) (bool, error) {
	conversation, err := s.repo.FindDirectByKey(ctx, directKey(userA, userB))
	if err != nil {
		return false, err
	}
	if conversation == nil {
		return false, nil
	}
	if conversation.Type != TypeDirect ||
		!isActiveMember(conversation, userA) ||
		!isActiveMember(conversation, userB) {
		return false, ErrForbidden
	}
	return true, nil
}

func (s *service) SendDirectMessage(
	ctx context.Context,
	senderID string,
	targetID string,
	content string,
) (*Message, bool, error) {
	if senderID == "" || targetID == "" || senderID == targetID {
		return nil, false, ErrInvalidDirectConversation
	}

	key := directKey(senderID, targetID)
	conversation, err := s.repo.FindDirectByKey(ctx, key)
	if err != nil {
		return nil, false, err
	}

	isNew := false
	if conversation == nil {
		allowed, err := s.policy.AllowsSocialDirectMessages(ctx, targetID)
		if err != nil {
			return nil, false, err
		}
		if !allowed {
			return nil, false, ErrDirectMessagesDisabled
		}

		now := time.Now().UTC()
		conversation = &Conversation{
			Type:      TypeDirect,
			DirectKey: key,
			CreatedBy: senderID,
			Members: []Member{
				{UserID: senderID, Role: RoleMember, JoinedAt: now},
				{UserID: targetID, Role: RoleMember, JoinedAt: now},
			},
			LastMessage:   content,
			LastMessageAt: now,
		}

		if err := s.repo.Create(ctx, conversation); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				conversation, err = s.repo.FindDirectByKey(ctx, key)
				if err != nil {
					return nil, false, err
				}
				if conversation == nil {
					return nil, false, ErrNotFound
				}
			} else {
				return nil, false, err
			}
		} else {
			isNew = true
		}
	}

	if conversation.Type != TypeDirect ||
		!isActiveMember(conversation, senderID) ||
		!isActiveMember(conversation, targetID) {
		return nil, false, ErrForbidden
	}

	msg := &Message{
		ConversationID: conversation.ID,
		SenderID:       senderID,
		Content:        content,
		SentAt:         time.Now().UTC(),
	}
	if err := s.repo.CreateMessage(ctx, msg); err != nil {
		return nil, false, err
	}

	if err := s.repo.UpdateLastMessage(ctx, conversation.ID, content, msg.SentAt); err != nil {
		slog.ErrorContext(ctx, "failed to update direct conversation metadata",
			"conversation_id", conversation.ID.Hex(), "error", err)
	}
	return msg, isNew, nil
}

func isActiveMember(conversation *Conversation, userID string) bool {
	for _, member := range conversation.Members {
		if member.UserID == userID && member.LeftAt == nil {
			return true
		}
	}
	return false
}

func directKey(userA, userB string) string {
	users := []string{userA, userB}
	sort.Strings(users)
	return strings.Join(users, "\x00")
}

func OtherDirectMember(conversation *Conversation, userID string) (string, error) {
	if conversation.Type != TypeDirect || len(conversation.Members) != 2 {
		return "", ErrInvalidDirectConversation
	}
	if !isActiveMember(conversation, userID) {
		return "", ErrForbidden
	}
	for _, member := range conversation.Members {
		if member.UserID != userID && member.LeftAt == nil {
			return member.UserID, nil
		}
	}
	return "", ErrForbidden
}
