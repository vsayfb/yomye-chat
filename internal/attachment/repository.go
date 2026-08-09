package attachment

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const Collection = "attachments"

type Repository interface {
	EnsureIndexes(ctx context.Context) error
	Create(ctx context.Context, item *Attachment) error
	FindByID(ctx context.Context, id bson.ObjectID) (*Attachment, error)
	MarkReady(
		ctx context.Context,
		id bson.ObjectID,
		uploaderID string,
		etag string,
		size int64,
		contentType string,
		expiresAt time.Time,
	) (bool, error)
	FindReady(
		ctx context.Context,
		ids []bson.ObjectID,
		scope Scope,
		scopeID bson.ObjectID,
		uploaderID string,
		now time.Time,
	) ([]Attachment, error)
	FindReadyProvisional(
		ctx context.Context,
		ids []bson.ObjectID,
		scope Scope,
		uploaderID string,
		targetID string,
		gigID string,
		now time.Time,
	) ([]Attachment, error)
	MarkAttached(
		ctx context.Context,
		ids []bson.ObjectID,
		scope Scope,
		scopeID bson.ObjectID,
		uploaderID string,
		messageID bson.ObjectID,
		now time.Time,
	) (int64, error)
	MarkProvisionalAttached(
		ctx context.Context,
		ids []bson.ObjectID,
		scope Scope,
		uploaderID string,
		targetID string,
		gigID string,
		scopeID bson.ObjectID,
		messageID bson.ObjectID,
		now time.Time,
	) (int64, error)
	ClaimExpired(ctx context.Context, now time.Time) (*Attachment, error)
	MarkDeleted(ctx context.Context, id bson.ObjectID, now time.Time) error
	RetryDeletion(ctx context.Context, id bson.ObjectID, next time.Time) error
}

type repository struct {
	collection *mongo.Collection
}

func NewRepository(db *mongo.Database) Repository {
	return &repository{collection: db.Collection(Collection)}
}

func (r *repository) EnsureIndexes(ctx context.Context) error {
	_, err := r.collection.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys: bson.D{
				{Key: "scope", Value: 1},
				{Key: "scope_id", Value: 1},
				{Key: "status", Value: 1},
			},
			Options: options.Index().SetName("scope_attachments"),
		},
		{
			Keys: bson.D{
				{Key: "scope", Value: 1},
				{Key: "uploader_id", Value: 1},
				{Key: "target_id", Value: 1},
				{Key: "gig_id", Value: 1},
				{Key: "status", Value: 1},
			},
			Options: options.Index().SetName("provisional_attachments"),
		},
		{
			Keys:    bson.D{{Key: "status", Value: 1}, {Key: "expires_at", Value: 1}},
			Options: options.Index().SetName("attachment_expiry"),
		},
		{
			Keys:    bson.D{{Key: "status", Value: 1}, {Key: "next_deletion_at", Value: 1}},
			Options: options.Index().SetName("attachment_deletion_retry"),
		},
	})
	return err
}

func (r *repository) Create(ctx context.Context, item *Attachment) error {
	if item.ID.IsZero() {
		item.ID = bson.NewObjectID()
	}
	_, err := r.collection.InsertOne(ctx, item)
	return err
}

func (r *repository) FindByID(ctx context.Context, id bson.ObjectID) (*Attachment, error) {
	var item Attachment
	err := r.collection.FindOne(ctx, bson.M{"_id": id}).Decode(&item)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *repository) MarkReady(
	ctx context.Context,
	id bson.ObjectID,
	uploaderID string,
	etag string,
	size int64,
	contentType string,
	expiresAt time.Time,
) (bool, error) {
	now := time.Now().UTC()
	result, err := r.collection.UpdateOne(ctx, bson.M{
		"_id": id, "uploader_id": uploaderID, "status": StatusPendingUpload,
		"expires_at": bson.M{"$gt": now},
	}, bson.M{
		"$set": bson.M{
			"status": StatusReady, "etag": etag, "size_bytes": size,
			"content_type": contentType, "expires_at": expiresAt, "updated_at": now,
		},
	})
	return result != nil && result.ModifiedCount == 1, err
}

