package llm

// ModelPricing holds per-million-token rates for a model.
type ModelPricing struct {
	InputPerMillion      float64
	OutputPerMillion     float64
	CacheWritePerMillion float64
	CacheReadPerMillion  float64
}

// pricingTable maps model IDs to their pricing.
var pricingTable = map[string]ModelPricing{
	"claude-sonnet-4-20250514": {
		InputPerMillion:      3.00,
		OutputPerMillion:     15.00,
		CacheWritePerMillion: 3.75,
		CacheReadPerMillion:  0.30,
	},
	"claude-sonnet-4-6": {
		InputPerMillion:      3.00,
		OutputPerMillion:     15.00,
		CacheWritePerMillion: 3.75,
		CacheReadPerMillion:  0.30,
	},
	"claude-haiku-3-5-20241022": {
		InputPerMillion:      0.80,
		OutputPerMillion:     4.00,
		CacheWritePerMillion: 1.00,
		CacheReadPerMillion:  0.08,
	},
	"claude-haiku-4-5-20251001": {
		InputPerMillion:      1.00,
		OutputPerMillion:     5.00,
		CacheWritePerMillion: 1.25,
		CacheReadPerMillion:  0.10,
	},
	"claude-haiku-4-5": {
		InputPerMillion:      1.00,
		OutputPerMillion:     5.00,
		CacheWritePerMillion: 1.25,
		CacheReadPerMillion:  0.10,
	},
	"claude-opus-4-5": {
		InputPerMillion:      5.00,
		OutputPerMillion:     25.00,
		CacheWritePerMillion: 6.25,
		CacheReadPerMillion:  0.50,
	},
	// OpenAI models — cache writes same as input price.
	"gpt-4o": {
		InputPerMillion:      2.50,
		OutputPerMillion:     10.00,
		CacheWritePerMillion: 2.50,
		CacheReadPerMillion:  1.25,
	},
	"gpt-4o-mini": {
		InputPerMillion:      0.15,
		OutputPerMillion:     0.60,
		CacheWritePerMillion: 0.15,
		CacheReadPerMillion:  0.075,
	},
	"gpt-4.1": {
		InputPerMillion:      2.00,
		OutputPerMillion:     8.00,
		CacheWritePerMillion: 2.00,
		CacheReadPerMillion:  0.50,
	},
	"gpt-4.1-mini": {
		InputPerMillion:      0.40,
		OutputPerMillion:     1.60,
		CacheWritePerMillion: 0.40,
		CacheReadPerMillion:  0.10,
	},
	"gpt-4.1-nano": {
		InputPerMillion:      0.10,
		OutputPerMillion:     0.40,
		CacheWritePerMillion: 0.10,
		CacheReadPerMillion:  0.025,
	},
	"gpt-5": {
		InputPerMillion:      1.25,
		OutputPerMillion:     10.00,
		CacheWritePerMillion: 1.25,
		CacheReadPerMillion:  0.125,
	},
	"gpt-5-mini": {
		InputPerMillion:      0.25,
		OutputPerMillion:     2.00,
		CacheWritePerMillion: 0.25,
		CacheReadPerMillion:  0.025,
	},
	"gpt-5-nano": {
		InputPerMillion:      0.05,
		OutputPerMillion:     0.40,
		CacheWritePerMillion: 0.05,
		CacheReadPerMillion:  0.005,
	},
	"gpt-5.1": {
		InputPerMillion:      1.25,
		OutputPerMillion:     10.00,
		CacheWritePerMillion: 1.25,
		CacheReadPerMillion:  0.125,
	},
	"gpt-5.2": {
		InputPerMillion:      1.75,
		OutputPerMillion:     14.00,
		CacheWritePerMillion: 1.75,
		CacheReadPerMillion:  0.175,
	},
	"o1": {
		InputPerMillion:      15.00,
		OutputPerMillion:     60.00,
		CacheWritePerMillion: 15.00,
		CacheReadPerMillion:  7.50,
	},
	"o3": {
		InputPerMillion:      2.00,
		OutputPerMillion:     8.00,
		CacheWritePerMillion: 2.00,
		CacheReadPerMillion:  0.50,
	},
	"o3-mini": {
		InputPerMillion:      1.10,
		OutputPerMillion:     4.40,
		CacheWritePerMillion: 1.10,
		CacheReadPerMillion:  0.55,
	},
	"o4-mini": {
		InputPerMillion:      1.10,
		OutputPerMillion:     4.40,
		CacheWritePerMillion: 1.10,
		CacheReadPerMillion:  0.275,
	},
}

// fallbackPricing is used for unknown models. Intentionally set to the most
// expensive known model tier to avoid under-reporting costs.
var fallbackPricing = ModelPricing{
	InputPerMillion:      15.00,
	OutputPerMillion:     75.00,
	CacheWritePerMillion: 18.75,
	CacheReadPerMillion:  1.50,
}

// modelMaxOutputTokens maps model IDs to their maximum output token limits.
// Used by MaxOutputTokens to auto-scale max_tokens per model.
var modelMaxOutputTokens = map[string]int{
	// Anthropic models
	"claude-haiku-3-5-20241022": 8192,
	"claude-haiku-4-5-20251001": 8192,
	"claude-haiku-4-5":          8192,
	"claude-sonnet-4-20250514":  16384, // up to 64K with output-128k beta
	"claude-sonnet-4-6":         16384, // up to 64K with output-128k beta
	"claude-opus-4-5":           32768, // up to 128K with output-128k beta
	// OpenAI GPT models
	"gpt-4o":       16384,
	"gpt-4o-mini":  16384,
	"gpt-4.1":      32768,
	"gpt-4.1-mini": 32768,
	"gpt-4.1-nano": 32768,
	"gpt-5":        32768,
	"gpt-5-mini":   32768,
	"gpt-5-nano":   32768,
	"gpt-5.1":      32768,
	"gpt-5.2":      32768,
	// OpenAI reasoning models
	"o1":      65536,
	"o3":      65536,
	"o3-mini": 65536,
	"o4-mini": 65536,
}

// MaxOutputTokens returns the maximum output token limit for the given model.
// Unknown models return defaultMaxOutputTokens (8192).
func MaxOutputTokens(model string) int {
	if v, ok := modelMaxOutputTokens[model]; ok {
		return v
	}
	return defaultMaxOutputTokens
}

// CalculateCost returns the estimated USD cost for a request given token counts.
// The bool return indicates whether fallback pricing was used (true = unknown model).
func CalculateCost(model string, regularInputTokens, cacheCreationTokens, cacheReadTokens, outputTokens int) (float64, bool) {
	pricing, ok := pricingTable[model]
	if !ok {
		pricing = fallbackPricing
	}

	cost := float64(regularInputTokens) * pricing.InputPerMillion / 1_000_000
	cost += float64(cacheCreationTokens) * pricing.CacheWritePerMillion / 1_000_000
	cost += float64(cacheReadTokens) * pricing.CacheReadPerMillion / 1_000_000
	cost += float64(outputTokens) * pricing.OutputPerMillion / 1_000_000

	return cost, !ok
}

// HasModelPricing reports whether the given model has an entry in the pricing table.
func HasModelPricing(model string) bool {
	_, ok := pricingTable[model]
	return ok
}
