//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/vsayfb/gig-platform-chat-service/internal/application"
	"github.com/vsayfb/gig-platform-chat-service/internal/smoke"
)

func TestDeployedSmokeRunnerUsesRealProtocolsAndPreservesIdempotency(t *testing.T) {
	env := newTestEnv(t, testEnvOptions{core: &coreBehavior{}})
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
	listingID := uuid.NewString()
	coreHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/core/listings" ||
			r.URL.Query().Get("author_id") != manifest.Users[1].ID {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "data": []any{}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": 200,
			"data": []map[string]any{{
				"id": listingID, "author": map[string]string{"id": manifest.Users[1].ID},
			}},
		})
	}))
	defer coreHTTP.Close()
	coreClient, err := smoke.NewCoreClient(coreHTTP.URL, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	first, err := smoke.Run(context.Background(), client, coreClient, manifest)
	if err != nil {
		t.Fatalf("first smoke run: %v", err)
	}
	if first.Realtime != 2 || first.Messages != 2 || first.Conversations != 1 {
		t.Fatalf("first smoke summary = %+v", first)
	}

	second, err := smoke.Run(context.Background(), client, coreClient, manifest)
	if err != nil {
		t.Fatalf("repeated smoke run: %v", err)
	}
	if second.Realtime != 0 || second.Messages != 2 || second.Conversations != 1 {
		t.Fatalf("repeated smoke summary = %+v", second)
	}

	assertCollectionCount(t, env.db.Collection(application.ApplicationsCollection), bson.M{}, 1)
	assertCollectionCount(t, env.db.Collection(application.MessagesCollection), bson.M{}, 2)
}
