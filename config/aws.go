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
	ParameterMongoURISecret                = "mongo_db_uri"
	ParameterMongoDBName                   = "mongo_db_name"
	ParameterSQSNotificationEventsQueueURL = "sqs_notification_events_queue_url"
	ParameterJWTSecret                     = "jwt_secret"
)

func loadAWS(ctx context.Context) (*Config, error) {

	awsCfg, err := awscfg.LoadDefaultConfig(ctx)

	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	ssmClient := ssm.NewFromConfig(awsCfg)

	params, err := loadParameters(ctx, ssmClient)

	slog.Info("Parameters are loaded", "params", params)

	if err != nil {
		return nil, err
	}

	return &Config{
		ServiceName:         getEnv(EnvServiceName, DefaultServiceName),
		AppEnv:              EnvironmentProduction,
		Port:                getEnv(EnvServerPort, DefaultServerPort),
		JWTSecret:           params[ParameterJWTSecret],
		MongoURI:            params[ParameterMongoURISecret],
		MongoDB:             params[ParameterMongoDBName],
		UserServiceGRPCAddr: getEnv(EnvUserServiceGRPCAddr, DefaultUserServiceGRPCAddr),
		MetricsServerPort:   getEnv(EnvMetricsServerPort, DefaultMetricsServerPort),
		OTelCollectorAddr:   getEnv(EnvOTelCollectorAddr, DefaultOtelCollectorAddr),
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

	params := make(map[string]string)

	for _, p := range out.Parameters {
		key := strings.TrimPrefix(aws.ToString(p.Name), ParameterPath+"/")
		params[key] = aws.ToString(p.Value)
	}

	return params, nil
}

func parameter(name string) string {
	return ParameterPath + name
}
