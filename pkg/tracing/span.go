package tracing

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const domainTracerName = "github.com/vsayfb/gig-platform-chat-service"

// StartDomain starts a privacy-safe domain span. Callers must only pass
// bounded, low-cardinality attributes; private identifiers and user content do
// not belong in telemetry.
func StartDomain(
	ctx context.Context,
	name string,
	attributes ...attribute.KeyValue,
) (context.Context, trace.Span) {
	return otel.Tracer(domainTracerName).Start(
		ctx,
		name,
		trace.WithAttributes(attributes...),
	)
}

// EndDomain marks a span failed without recording the error text. Error
// strings at these boundaries can contain private identifiers or provider
// details, so logs remain the controlled diagnostic channel for that data.
func EndDomain(span trace.Span, err error) {
	if err != nil {
		span.SetStatus(codes.Error, "")
		span.SetAttributes(attribute.String("outcome", "error"))
	} else {
		span.SetStatus(codes.Ok, "")
		span.SetAttributes(attribute.String("outcome", "success"))
	}
	span.End()
}
