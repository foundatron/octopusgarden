package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"golang.org/x/sync/errgroup"
)

// Compile-time checks that AnthropicClient implements Client and AgentClient.
var (
	_ Client      = (*AnthropicClient)(nil)
	_ AgentClient = (*AnthropicClient)(nil)
)

// maxRetries is set higher than the SDK default (2) to handle sustained
// bursts of 529 Overloaded responses during parallel judge calls.
const maxRetries = 5

// defaultMaxTurns is the fallback when AgentRequest.MaxTurns is zero.
const defaultMaxTurns = 10

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

// AgentLoop runs an agentic tool-use loop until end_turn or max turns.
// It calls handler for each tool_use block returned by the model.
// On ErrMaxTurnsExceeded, a partial AgentResponse with accumulated stats is also returned.
func (c *AnthropicClient) AgentLoop(ctx context.Context, req AgentRequest, handler ToolHandler) (AgentResponse, error) {
	maxTurns := req.MaxTurns
	if maxTurns == 0 {
		maxTurns = defaultMaxTurns
	}
	maxTokens := int64(req.MaxTokens)
	if maxTokens == 0 {
		maxTokens = defaultGenerateMaxTokens
	}

	// Build system blocks using same pattern as Generate.
	var system []anthropic.TextBlockParam
	if req.SystemPrompt != "" {
		block := anthropic.TextBlockParam{Text: req.SystemPrompt}
		if req.CacheControl != nil {
			block.CacheControl = anthropic.CacheControlEphemeralParam{TTL: anthropic.CacheControlEphemeralTTLTTL5m}
		}
		system = append(system, block)
	}

	tools, err := buildAgentToolParams(req.Tools)
	if err != nil {
		return AgentResponse{}, err
	}
	messages := buildAgentMessages(req.Messages)

	var totalInput, totalOutput int
	var totalCost float64

	for turn := range maxTurns {
		params := anthropic.MessageNewParams{
			Model:     anthropic.Model(req.Model),
			MaxTokens: maxTokens,
			Messages:  messages,
			System:    system,
			Tools:     tools,
		}

		resp, err := c.client.Messages.New(ctx, params)
		if err != nil {
			return AgentResponse{Turns: turn, InputTokens: totalInput, OutputTokens: totalOutput, TotalCost: totalCost},
				fmt.Errorf("anthropic agent loop: %w", err)
		}

		m := c.logUsage("anthropic agent loop", req.Model, resp.Usage)
		totalInput += m.inputTokens + m.cacheCreationTokens + m.cacheReadTokens
		totalOutput += m.outputTokens
		totalCost += m.cost
		currentTurn := turn + 1

		switch resp.StopReason {
		case anthropic.StopReasonEndTurn:
			return AgentResponse{
				Content:      agentExtractText(resp.Content),
				Turns:        currentTurn,
				InputTokens:  totalInput,
				OutputTokens: totalOutput,
				TotalCost:    totalCost,
			}, nil

		case anthropic.StopReasonToolUse:
			echo, results, err := c.agentProcessToolUse(ctx, resp.Content, handler)
			if err != nil {
				return AgentResponse{Turns: currentTurn, InputTokens: totalInput, OutputTokens: totalOutput, TotalCost: totalCost}, err
			}
			messages = append(messages,
				anthropic.MessageParam{Role: anthropic.MessageParamRoleAssistant, Content: echo},
				anthropic.MessageParam{Role: anthropic.MessageParamRoleUser, Content: results},
			)

		default:
			return AgentResponse{
				Content:      agentExtractText(resp.Content),
				Turns:        currentTurn,
				InputTokens:  totalInput,
				OutputTokens: totalOutput,
				TotalCost:    totalCost,
			}, nil
		}
	}

	return AgentResponse{
		Turns:        maxTurns,
		InputTokens:  totalInput,
		OutputTokens: totalOutput,
		TotalCost:    totalCost,
	}, ErrMaxTurnsExceeded
}

// buildAgentToolParams converts a ToolDef slice into Anthropic SDK tool params.
func buildAgentToolParams(tools []ToolDef) ([]anthropic.ToolUnionParam, error) {
	params := make([]anthropic.ToolUnionParam, len(tools))
	for i, td := range tools {
		inputSchema, err := toolInputSchema(td)
		if err != nil {
			return nil, err
		}
		params[i] = anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        td.Name,
				Description: anthropic.String(td.Description),
				InputSchema: inputSchema,
			},
		}
	}
	return params, nil
}

