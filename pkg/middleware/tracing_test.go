package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTracingUsesRoutePathWithoutQueryCredentials(t *testing.T) {
	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	defer func() {
		_ = provider.Shutdown(t.Context())
		otel.SetTracerProvider(previous)
	}()

	router := chi.NewRouter()
	router.Use(TracingMiddleware)
	router.Get("/ws", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/ws?ticket=super-secret-ticket", nil))

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	span := spans[0]
	if span.Name() != "/ws" {
		t.Fatalf("span name = %q, want route /ws", span.Name())
	}
	if strings.Contains(span.Name(), "super-secret-ticket") {
		t.Fatalf("span name exposed query credential: %q", span.Name())
	}
	for _, attr := range span.Attributes() {
		if attr.Value.Type() == attribute.STRING && strings.Contains(attr.Value.AsString(), "super-secret-ticket") {
			t.Fatalf("span attribute %q exposed query credential", attr.Key)
		}
	}
}
