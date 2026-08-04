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
		R2: R2Config{
			AccountID:       mustGetEnv(EnvR2AccountID),
			AccessKeyID:     mustGetEnv(EnvR2AccessKeyID),
			SecretAccessKey: mustGetEnv(EnvR2SecretAccessKey),
			Bucket:          mustGetEnv(EnvR2Bucket),
			ObjectPrefix:    getEnv(EnvR2ObjectPrefix, DefaultR2ObjectPrefix),
			UploadURLExpiry: mustParseDuration(
				getEnv(EnvR2UploadURLExpiry, DefaultR2UploadURLExpiry),
			),
			DownloadURLExpiry: mustParseDuration(
				getEnv(EnvR2DownloadURLExpiry, DefaultR2DownloadURLExpiry),
			),
		},
		AWS: AWSConfig{
			Region:                  mustGetEnv(EnvAWSRegion),
			AccessKeyID:             mustGetEnv(EnvAWSAccessKeyID),
			SecretAccessKey:         mustGetEnv(EnvAWSSecretAccessKey),
			SQSEndpoint:             mustGetEnv(EnvSQSEndpoint),
			SQSNotificationQueueURL: mustGetEnv(SQSNotificationQueueURL),
		},
	}, nil
}
