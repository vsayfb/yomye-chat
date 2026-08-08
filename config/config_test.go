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
