package llm

import "testing"

func TestMaxOutputTokens(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		model string
		want  int
	}{
		{name: "claude haiku 3.5", model: "claude-haiku-3-5-20241022", want: 8192},
		{name: "claude haiku 4.5 dated", model: "claude-haiku-4-5-20251001", want: 8192},
		{name: "claude haiku 4.5", model: "claude-haiku-4-5", want: 8192},
		{name: "claude sonnet 4 dated", model: "claude-sonnet-4-20250514", want: 16384},
		{name: "claude sonnet 4.6", model: "claude-sonnet-4-6", want: 16384},
		{name: "claude opus 4.5", model: "claude-opus-4-5", want: 32768},
		{name: "gpt-4o", model: "gpt-4o", want: 16384},
		{name: "gpt-4o-mini", model: "gpt-4o-mini", want: 16384},
		{name: "gpt-4.1", model: "gpt-4.1", want: 32768},
		{name: "gpt-4.1-mini", model: "gpt-4.1-mini", want: 32768},
		{name: "gpt-4.1-nano", model: "gpt-4.1-nano", want: 32768},
		{name: "gpt-5", model: "gpt-5", want: 32768},
		{name: "gpt-5-mini", model: "gpt-5-mini", want: 32768},
		{name: "gpt-5-nano", model: "gpt-5-nano", want: 32768},
		{name: "gpt-5.1", model: "gpt-5.1", want: 32768},
		{name: "gpt-5.2", model: "gpt-5.2", want: 32768},
		{name: "o1", model: "o1", want: 65536},
		{name: "o3", model: "o3", want: 65536},
		{name: "o3-mini", model: "o3-mini", want: 65536},
		{name: "o4-mini", model: "o4-mini", want: 65536},
		{name: "unknown model returns default", model: "llama-42b", want: defaultMaxOutputTokens},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := MaxOutputTokens(tt.model)
			if got != tt.want {
				t.Errorf("MaxOutputTokens(%q) = %d, want %d", tt.model, got, tt.want)
			}
		})
	}
}

func TestPricingTableCoverage(t *testing.T) {
	t.Parallel()
	// Every model in pricingTable must have a corresponding entry in modelMaxOutputTokens.
	for model := range pricingTable {
		t.Run(model, func(t *testing.T) {
			t.Parallel()
			if _, ok := modelMaxOutputTokens[model]; !ok {
				t.Errorf("model %q is in pricingTable but missing from modelMaxOutputTokens", model)
			}
		})
	}
	// Every model in modelMaxOutputTokens must have a corresponding entry in pricingTable.
	for model := range modelMaxOutputTokens {
		t.Run(model, func(t *testing.T) {
			t.Parallel()
			if _, ok := pricingTable[model]; !ok {
				t.Errorf("model %q is in modelMaxOutputTokens but missing from pricingTable", model)
			}
		})
	}
}
