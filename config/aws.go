package config

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

const ParameterPath = "/yevmiye/staging/"

const (
	ParameterMongoURISecret                = "mongo-db-uri"
	ParameterMongoDBName                   = "mongo-db-name"
	ParameterSQSNotificationEventsQueueURL = "sqs-notification-events-queue-url"
	ParameterJWTSecret                     = "jwt-secret"
	SSMParameterR2AccountID                = "r2-account-id"
	SSMParameterR2AccessKeyID              = "r2-access-key-id"
	SSMParameterR2SecretAccessKey          = "r2-secret-access-key"
	SSMParameterR2Bucket                   = "r2-bucket"
)

func loadAWS(ctx context.Context) (*Config, error) {
	awsCfg, err := awscfg.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	ssmClient := ssm.NewFromConfig(awsCfg)

	params, err := loadParameters(ctx, ssmClient)
	if err != nil {
		return nil, err
	}

	// Log only parameter names, never values.
	for k := range params {
		slog.Debug("loaded SSM parameter", "name", k)
	}

	return &Config{
		ServiceName: getEnv(EnvServiceName, DefaultServiceName),
		AppEnv:      EnvironmentStaging,
		Port:        getEnv(EnvServerPort, DefaultServerPort),

		JWTSecret: params[ParameterJWTSecret],
		MongoURI:  params[ParameterMongoURISecret],
		MongoDB:   params[ParameterMongoDBName],

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

func loadParameters(ctx context.Context, client *ssm.Client) (map[string]string, error) {
	names := []string{
		parameter(ParameterJWTSecret),
		parameter(ParameterMongoURISecret),
		parameter(ParameterMongoDBName),
		parameter(ParameterSQSNotificationEventsQueueURL),
		parameter(SSMParameterR2AccountID),
		parameter(SSMParameterR2AccessKeyID),
		parameter(SSMParameterR2SecretAccessKey),
		parameter(SSMParameterR2Bucket),
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

		key, found := strings.CutPrefix(name, ParameterPath)
		if !found {
			return nil, fmt.Errorf(
				"unexpected SSM parameter name %q; expected prefix %q",
				name,
				ParameterPath,
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
	}

	for _, key := range required {
		if _, ok := params[key]; !ok {
			return nil, fmt.Errorf("required SSM parameter %q was not returned", key)
		}
	}

	return params, nil
}

func parameter(name string) string {
	return ParameterPath + name
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
