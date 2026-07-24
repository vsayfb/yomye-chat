package application

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type Repository interface {
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

	UpdateApplicationStatus(
		ctx context.Context,
		threadID bson.ObjectID,
		status ApplicationStatus,
	) error

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
		before *time.Time,
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

func (r *applicationRepository) FindApplicationByParticipants(
	ctx context.Context,
	gigID string,
	userA string,
	userB string,
) (*Application, error) {

	filter := bson.M{
		"gig_id": gigID,
		"$or": bson.A{
			bson.M{
				"initiator_id": userA,
				"target_id":    userB,
			},
			bson.M{
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

func (r *applicationRepository) UpdateApplicationStatus(
	ctx context.Context,
	threadID bson.ObjectID,
	status ApplicationStatus,
) error {

	filter := bson.M{
		"_id": threadID,
	}

	update := bson.M{
		"$set": bson.M{
			"status": status,
		},
	}

	_, err := r.applications.UpdateOne(ctx, filter, update)
	return err
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
	before *time.Time,
) ([]Message, error) {

	filter := bson.M{
		"thread_id": threadID,
	}

	if before != nil {
		filter["sent_at"] = bson.M{
			"$lt": *before,
		}
	}

	opts := options.Find().
		SetSort(bson.D{{Key: "sent_at", Value: -1}})

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
