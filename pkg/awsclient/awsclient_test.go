package awsclient

import (
	"context"
	"testing"

	"github.com/vsayfb/gig-platform-chat-service/config"
)

func TestNewUsesStaticCredentialsOnlyForDevelopment(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "environment-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "environment-secret")
	t.Setenv("AWS_SESSION_TOKEN", "")

	cfg, err := New(context.Background(), &config.Config{
		AppEnv: config.EnvironmentDevelopment,
		AWS: config.AWSConfig{
			Region:          "eu-central-1",
			AccessKeyID:     "development-key",
			SecretAccessKey: "development-secret",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := cfg.Credentials.Retrieve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if credentials.AccessKeyID != "development-key" {
		t.Fatalf("development credentials = %q, want static development key", credentials.AccessKeyID)
	}
}

func TestNewUsesDefaultCredentialChainOutsideDevelopment(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "environment-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "environment-secret")
	t.Setenv("AWS_SESSION_TOKEN", "rotating-session-token")

	cfg, err := New(context.Background(), &config.Config{
		AppEnv: config.EnvironmentStaging,
		AWS: config.AWSConfig{
			Region:          "eu-central-1",
			AccessKeyID:     "must-not-be-used",
			SecretAccessKey: "must-not-be-used",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := cfg.Credentials.Retrieve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if credentials.AccessKeyID != "environment-key" || credentials.SessionToken != "rotating-session-token" {
		t.Fatalf("staging credentials = %#v, want default-chain environment credentials", credentials)
	}
}
