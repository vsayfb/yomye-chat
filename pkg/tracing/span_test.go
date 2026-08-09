package tracing

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestDomainSpanDoesNotRecordErrorText(t *testing.T) {
	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})

	const privateValue = "chat/conversations/private-id/secret-photo.jpg"
	ctx, span := StartDomain(context.Background(), "chat.test",
		attribute.String("chat.domain", "direct"),
	)
	_ = ctx
	EndDomain(span, errors.New(privateValue))

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(ended))
	}
	got := ended[0]
	if got.Status().Code != codes.Error {
		t.Fatalf("span status = %v, want error", got.Status().Code)
	}

	var rendered strings.Builder
	rendered.WriteString(got.Name())
	rendered.WriteString(got.Status().Description)
	for _, attr := range got.Attributes() {
		rendered.WriteString(string(attr.Key))
		rendered.WriteString(attr.Value.Emit())
	}
	for _, event := range got.Events() {
		rendered.WriteString(event.Name)
		for _, attr := range event.Attributes {
			rendered.WriteString(string(attr.Key))
			rendered.WriteString(attr.Value.Emit())
		}
	}
	if strings.Contains(rendered.String(), privateValue) {
		t.Fatal("private error text was exported in the span")
	}
}

func TestDomainSpanPreservesParentAcrossWithoutCancel(t *testing.T) {
	previous := otel.GetTracerProvider()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})

	parentCtx, parent := otel.Tracer("test").Start(context.Background(), "socket")
	detached := context.WithoutCancel(parentCtx)
	_, child := StartDomain(detached, "chat.message.process")
	EndDomain(child, nil)
	parent.End()

	ended := recorder.Ended()
	if len(ended) != 2 {
		t.Fatalf("ended spans = %d, want 2", len(ended))
	}
	var childSpan sdktrace.ReadOnlySpan
	for _, candidate := range ended {
		if candidate.Name() == "chat.message.process" {
			childSpan = candidate
			break
		}
	}
	if childSpan == nil {
		t.Fatal("message-processing span was not exported")
	}
	if childSpan.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Fatal("message-processing span lost its socket parent")
	}
}
