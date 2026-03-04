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
	// OpenAI models — cache writes same as input, cache reads 50% off.
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
	"o1": {
		InputPerMillion:      15.00,
		OutputPerMillion:     60.00,
		CacheWritePerMillion: 15.00,
		CacheReadPerMillion:  7.50,
	},
	"o3-mini": {
		InputPerMillion:      1.10,
		OutputPerMillion:     4.40,
		CacheWritePerMillion: 1.10,
		CacheReadPerMillion:  0.55,
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
