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

func checkAttrs(t *testing.T, label string, r slog.Record, want map[string]any) {
	t.Helper()
	got := make(map[string]any)
	r.Attrs(func(a slog.Attr) bool {
		got[a.Key] = a.Value.Any()
		return true
	})
	for key, wantVal := range want {
		gotVal, ok := got[key]
		if !ok {
			t.Errorf("%s: missing log field %q", label, key)
			continue
		}
		if gotVal != wantVal {
			t.Errorf("%s: field %q = %v (%T), want %v (%T)", label, key, gotVal, gotVal, wantVal, wantVal)
		}
	}
}

func TestUsageMetricsLog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		m         usageMetrics
		prefix    string
		wantInfo  map[string]any
		wantDebug map[string]any
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
			wantInfo: map[string]any{
				"model":     "gpt-4o",
				"cost_usd":  0.005,
				"cache_hit": true,
			},
			wantDebug: map[string]any{
				"input_tokens":          int64(100),
				"cache_creation_tokens": int64(10),
				"cache_read_tokens":     int64(50),
				"output_tokens":         int64(200),
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
			wantInfo: map[string]any{
				"model":     "claude-3-5-haiku-latest",
				"cost_usd":  0.0,
				"cache_hit": false,
			},
			wantDebug: map[string]any{
				"input_tokens":          int64(80),
				"cache_creation_tokens": int64(0),
				"cache_read_tokens":     int64(0),
				"output_tokens":         int64(120),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := &recordHandler{}
			logger := slog.New(h)

			tc.m.log(logger, tc.prefix)

			if len(h.records) != 2 {
				t.Fatalf("expected 2 log records (info + debug), got %d", len(h.records))
			}

			// First record: Info with compact fields.
			infoRec := h.records[0]
			if infoRec.Level != slog.LevelInfo {
				t.Errorf("record[0] level = %v, want Info", infoRec.Level)
			}
			if infoRec.Message != tc.prefix {
				t.Errorf("record[0] message = %q, want %q", infoRec.Message, tc.prefix)
			}
			checkAttrs(t, "info", infoRec, tc.wantInfo)

			// Second record: Debug with token breakdown.
			debugRec := h.records[1]
			if debugRec.Level != slog.LevelDebug {
				t.Errorf("record[1] level = %v, want Debug", debugRec.Level)
			}
			wantDebugMsg := tc.prefix + " tokens"
			if debugRec.Message != wantDebugMsg {
				t.Errorf("record[1] message = %q, want %q", debugRec.Message, wantDebugMsg)
			}
			checkAttrs(t, "debug", debugRec, tc.wantDebug)
		})
	}
}
