package llm

import "log/slog"

// usageMetrics holds token counts and cost for a single LLM API call.
type usageMetrics struct {
	model               string
	inputTokens         int // Anthropic: regular input only; OpenAI: total prompt tokens
	cacheCreationTokens int
	cacheReadTokens     int
	outputTokens        int
	cost                float64
}

// log emits structured slog attributes for this metrics snapshot.
// prefix distinguishes call types (e.g. "anthropic generate", "openai judge").
func (m *usageMetrics) log(logger *slog.Logger, prefix string) {
	logger.Info(prefix,
		"model", m.model,
		"input_tokens", m.inputTokens,
		"cache_creation_tokens", m.cacheCreationTokens,
		"cache_read_tokens", m.cacheReadTokens,
		"output_tokens", m.outputTokens,
		"cost_usd", m.cost,
		"cache_hit", m.cacheReadTokens > 0,
	)
}
