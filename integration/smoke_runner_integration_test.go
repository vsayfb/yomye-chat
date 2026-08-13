//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/vsayfb/gig-platform-chat-service/internal/conversation"
	"github.com/vsayfb/gig-platform-chat-service/internal/smoke"
)

func TestDeployedSmokeRunnerUsesRealProtocolsAndPreservesIdempotency(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{
		core: &coreBehavior{AllowDirectMessages: true},
	})
	manifest := &smoke.Manifest{
		Version: smoke.ManifestVersion, RunID: "integration-deployment-sha",
		ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
		Users:     make([]smoke.User, smoke.CohortSize),
	}
	for i := range manifest.Users {
		id := uuid.NewString()
		manifest.Users[i] = smoke.User{
			ID: id, Name: "Smoke User",
			AccessToken: signedAccessToken(t, id, "Smoke User"),
		}
	}
	client, err := smoke.NewClient(env.server.URL, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	first, err := smoke.Run(context.Background(), client, manifest)
	if err != nil {
		t.Fatalf("first smoke run: %v", err)
	}
	if first.Realtime != 2 || first.Messages != 2 || first.Conversations != 1 {
		t.Fatalf("first smoke summary = %+v", first)
	}

	second, err := smoke.Run(context.Background(), client, manifest)
	if err != nil {
		t.Fatalf("repeated smoke run: %v", err)
	}
	if second.Realtime != 0 || second.Messages != 2 || second.Conversations != 1 {
		t.Fatalf("repeated smoke summary = %+v", second)
	}

	assertCollectionCount(t, env.db.Collection(conversation.ConversationsCollection), bson.M{}, 1)
	assertCollectionCount(t, env.db.Collection(conversation.MessagesCollection), bson.M{}, 2)
}
