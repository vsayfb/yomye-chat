package config

import (
	"context"
	"fmt"
	"os"
	"time"
)

type Config struct {
	ServiceName string
	AppEnv      string
	Port        string
	GRPCPort    string
	JWTSecret   string

	MongoURI string
	MongoDB  string

	UserServiceGRPCAddr string
	MetricsServerPort   string
	OTelCollectorAddr   string

	R2 R2Config

	AWS AWSConfig
}

type AWSConfig struct {
	Region                  string
	AccessKeyID             string
	SecretAccessKey         string
	SQSEndpoint             string
	SQSNotificationQueueURL string
	SQSChatQueueURL         string
}

type R2Config struct {
	AccountID         string
	AccessKeyID       string
	SecretAccessKey   string
	Bucket            string
	ObjectPrefix      string
	UploadURLExpiry   time.Duration
	DownloadURLExpiry time.Duration
}

func Load(ctx context.Context) (*Config, error) {
	env := getEnv(AppEnv, EnvironmentDevelopment)

	if env == EnvironmentProduction {
		return loadAWS(ctx)
	}

	return loadEnv()
}

func mustGetEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("missing required env var: %s", key))
	}
	return v
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}
