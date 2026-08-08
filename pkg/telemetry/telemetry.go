package telemetry

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
)

const collectorConnectTimeout = 5 * time.Second

type Shutdown func(context.Context) error

func Init(
	ctx context.Context,
	serviceName string,
	collectorAddr string,
	traceSampleRatio float64,
) (Shutdown, error) {
	if math.IsNaN(traceSampleRatio) || traceSampleRatio < 0 || traceSampleRatio > 1 {
		return nil, fmt.Errorf(
			"invalid trace sample ratio %v; expected a number between 0 and 1",
			traceSampleRatio,
		)
	}

	conn, err := connectCollector(ctx, collectorAddr)
	if err != nil {
		return nil, err
	}

	closeConnOnError := true
	defer func() {
		if closeConnOnError {
			_ = conn.Close()
		}
	}()

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create otel resource: %w", err)
	}

	traceExporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithGRPCConn(conn))
	if err != nil {
		return nil, fmt.Errorf("create otlp trace exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(
			sdktrace.ParentBased(sdktrace.TraceIDRatioBased(traceSampleRatio)),
		),
	)

	metricExporter, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithGRPCConn(conn))
	if err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tp.Shutdown(shutdownCtx)
		return nil, fmt.Errorf("create otlp metric exporter: %w", err)
	}

	reader := sdkmetric.NewPeriodicReader(
		metricExporter,
		sdkmetric.WithInterval(15*time.Second),
	)

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithResource(res),
	)

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	closeConnOnError = false

	shutdown := func(ctx context.Context) error {
		var errs []error

		if err := tp.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown tracer provider: %w", err))
		}
		if err := mp.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown meter provider: %w", err))
		}
		if err := conn.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close otel connection: %w", err))
		}

		return errors.Join(errs...)
	}

	return shutdown, nil
}

func connectCollector(ctx context.Context, collectorAddr string) (*grpc.ClientConn, error) {
	conn, err := grpc.NewClient(
		collectorAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("create otel collector client: %w", err)
	}

	connectCtx, cancel := context.WithTimeout(ctx, collectorConnectTimeout)
	defer cancel()

	conn.Connect()

	for {
		state := conn.GetState()
		switch state {
		case connectivity.Ready:
			return conn, nil
		case connectivity.Shutdown:
			_ = conn.Close()
			return nil, errors.New("otel collector connection shut down before becoming ready")
		}

		if !conn.WaitForStateChange(connectCtx, state) {
			_ = conn.Close()
			return nil, fmt.Errorf(
				"connect to otel collector %q: %w",
				collectorAddr,
				connectCtx.Err(),
			)
		}
	}
}
