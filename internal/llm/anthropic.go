package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Compile-time check that AnthropicClient implements Client.
var _ Client = (*AnthropicClient)(nil)

// AnthropicClient implements Client using the Anthropic API with prompt caching.
type AnthropicClient struct {
	client anthropic.Client
	logger *slog.Logger
}

// NewAnthropicClient creates a new Anthropic client. The variadic opts enable
// test injection via option.WithBaseURL(server.URL).
func NewAnthropicClient(apiKey string, logger *slog.Logger, opts ...option.RequestOption) *AnthropicClient {
	allOpts := make([]option.RequestOption, 0, 2+len(opts))
	allOpts = append(allOpts, option.WithAPIKey(apiKey), option.WithMaxRetries(5))
	allOpts = append(allOpts, opts...)
	return &AnthropicClient{
		client: anthropic.NewClient(allOpts...),
		logger: logger,
	}
}

// Generate calls the Anthropic Messages API to generate code.
func (c *AnthropicClient) Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error) {
	maxTokens := int64(req.MaxTokens)
	if maxTokens == 0 {
		maxTokens = 8192
	}

	// Build system prompt blocks.
	var system []anthropic.TextBlockParam
	if req.SystemPrompt != "" {
		block := anthropic.TextBlockParam{
			Text: req.SystemPrompt,
		}
		if req.CacheControl != nil {
			block.CacheControl = anthropic.CacheControlEphemeralParam{
				TTL: anthropic.CacheControlEphemeralTTLTTL5m,
			}
		}
		system = append(system, block)
	}

	// Build messages.
	messages := make([]anthropic.MessageParam, len(req.Messages))
	for i, msg := range req.Messages {
		role := anthropic.MessageParamRoleUser
		if msg.Role == "assistant" {
			role = anthropic.MessageParamRoleAssistant
		}
		messages[i] = anthropic.MessageParam{
			Role:    role,
			Content: []anthropic.ContentBlockParamUnion{anthropic.NewTextBlock(msg.Content)},
		}
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(req.Model),
		MaxTokens: maxTokens,
		Messages:  messages,
		System:    system,
	}

	resp, err := c.client.Messages.New(ctx, params)
	if err != nil {
		return GenerateResponse{}, fmt.Errorf("anthropic generate: %w", err)
	}

	// Extract text content from response.
	var content string
	if len(resp.Content) > 0 {
		text := resp.Content[0].AsText()
		content = text.Text
	}

	cacheCreation := int(resp.Usage.CacheCreationInputTokens)
	cacheRead := int(resp.Usage.CacheReadInputTokens)
	regularInput := int(resp.Usage.InputTokens)
	output := int(resp.Usage.OutputTokens)

	cost, usingFallback := CalculateCost(req.Model, regularInput, cacheCreation, cacheRead, output)
	if usingFallback {
		c.logger.Warn("using fallback pricing for unknown model", "model", req.Model)
	}

	c.logger.Info("anthropic generate",
		"model", req.Model,
		"input_tokens", regularInput,
		"cache_creation_tokens", cacheCreation,
		"cache_read_tokens", cacheRead,
		"output_tokens", output,
		"cost_usd", cost,
		"cache_hit", cacheRead > 0,
	)

	return GenerateResponse{
		Content:      content,
		InputTokens:  regularInput + cacheCreation + cacheRead,
		OutputTokens: output,
		CacheHit:     cacheRead > 0,
		CostUSD:      cost,
	}, nil
}

// AvailableModel holds metadata about an available model.
type AvailableModel struct {
	ID          string
	DisplayName string
	CreatedAt   time.Time
}

// ListModels queries the Anthropic API for available models.
func (c *AnthropicClient) ListModels(ctx context.Context) ([]AvailableModel, error) {
	iter := c.client.Models.ListAutoPaging(ctx, anthropic.ModelListParams{})
	var models []AvailableModel
	for iter.Next() {
		m := iter.Current()
		models = append(models, AvailableModel{
			ID:          m.ID,
			DisplayName: m.DisplayName,
			CreatedAt:   m.CreatedAt,
		})
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("anthropic list models: %w", err)
	}
	return models, nil
}

// judgeResult is the expected JSON structure from the judge LLM.
type judgeResult struct {
	Score     int      `json:"score"`
	Reasoning string   `json:"reasoning"`
	Failures  []string `json:"failures"`
}

// Judge calls the Anthropic Messages API to score satisfaction.
func (c *AnthropicClient) Judge(ctx context.Context, req JudgeRequest) (JudgeResponse, error) {
	var system []anthropic.TextBlockParam
	if req.SystemPrompt != "" {
		block := anthropic.TextBlockParam{
			Text: req.SystemPrompt,
		}
		if req.CacheControl != nil {
			block.CacheControl = anthropic.CacheControlEphemeralParam{
				TTL: anthropic.CacheControlEphemeralTTLTTL5m,
			}
		}
		system = append(system, block)
	}

	messages := []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock(req.UserPrompt)),
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(req.Model),
		MaxTokens: 4096,
		Messages:  messages,
		System:    system,
	}

	resp, err := c.client.Messages.New(ctx, params)
	if err != nil {
		return JudgeResponse{}, fmt.Errorf("anthropic judge: %w", err)
	}

	var content string
	if len(resp.Content) > 0 {
		text := resp.Content[0].AsText()
		content = text.Text
	}

	cacheCreation := int(resp.Usage.CacheCreationInputTokens)
	cacheRead := int(resp.Usage.CacheReadInputTokens)
	regularInput := int(resp.Usage.InputTokens)
	output := int(resp.Usage.OutputTokens)
	cost, usingFallback := CalculateCost(req.Model, regularInput, cacheCreation, cacheRead, output)
	if usingFallback {
		c.logger.Warn("using fallback pricing for unknown model", "model", req.Model)
	}

	c.logger.Info("anthropic judge",
		"model", req.Model,
		"input_tokens", regularInput,
		"cache_creation_tokens", cacheCreation,
		"cache_read_tokens", cacheRead,
		"output_tokens", output,
		"cost_usd", cost,
		"cache_hit", cacheRead > 0,
	)

	// Parse JSON response — strip markdown code fences if present.
	cleaned := extractJSON(content)
	var result judgeResult
	if err := json.Unmarshal([]byte(cleaned), &result); err != nil {
		// On malformed JSON: return Score=0 with raw text as reasoning, not an error.
		return JudgeResponse{
			Score:     0,
			Reasoning: content,
			CostUSD:   cost,
		}, nil
	}

	return JudgeResponse{
		Score:     result.Score,
		Reasoning: result.Reasoning,
		Failures:  result.Failures,
		CostUSD:   cost,
	}, nil
}

// extractJSON strips markdown code fences from LLM output to get raw JSON.
// Handles ```json\n...\n``` and ```\n...\n``` patterns.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		// Strip opening fence (with optional language tag).
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		// Strip closing fence.
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
		s = strings.TrimSpace(s)
	}
	return s
}
