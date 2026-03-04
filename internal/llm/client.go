package llm

import "context"

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
