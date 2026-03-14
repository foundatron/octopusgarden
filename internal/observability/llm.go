package observability

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/foundatron/octopusgarden/internal/llm"
)

// Compile-time interface satisfaction checks.
// TracingLLMClient always satisfies AgentClient at the type level; callers doing
// client.(AgentClient) will always get ok=true. AgentLoop returns
// ErrAgentLoopNotSupported at runtime when the inner client does not implement AgentClient.
var (
	_ llm.Client      = (*TracingLLMClient)(nil)
	_ llm.AgentClient = (*TracingLLMClient)(nil)
)

// TracingLLMClient wraps an llm.Client with OpenTelemetry spans.
type TracingLLMClient struct {
	inner  llm.Client
	tracer trace.Tracer
}

// NewTracingLLMClient creates a TracingLLMClient using the given TracerProvider.
func NewTracingLLMClient(inner llm.Client, tp trace.TracerProvider) *TracingLLMClient {
	return &TracingLLMClient{
		inner:  inner,
		tracer: tp.Tracer("octog/llm"),
	}
}

// Generate delegates to the inner client and records an llm.generate span.
func (t *TracingLLMClient) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	ctx, span := t.tracer.Start(ctx, "llm.generate", trace.WithAttributes(
		attribute.String("llm.model", req.Model),
	))
	defer span.End()

	resp, err := t.inner.Generate(ctx, req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return resp, err
	}

	span.SetAttributes(
		attribute.Int("llm.input_tokens", resp.InputTokens),
		attribute.Int("llm.output_tokens", resp.OutputTokens),
		attribute.Bool("llm.cache_hit", resp.CacheHit),
		attribute.Float64("llm.cost_usd", resp.CostUSD),
		attribute.String("llm.finish_reason", resp.FinishReason),
	)
	return resp, nil
}

// AgentLoop delegates to the inner client if it implements AgentClient, recording an llm.agent_loop span.
// Returns ErrAgentLoopNotSupported if the inner client does not implement AgentClient.
func (t *TracingLLMClient) AgentLoop(ctx context.Context, req llm.AgentRequest, handler llm.ToolHandler) (llm.AgentResponse, error) {
	ac, ok := t.inner.(llm.AgentClient)
	if !ok {
		return llm.AgentResponse{}, llm.ErrAgentLoopNotSupported
	}

	ctx, span := t.tracer.Start(ctx, "llm.agent_loop", trace.WithAttributes(
		attribute.String("llm.model", req.Model),
	))
	defer span.End()

	resp, err := ac.AgentLoop(ctx, req, handler)
	if err != nil {
		if errors.Is(err, llm.ErrMaxTurnsExceeded) {
			// Max turns is an expected control-flow outcome, not an API failure.
			span.SetAttributes(attribute.Bool("llm.max_turns_exceeded", true))
		} else {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		return resp, err
	}

	span.SetAttributes(
		attribute.Int("llm.turns", resp.Turns),
		attribute.Int("llm.input_tokens", resp.InputTokens),
		attribute.Int("llm.output_tokens", resp.OutputTokens),
		attribute.Float64("llm.cost_usd", resp.TotalCost),
	)
	return resp, nil
}

// Judge delegates to the inner client and records an llm.judge span.
func (t *TracingLLMClient) Judge(ctx context.Context, req llm.JudgeRequest) (llm.JudgeResponse, error) {
	ctx, span := t.tracer.Start(ctx, "llm.judge", trace.WithAttributes(
		attribute.String("llm.model", req.Model),
	))
	defer span.End()

	resp, err := t.inner.Judge(ctx, req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return resp, err
	}

	span.SetAttributes(
		attribute.Int("llm.score", resp.Score),
		attribute.Float64("llm.cost_usd", resp.CostUSD),
		attribute.Int("llm.failure_count", len(resp.Failures)),
	)
	return resp, nil
}
