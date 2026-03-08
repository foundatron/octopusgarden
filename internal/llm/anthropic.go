package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Compile-time check that AnthropicClient implements Client.
var _ Client = (*AnthropicClient)(nil)

// maxRetries is set higher than the SDK default (2) to handle sustained
// bursts of 529 Overloaded responses during parallel judge calls.
const maxRetries = 5

// AnthropicClient implements Client using the Anthropic API with prompt caching.
type AnthropicClient struct {
	client anthropic.Client
	logger *slog.Logger
}

// NewAnthropicClient creates a new Anthropic client. The variadic opts enable
// test injection via option.WithBaseURL(server.URL).
func NewAnthropicClient(apiKey string, logger *slog.Logger, opts ...option.RequestOption) *AnthropicClient {
	allOpts := make([]option.RequestOption, 0, 2+len(opts))
	allOpts = append(allOpts, option.WithAPIKey(apiKey), option.WithMaxRetries(maxRetries))
	allOpts = append(allOpts, opts...)
	return &AnthropicClient{
		client: anthropic.NewClient(allOpts...),
		logger: logger,
	}
}

// logUsage extracts token counts, calculates cost, logs structured metrics,
// and returns a usageMetrics for the Anthropic API call.
// The prefix distinguishes call types (e.g. "anthropic generate", "anthropic judge").
func (c *AnthropicClient) logUsage(prefix, model string, usage anthropic.Usage) usageMetrics {
	inputTokens := int(usage.InputTokens)
	cacheCreationTokens := int(usage.CacheCreationInputTokens)
	cacheReadTokens := int(usage.CacheReadInputTokens)
	outputTokens := int(usage.OutputTokens)

	cost, usingFallback := CalculateCost(model, inputTokens, cacheCreationTokens, cacheReadTokens, outputTokens)
	if usingFallback {
		c.logger.Warn("using fallback pricing for unknown model", "model", model)
	}

	m := usageMetrics{
		model:               model,
		inputTokens:         inputTokens,
		cacheCreationTokens: cacheCreationTokens,
		cacheReadTokens:     cacheReadTokens,
		outputTokens:        outputTokens,
		cost:                cost,
	}
	m.log(c.logger, prefix)
	return m
}

// Generate calls the Anthropic Messages API to generate code.
func (c *AnthropicClient) Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error) {
	maxTokens := int64(req.MaxTokens)
	if maxTokens == 0 {
		maxTokens = defaultGenerateMaxTokens
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
	if req.Temperature != nil {
		params.Temperature = anthropic.Float(*req.Temperature)
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

	m := c.logUsage("anthropic generate", req.Model, resp.Usage)

	return GenerateResponse{
		Content:      content,
		InputTokens:  m.inputTokens + m.cacheCreationTokens + m.cacheReadTokens,
		OutputTokens: m.outputTokens,
		CacheHit:     m.cacheReadTokens > 0,
		CostUSD:      m.cost,
		FinishReason: string(resp.StopReason),
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
		MaxTokens: defaultJudgeMaxTokens,
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

	m := c.logUsage("anthropic judge", req.Model, resp.Usage)

	// Parse JSON response — strip markdown code fences if present.
	cleaned := ExtractJSON(content)
	var result judgeResult
	if err := json.Unmarshal([]byte(cleaned), &result); err != nil {
		// On malformed JSON: return Score=0 with raw text as reasoning, not an error.
		return JudgeResponse{
			Score:     0,
			Reasoning: content,
			CostUSD:   m.cost,
		}, nil
	}

	return JudgeResponse{
		Score:     result.Score,
		Reasoning: result.Reasoning,
		Failures:  result.Failures,
		CostUSD:   m.cost,
	}, nil
}
