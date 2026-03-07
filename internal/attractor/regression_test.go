package attractor

import (
	"strings"
	"testing"
)

func TestParseScenarioLine(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantID    string
		wantScore float64
		wantOK    bool
	}{
		{
			name:      "passing scenario",
			line:      "✓ login flow (98/100)",
			wantID:    "login flow",
			wantScore: 98,
			wantOK:    true,
		},
		{
			name:      "failing scenario",
			line:      "✗ login flow (45/100)",
			wantID:    "login flow",
			wantScore: 45,
			wantOK:    true,
		},
		{
			name:      "failing scenario with zero score",
			line:      "✗ auth flow (0/100)",
			wantID:    "auth flow",
			wantScore: 0,
			wantOK:    true,
		},
		{
			name:      "passing scenario with decimal score",
			line:      "✓ crud items (72/100)",
			wantID:    "crud items",
			wantScore: 72,
			wantOK:    true,
		},
		{
			name:   "malformed — no prefix",
			line:   "login flow (98/100)",
			wantOK: false,
		},
		{
			name:   "malformed — missing score paren",
			line:   "✓ login flow",
			wantOK: false,
		},
		{
			name:   "malformed — missing /100) suffix",
			line:   "✓ login flow (98)",
			wantOK: false,
		},
		{
			name:   "malformed — non-numeric score",
			line:   "✓ login flow (abc/100)",
			wantOK: false,
		},
		{
			name:   "empty string",
			line:   "",
			wantOK: false,
		},
		{
			name:   "indented step line",
			line:   "  ✓ step: GET /health",
			wantOK: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, score, ok := parseScenarioLine(tc.line)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if id != tc.wantID {
				t.Errorf("id = %q, want %q", id, tc.wantID)
			}
			if score != tc.wantScore {
				t.Errorf("score = %v, want %v", score, tc.wantScore)
			}
		})
	}
}

func TestParseAllScenarios(t *testing.T) {
	tests := []struct {
		name     string
		failures []string
		want     map[string]float64
	}{
		{
			name:     "empty input returns empty map",
			failures: nil,
			want:     map[string]float64{},
		},
		{
			name: "mixed passing and failing entries",
			failures: []string{
				"✓ login flow (98/100)\n  ✓ GET /login (100/100)",
				"✗ auth flow (45/100)\n  ✗ POST /auth (45/100)",
			},
			want: map[string]float64{
				"login flow": 98,
				"auth flow":  45,
			},
		},
		{
			name: "all passing",
			failures: []string{
				"✓ scenario-a (100/100)",
				"✓ scenario-b (80/100)",
			},
			want: map[string]float64{
				"scenario-a": 100,
				"scenario-b": 80,
			},
		},
		{
			name: "all failing",
			failures: []string{
				"✗ scenario-a (20/100)",
				"✗ scenario-b (0/100)",
			},
			want: map[string]float64{
				"scenario-a": 20,
				"scenario-b": 0,
			},
		},
		{
			name: "malformed entries skipped",
			failures: []string{
				"✓ good scenario (90/100)",
				"this is not a scenario line",
				"",
			},
			want: map[string]float64{
				"good scenario": 90,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseAllScenarios(tc.failures)
			if len(got) != len(tc.want) {
				t.Fatalf("len(got) = %d, want %d: got=%v", len(got), len(tc.want), got)
			}
			for id, wantScore := range tc.want {
				gotScore, ok := got[id]
				if !ok {
					t.Errorf("missing scenario %q in result", id)
					continue
				}
				if gotScore != wantScore {
					t.Errorf("scenario %q: score = %v, want %v", id, gotScore, wantScore)
				}
			}
		})
	}
}

