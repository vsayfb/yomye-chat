package database

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

func NewMongoDB(
	ctx context.Context,
	uri string,
	dbName string,
) (*mongo.Client, *mongo.Database, error) {

	clientOptions := options.Client().
		ApplyURI(uri).
		SetConnectTimeout(10 * time.Second).
		SetServerSelectionTimeout(15 * time.Second)

	client, err := mongo.Connect(clientOptions)
	if err != nil {
		return nil, nil, fmt.Errorf("create MongoDB client: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	if err := client.Ping(pingCtx, readpref.Primary()); err != nil {
		disconnectCtx, disconnectCancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer disconnectCancel()

		_ = client.Disconnect(disconnectCtx)

		return nil, nil, fmt.Errorf("ping MongoDB primary: %w", err)
	}

	return client, client.Database(dbName), nil
}
