package llm

import (
	"context"
	"log/slog"
	"testing"
)

// recordHandler captures slog records for inspection in tests.
type recordHandler struct {
	records []slog.Record
}

func (h *recordHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *recordHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r)
	return nil
}

func (h *recordHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *recordHandler) WithGroup(name string) slog.Handler       { return h }

func TestUsageMetricsLog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		m      usageMetrics
		prefix string
		want   map[string]any
	}{
		{
			name:   "all fields populated",
			prefix: "openai generate",
			m: usageMetrics{
				model:               "gpt-4o",
				inputTokens:         100,
				cacheCreationTokens: 10,
				cacheReadTokens:     50,
				outputTokens:        200,
				cost:                0.005,
			},
			want: map[string]any{
				"model":                 "gpt-4o",
				"input_tokens":          int64(100),
				"cache_creation_tokens": int64(10),
				"cache_read_tokens":     int64(50),
				"output_tokens":         int64(200),
				"cost_usd":              0.005,
				"cache_hit":             true,
			},
		},
		{
			name:   "no cache hit",
			prefix: "anthropic judge",
			m: usageMetrics{
				model:        "claude-3-5-haiku-latest",
				inputTokens:  80,
				outputTokens: 120,
			},
			want: map[string]any{
				"model":                 "claude-3-5-haiku-latest",
				"input_tokens":          int64(80),
				"cache_creation_tokens": int64(0),
				"cache_read_tokens":     int64(0),
				"output_tokens":         int64(120),
				"cost_usd":              0.0,
				"cache_hit":             false,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := &recordHandler{}
			logger := slog.New(h)

			tc.m.log(logger, tc.prefix)

			if len(h.records) != 1 {
				t.Fatalf("expected 1 log record, got %d", len(h.records))
			}
			r := h.records[0]
			if r.Message != tc.prefix {
				t.Errorf("message = %q, want %q", r.Message, tc.prefix)
			}

			got := make(map[string]any)
			r.Attrs(func(a slog.Attr) bool {
				got[a.Key] = a.Value.Any()
				return true
			})

			for key, wantVal := range tc.want {
				gotVal, ok := got[key]
				if !ok {
					t.Errorf("missing log field %q", key)
					continue
				}
				if gotVal != wantVal {
					t.Errorf("field %q = %v (%T), want %v (%T)", key, gotVal, gotVal, wantVal, wantVal)
				}
			}
		})
	}
}
