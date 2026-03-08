package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// Compile-time check that OpenAIClient implements Client.
var _ Client = (*OpenAIClient)(nil)

var errNoChoices = errors.New("API returned no choices")

// OpenAIClient implements Client using the OpenAI-compatible API.
type OpenAIClient struct {
	client   openai.Client
	zeroCost bool
	logger   *slog.Logger
}

// NewOpenAIClient creates a new OpenAI client. If baseURL is non-empty, it overrides the
// default OpenAI API endpoint (useful for Ollama or other compatible servers). When zeroCost
// is true, cost calculation is skipped (for local models with no billing).
func NewOpenAIClient(apiKey, baseURL string, zeroCost bool, logger *slog.Logger) *OpenAIClient {
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &OpenAIClient{
		client:   openai.NewClient(opts...),
		zeroCost: zeroCost,
		logger:   logger,
	}
}

// logUsage extracts token counts, calculates cost, logs structured metrics,
// and returns a usageMetrics for the OpenAI API call.
// The prefix distinguishes call types (e.g. "openai generate", "openai judge").
func (c *OpenAIClient) logUsage(prefix, model string, usage openai.CompletionUsage) usageMetrics {
	inputTokens := int(usage.PromptTokens)
	outputTokens := int(usage.CompletionTokens)
	cacheReadTokens := int(usage.PromptTokensDetails.CachedTokens)

	var cost float64
	if !c.zeroCost {
		// OpenAI: regular input = total prompt minus cached, cache write = 0 (same price),
		// cache read = cached tokens.
		regularInput := inputTokens - cacheReadTokens
		var usingFallback bool
		cost, usingFallback = CalculateCost(model, regularInput, 0, cacheReadTokens, outputTokens)
		if usingFallback {
			c.logger.Warn("using fallback pricing for unknown model", "model", model)
		}
	}

	m := usageMetrics{
		model:           model,
		inputTokens:     inputTokens,
		cacheReadTokens: cacheReadTokens,
		outputTokens:    outputTokens,
		cost:            cost,
	}
	m.log(c.logger, prefix)
	return m
}

// Generate calls the OpenAI Chat Completions API to generate code.
func (c *OpenAIClient) Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error) {
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = defaultGenerateMaxTokens
	}

	messages := make([]openai.ChatCompletionMessageParamUnion, 0, len(req.Messages)+1)

	if req.SystemPrompt != "" {
		messages = append(messages, openai.DeveloperMessage(req.SystemPrompt))
	}

	for _, msg := range req.Messages {
		if msg.Role == "assistant" {
			messages = append(messages, openai.AssistantMessage(msg.Content))
		} else {
			messages = append(messages, openai.UserMessage(msg.Content))
		}
	}

	chatParams := openai.ChatCompletionNewParams{
		Model:               req.Model,
		Messages:            messages,
		MaxCompletionTokens: openai.Int(int64(maxTokens)),
	}
	if req.Temperature != nil {
		chatParams.Temperature = openai.Float(*req.Temperature)
	}

	resp, err := c.client.Chat.Completions.New(ctx, chatParams)
	if err != nil {
		return GenerateResponse{}, fmt.Errorf("openai generate: %w", err)
	}

	if len(resp.Choices) == 0 {
		return GenerateResponse{}, fmt.Errorf("openai generate: %w", errNoChoices)
	}

	content := resp.Choices[0].Message.Content
	finishReason := resp.Choices[0].FinishReason
	m := c.logUsage("openai generate", req.Model, resp.Usage)

	return GenerateResponse{
		Content:      content,
		InputTokens:  m.inputTokens,
		OutputTokens: m.outputTokens,
		CacheHit:     m.cacheReadTokens > 0,
		CostUSD:      m.cost,
		FinishReason: finishReason,
	}, nil
}

// Judge calls the OpenAI Chat Completions API to score satisfaction.
func (c *OpenAIClient) Judge(ctx context.Context, req JudgeRequest) (JudgeResponse, error) {
	messages := make([]openai.ChatCompletionMessageParamUnion, 0, 2)

	if req.SystemPrompt != "" {
		messages = append(messages, openai.DeveloperMessage(req.SystemPrompt))
	}

	messages = append(messages, openai.UserMessage(req.UserPrompt))

	resp, err := c.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:               req.Model,
		Messages:            messages,
		MaxCompletionTokens: openai.Int(defaultJudgeMaxTokens),
	})
	if err != nil {
		return JudgeResponse{}, fmt.Errorf("openai judge: %w", err)
	}

	if len(resp.Choices) == 0 {
		return JudgeResponse{}, fmt.Errorf("openai judge: %w", errNoChoices)
	}

	content := resp.Choices[0].Message.Content
	m := c.logUsage("openai judge", req.Model, resp.Usage)

	// Parse JSON response — strip markdown code fences if present.
	cleaned := ExtractJSON(content)
	var result judgeResult
	if err := json.Unmarshal([]byte(cleaned), &result); err != nil {
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

// ListModels queries the OpenAI API for available models.
func (c *OpenAIClient) ListModels(ctx context.Context) ([]AvailableModel, error) {
	iter := c.client.Models.ListAutoPaging(ctx)
	var models []AvailableModel
	for iter.Next() {
		m := iter.Current()
		models = append(models, AvailableModel{
			ID:          m.ID,
			DisplayName: m.ID,
			CreatedAt:   time.Unix(m.Created, 0),
		})
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("openai list models: %w", err)
	}
	return models, nil
}
