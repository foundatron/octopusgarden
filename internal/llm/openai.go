package llm

import (
	"context"
	"fmt"
)

// Compile-time check that OpenAIClient implements Client.
var _ Client = (*OpenAIClient)(nil)

// OpenAIClient is a stub implementation of Client for OpenAI/Ollama backends.
type OpenAIClient struct{}

// Generate returns a "not yet implemented" error.
func (c *OpenAIClient) Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error) {
	return GenerateResponse{}, fmt.Errorf("openai client: not yet implemented")
}

// Judge returns a "not yet implemented" error.
func (c *OpenAIClient) Judge(ctx context.Context, req JudgeRequest) (JudgeResponse, error) {
	return JudgeResponse{}, fmt.Errorf("openai client: not yet implemented")
}
