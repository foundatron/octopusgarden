package observability

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/codes"

	"github.com/foundatron/octopusgarden/internal/attractor"
)

func TestWrapValidateFnSuccess(t *testing.T) {
	exp, tp := newTestTP()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	inner := func(_ context.Context, _ string, _ attractor.RestartFunc) (float64, []string, float64, error) {
		return 85.0, []string{"minor issue"}, 0.005, nil
	}

	wrapped := WrapValidateFn(inner, tp)
	sat, failures, cost, err := wrapped(context.Background(), "http://localhost:8080", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sat != 85.0 {
		t.Errorf("expected satisfaction 85.0, got %.1f", sat)
	}
	if len(failures) != 1 {
		t.Errorf("expected 1 failure, got %d", len(failures))
	}
	if cost != 0.005 {
		t.Errorf("expected cost 0.005, got %f", cost)
	}

	_ = tp.ForceFlush(context.Background())
	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Name != "scenario.validate" {
		t.Errorf("expected span name scenario.validate, got %q", spans[0].Name)
	}

	assertHasAttr(t, spans[0].Attributes, "scenario.target_url")
	assertHasAttr(t, spans[0].Attributes, "scenario.satisfaction")
	assertHasAttr(t, spans[0].Attributes, "scenario.failure_count")
	assertHasAttr(t, spans[0].Attributes, "scenario.cost_usd")
}

func TestWrapValidateFnError(t *testing.T) {
	exp, tp := newTestTP()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	inner := func(_ context.Context, _ string, _ attractor.RestartFunc) (float64, []string, float64, error) {
		return 0, nil, 0, errMock
	}

	wrapped := WrapValidateFn(inner, tp)
	_, _, _, err := wrapped(context.Background(), "http://localhost:8080", nil)
	if err == nil {
		t.Fatal("expected error")
	}

	_ = tp.ForceFlush(context.Background())
	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Status.Code != codes.Error {
		t.Error("expected error status on span")
	}
}
