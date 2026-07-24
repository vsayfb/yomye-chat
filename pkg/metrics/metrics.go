package metrics

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

var (
	meter = otel.Meter("http.server")

	HttpRequestsTotal   metric.Int64Counter
	HttpRequestDuration metric.Float64Histogram
)

func Register() error {
	var err error

	HttpRequestsTotal, err = meter.Int64Counter(
		"chat.http.requests_total",
		metric.WithDescription("Total HTTP requests handled, by route, method and status."),
		metric.WithUnit("1"),
	)

	if err != nil {
		return err
	}

	HttpRequestDuration, err = meter.Float64Histogram(
		"chat.http.request.duration",
		metric.WithDescription("HTTP request latency in seconds."),
		metric.WithUnit("s"),
	)

	if err != nil {
		return err
	}

	return nil
}
