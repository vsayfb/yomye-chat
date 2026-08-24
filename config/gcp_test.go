package config

import (
	"context"
	"errors"
	"strings"
	"testing"

	"cloud.google.com/go/parametermanager/apiv1/parametermanagerpb"
)

type fakeGCPParameterClient struct {
	values    map[string][]byte
	errFor    map[string]error
	requested []string
	closed    bool
}

func (f *fakeGCPParameterClient) RenderParameterVersion(
	_ context.Context,
	req *parametermanagerpb.RenderParameterVersionRequest,
) (*parametermanagerpb.RenderParameterVersionResponse, error) {
	f.requested = append(f.requested, req.GetName())
	name := req.GetName()
	if err := f.errFor[name]; err != nil {
		return nil, err
	}
	return &parametermanagerpb.RenderParameterVersionResponse{RenderedPayload: f.values[name]}, nil
}

func (f *fakeGCPParameterClient) Close() error {
	f.closed = true
	return nil
}

func TestLoadGCPSuccess(t *testing.T) {
	project := "yevmiye-production"
	location := "europe-west3"
	values := validGCPParameters(project, location)
	fake := &fakeGCPParameterClient{values: values, errFor: map[string]error{}}
	installGCPClientFactory(t, fake)
	setGCPEnvironment(t, project, location)

	cfg, err := loadGCP(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !fake.closed {
		t.Fatal("Parameter Manager client was not closed")
	}
	if cfg.AppEnv != EnvironmentProduction || cfg.GCP.ProjectID != project ||
		cfg.GCP.ParameterLocation != location {
		t.Fatalf("unexpected production config: %+v", cfg.GCP)
	}
	if cfg.GCP.NotificationTopicName != "projects/yevmiye-production/topics/chat-notifications" {
		t.Fatalf("notification topic = %q", cfg.GCP.NotificationTopicName)
	}
	if cfg.JWTSecret != "jwt-private-value" || cfg.MongoURI != "mongodb://mongo.example/chat" ||
		cfg.R2.SecretAccessKey != "r2-private-value" {
		t.Fatal("sensitive configuration was not mapped correctly")
	}
	if len(fake.requested) != len(gcpParameterNames) {
		t.Fatalf("render requests = %d, want %d", len(fake.requested), len(gcpParameterNames))
	}
}

func TestLoadGCPRequiresProject(t *testing.T) {
	t.Setenv(EnvGoogleCloudProject, "")
	called := false
	original := newGCPParameterClient
	newGCPParameterClient = func(context.Context) (gcpParameterRenderer, error) {
		called = true
		return nil, errors.New("must not be called")
	}
	t.Cleanup(func() { newGCPParameterClient = original })

	_, err := loadGCP(context.Background())
	if err == nil || !strings.Contains(err.Error(), EnvGoogleCloudProject) {
		t.Fatalf("loadGCP() error = %v", err)
	}
	if called {
		t.Fatal("created GCP client without a project ID")
	}
}

func TestLoadGCPDefaultsParameterLocation(t *testing.T) {
	project := "yevmiye-production"
	fake := &fakeGCPParameterClient{
		values: validGCPParameters(project, DefaultGCPParameterLocation),
		errFor: map[string]error{},
	}
	installGCPClientFactory(t, fake)
	setGCPEnvironment(t, project, "")

	cfg, err := loadGCP(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GCP.ParameterLocation != DefaultGCPParameterLocation {
		t.Fatalf("location = %q", cfg.GCP.ParameterLocation)
	}
}

func TestLoadGCPRejectsMissingParameter(t *testing.T) {
	project, location := "yevmiye-production", "global"
	values := validGCPParameters(project, location)
	delete(values, gcpResource(project, location, ParameterMongoDBName))
	fake := &fakeGCPParameterClient{values: values, errFor: map[string]error{}}
	installGCPClientFactory(t, fake)
	setGCPEnvironment(t, project, location)

	_, err := loadGCP(context.Background())
	if err == nil || !strings.Contains(err.Error(), ParameterMongoDBName) {
		t.Fatalf("loadGCP() error = %v", err)
	}
	if !fake.closed {
		t.Fatal("client not closed after missing parameter")
	}
}

func TestLoadGCPParameterAccessFailureDoesNotExposeSecrets(t *testing.T) {
	project, location := "yevmiye-production", "global"
	secretPayload := "do-not-leak-this-secret"
	failedResource := gcpResource(project, location, ParameterJWTSecret)
	fake := &fakeGCPParameterClient{
		values: validGCPParameters(project, location),
		errFor: map[string]error{failedResource: errors.New("permission denied")},
	}
	fake.values[failedResource] = []byte(secretPayload)
	installGCPClientFactory(t, fake)
	setGCPEnvironment(t, project, location)

	_, err := loadGCP(context.Background())
	if err == nil || !strings.Contains(err.Error(), ParameterJWTSecret) {
		t.Fatalf("loadGCP() error = %v", err)
	}
	if strings.Contains(err.Error(), secretPayload) {
		t.Fatal("error exposed a secret payload")
	}
}

func TestLoadGCPRejectsInvalidWebSocketOrigins(t *testing.T) {
	project, location := "yevmiye-production", "global"
	values := validGCPParameters(project, location)
	values[gcpResource(project, location, SSMParameterWSAllowedOrigins)] = []byte("https://example.com/private")
	fake := &fakeGCPParameterClient{values: values, errFor: map[string]error{}}
	installGCPClientFactory(t, fake)
	setGCPEnvironment(t, project, location)

	_, err := loadGCP(context.Background())
	if err == nil || !strings.Contains(err.Error(), "websocket origin") {
		t.Fatalf("loadGCP() error = %v", err)
	}
}

func TestLoadGCPRejectsInvalidPubSubTopic(t *testing.T) {
	project, location := "yevmiye-production", "global"
	values := validGCPParameters(project, location)
	values[gcpResource(project, location, ParameterSQSNotificationEventsQueueURL)] = []byte("chat-notifications")
	fake := &fakeGCPParameterClient{values: values, errFor: map[string]error{}}
	installGCPClientFactory(t, fake)
	setGCPEnvironment(t, project, location)

	_, err := loadGCP(context.Background())
	if err == nil || !strings.Contains(err.Error(), "fully qualified Pub/Sub topic") {
		t.Fatalf("loadGCP() error = %v", err)
	}
}

func installGCPClientFactory(t *testing.T, fake gcpParameterRenderer) {
	t.Helper()
	original := newGCPParameterClient
	newGCPParameterClient = func(context.Context) (gcpParameterRenderer, error) { return fake, nil }
	t.Cleanup(func() { newGCPParameterClient = original })
}

func setGCPEnvironment(t *testing.T, project, location string) {
	t.Helper()
	t.Setenv(EnvGoogleCloudProject, project)
	t.Setenv(EnvGCPParameterLocation, location)
	t.Setenv(EnvOTelTraceSampleRatio, "")
	for _, key := range []string{
		EnvRateLimitHTTPReadPerMinute, EnvRateLimitHTTPReadBurst,
		EnvRateLimitHTTPWritePerMinute, EnvRateLimitHTTPWriteBurst,
		EnvRateLimitWSTicketPerMinute, EnvRateLimitWSTicketBurst,
		EnvRateLimitWSMessagePerSecond, EnvRateLimitWSMessageBurst,
	} {
		t.Setenv(key, "")
	}
}

func validGCPParameters(project, location string) map[string][]byte {
	values := map[string]string{
		ParameterJWTSecret:                     "jwt-private-value",
		ParameterMongoURISecret:                "mongodb://mongo.example/chat",
		ParameterMongoDBName:                   "chat",
		ParameterSQSNotificationEventsQueueURL: "projects/yevmiye-production/topics/chat-notifications",
		SSMParameterR2AccountID:                "r2-account",
		SSMParameterR2AccessKeyID:              "r2-access",
		SSMParameterR2SecretAccessKey:          "r2-private-value",
		SSMParameterR2Bucket:                   "private-chat",
		SSMParameterWSAllowedOrigins:           "https://app.yevmiye.com",
	}
	result := make(map[string][]byte, len(values))
	for name, value := range values {
		result[gcpResource(project, location, name)] = []byte(value)
	}
	return result
}

func gcpResource(project, location, name string) string {
	return "projects/" + project + "/locations/" + location + "/parameters/" + name + "/versions/latest"
}
