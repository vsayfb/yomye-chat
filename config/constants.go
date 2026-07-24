package config

const AppEnv = "APP_ENV"

const (
	EnvironmentDevelopment = "development"
	EnvironmentProduction  = "production"
)

const (
	EnvServiceName = "SERVICE_NAME"
	EnvServerPort  = "SERVER_PORT"

	EnvJWTSecret = "JWT_SECRET"

	EnvMongoURI = "MONGO_URI"
	EnvMongoDB  = "MONGO_DB"

	EnvUserServiceGRPCAddr = "USER_SERVICE_GRPC_ADDR"
	EnvMetricsServerPort   = "METRICS_SERVER_PORT"
	EnvOTelCollectorAddr   = "OTEL_COLLECTOR_ADDR"
)

const (
	DefaultServiceName         = "chat-service"
	DefaultServerPort          = "8081"
	DefaultMetricsServerPort   = ":9100"
	DefaultOtelCollectorAddr   = "localhost:4317"
	DefaultUserServiceGRPCAddr = "localhost:9090"
)
