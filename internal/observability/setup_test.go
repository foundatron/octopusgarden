package observability

import (
	"context"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestInitTracerEmptyEndpoint(t *testing.T) {
	tp, shutdown, err := InitTracer(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should return noop provider (not an SDK provider).
	if _, ok := tp.(*sdktrace.TracerProvider); ok {
		t.Errorf("expected noop TracerProvider, got %T", tp)
	}

	// Shutdown should be safe to call.
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown error: %v", err)
	}
}

func TestInitTracerNonEmptyEndpoint(t *testing.T) {
	// Use a non-routable address — we only test that the provider is created, not that it connects.
	tp, shutdown, err := InitTracer(context.Background(), "localhost:4318")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should be a real SDK TracerProvider, not noop.
	if _, ok := tp.(*sdktrace.TracerProvider); !ok {
		t.Errorf("expected *sdktrace.TracerProvider, got %T", tp)
	}

	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown error: %v", err)
	}
}
