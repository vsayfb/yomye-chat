package config

const AppEnv = "APP_ENV"

const (
	EnvironmentDevelopment = "development"
	EnvironmentStaging     = "staging"
	EnvironmentProduction  = "production"
)

const (
	EnvServiceName = "SERVICE_NAME"
	EnvServerPort  = "SERVER_PORT"

	EnvJWTSecret = "JWT_SECRET"

	EnvMongoURI = "MONGO_URI"
	EnvMongoDB  = "MONGO_DB"

	EnvUserServiceGRPCAddr  = "USER_SERVICE_GRPC_ADDR"
	EnvOTelCollectorAddr    = "OTEL_COLLECTOR_ADDR"
	EnvOTelTraceSampleRatio = "OTEL_TRACE_SAMPLE_RATIO"

	EnvR2AccountID         = "R2_ACCOUNT_ID"
	EnvR2AccessKeyID       = "R2_ACCESS_KEY_ID"
	EnvR2SecretAccessKey   = "R2_SECRET_ACCESS_KEY"
	EnvR2Bucket            = "R2_BUCKET"
	EnvR2ObjectPrefix      = "R2_OBJECT_PREFIX"
	EnvR2UploadURLExpiry   = "R2_UPLOAD_URL_EXPIRY"
	EnvR2DownloadURLExpiry = "R2_DOWNLOAD_URL_EXPIRY"
)

const (
	DefaultServiceName                 = "chat-service"
	DefaultServerPort                  = "8081"
	DefaultOtelCollectorAddr           = "localhost:4317"
	DefaultUserServiceGRPCAddr         = "localhost:9090"
	DefaultDevelopmentTraceSampleRatio = "1.0"
	DefaultStagingTraceSampleRatio     = "0.25"

	DefaultJWTExpiration        = "15m"
	DefaultJWTRefreshExpiration = "720h"
	DefaultR2ObjectPrefix       = "chat"
	DefaultR2UploadURLExpiry    = "10m"
	DefaultR2DownloadURLExpiry  = "5m"
)
