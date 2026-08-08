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
