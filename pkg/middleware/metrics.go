package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/vsayfb/gig-platform-chat-service/pkg/metrics"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

func MetricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Use chi's WrapResponseWriter to prevent hijacking error
		rw := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(rw, r)

		route := chi.RouteContext(r.Context()).RoutePattern()

		if route == "" {
			route = "unknown"
		}

		status := strconv.Itoa(rw.Status())
		duration := time.Since(start).Seconds()

		attrs := metric.WithAttributes(
			attribute.String("route", route),
			attribute.String("method", r.Method),
			attribute.String("status", status),
		)

		metrics.HttpRequestsTotal.Add(r.Context(), 1, attrs)
		metrics.HttpRequestDuration.Record(r.Context(), duration, attrs)
	})
}
