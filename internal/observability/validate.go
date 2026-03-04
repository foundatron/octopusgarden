package observability

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/foundatron/octopusgarden/internal/attractor"
)

// WrapValidateFn wraps a ValidateFn with a scenario.validate span.
func WrapValidateFn(fn attractor.ValidateFn, tp trace.TracerProvider) attractor.ValidateFn {
	tracer := tp.Tracer("octog/scenario")
	return func(ctx context.Context, url string) (float64, []string, float64, error) {
		ctx, span := tracer.Start(ctx, "scenario.validate", trace.WithAttributes(
			attribute.String("scenario.target_url", url),
		))
		defer span.End()

		satisfaction, failures, cost, err := fn(ctx, url)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return satisfaction, failures, cost, err
		}

		span.SetAttributes(
			attribute.Float64("scenario.satisfaction", satisfaction),
			attribute.Int("scenario.failure_count", len(failures)),
			attribute.Float64("scenario.cost_usd", cost),
		)
		return satisfaction, failures, cost, nil
	}
}
