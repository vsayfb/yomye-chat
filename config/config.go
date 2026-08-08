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

	UserServiceGRPCAddr  string
	OTelCollectorAddr    string
	OTelTraceSampleRatio float64
	WSAllowedOrigins     []string
	RateLimits           RateLimitConfig

	R2 R2Config

	AWS AWSConfig
}

type RateLimitConfig struct {
	HTTPReadPerMinute  int
	HTTPReadBurst      int
	HTTPWritePerMinute int
	HTTPWriteBurst     int
	WSTicketPerMinute  int
	WSTicketBurst      int
	WSMessagePerSecond int
	WSMessageBurst     int
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

	if env == EnvironmentStaging {
		return loadAWS(ctx, EnvironmentStaging)
	}

	if env == EnvironmentProduction {
		return loadGCP(ctx)
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

func loadRateLimits() RateLimitConfig {
	return RateLimitConfig{
		HTTPReadPerMinute: mustParsePositiveInt(getEnv(
			EnvRateLimitHTTPReadPerMinute, DefaultRateLimitHTTPReadPerMinute,
		)),
		HTTPReadBurst: mustParsePositiveInt(getEnv(
			EnvRateLimitHTTPReadBurst, DefaultRateLimitHTTPReadBurst,
		)),
		HTTPWritePerMinute: mustParsePositiveInt(getEnv(
			EnvRateLimitHTTPWritePerMinute, DefaultRateLimitHTTPWritePerMinute,
		)),
		HTTPWriteBurst: mustParsePositiveInt(getEnv(
			EnvRateLimitHTTPWriteBurst, DefaultRateLimitHTTPWriteBurst,
		)),
		WSTicketPerMinute: mustParsePositiveInt(getEnv(
			EnvRateLimitWSTicketPerMinute, DefaultRateLimitWSTicketPerMinute,
		)),
		WSTicketBurst: mustParsePositiveInt(getEnv(
			EnvRateLimitWSTicketBurst, DefaultRateLimitWSTicketBurst,
		)),
		WSMessagePerSecond: mustParsePositiveInt(getEnv(
			EnvRateLimitWSMessagePerSecond, DefaultRateLimitWSMessagePerSecond,
		)),
		WSMessageBurst: mustParsePositiveInt(getEnv(
			EnvRateLimitWSMessageBurst, DefaultRateLimitWSMessageBurst,
		)),
	}
}
