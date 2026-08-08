package tracing

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestOTelHandlerInjectsTraceAndSpanIDs(t *testing.T) {
	t.Parallel()

	traceID := trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	spanID := trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8}
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID,
		SpanID:  spanID,
		Remote:  true,
	})
	ctx := trace.ContextWithRemoteSpanContext(context.Background(), spanContext)

	var output bytes.Buffer
	logger := slog.New(NewOTelHandler(slog.NewJSONHandler(&output, nil)))
	logger.InfoContext(ctx, "message", "key", "value")

	logged := output.String()
	if !strings.Contains(logged, traceID.String()) {
		t.Fatalf("log missing trace ID: %s", logged)
	}
	if !strings.Contains(logged, spanID.String()) {
		t.Fatalf("log missing span ID: %s", logged)
	}
	if !strings.Contains(logged, `"key":"value"`) {
		t.Fatalf("wrapped handler lost attributes: %s", logged)
	}
}

func TestOTelHandlerDoesNotInventTraceFields(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	logger := slog.New(NewOTelHandler(slog.NewJSONHandler(&output, nil)))
	logger.InfoContext(context.Background(), "message")

	logged := output.String()
	if strings.Contains(logged, "trace_id") || strings.Contains(logged, "span_id") {
		t.Fatalf("log unexpectedly contains trace fields without a valid span: %s", logged)
	}
}
