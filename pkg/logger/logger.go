package logger

import (
	"log/slog"
	"os"
)

func Init(env string) slog.Handler {
	var opts slog.HandlerOptions

	switch env {
	case "production", "stage":
		opts = slog.HandlerOptions{Level: slog.LevelInfo}
		handler := slog.NewJSONHandler(os.Stdout, &opts)
		slog.SetDefault(slog.New(handler))
		return handler
	default:
		opts = slog.HandlerOptions{Level: slog.LevelDebug}
		handler := slog.NewTextHandler(os.Stdout, &opts)
		slog.SetDefault(slog.New(handler))
		return handler
	}
}
