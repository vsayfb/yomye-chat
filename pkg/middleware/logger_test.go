package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoggerDoesNotLogQueryStringCredentials(t *testing.T) {
	previous := slog.Default()
	var output bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	defer slog.SetDefault(previous)

	req := httptest.NewRequest(http.MethodGet, "/ws?ticket=super-secret-ticket&token=legacy-secret", nil)
	rr := httptest.NewRecorder()

	Logger(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rr, req)

	logged := output.String()
	if strings.Contains(logged, "super-secret-ticket") || strings.Contains(logged, "legacy-secret") {
		t.Fatalf("request logger exposed query-string credential: %s", logged)
	}
	if !strings.Contains(logged, `"path":"/ws"`) {
		t.Fatalf("request logger did not record sanitized path: %s", logged)
	}
}
