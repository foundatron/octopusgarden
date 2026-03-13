package llm

import (
	"context"
	"encoding/json"
	"errors"
)

// Sentinel errors for agent loop operations.
var (
	ErrAgentLoopNotSupported = errors.New("agent loop not supported by this client")
	ErrMaxTurnsExceeded      = errors.New("agent loop exceeded max turns")
)

const (
	defaultGenerateMaxTokens = 8192
	defaultJudgeMaxTokens    = 4096
)

// Client is the model-agnostic LLM interface used by the attractor loop and judge.
type Client interface {
	Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error)
	Judge(ctx context.Context, req JudgeRequest) (JudgeResponse, error)
}

// GenerateRequest contains parameters for code generation.
type GenerateRequest struct {
	SystemPrompt string
	Messages     []Message
	MaxTokens    int
	Model        string
	CacheControl *CacheControl
	Temperature  *float64 // nil = provider default
}

// CacheControl configures prompt caching behavior.
type CacheControl struct {
	Type string // "ephemeral"
}

// GenerateResponse contains the result of a generation call.
type GenerateResponse struct {
	Content      string
	InputTokens  int
	OutputTokens int
	CacheHit     bool
	CostUSD      float64
	FinishReason string
}

// JudgeRequest contains parameters for satisfaction judging.
type JudgeRequest struct {
	SystemPrompt string
	UserPrompt   string
	Model        string
	CacheControl *CacheControl
}

// JudgeResponse contains the result of a judge call.
type JudgeResponse struct {
	Score     int
	Reasoning string
	Failures  []string
	CostUSD   float64
}

// Message represents a single message in a conversation.
type Message struct {
	Role    string // "user" or "assistant"
	Content string
}

// ToolDef defines a tool available to the agent.
type ToolDef struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// ToolCall represents a tool invocation by the model.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolHandler processes a tool call and returns the result.
type ToolHandler func(ctx context.Context, call ToolCall) (string, error)

// AgentRequest contains parameters for an agent loop call.
type AgentRequest struct {
	SystemPrompt string
	Messages     []Message
	Tools        []ToolDef
	Model        string
	MaxTokens    int
	MaxTurns     int
	CacheControl *CacheControl
}

// AgentResponse contains the result of an agent loop call.
type AgentResponse struct {
	Content      string
	Turns        int
	InputTokens  int
	OutputTokens int
	TotalCost    float64
}

// AgentClient extends Client with an agentic tool-use loop.
type AgentClient interface {
	AgentLoop(ctx context.Context, req AgentRequest, handler ToolHandler) (AgentResponse, error)
}
