package config

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

const ParameterPath = "/yevmiye/app/"

const (
	ParameterMongoURISecret                = "mongo-db-uri"
	ParameterMongoDBName                   = "mongo-db-name"
	ParameterSQSNotificationEventsQueueURL = "sqs-notification-events-queue-url"
	ParameterJWTSecret                     = "jwt-secret"
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

	creds, err := awsCfg.Credentials.Retrieve(ctx)
	if err != nil {
		return nil, fmt.Errorf("retrieve AWS credentials: %w", err)
	}

	var endpoint string
	if awsCfg.BaseEndpoint != nil {
		endpoint = *awsCfg.BaseEndpoint
	}

	return &Config{
		ServiceName: getEnv(EnvServiceName, DefaultServiceName),
		AppEnv:      EnvironmentProduction,
		Port:        getEnv(EnvServerPort, DefaultServerPort),

		JWTSecret: params[ParameterJWTSecret],
		MongoURI:  params[ParameterMongoURISecret],
		MongoDB:   params[ParameterMongoDBName],

		AWS: AWSConfig{
			Region:                  awsCfg.Region,
			AccessKeyID:             creds.AccessKeyID,
			SecretAccessKey:         creds.SecretAccessKey,
			SQSEndpoint:             endpoint,
			SQSNotificationQueueURL: params[ParameterSQSNotificationEventsQueueURL],
		},

		UserServiceGRPCAddr: getEnv(
			EnvUserServiceGRPCAddr,
			DefaultUserServiceGRPCAddr,
		),
		MetricsServerPort: getEnv(
			EnvMetricsServerPort,
			DefaultMetricsServerPort,
		),
		OTelCollectorAddr: getEnv(
			EnvOTelCollectorAddr,
			DefaultOtelCollectorAddr,
		),
	}, nil
}

func loadParameters(ctx context.Context, client *ssm.Client) (map[string]string, error) {
	names := []string{
		parameter(ParameterJWTSecret),
		parameter(ParameterMongoURISecret),
		parameter(ParameterMongoDBName),
		parameter(ParameterSQSNotificationEventsQueueURL),
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
