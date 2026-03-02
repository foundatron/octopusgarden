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
	"claude-sonnet-4-6": {
		InputPerMillion:      3.00,
		OutputPerMillion:     15.00,
		CacheWritePerMillion: 3.75,
		CacheReadPerMillion:  0.30,
	},
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
	"claude-haiku-4-5-20251001": {
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
}

// fallbackPricing is used for unknown models (conservative/expensive).
var fallbackPricing = ModelPricing{
	InputPerMillion:      15.00,
	OutputPerMillion:     75.00,
	CacheWritePerMillion: 18.75,
	CacheReadPerMillion:  1.50,
}

// modelAliases maps short names to full model IDs.
var modelAliases = map[string]string{
	"sonnet":        "claude-sonnet-4-6",
	"claude-sonnet": "claude-sonnet-4-6",
	"haiku":         "claude-haiku-4-5-20251001",
	"claude-haiku":  "claude-haiku-4-5-20251001",
	"opus":          "claude-opus-4-5",
	"claude-opus":   "claude-opus-4-5",
}

// ResolveModel returns the full model ID for a given alias, or the input unchanged if not an alias.
func ResolveModel(model string) string {
	if full, ok := modelAliases[model]; ok {
		return full
	}
	return model
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
