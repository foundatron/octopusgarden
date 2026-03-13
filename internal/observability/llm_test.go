package observability

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/foundatron/octopusgarden/internal/llm"
)

type mockLLMClient struct {
	generateFn func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error)
	judgeFn    func(ctx context.Context, req llm.JudgeRequest) (llm.JudgeResponse, error)
}

func (m *mockLLMClient) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	return m.generateFn(ctx, req)
}

func (m *mockLLMClient) Judge(ctx context.Context, req llm.JudgeRequest) (llm.JudgeResponse, error) {
	return m.judgeFn(ctx, req)
}

func newTestTP() (*tracetest.InMemoryExporter, *sdktrace.TracerProvider) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	return exp, tp
}

var errMock = errors.New("mock error")

func TestTracingLLMClientGenerate(t *testing.T) {
	tests := []struct {
		name      string
		resp      llm.GenerateResponse
		err       error
		wantErr   bool
		wantAttrs []string
	}{
		{
			name: "success",
			resp: llm.GenerateResponse{
				Content:      "output",
				InputTokens:  100,
				OutputTokens: 50,
				CacheHit:     true,
				CostUSD:      0.01,
				FinishReason: "end_turn",
			},
			wantAttrs: []string{"llm.model", "llm.input_tokens", "llm.output_tokens", "llm.cache_hit", "llm.cost_usd", "llm.finish_reason"},
		},
		{
			name:    "error",
			err:     errMock,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exp, tp := newTestTP()
			defer func() { _ = tp.Shutdown(context.Background()) }()

			client := &mockLLMClient{
				generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
					return tt.resp, tt.err
				},
			}

			traced := NewTracingLLMClient(client, tp)
			_, err := traced.Generate(context.Background(), llm.GenerateRequest{Model: "test-model"})

			if (err != nil) != tt.wantErr {
				t.Fatalf("wantErr=%v, got %v", tt.wantErr, err)
			}

			// Force flush.
			_ = tp.ForceFlush(context.Background())
			spans := exp.GetSpans()
			if len(spans) != 1 {
				t.Fatalf("expected 1 span, got %d", len(spans))
			}
			if spans[0].Name != "llm.generate" {
				t.Errorf("expected span name llm.generate, got %q", spans[0].Name)
			}

			if tt.wantErr {
				if spans[0].Status.Code != codes.Error {
					t.Error("expected error status on span")
				}
			} else {
				for _, key := range tt.wantAttrs {
					assertHasAttr(t, spans[0].Attributes, key)
				}

				// Verify actual attribute values match mock response.
				assertAttrString(t, spans[0].Attributes, "llm.model", "test-model")
				assertAttrInt(t, spans[0].Attributes, "llm.input_tokens", tt.resp.InputTokens)
				assertAttrInt(t, spans[0].Attributes, "llm.output_tokens", tt.resp.OutputTokens)
				assertAttrBool(t, spans[0].Attributes, "llm.cache_hit", tt.resp.CacheHit)
				assertAttrFloat64(t, spans[0].Attributes, "llm.cost_usd", tt.resp.CostUSD)
				assertAttrString(t, spans[0].Attributes, "llm.finish_reason", tt.resp.FinishReason)
			}
		})
	}
}

