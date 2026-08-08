package config

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

const StagingParameterPath = "/yevmiye/staging/"

const ProductionParameterPath = "/yevmiye/production/"

const (
	ParameterMongoURISecret                = "mongo-db-uri"
	ParameterMongoDBName                   = "mongo-db-name"
	ParameterSQSNotificationEventsQueueURL = "sqs-notification-events-queue-url"
	ParameterJWTSecret                     = "jwt-secret"
	SSMParameterR2AccountID                = "r2-account-id"
	SSMParameterR2AccessKeyID              = "r2-access-key-id"
	SSMParameterR2SecretAccessKey          = "r2-secret-access-key"
	SSMParameterR2Bucket                   = "r2-bucket"
	SSMParameterWSAllowedOrigins           = "ws-allowed-origins"
)

func loadAWS(ctx context.Context, environment string) (*Config, error) {
	awsCfg, err := awscfg.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	ssmClient := ssm.NewFromConfig(awsCfg)

	parameterPath, err := awsParameterPath(environment)
	if err != nil {
		return nil, err
	}

	params, err := loadParameters(ctx, ssmClient, parameterPath)
	if err != nil {
		return nil, err
	}

	// Log only parameter names, never values.
	for k := range params {
		slog.Debug("loaded SSM parameter", "name", k)
	}

	return &Config{
		ServiceName: getEnv(EnvServiceName, DefaultServiceName),
		AppEnv:      environment,
		Port:        getEnv(EnvServerPort, DefaultServerPort),

		JWTSecret:        params[ParameterJWTSecret],
		WSAllowedOrigins: mustParseAllowedOrigins(params[SSMParameterWSAllowedOrigins]),
		MongoURI:         params[ParameterMongoURISecret],
		MongoDB:          params[ParameterMongoDBName],

		AWS: AWSConfig{
			Region:                  awsCfg.Region,
			SQSNotificationQueueURL: params[ParameterSQSNotificationEventsQueueURL],
		},

		R2: R2Config{
			AccountID:         params[SSMParameterR2AccountID],
			AccessKeyID:       params[SSMParameterR2AccessKeyID],
			SecretAccessKey:   params[SSMParameterR2SecretAccessKey],
			Bucket:            params[SSMParameterR2Bucket],
			ObjectPrefix:      DefaultR2ObjectPrefix,
			UploadURLExpiry:   mustParseDuration(DefaultR2UploadURLExpiry),
			DownloadURLExpiry: mustParseDuration(DefaultR2DownloadURLExpiry),
		},

		UserServiceGRPCAddr: getEnv(
			EnvUserServiceGRPCAddr,
			DefaultUserServiceGRPCAddr,
		),
		OTelCollectorAddr: getEnv(
			EnvOTelCollectorAddr,
			DefaultOtelCollectorAddr,
		),
		OTelTraceSampleRatio: mustParseRatio(getEnv(
			EnvOTelTraceSampleRatio,
			DefaultStagingTraceSampleRatio,
		)),
	}, nil
}

func awsParameterPath(environment string) (string, error) {
	switch environment {
	case EnvironmentStaging:
		return StagingParameterPath, nil
	case EnvironmentProduction:
		return ProductionParameterPath, nil
	default:
		return "", fmt.Errorf("unsupported AWS environment %q", environment)
	}
}

func loadParameters(ctx context.Context, client *ssm.Client, parameterPath string) (map[string]string, error) {
	names := []string{
		parameter(parameterPath, ParameterJWTSecret),
		parameter(parameterPath, ParameterMongoURISecret),
		parameter(parameterPath, ParameterMongoDBName),
		parameter(parameterPath, ParameterSQSNotificationEventsQueueURL),
		parameter(parameterPath, SSMParameterR2AccountID),
		parameter(parameterPath, SSMParameterR2AccessKeyID),
		parameter(parameterPath, SSMParameterR2SecretAccessKey),
		parameter(parameterPath, SSMParameterR2Bucket),
		parameter(parameterPath, SSMParameterWSAllowedOrigins),
	}

	out, err := client.GetParameters(ctx, &ssm.GetParametersInput{
		Names:          names,
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("read parameter store: %w", err)
	}

	if len(out.InvalidParameters) > 0 {
		return nil, fmt.Errorf(
			"missing or invalid SSM parameters: %v",
			out.InvalidParameters,
		)
	}

	params := make(map[string]string, len(out.Parameters))

	for _, p := range out.Parameters {
		name := aws.ToString(p.Name)
		value := aws.ToString(p.Value)

		key, found := strings.CutPrefix(name, parameterPath)
		if !found {
			return nil, fmt.Errorf(
				"unexpected SSM parameter name %q; expected prefix %q",
				name,
				parameterPath,
			)
		}

		params[key] = value
	}

	// Verify that every expected parameter was returned.
	required := []string{
		ParameterJWTSecret,
		ParameterMongoURISecret,
		ParameterMongoDBName,
		ParameterSQSNotificationEventsQueueURL,
		SSMParameterR2AccountID,
		SSMParameterR2AccessKeyID,
		SSMParameterR2SecretAccessKey,
		SSMParameterR2Bucket,
		SSMParameterWSAllowedOrigins,
	}

	for _, key := range required {
		if _, ok := params[key]; !ok {
			return nil, fmt.Errorf("required SSM parameter %q was not returned", key)
		}
	}

	return params, nil
}

func parameter(parameterPath, name string) string {
	return parameterPath + name
}

func mustParseDuration(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		panic(fmt.Sprintf("invalid duration %q: %v", s, err))
	}
	return d
}

func mustParseRatio(s string) float64 {
	ratio, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(ratio) || ratio < 0 || ratio > 1 {
		panic(fmt.Sprintf("invalid trace sample ratio %q; expected a number between 0 and 1", s))
	}

	return ratio
}

func mustParseAllowedOrigins(value string) []string {
	parts := strings.Split(value, ",")
	origins := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))

	for _, part := range parts {
		origin, err := canonicalOrigin(strings.TrimSpace(part))
		if err != nil {
			panic(fmt.Sprintf("invalid websocket origin %q: %v", part, err))
		}
		if _, ok := seen[origin]; ok {
			continue
		}
		seen[origin] = struct{}{}
		origins = append(origins, origin)
	}

	if len(origins) == 0 {
		panic("websocket allowed origins must not be empty")
	}

	return origins
}

func canonicalOrigin(value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("origin is empty")
	}

	u, err := url.Parse(value)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("scheme must be http or https")
	}
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("origin must contain only scheme and host")
	}
	if u.Path != "" && u.Path != "/" {
		return "", fmt.Errorf("origin must not contain a path")
	}

	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), nil
}
