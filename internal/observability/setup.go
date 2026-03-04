package observability

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// InitTracer creates a TracerProvider based on the endpoint.
// Empty endpoint returns a noop provider with a no-op shutdown function.
// Non-empty endpoint creates an OTLP/HTTP exporter with a batch span processor.
func InitTracer(ctx context.Context, endpoint string) (trace.TracerProvider, func(context.Context) error, error) {
	if endpoint == "" {
		return noop.NewTracerProvider(), func(context.Context) error { return nil }, nil
	}

	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpoint(endpoint), otlptracehttp.WithInsecure())
	if err != nil {
		return nil, nil, fmt.Errorf("init tracer: create exporter: %w", err)
	}

	res, err := resource.New(ctx, resource.WithAttributes(semconv.ServiceNameKey.String("octog")))
	if err != nil {
		return nil, nil, fmt.Errorf("init tracer: create resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)

	return tp, func(ctx context.Context) error { return tp.Shutdown(ctx) }, nil
}
