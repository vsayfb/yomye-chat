package gcppubsub

import (
	"context"
	"encoding/json"
	"fmt"

	"cloud.google.com/go/pubsub/v2"
	"github.com/vsayfb/gig-platform-chat-service/pkg/sqs"
	chatTracing "github.com/vsayfb/gig-platform-chat-service/pkg/tracing"
	"go.opentelemetry.io/otel/attribute"
)

type Publisher struct {
	client    *pubsub.Client
	publisher *pubsub.Publisher
}

func New(ctx context.Context, projectID, topicName string) (*Publisher, error) {
	client, err := pubsub.NewClient(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("create Pub/Sub client: %w", err)
	}
	return &Publisher{client: client, publisher: client.Publisher(topicName)}, nil
}

func (p *Publisher) PublishNewMessage(ctx context.Context, event sqs.Envelope) (err error) {
	ctx, span := chatTracing.StartDomain(ctx, "chat.notification.publish",
		attribute.String("messaging.system", "gcp_pubsub"),
		attribute.String("notification.type", string(event.Type)),
	)
	defer func() { chatTracing.EndDomain(span, err) }()

	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("pubsub: marshal event: %w", err)
	}
	result := p.publisher.Publish(ctx, &pubsub.Message{
		Data:       body,
		Attributes: map[string]string{"type": string(event.Type)},
	})
	if _, err := result.Get(ctx); err != nil {
		return fmt.Errorf("pubsub: publish message: %w", err)
	}
	return nil
}

func (p *Publisher) Close() error {
	p.publisher.Stop()
	return p.client.Close()
}
