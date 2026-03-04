package observability

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/foundatron/octopusgarden/internal/attractor"
	"github.com/foundatron/octopusgarden/internal/container"
)

// TracingContainerManager wraps an attractor.ContainerManager with OpenTelemetry spans.
type TracingContainerManager struct {
	inner  attractor.ContainerManager
	tracer trace.Tracer
}

// NewTracingContainerManager creates a TracingContainerManager using the given TracerProvider.
func NewTracingContainerManager(inner attractor.ContainerManager, tp trace.TracerProvider) *TracingContainerManager {
	return &TracingContainerManager{
		inner:  inner,
		tracer: tp.Tracer("octog/container"),
	}
}

// Build delegates to the inner manager and records a container.build span.
func (t *TracingContainerManager) Build(ctx context.Context, dir, tag string) error {
	ctx, span := t.tracer.Start(ctx, "container.build", trace.WithAttributes(
		attribute.String("container.tag", tag),
	))
	defer span.End()

	err := t.inner.Build(ctx, dir, tag)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		span.SetAttributes(attribute.Bool("container.success", false))
		return err
	}

	span.SetAttributes(attribute.Bool("container.success", true))
	return nil
}

// Run delegates to the inner manager and records a container.run span.
// The span covers the full container lifetime — it ends when the returned StopFunc is called.
func (t *TracingContainerManager) Run(ctx context.Context, tag string) (string, container.StopFunc, error) {
	ctx, span := t.tracer.Start(ctx, "container.run", trace.WithAttributes(
		attribute.String("container.tag", tag),
	))

	url, stop, err := t.inner.Run(ctx, tag)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		span.SetAttributes(attribute.Bool("container.success", false))
		span.End()
		return url, stop, err
	}

	span.SetAttributes(
		attribute.Bool("container.success", true),
		attribute.String("container.url", url),
	)
	wrappedStop := func() {
		stop()
		span.End()
	}
	return url, wrappedStop, nil
}

// WaitHealthy delegates to the inner manager and records a container.health span.
func (t *TracingContainerManager) WaitHealthy(ctx context.Context, url string, timeout time.Duration) error {
	ctx, span := t.tracer.Start(ctx, "container.health", trace.WithAttributes(
		attribute.String("container.url", url),
		attribute.String("container.health_timeout", timeout.String()),
	))
	defer span.End()

	err := t.inner.WaitHealthy(ctx, url, timeout)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		span.SetAttributes(attribute.Bool("container.healthy", false))
		return err
	}

	span.SetAttributes(attribute.Bool("container.healthy", true))
	return nil
}

// StartSession delegates to the inner manager and records a container.session span.
// The span covers the full session lifetime — it ends when the returned StopFunc is called.
func (t *TracingContainerManager) StartSession(ctx context.Context, tag string) (*container.Session, container.StopFunc, error) {
	ctx, span := t.tracer.Start(ctx, "container.session", trace.WithAttributes(
		attribute.String("container.tag", tag),
	))

	session, stop, err := t.inner.StartSession(ctx, tag)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		span.SetAttributes(attribute.Bool("container.success", false))
		span.End()
		return session, stop, err
	}

	span.SetAttributes(attribute.Bool("container.success", true))
	wrappedStop := func() {
		stop()
		span.End()
	}
	return session, wrappedStop, nil
}
