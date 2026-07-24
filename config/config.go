package config

import (
	"context"
	"fmt"
	"os"
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

	AWS AWSConfig
}

type AWSConfig struct {
	Region              string
	AccessKeyID         string
	SecretAccessKey     string
	SQSEndpoint         string
	SQSNotificationQueueURL string
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
