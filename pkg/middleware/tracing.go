package middleware

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

func TracingMiddleware(next http.Handler) http.Handler {
	tracer := otel.Tracer("http.server")
	propagator := otel.GetTextMapPropagator()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))

		ctx, span := tracer.Start(ctx, r.URL.Path,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				semconv.HTTPRequestMethodKey.String(r.Method),
				semconv.URLPath(r.URL.Path),
			),
		)
		defer span.End()

		rw := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(rw, r.WithContext(ctx))

		if pattern := chi.RouteContext(r.Context()).RoutePattern(); pattern != "" {
			span.SetName(pattern)
			span.SetAttributes(semconv.HTTPRoute(pattern))
		}

		span.SetAttributes(attribute.Int("http.status_code", rw.Status()))
	})
}
