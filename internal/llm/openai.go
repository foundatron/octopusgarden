package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

// Compile-time check that OpenAIClient implements Client.
var _ Client = (*OpenAIClient)(nil)

var errNoChoices = errors.New("API returned no choices")

// OpenAIClient implements Client using the OpenAI-compatible API.
type OpenAIClient struct {
	client   *openai.Client
	zeroCost bool
	logger   *slog.Logger
}

// NewOpenAIClient creates a new OpenAI client. If baseURL is non-empty, it overrides the
// default OpenAI API endpoint (useful for Ollama or other compatible servers). When zeroCost
// is true, cost calculation is skipped (for local models with no billing).
func NewOpenAIClient(apiKey, baseURL string, zeroCost bool, logger *slog.Logger) *OpenAIClient {
	cfg := openai.DefaultConfig(apiKey)
	if baseURL != "" {
		cfg.BaseURL = baseURL
	}
	return &OpenAIClient{
		client:   openai.NewClientWithConfig(cfg),
		zeroCost: zeroCost,
		logger:   logger,
	}
}

// openaiMetrics holds extracted token counts and cost from an OpenAI API response.
type openaiMetrics struct {
	inputTokens  int
	outputTokens int
	cachedTokens int
	cost         float64
}

// logUsage extracts token counts, calculates cost, and logs structured metrics
// for an OpenAI API call. The prefix distinguishes call types (e.g. "openai generate",
// "openai judge").
func (c *OpenAIClient) logUsage(prefix, model string, usage openai.Usage) openaiMetrics {
	inputTokens := usage.PromptTokens
	outputTokens := usage.CompletionTokens

	var cachedTokens int
	if usage.PromptTokensDetails != nil {
		cachedTokens = usage.PromptTokensDetails.CachedTokens
	}

	var cost float64
	if !c.zeroCost {
		// OpenAI: regular input = total prompt minus cached, cache write = 0 (same price),
		// cache read = cached tokens.
		regularInput := inputTokens - cachedTokens
		var usingFallback bool
		cost, usingFallback = CalculateCost(model, regularInput, 0, cachedTokens, outputTokens)
		if usingFallback {
			c.logger.Warn("using fallback pricing for unknown model", "model", model)
		}
	}

	c.logger.Info(prefix,
		"model", model,
		"input_tokens", inputTokens,
		"cached_tokens", cachedTokens,
		"output_tokens", outputTokens,
		"cost_usd", cost,
		"cache_hit", cachedTokens > 0,
	)

	return openaiMetrics{
		inputTokens:  inputTokens,
		outputTokens: outputTokens,
		cachedTokens: cachedTokens,
		cost:         cost,
	}
}

// Generate calls the OpenAI Chat Completions API to generate code.
func (c *OpenAIClient) Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error) {
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 8192
	}

	messages := make([]openai.ChatCompletionMessage, 0, len(req.Messages)+1)

	if req.SystemPrompt != "" {
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: req.SystemPrompt,
		})
	}

	for _, msg := range req.Messages {
		role := openai.ChatMessageRoleUser
		if msg.Role == "assistant" {
			role = openai.ChatMessageRoleAssistant
		}
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    role,
			Content: msg.Content,
		})
	}

	resp, err := c.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:     req.Model,
		Messages:  messages,
		MaxTokens: maxTokens,
	})
	if err != nil {
		return GenerateResponse{}, fmt.Errorf("openai generate: %w", err)
	}

	if len(resp.Choices) == 0 {
		return GenerateResponse{}, fmt.Errorf("openai generate: %w", errNoChoices)
	}

	content := resp.Choices[0].Message.Content
	finishReason := string(resp.Choices[0].FinishReason)
	m := c.logUsage("openai generate", req.Model, resp.Usage)

	return GenerateResponse{
		Content:      content,
		InputTokens:  m.inputTokens,
		OutputTokens: m.outputTokens,
		CacheHit:     m.cachedTokens > 0,
		CostUSD:      m.cost,
		FinishReason: finishReason,
	}, nil
}

// Judge calls the OpenAI Chat Completions API to score satisfaction.
func (c *OpenAIClient) Judge(ctx context.Context, req JudgeRequest) (JudgeResponse, error) {
	messages := make([]openai.ChatCompletionMessage, 0, 2)

	if req.SystemPrompt != "" {
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: req.SystemPrompt,
		})
	}

	messages = append(messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: req.UserPrompt,
	})

	resp, err := c.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:     req.Model,
		Messages:  messages,
		MaxTokens: 4096,
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
	cleaned := extractJSON(content)
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
	resp, err := c.client.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("openai list models: %w", err)
	}

	models := make([]AvailableModel, 0, len(resp.Models))
	for _, m := range resp.Models {
		models = append(models, AvailableModel{
			ID:          m.ID,
			DisplayName: m.ID,
			CreatedAt:   time.Unix(m.CreatedAt, 0),
		})
	}
	return models, nil
}
