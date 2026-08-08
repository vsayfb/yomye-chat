package conversation

import (
	"context"
	"errors"
	"time"

	"github.com/vsayfb/gig-platform-chat-service/internal/messagecursor"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	ConversationsCollection = "conversations"
	MessagesCollection      = "conversation_messages"
)

type Repository interface {
	EnsureIndexes(ctx context.Context) error
	FindDirectByKey(ctx context.Context, key string) (*Conversation, error)
	FindByID(ctx context.Context, id bson.ObjectID) (*Conversation, error)
	ListForUser(ctx context.Context, userID string) ([]Conversation, error)
	Create(ctx context.Context, conversation *Conversation) error
	UpdateLastMessage(ctx context.Context, id bson.ObjectID, content string, sentAt time.Time) error
	FindMessageByClientMessageID(ctx context.Context, senderID, clientMessageID string) (*Message, error)
	CreateOrGetMessage(ctx context.Context, message *Message) (*Message, bool, error)
	ListMessages(
		ctx context.Context,
		conversationID bson.ObjectID,
		limit int,
		cursor *messagecursor.Cursor,
	) ([]Message, error)
}

type repository struct {
	conversations *mongo.Collection
	messages      *mongo.Collection
}

func NewRepository(db *mongo.Database) Repository {
	return &repository{
		conversations: db.Collection(ConversationsCollection),
		messages:      db.Collection(MessagesCollection),
	}
}

func (r *repository) EnsureIndexes(ctx context.Context) error {
	if _, err := r.conversations.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys: bson.D{{Key: "direct_key", Value: 1}},
			Options: options.Index().
				SetName("unique_direct_conversation").
				SetUnique(true).
				SetPartialFilterExpression(bson.M{"type": TypeDirect}),
		},
		{
			Keys:    bson.D{{Key: "members.user_id", Value: 1}, {Key: "last_message_at", Value: -1}},
			Options: options.Index().SetName("member_conversations"),
		},
	}); err != nil {
		return err
	}

	_, err := r.messages.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys: bson.D{
				{Key: "conversation_id", Value: 1},
				{Key: "sent_at", Value: -1},
				{Key: "_id", Value: -1},
			},
			Options: options.Index().SetName("conversation_messages_v2"),
		},
		{
			Keys: bson.D{
				{Key: "sender_id", Value: 1},
				{Key: "client_message_id", Value: 1},
			},
			Options: options.Index().
				SetName("conversation_message_idempotency").
				SetUnique(true).
				SetPartialFilterExpression(bson.M{
					"client_message_id": bson.M{"$type": "string"},
				}),
		},
	})
	return err
}

func (r *repository) FindDirectByKey(ctx context.Context, key string) (*Conversation, error) {
	var conversation Conversation
	err := r.conversations.FindOne(ctx, bson.M{
		"type":       TypeDirect,
		"direct_key": key,
	}).Decode(&conversation)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &conversation, nil
}

func (r *repository) FindByID(ctx context.Context, id bson.ObjectID) (*Conversation, error) {
	var conversation Conversation
	err := r.conversations.FindOne(ctx, bson.M{"_id": id}).Decode(&conversation)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &conversation, nil
}

func (r *repository) ListForUser(ctx context.Context, userID string) ([]Conversation, error) {
	cur, err := r.conversations.Find(
		ctx,
		bson.M{
			"members": bson.M{
				"$elemMatch": bson.M{"user_id": userID, "left_at": bson.M{"$exists": false}},
			},
		},
		options.Find().SetSort(bson.D{{Key: "last_message_at", Value: -1}}),
	)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	var conversations []Conversation
	if err := cur.All(ctx, &conversations); err != nil {
		return nil, err
	}
	return conversations, nil
}

func (r *repository) Create(ctx context.Context, conversation *Conversation) error {
	if conversation.ID.IsZero() {
		conversation.ID = bson.NewObjectID()
	}
	if conversation.CreatedAt.IsZero() {
		conversation.CreatedAt = time.Now().UTC()
	}
	_, err := r.conversations.InsertOne(ctx, conversation)
	return err
}

func (r *repository) UpdateLastMessage(
	ctx context.Context,
	id bson.ObjectID,
	content string,
	sentAt time.Time,
) error {
	_, err := r.conversations.UpdateOne(ctx, bson.M{"_id": id}, bson.M{
		"$set": bson.M{"last_message": content, "last_message_at": sentAt},
	})
	return err
}

func (r *repository) FindMessageByClientMessageID(
	ctx context.Context,
	senderID string,
	clientMessageID string,
) (*Message, error) {
	var message Message
	err := r.messages.FindOne(ctx, bson.M{
		"sender_id":         senderID,
		"client_message_id": clientMessageID,
	}).Decode(&message)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &message, nil
}

func (r *repository) CreateOrGetMessage(
	ctx context.Context,
	message *Message,
) (*Message, bool, error) {
	if message.ID.IsZero() {
		message.ID = bson.NewObjectID()
	}
	if message.SentAt.IsZero() {
		message.SentAt = time.Now().UTC()
	}

	var persisted Message
	err := r.messages.FindOneAndUpdate(
		ctx,
		bson.M{
			"sender_id":         message.SenderID,
			"client_message_id": message.ClientMessageID,
		},
		bson.M{"$setOnInsert": message},
		options.FindOneAndUpdate().
			SetUpsert(true).
			SetReturnDocument(options.After),
	).Decode(&persisted)
	if err != nil {
		return nil, false, err
	}

	return &persisted, persisted.ID == message.ID, nil
}

func (r *repository) ListMessages(
	ctx context.Context,
	conversationID bson.ObjectID,
	limit int,
	cursor *messagecursor.Cursor,
) ([]Message, error) {
	filter := bson.M{"conversation_id": conversationID}
	if beforeFilter := messagecursor.BeforeFilter(cursor); beforeFilter != nil {
		filter["$or"] = beforeFilter["$or"]
	}

	opts := options.Find().SetSort(
		bson.D{{Key: "sent_at", Value: -1}, {Key: "_id", Value: -1}},
	)
	if limit > 0 {
		opts.SetLimit(int64(limit))
	}

	cur, err := r.messages.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	var messages []Message
	if err := cur.All(ctx, &messages); err != nil {
		return nil, err
	}
	return messages, nil
}
