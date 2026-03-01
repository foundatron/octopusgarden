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
	"claude-haiku-3-5-20241022": {
		InputPerMillion:      0.80,
		OutputPerMillion:     4.00,
		CacheWritePerMillion: 1.00,
		CacheReadPerMillion:  0.08,
	},
	"claude-opus-4-5": {
		InputPerMillion:      15.00,
		OutputPerMillion:     75.00,
		CacheWritePerMillion: 18.75,
		CacheReadPerMillion:  1.50,
	},
}

// fallbackPricing is used for unknown models (conservative/expensive).
var fallbackPricing = ModelPricing{
	InputPerMillion:      15.00,
	OutputPerMillion:     75.00,
	CacheWritePerMillion: 18.75,
	CacheReadPerMillion:  1.50,
}

// CalculateCost returns the estimated USD cost for a request given token counts.
func CalculateCost(model string, regularInputTokens, cacheCreationTokens, cacheReadTokens, outputTokens int) float64 {
	pricing, ok := pricingTable[model]
	if !ok {
		pricing = fallbackPricing
	}

	cost := float64(regularInputTokens) * pricing.InputPerMillion / 1_000_000
	cost += float64(cacheCreationTokens) * pricing.CacheWritePerMillion / 1_000_000
	cost += float64(cacheReadTokens) * pricing.CacheReadPerMillion / 1_000_000
	cost += float64(outputTokens) * pricing.OutputPerMillion / 1_000_000

	return cost
}