func TestDetectRegressions(t *testing.T) {
	const threshold = 95.0

	tests := []struct {
		name          string
		prevScores    map[string]float64
		prevIteration int
		currScores    map[string]float64
		currIteration int
		threshold     float64
		wantIDs       []string // nil = expect no regressions
	}{
		{
			name:          "no regression when scores improve",
			prevScores:    map[string]float64{"scenario-a": 60},
			prevIteration: 1,
			currScores:    map[string]float64{"scenario-a": 80},
			currIteration: 2,
			threshold:     threshold,
			wantIDs:       nil,
		},
		{
			name:          "regression when prev >= threshold and curr < threshold",
			prevScores:    map[string]float64{"login flow": 98},
			prevIteration: 1,
			currScores:    map[string]float64{"login flow": 45},
			currIteration: 2,
			threshold:     threshold,
			wantIDs:       []string{"login flow"},
		},
		{
			name:          "no regression on first iteration (prev nil)",
			prevScores:    nil,
			prevIteration: 0,
			currScores:    map[string]float64{"scenario-a": 45},
			currIteration: 1,
			threshold:     threshold,
			wantIDs:       nil,
		},
		{
			name:          "no regression on first iteration (prev empty)",
			prevScores:    map[string]float64{},
			prevIteration: 0,
			currScores:    map[string]float64{"scenario-a": 45},
			currIteration: 1,
			threshold:     threshold,
			wantIDs:       nil,
		},
		{
			name:          "no regression for scenario already below threshold",
			prevScores:    map[string]float64{"scenario-a": 80},
			prevIteration: 1,
			currScores:    map[string]float64{"scenario-a": 50},
			currIteration: 2,
			threshold:     threshold,
			wantIDs:       nil,
		},
		{
			name: "all scenarios regress simultaneously",
			prevScores: map[string]float64{
				"scenario-a": 98,
				"scenario-b": 96,
			},
			prevIteration: 1,
			currScores: map[string]float64{
				"scenario-a": 20,
				"scenario-b": 10,
			},
			currIteration: 2,
			threshold:     threshold,
			wantIDs:       []string{"scenario-a", "scenario-b"},
		},
		{
			name:          "custom threshold: prev=85 curr=70 with threshold=80",
			prevScores:    map[string]float64{"scenario-a": 85},
			prevIteration: 1,
			currScores:    map[string]float64{"scenario-a": 70},
			currIteration: 2,
			threshold:     80,
			wantIDs:       []string{"scenario-a"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := detectRegressions(tc.prevScores, tc.prevIteration, tc.currScores, tc.currIteration, tc.threshold)

			if len(tc.wantIDs) == 0 {
				if len(got) != 0 {
					t.Errorf("expected no regressions, got %v", got)
				}
				return
			}

			if len(got) != len(tc.wantIDs) {
				t.Fatalf("len(regressions) = %d, want %d: got=%v", len(got), len(tc.wantIDs), got)
			}
			for i, r := range got {
				if r.ScenarioID != tc.wantIDs[i] {
					t.Errorf("regression[%d].ScenarioID = %q, want %q", i, r.ScenarioID, tc.wantIDs[i])
				}
				prev, ok := tc.prevScores[r.ScenarioID]
				if !ok {
					t.Errorf("regression[%d].ScenarioID %q not in prevScores", i, r.ScenarioID)
					continue
				}
				if r.PrevScore != prev {
					t.Errorf("regression[%d].PrevScore = %v, want %v", i, r.PrevScore, prev)
				}
				if r.CurrScore != tc.currScores[r.ScenarioID] {
					t.Errorf("regression[%d].CurrScore = %v, want %v", i, r.CurrScore, tc.currScores[r.ScenarioID])
				}
				if r.PrevIteration != tc.prevIteration {
					t.Errorf("regression[%d].PrevIteration = %d, want %d", i, r.PrevIteration, tc.prevIteration)
				}
				if r.CurrIteration != tc.currIteration {
					t.Errorf("regression[%d].CurrIteration = %d, want %d", i, r.CurrIteration, tc.currIteration)
				}
			}
		})
	}
}

// TestDetectRegressionsOscillation simulates a scenario that oscillates:
// passes → fails → passes across three consecutive detectRegressions calls.
func TestDetectRegressionsOscillation(t *testing.T) {
	const threshold = 95.0
	runOscillationTest(t, threshold)
}

// runOscillationTest simulates a scenario that passes → fails → passes.
func runOscillationTest(t *testing.T, threshold float64) {
	t.Helper()

	iter1Scores := map[string]float64{"scenario-a": 98} // passes (>= threshold)
	iter2Scores := map[string]float64{"scenario-a": 40} // fails (< threshold)
	iter3Scores := map[string]float64{"scenario-a": 97} // passes again

	// Iter 1 → Iter 2: regression expected (98 → 40).
	r12 := detectRegressions(iter1Scores, 1, iter2Scores, 2, threshold)
	if len(r12) != 1 || r12[0].ScenarioID != "scenario-a" {
		t.Errorf("iter1→iter2: expected regression for scenario-a, got %v", r12)
	}

	// Iter 2 → Iter 3: no regression (prev was 40 < threshold, so no regression).
	r23 := detectRegressions(iter2Scores, 2, iter3Scores, 3, threshold)
	if len(r23) != 0 {
		t.Errorf("iter2→iter3: expected no regressions, got %v", r23)
	}
}

func TestFormatRegressions(t *testing.T) {
	tests := []struct {
		name        string
		regressions []Regression
		wantEmpty   bool
		wantContain []string
		wantSorted  bool
	}{
		{
			name:        "empty input returns empty string",
			regressions: nil,
			wantEmpty:   true,
		},
		{
			name: "single regression includes id, old score, new score, iterations",
			regressions: []Regression{
				{ScenarioID: "login flow", PrevScore: 98, CurrScore: 45, PrevIteration: 1, CurrIteration: 2},
			},
			wantContain: []string{"login flow", "98", "45", "1", "2"},
		},
		{
			// Input is pre-sorted as detectRegressions guarantees; verify output order is preserved.
			name: "multiple regressions sorted by scenario ID",
			regressions: []Regression{
				{ScenarioID: "a-scenario", PrevScore: 100, CurrScore: 10, PrevIteration: 2, CurrIteration: 3},
				{ScenarioID: "z-scenario", PrevScore: 95, CurrScore: 30, PrevIteration: 2, CurrIteration: 3},
			},
			wantContain: []string{"z-scenario", "a-scenario"},
			wantSorted:  true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatRegressions(tc.regressions)

			if tc.wantEmpty {
				if got != "" {
					t.Errorf("expected empty string, got %q", got)
				}
				return
			}

			for _, s := range tc.wantContain {
				if !strings.Contains(got, s) {
					t.Errorf("output missing %q: %q", s, got)
				}
			}

			if tc.wantSorted && len(tc.regressions) > 1 {
				// Verify a-scenario appears before z-scenario in the output.
				aIdx := strings.Index(got, "a-scenario")
				zIdx := strings.Index(got, "z-scenario")
				if aIdx < 0 || zIdx < 0 || aIdx >= zIdx {
					t.Errorf("expected a-scenario before z-scenario in output: %q", got)
				}
			}
		})
	}
}
