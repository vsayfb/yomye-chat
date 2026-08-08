package application

import (
	"context"
	"errors"
	"time"

	"github.com/vsayfb/gig-platform-chat-service/internal/messagecursor"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type Repository interface {
	EnsureIndexes(ctx context.Context) error

	// Applications

	FindApplicationByParticipants(
		ctx context.Context,
		gigID string,
		userA string,
		userB string,
	) (*Application, error)

	FindApplicationByID(
		ctx context.Context,
		threadID bson.ObjectID,
	) (*Application, error)

	ListApplications(
		ctx context.Context,
		userID string,
	) ([]Application, error)

	CreateApplication(
		ctx context.Context,
		application *Application,
	) error

	DeleteApplication(ctx context.Context, threadID bson.ObjectID) error

	TransitionApplicationStatus(
		ctx context.Context,
		threadID bson.ObjectID,
		targetID string,
		from ApplicationStatus,
		to ApplicationStatus,
	) (bool, error)

	UpdateLastMessage(
		ctx context.Context,
		threadID bson.ObjectID,
		message string,
		sentAt time.Time,
	) error

	// Messages

	CreateMessage(
		ctx context.Context,
		message *Message,
	) error

	ListMessages(
		ctx context.Context,
		threadID bson.ObjectID,
		limit int,
		cursor *messagecursor.Cursor,
	) ([]Message, error)
}

const (
	ApplicationsCollection = "applications"
	MessagesCollection     = "messages"
)

type applicationRepository struct {
	applications *mongo.Collection
	messages     *mongo.Collection
}

func NewApplicationRepository(db *mongo.Database) Repository {
	return &applicationRepository{
		applications: db.Collection(ApplicationsCollection),
		messages:     db.Collection(MessagesCollection),
	}
}

func (r *applicationRepository) EnsureIndexes(ctx context.Context) error {
	if _, err := r.applications.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys: bson.D{{Key: "conversation_key", Value: 1}},
			Options: options.Index().
				SetName("unique_conversation").
				SetUnique(true).
				SetPartialFilterExpression(bson.M{
					"conversation_key": bson.M{"$type": "string"},
				}),
		},
		{
			Keys: bson.D{
				{Key: "initiator_id", Value: 1},
				{Key: "last_message_at", Value: -1},
			},
			Options: options.Index().SetName("initiator_applications"),
		},
		{
			Keys: bson.D{
				{Key: "target_id", Value: 1},
				{Key: "last_message_at", Value: -1},
			},
			Options: options.Index().SetName("target_applications"),
		},
	}); err != nil {
		return err
	}

	_, err := r.messages.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "thread_id", Value: 1},
			{Key: "sent_at", Value: -1},
			{Key: "_id", Value: -1},
		},
		Options: options.Index().SetName("application_messages_v2"),
	})
	return err
}

func (r *applicationRepository) FindApplicationByParticipants(
	ctx context.Context,
	gigID string,
	userA string,
	userB string,
) (*Application, error) {

	filter := bson.M{
		"$or": bson.A{
			bson.M{"conversation_key": conversationKey(gigID, userA, userB)},
			bson.M{
				"gig_id":       gigID,
				"initiator_id": userA,
				"target_id":    userB,
			},
			bson.M{
				"gig_id":       gigID,
				"initiator_id": userB,
				"target_id":    userA,
			},
		},
	}

	var app Application

	err := r.applications.FindOne(ctx, filter).Decode(&app)

	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}

		return nil, err
	}

	return &app, nil
}

func (r *applicationRepository) DeleteApplication(
	ctx context.Context,
	threadID bson.ObjectID,
) error {
	_, err := r.applications.DeleteOne(ctx, bson.M{"_id": threadID})
	return err
}

func (r *applicationRepository) CreateApplication(
	ctx context.Context,
	app *Application,
) error {

	if app.ID.IsZero() {
		app.ID = bson.NewObjectID()
	}

	if app.CreatedAt.IsZero() {
		app.CreatedAt = time.Now().UTC()
	}

	_, err := r.applications.InsertOne(ctx, app)
	return err
}

func (r *applicationRepository) FindApplicationByID(
	ctx context.Context,
	threadID bson.ObjectID,
) (*Application, error) {

	filter := bson.M{
		"_id": threadID,
	}

	var app Application

	err := r.applications.FindOne(ctx, filter).Decode(&app)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}

		return nil, err
	}

	return &app, nil
}

func (r *applicationRepository) TransitionApplicationStatus(
	ctx context.Context,
	threadID bson.ObjectID,
	targetID string,
	from ApplicationStatus,
	to ApplicationStatus,
) (bool, error) {
	result, err := r.applications.UpdateOne(ctx, bson.M{
		"_id":       threadID,
		"target_id": targetID,
		"status":    from,
	}, bson.M{
		"$set": bson.M{"status": to},
	})
	if err != nil {
		return false, err
	}
	return result.ModifiedCount == 1, nil
}

func (r *applicationRepository) UpdateLastMessage(
	ctx context.Context,
	threadID bson.ObjectID,
	message string,
	sentAt time.Time,
) error {

	filter := bson.M{
		"_id": threadID,
	}

	update := bson.M{
		"$set": bson.M{
			"last_message":    message,
			"last_message_at": sentAt,
		},
	}

	_, err := r.applications.UpdateOne(ctx, filter, update)
	return err
}

func (r *applicationRepository) CreateMessage(
	ctx context.Context,
	message *Message,
) error {
	if message.ID.IsZero() {
		message.ID = bson.NewObjectID()
	}

	if message.SentAt.IsZero() {
		message.SentAt = time.Now().UTC()
	}

	_, err := r.messages.InsertOne(ctx, message)

	return err
}

func (r *applicationRepository) ListMessages(
	ctx context.Context,
	threadID bson.ObjectID,
	limit int,
	cursor *messagecursor.Cursor,
) ([]Message, error) {

	filter := bson.M{
		"thread_id": threadID,
	}

	if beforeFilter := messagecursor.BeforeFilter(cursor); beforeFilter != nil {
		filter["$or"] = beforeFilter["$or"]
	}

	opts := options.Find().
		SetSort(bson.D{{Key: "sent_at", Value: -1}, {Key: "_id", Value: -1}})

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

func (r *applicationRepository) ListApplications(
	ctx context.Context,
	userID string,
) ([]Application, error) {

	filter := bson.M{
		"$or": bson.A{
			bson.M{
				"initiator_id": userID,
			},
			bson.M{
				"target_id": userID,
			},
		},
	}

	opts := options.Find().
		SetSort(bson.D{
			{Key: "last_message_at", Value: -1},
		})

	cur, err := r.applications.Find(ctx, filter, opts)

	if err != nil {
		return nil, err
	}

	defer cur.Close(ctx)

	var applications []Application

	if err := cur.All(ctx, &applications); err != nil {
		return nil, err
	}

	return applications, nil
}
