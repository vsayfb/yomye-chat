package config

import "testing"

func TestMustParseRatio(t *testing.T) {
	tests := []struct {
		input string
		want  float64
	}{
		{input: "0", want: 0},
		{input: "0.25", want: 0.25},
		{input: "1", want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := mustParseRatio(tt.input); got != tt.want {
				t.Fatalf("mustParseRatio(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestMustParseRatioRejectsOutOfRange(t *testing.T) {
	for _, input := range []string{"-0.1", "1.1", "NaN", "not-a-number"} {
		t.Run(input, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("mustParseRatio(%q) did not panic", input)
				}
			}()

			_ = mustParseRatio(input)
		})
	}
}

func TestMustParseAllowedOrigins(t *testing.T) {
	got := mustParseAllowedOrigins("https://Staging.Yevmiye.com, http://localhost:3000/,https://staging.yevmiye.com")
	want := []string{"https://staging.yevmiye.com", "http://localhost:3000"}
	if len(got) != len(want) {
		t.Fatalf("mustParseAllowedOrigins() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mustParseAllowedOrigins()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestMustParseAllowedOriginsRejectsInvalidOrigins(t *testing.T) {
	for _, input := range []string{"", "staging.yevmiye.com", "wss://staging.yevmiye.com", "https://staging.yevmiye.com/path"} {
		t.Run(input, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("mustParseAllowedOrigins(%q) did not panic", input)
				}
			}()
			_ = mustParseAllowedOrigins(input)
		})
	}
}

func TestAWSParameterPathByEnvironment(t *testing.T) {
	staging, err := awsParameterPath(EnvironmentStaging)
	if err != nil || staging != StagingParameterPath {
		t.Fatalf("staging path = %q, err = %v", staging, err)
	}
	production, err := awsParameterPath(EnvironmentProduction)
	if err != nil || production != ProductionParameterPath {
		t.Fatalf("production path = %q, err = %v", production, err)
	}
	if _, err := awsParameterPath(EnvironmentDevelopment); err == nil {
		t.Fatal("development unexpectedly has an AWS parameter path")
	}
}

func TestLoadEnvKeepsOTelCollectorEnvironmentControlled(t *testing.T) {
	setRequiredDevelopmentEnv(t)
	t.Setenv(EnvOTelCollectorAddr, "otel-collector:4317")

	cfg, err := loadEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OTelCollectorAddr != "otel-collector:4317" {
		t.Fatalf("OTelCollectorAddr = %q, want Docker collector hostname", cfg.OTelCollectorAddr)
	}
}

func TestLoadEnvDefaultsOTelCollectorToLocalhost(t *testing.T) {
	setRequiredDevelopmentEnv(t)
	t.Setenv(EnvOTelCollectorAddr, "")

	cfg, err := loadEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OTelCollectorAddr != DefaultOtelCollectorAddr {
		t.Fatalf("OTelCollectorAddr = %q, want %q", cfg.OTelCollectorAddr, DefaultOtelCollectorAddr)
	}
}

func TestMustParseDurationRejectsInvalidValue(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("mustParseDuration() did not panic")
		}
	}()
	_ = mustParseDuration("not-a-duration")
}

func setRequiredDevelopmentEnv(t *testing.T) {
	t.Helper()
	values := map[string]string{
		EnvJWTSecret:            "secret",
		EnvMongoURI:             "mongodb://localhost:27017",
		EnvMongoDB:              "chat_test",
		EnvUserServiceGRPCAddr:  "core:9090",
		EnvOTelTraceSampleRatio: "1.0",
		EnvR2AccountID:          "account",
		EnvR2AccessKeyID:        "access",
		EnvR2SecretAccessKey:    "secret",
		EnvR2Bucket:             "bucket",
		EnvR2ObjectPrefix:       "chat",
		EnvR2UploadURLExpiry:    "10m",
		EnvR2DownloadURLExpiry:  "5m",
		EnvAWSRegion:            "eu-central-1",
		EnvAWSAccessKeyID:       "test",
		EnvAWSSecretAccessKey:   "test",
		EnvSQSEndpoint:          "http://localhost:4566",
		SQSNotificationQueueURL: "http://localhost:4566/000000000000/notifications",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
}