func TestTracingLLMClientJudge(t *testing.T) {
	tests := []struct {
		name      string
		resp      llm.JudgeResponse
		err       error
		wantErr   bool
		wantAttrs []string
	}{
		{
			name:      "success",
			resp:      llm.JudgeResponse{Score: 85, CostUSD: 0.005, Failures: []string{"minor"}},
			wantAttrs: []string{"llm.model", "llm.score", "llm.cost_usd", "llm.failure_count"},
		},
		{
			name:    "error",
			err:     errMock,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exp, tp := newTestTP()
			defer func() { _ = tp.Shutdown(context.Background()) }()

			client := &mockLLMClient{
				judgeFn: func(_ context.Context, _ llm.JudgeRequest) (llm.JudgeResponse, error) {
					return tt.resp, tt.err
				},
			}

			traced := NewTracingLLMClient(client, tp)
			_, err := traced.Judge(context.Background(), llm.JudgeRequest{Model: "judge-model"})

			if (err != nil) != tt.wantErr {
				t.Fatalf("wantErr=%v, got %v", tt.wantErr, err)
			}

			_ = tp.ForceFlush(context.Background())
			spans := exp.GetSpans()
			if len(spans) != 1 {
				t.Fatalf("expected 1 span, got %d", len(spans))
			}
			if spans[0].Name != "llm.judge" {
				t.Errorf("expected span name llm.judge, got %q", spans[0].Name)
			}

			if tt.wantErr {
				if spans[0].Status.Code != codes.Error {
					t.Error("expected error status on span")
				}
			} else {
				for _, key := range tt.wantAttrs {
					assertHasAttr(t, spans[0].Attributes, key)
				}

				// Verify actual attribute values match mock response.
				assertAttrString(t, spans[0].Attributes, "llm.model", "judge-model")
				assertAttrInt(t, spans[0].Attributes, "llm.score", tt.resp.Score)
				assertAttrFloat64(t, spans[0].Attributes, "llm.cost_usd", tt.resp.CostUSD)
				assertAttrInt(t, spans[0].Attributes, "llm.failure_count", len(tt.resp.Failures))
			}
		})
	}
}

type mockAgentLLMClient struct {
	mockLLMClient
	agentLoopFn func(ctx context.Context, req llm.AgentRequest, handler llm.ToolHandler) (llm.AgentResponse, error)
}

func (m *mockAgentLLMClient) AgentLoop(ctx context.Context, req llm.AgentRequest, handler llm.ToolHandler) (llm.AgentResponse, error) {
	return m.agentLoopFn(ctx, req, handler)
}

func TestTracingLLMClient_AgentLoop_Delegates(t *testing.T) {
	exp, tp := newTestTP()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	want := llm.AgentResponse{Content: "ok", Turns: 1, InputTokens: 50, OutputTokens: 20, TotalCost: 0.01}
	client := &mockAgentLLMClient{
		agentLoopFn: func(_ context.Context, _ llm.AgentRequest, _ llm.ToolHandler) (llm.AgentResponse, error) {
			return want, nil
		},
	}

	traced := NewTracingLLMClient(client, tp)
	got, err := traced.AgentLoop(context.Background(), llm.AgentRequest{Model: "test-model"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("response = %+v, want %+v", got, want)
	}

	_ = tp.ForceFlush(context.Background())
	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if spans[0].Name != "llm.agent_loop" {
		t.Errorf("span name = %q, want %q", spans[0].Name, "llm.agent_loop")
	}
	assertAttrString(t, spans[0].Attributes, "llm.model", "test-model")
	assertAttrInt(t, spans[0].Attributes, "llm.turns", want.Turns)
	assertAttrFloat64(t, spans[0].Attributes, "llm.cost_usd", want.TotalCost)
}

func TestTracingLLMClient_AgentLoop_NotSupported(t *testing.T) {
	tp := noop.NewTracerProvider()
	client := &mockLLMClient{}

	traced := NewTracingLLMClient(client, tp)
	_, err := traced.AgentLoop(context.Background(), llm.AgentRequest{}, nil)
	if !errors.Is(err, llm.ErrAgentLoopNotSupported) {
		t.Fatalf("expected ErrAgentLoopNotSupported, got %v", err)
	}
}

func TestTracingLLMClientNoopCreatesNoSpans(t *testing.T) {
	tp := noop.NewTracerProvider()
	client := &mockLLMClient{
		generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{}, nil
		},
		judgeFn: func(_ context.Context, _ llm.JudgeRequest) (llm.JudgeResponse, error) {
			return llm.JudgeResponse{}, nil
		},
	}

	traced := NewTracingLLMClient(client, tp)
	if _, err := traced.Generate(context.Background(), llm.GenerateRequest{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := traced.Judge(context.Background(), llm.JudgeRequest{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No panic, no exported spans — noop works correctly.
}
