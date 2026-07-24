package sqs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

type Publisher struct {
	client   *sqs.Client
	queueURL string
}

func NewPublisher(client *sqs.Client, queueURL string) *Publisher {
	return &Publisher{client: client, queueURL: queueURL}
}

type NotificationEventType string

const (
	EventNewMessage NotificationEventType = "new_message"
)

type Envelope struct {
	Type    NotificationEventType       `json:"type"`
	Payload NewMessageNotificationEvent `json:"payload"`
}

type NewMessageNotificationEvent struct {
	RecipientID    string    `json:"recipient_id"`
	SenderID       string    `json:"sender_id"`
	SenderName     string    `json:"sender_name"`
	ThreadID       string    `json:"thread_id"`
	GigID          string    `json:"gig_id"`
	MessageID      string    `json:"message_id"`
	MessagePreview string    `json:"message_preview"`
	OccurredAt     time.Time `json:"occurred_at"`
}

func (p *Publisher) PublishNewMessage(ctx context.Context, event Envelope) error {

	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("sqs: marshal event: %w", err)
	}

	inp := &sqs.SendMessageInput{
		QueueUrl:    aws.String(p.queueURL),
		MessageBody: aws.String(string(body)),
	}

	inf, err := p.client.SendMessage(ctx, inp)

	if err != nil {
		return fmt.Errorf("sqs: send message: %w", err)
	}

	slog.InfoContext(ctx, "sqs message sent (gig):", "message", aws.ToString(inf.MessageId))

	return nil
}
