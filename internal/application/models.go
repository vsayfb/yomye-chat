package application

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type ApplicationStatus string

const (
	ApplicationStatusPending  ApplicationStatus = "pending"
	ApplicationStatusAccepted ApplicationStatus = "accepted"
	ApplicationStatusRejected ApplicationStatus = "rejected"
	ApplicationStatusArchived ApplicationStatus = "archived"
)

type Application struct {
	ID bson.ObjectID `bson:"_id,omitempty" json:"id"`

	GigID string `bson:"gig_id" json:"gig_id"`

	InitiatorID string `bson:"initiator_id" json:"initiator_id"`
	TargetID    string `bson:"target_id" json:"target_id"`

	Status ApplicationStatus `bson:"status" json:"status"`

	LastMessage   string    `bson:"last_message" json:"last_message"`
	LastMessageAt time.Time `bson:"last_message_at" json:"last_message_at"`
	CreatedAt     time.Time `bson:"created_at" json:"created_at"`
}

type ApplicationDetails struct {
	ThreadID bson.ObjectID `json:"thread_id"`
	GigID    string        `json:"gig_id"`

	TargetUserID string `json:"target_user_id"`

	Status ApplicationStatus `json:"status"`

	LastMessage   string    `json:"last_message"`
	LastMessageAt time.Time `json:"last_message_at"`
}

type Message struct {
	ID bson.ObjectID `bson:"_id,omitempty" json:"id"`

	ThreadID bson.ObjectID `bson:"thread_id" json:"thread_id"`
	SenderID string        `bson:"sender_id" json:"sender_id"`

	Content string `bson:"content" json:"content"`

	IsRead bool `bson:"is_read" json:"is_read"`

	SentAt time.Time `bson:"sent_at" json:"sent_at"`
}

type ApplicationSummary struct {
	ThreadID bson.ObjectID `json:"thread_id"`
	GigID    string        `json:"gig_id"`
}
