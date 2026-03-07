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

// Compile-time interface satisfaction check.
var _ attractor.ContainerManager = (*TracingContainerManager)(nil)

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
func (t *TracingContainerManager) Run(ctx context.Context, tag string) (container.RunResult, container.StopFunc, error) {
	ctx, span := t.tracer.Start(ctx, "container.run", trace.WithAttributes(
		attribute.String("container.tag", tag),
	))

	result, stop, err := t.inner.Run(ctx, tag)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		span.SetAttributes(attribute.Bool("container.success", false))
		span.End()
		return result, stop, err
	}

	span.SetAttributes(
		attribute.Bool("container.success", true),
		attribute.String("container.url", result.URL),
	)
	wrappedStop := func() {
		stop()
		span.End()
	}
	return result, wrappedStop, nil
}

// RunTest delegates to the inner manager and records a container.run_test span.
func (t *TracingContainerManager) RunTest(ctx context.Context, containerID, command string) (container.ExecResult, error) {
	ctx, span := t.tracer.Start(ctx, "container.run_test", trace.WithAttributes(
		attribute.String("container.id", containerID),
	))
	defer span.End()

	result, err := t.inner.RunTest(ctx, containerID, command)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return result, err
	}

	span.SetAttributes(attribute.Int("container.exit_code", result.ExitCode))
	return result, nil
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

// RunMultiPort delegates to the inner manager and records a container.run_multi_port span.
func (t *TracingContainerManager) RunMultiPort(ctx context.Context, tag string, extraPorts []string) (container.RunResult, container.StopFunc, error) {
	ctx, span := t.tracer.Start(ctx, "container.run_multi_port", trace.WithAttributes(
		attribute.String("container.tag", tag),
		attribute.Int("container.extra_ports", len(extraPorts)),
	))

	result, stop, err := t.inner.RunMultiPort(ctx, tag, extraPorts)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		span.SetAttributes(attribute.Bool("container.success", false))
		span.End()
		return result, stop, err
	}

	span.SetAttributes(
		attribute.Bool("container.success", true),
		attribute.String("container.url", result.URL),
	)
	wrappedStop := func() {
		stop()
		span.End()
	}
	return result, wrappedStop, nil
}

// WaitPort delegates to the inner manager and records a container.wait_port span.
func (t *TracingContainerManager) WaitPort(ctx context.Context, addr string, timeout time.Duration) error {
	ctx, span := t.tracer.Start(ctx, "container.wait_port", trace.WithAttributes(
		attribute.String("container.addr", addr),
		attribute.String("container.port_timeout", timeout.String()),
	))
	defer span.End()

	err := t.inner.WaitPort(ctx, addr, timeout)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		span.SetAttributes(attribute.Bool("container.port_ready", false))
		return err
	}

	span.SetAttributes(attribute.Bool("container.port_ready", true))
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
