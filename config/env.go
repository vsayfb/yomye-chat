package config

const (
	EnvAWSRegion            = "AWS_REGION"
	EnvAWSAccessKeyID       = "AWS_ACCESS_KEY_ID"
	EnvAWSSecretAccessKey   = "AWS_SECRET_ACCESS_KEY"
	EnvSQSEndpoint          = "AWS_SQS_ENDPOINT"
	SQSNotificationQueueURL = "SQS_NOTIFICATION_QUEUE_URL"
)

func loadEnv() (*Config, error) {
	return &Config{
		ServiceName:         getEnv(EnvServiceName, DefaultServiceName),
		AppEnv:              EnvironmentDevelopment,
		Port:                getEnv(EnvServerPort, DefaultServerPort),
		JWTSecret:           mustGetEnv(EnvJWTSecret),
		MongoURI:            mustGetEnv(EnvMongoURI),
		MongoDB:             mustGetEnv(EnvMongoDB),
		UserServiceGRPCAddr: getEnv(EnvUserServiceGRPCAddr, DefaultUserServiceGRPCAddr),
		MetricsServerPort:   getEnv(EnvMetricsServerPort, DefaultMetricsServerPort),
		OTelCollectorAddr:   getEnv(EnvOTelCollectorAddr, DefaultOtelCollectorAddr),
		AWS: AWSConfig{
			Region:                  mustGetEnv(EnvAWSRegion),
			AccessKeyID:             mustGetEnv(EnvAWSAccessKeyID),
			SecretAccessKey:         mustGetEnv(EnvAWSSecretAccessKey),
			SQSEndpoint:             mustGetEnv(EnvSQSEndpoint),
			SQSNotificationQueueURL: mustGetEnv(SQSNotificationQueueURL),
		},
	}, nil
}