func (r *repository) FindReady(
	ctx context.Context,
	ids []bson.ObjectID,
	scope Scope,
	scopeID bson.ObjectID,
	uploaderID string,
	now time.Time,
) ([]Attachment, error) {
	cur, err := r.collection.Find(ctx, bson.M{
		"_id": bson.M{"$in": ids}, "scope": scope, "scope_id": scopeID,
		"uploader_id": uploaderID, "status": StatusReady,
		"expires_at": bson.M{"$gt": now},
	})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var items []Attachment
	if err := cur.All(ctx, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func (r *repository) FindReadyProvisional(
	ctx context.Context,
	ids []bson.ObjectID,
	scope Scope,
	uploaderID string,
	targetID string,
	gigID string,
	now time.Time,
) ([]Attachment, error) {
	filter := bson.M{
		"_id": bson.M{"$in": ids}, "scope": scope,
		"uploader_id": uploaderID, "target_id": targetID,
		"provisional": true, "status": StatusReady,
		"expires_at": bson.M{"$gt": now},
	}
	if gigID == "" {
		filter["gig_id"] = bson.M{"$exists": false}
	} else {
		filter["gig_id"] = gigID
	}
	cur, err := r.collection.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var items []Attachment
	if err := cur.All(ctx, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func (r *repository) MarkAttached(
	ctx context.Context,
	ids []bson.ObjectID,
	scope Scope,
	scopeID bson.ObjectID,
	uploaderID string,
	messageID bson.ObjectID,
	now time.Time,
) (int64, error) {
	result, err := r.collection.UpdateMany(ctx, bson.M{
		"_id": bson.M{"$in": ids}, "scope": scope, "scope_id": scopeID,
		"uploader_id": uploaderID, "status": StatusReady,
		"expires_at": bson.M{"$gt": now},
	}, bson.M{
		"$set": bson.M{
			"status": StatusAttached, "message_id": messageID, "updated_at": now,
		},
		"$unset": bson.M{"expires_at": ""},
	})
	if err != nil {
		return 0, err
	}
	return result.ModifiedCount, nil
}

func (r *repository) MarkProvisionalAttached(
	ctx context.Context,
	ids []bson.ObjectID,
	scope Scope,
	uploaderID string,
	targetID string,
	gigID string,
	scopeID bson.ObjectID,
	messageID bson.ObjectID,
	now time.Time,
) (int64, error) {
	filter := bson.M{
		"_id": bson.M{"$in": ids}, "scope": scope,
		"uploader_id": uploaderID, "target_id": targetID,
		"provisional": true, "status": StatusReady,
		"expires_at": bson.M{"$gt": now},
	}
	if gigID == "" {
		filter["gig_id"] = bson.M{"$exists": false}
	} else {
		filter["gig_id"] = gigID
	}
	result, err := r.collection.UpdateMany(ctx, filter, bson.M{
		"$set": bson.M{
			"scope_id": scopeID, "status": StatusAttached,
			"message_id": messageID, "updated_at": now,
		},
		"$unset": bson.M{
			"provisional": "", "target_id": "", "gig_id": "", "expires_at": "",
		},
	})
	if err != nil {
		return 0, err
	}
	return result.ModifiedCount, nil
}

func (r *repository) ClaimExpired(ctx context.Context, now time.Time) (*Attachment, error) {
	filter := bson.M{"$or": bson.A{
		bson.M{
			"status":     bson.M{"$in": bson.A{StatusPendingUpload, StatusReady}},
			"expires_at": bson.M{"$lte": now},
		},
		bson.M{
			"status":           StatusDeleting,
			"next_deletion_at": bson.M{"$lte": now},
		},
	}}
	update := bson.M{
		"$set": bson.M{
			"status": StatusDeleting, "updated_at": now,
			"next_deletion_at": now.Add(5 * time.Minute),
		},
		"$inc": bson.M{"deletion_attempts": 1},
	}
	var item Attachment
	err := r.collection.FindOneAndUpdate(
		ctx,
		filter,
		update,
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&item)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *repository) MarkDeleted(ctx context.Context, id bson.ObjectID, now time.Time) error {
	_, err := r.collection.UpdateOne(ctx, bson.M{
		"_id": id, "status": StatusDeleting,
	}, bson.M{
		"$set":   bson.M{"status": StatusDeleted, "deleted_at": now, "updated_at": now},
		"$unset": bson.M{"next_deletion_at": ""},
	})
	return err
}

func (r *repository) RetryDeletion(ctx context.Context, id bson.ObjectID, next time.Time) error {
	_, err := r.collection.UpdateOne(ctx, bson.M{
		"_id": id, "status": StatusDeleting,
	}, bson.M{"$set": bson.M{"next_deletion_at": next, "updated_at": time.Now().UTC()}})
	return err
}
