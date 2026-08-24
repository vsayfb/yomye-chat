package config

import (
	"context"
	"fmt"
	"strings"

	"cloud.google.com/go/parametermanager/apiv1"
	"cloud.google.com/go/parametermanager/apiv1/parametermanagerpb"
)

var gcpParameterNames = []string{
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

type gcpParameterRenderer interface {
	RenderParameterVersion(context.Context, *parametermanagerpb.RenderParameterVersionRequest) (*parametermanagerpb.RenderParameterVersionResponse, error)
	Close() error
}

type parameterManagerClient struct {
	*parametermanager.Client
}

func (c *parameterManagerClient) RenderParameterVersion(
	ctx context.Context,
	req *parametermanagerpb.RenderParameterVersionRequest,
) (*parametermanagerpb.RenderParameterVersionResponse, error) {
	return c.Client.RenderParameterVersion(ctx, req)
}

var newGCPParameterClient = func(ctx context.Context) (gcpParameterRenderer, error) {
	client, err := parametermanager.NewClient(ctx)
	if err != nil {
		return nil, err
	}
	return &parameterManagerClient{Client: client}, nil
}

func loadGCP(ctx context.Context) (*Config, error) {
	projectID := strings.TrimSpace(getEnv(EnvGoogleCloudProject, ""))
	if projectID == "" {
		return nil, fmt.Errorf("%s is required for production", EnvGoogleCloudProject)
	}
	location := strings.TrimSpace(getEnv(EnvGCPParameterLocation, DefaultGCPParameterLocation))
	if location == "" {
		location = DefaultGCPParameterLocation
	}

	client, err := newGCPParameterClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create GCP Parameter Manager client: %w", err)
	}
	defer client.Close()

	params, err := loadGCPParameters(ctx, client, projectID, location, gcpParameterNames)
	if err != nil {
		return nil, err
	}
	origins, err := parseAllowedOrigins(params[SSMParameterWSAllowedOrigins])
	if err != nil {
		return nil, err
	}
	topic := params[ParameterSQSNotificationEventsQueueURL]
	if err := validatePubSubTopicName(topic); err != nil {
		return nil, fmt.Errorf("parameter %q: %w", ParameterSQSNotificationEventsQueueURL, err)
	}

	return &Config{
		ServiceName:      getEnv(EnvServiceName, DefaultServiceName),
		AppEnv:           EnvironmentProduction,
		Port:             getEnv(EnvServerPort, DefaultServerPort),
		JWTSecret:        params[ParameterJWTSecret],
		WSAllowedOrigins: origins,
		MongoURI:         params[ParameterMongoURISecret],
		MongoDB:          params[ParameterMongoDBName],
		GCP: GCPConfig{
			ProjectID:             projectID,
			ParameterLocation:     location,
			NotificationTopicName: topic,
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
		UserServiceGRPCAddr:  getEnv(EnvUserServiceGRPCAddr, DefaultUserServiceGRPCAddr),
		OTelCollectorAddr:    getEnv(EnvOTelCollectorAddr, DefaultOtelCollectorAddr),
		OTelTraceSampleRatio: mustParseRatio(getEnv(EnvOTelTraceSampleRatio, DefaultProductionTraceSampleRatio)),
		RateLimits:           loadRateLimits(),
	}, nil
}

func loadGCPParameters(
	ctx context.Context,
	client gcpParameterRenderer,
	projectID string,
	location string,
	names []string,
) (map[string]string, error) {
	params := make(map[string]string, len(names))
	for _, name := range names {
		resourceName := fmt.Sprintf(
			"projects/%s/locations/%s/parameters/%s/versions/latest",
			projectID, location, name,
		)
		response, err := client.RenderParameterVersion(ctx, &parametermanagerpb.RenderParameterVersionRequest{Name: resourceName})
		if err != nil {
			return nil, fmt.Errorf("render GCP parameter %q: %w", name, err)
		}
		if response == nil || len(response.GetRenderedPayload()) == 0 {
			return nil, fmt.Errorf("GCP parameter %q returned an empty payload", name)
		}
		params[name] = string(response.GetRenderedPayload())
	}
	return params, nil
}

func validatePubSubTopicName(value string) error {
	parts := strings.Split(value, "/")
	if len(parts) != 4 || parts[0] != "projects" || parts[1] == "" || parts[2] != "topics" || parts[3] == "" {
		return fmt.Errorf("must be a fully qualified Pub/Sub topic resource name")
	}
	return nil
}