// toolInputSchema converts a ToolDef's JSON schema into a ToolInputSchemaParam.
// All JSON schema fields are preserved: properties and required map to dedicated
// struct fields; remaining fields (e.g. additionalProperties, enum, oneOf) go
// into ExtraFields so they survive marshaling.
// "type" is omitted — ToolInputSchemaParam always marshals it as "object".
func toolInputSchema(td ToolDef) (anthropic.ToolInputSchemaParam, error) {
	var schemaMap map[string]any
	if err := json.Unmarshal(td.InputSchema, &schemaMap); err != nil {
		return anthropic.ToolInputSchemaParam{}, fmt.Errorf("anthropic agent loop: unmarshal tool schema %q: %w", td.Name, err)
	}
	inputSchema := anthropic.ToolInputSchemaParam{}
	if props, ok := schemaMap["properties"]; ok {
		inputSchema.Properties = props
	}
	if reqd, ok := schemaMap["required"]; ok {
		if reqSlice, ok := reqd.([]any); ok {
			required := make([]string, 0, len(reqSlice))
			for _, r := range reqSlice {
				if s, ok := r.(string); ok {
					required = append(required, s)
				}
			}
			inputSchema.Required = required
		}
	}
	for k, v := range schemaMap {
		switch k {
		case "type", "properties", "required":
			// handled above or fixed constant
		default:
			if inputSchema.ExtraFields == nil {
				inputSchema.ExtraFields = make(map[string]any)
			}
			inputSchema.ExtraFields[k] = v
		}
	}
	return inputSchema, nil
}

// buildAgentMessages converts a Message slice into Anthropic SDK message params.
func buildAgentMessages(msgs []Message) []anthropic.MessageParam {
	out := make([]anthropic.MessageParam, len(msgs))
	for i, msg := range msgs {
		role := anthropic.MessageParamRoleUser
		if msg.Role == "assistant" {
			role = anthropic.MessageParamRoleAssistant
		}
		out[i] = anthropic.MessageParam{
			Role:    role,
			Content: []anthropic.ContentBlockParamUnion{anthropic.NewTextBlock(msg.Content)},
		}
	}
	return out
}

// agentExtractText returns the text from the first text content block.
func agentExtractText(content []anthropic.ContentBlockUnion) string {
	for _, block := range content {
		if block.Type == "text" {
			return block.AsText().Text
		}
	}
	return ""
}

// agentProcessToolUse builds the assistant echo message and calls the handler for each
// tool_use block in parallel, returning the echo blocks and result blocks to append to messages.
// Result order matches the order of tool_use blocks in content.
func (c *AnthropicClient) agentProcessToolUse(
	ctx context.Context,
	content []anthropic.ContentBlockUnion,
	handler ToolHandler,
) ([]anthropic.ContentBlockParamUnion, []anthropic.ContentBlockParamUnion, error) {
	type toolWork struct {
		idx   int
		id    string
		name  string
		input json.RawMessage
	}

	echo := make([]anthropic.ContentBlockParamUnion, 0, len(content))
	var works []toolWork
	for _, block := range content {
		switch block.Type {
		case "text":
			echo = append(echo, anthropic.NewTextBlock(block.AsText().Text))
		case "tool_use":
			tu := block.AsToolUse()
			echo = append(echo, anthropic.NewToolUseBlock(tu.ID, tu.Input, tu.Name))
			works = append(works, toolWork{idx: len(works), id: tu.ID, name: tu.Name, input: tu.Input})
		}
	}

	if len(works) == 0 {
		return echo, nil, nil
	}

	results := make([]anthropic.ContentBlockParamUnion, len(works))
	g, gCtx := errgroup.WithContext(ctx)
	for _, w := range works {
		g.Go(func() error {
			result, err := handler(gCtx, ToolCall{ID: w.id, Name: w.name, Input: w.input})
			if err != nil {
				return fmt.Errorf("anthropic agent loop: tool %q: %w", w.name, err)
			}
			results[w.idx] = anthropic.NewToolResultBlock(w.id, result, false)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, nil, err
	}
	return echo, results, nil
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
