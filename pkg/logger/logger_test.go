package logger

import (
	"context"
	"log/slog"
	"testing"
)

func TestInitStagingUsesJSONAtInfoLevel(t *testing.T) {
	handler := Init("staging")

	if _, ok := handler.(*slog.JSONHandler); !ok {
		t.Fatalf("handler type = %T, want *slog.JSONHandler", handler)
	}

	if handler.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("staging logger unexpectedly enables debug logs")
	}
	if !handler.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("staging logger does not enable info logs")
	}
}

func TestInitDevelopmentUsesTextAtDebugLevel(t *testing.T) {
	handler := Init("development")

	if _, ok := handler.(*slog.TextHandler); !ok {
		t.Fatalf("handler type = %T, want *slog.TextHandler", handler)
	}

	if !handler.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("development logger does not enable debug logs")
	}
}
