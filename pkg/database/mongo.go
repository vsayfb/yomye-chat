package database

import (
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func NewMongoDB(uri string, dbName string) (*mongo.Client, *mongo.Database, error) {
	client, err := mongo.Connect(
		options.Client().ApplyURI(uri),
	)

	if err != nil {
		return nil, nil, err
	}

	db := client.Database(dbName)

	return client, db, nil
}
