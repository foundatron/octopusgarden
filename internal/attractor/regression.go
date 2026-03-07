package attractor

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// passScenarioPrefix identifies a passing scenario summary line.
// cmd/octog uses "✓ id (score/100)" format for passing entries;
// this constant defines the parse-side counterpart to that convention.
const passScenarioPrefix = "✓ "

// Regression records a per-scenario regression across two consecutive validated iterations.
// A regression occurs when a scenario was at or above the convergence threshold in a prior
// iteration but drops below it in the current iteration.
type Regression struct {
	ScenarioID    string
	PrevScore     float64
	CurrScore     float64
	PrevIteration int
	CurrIteration int
}

// parseScenarioLine parses a single scenario summary line in the format
// "✓ id (score/100)" or "✗ id (score/100)". Returns id, score, and ok=true
// on success. Returns ("", 0, false) for any malformed or unrecognized line.
func parseScenarioLine(line string) (id string, score float64, ok bool) {
	// Strip either pass or fail prefix.
	rest, hasFail := strings.CutPrefix(line, failScenarioPrefix)
	if !hasFail {
		rest2, hasPass := strings.CutPrefix(line, passScenarioPrefix)
		if !hasPass {
			return "", 0, false
		}
		rest = rest2
	}

	// rest is now "id (score/100)" — split on last "(" to separate id from score.
	parenIdx := strings.LastIndex(rest, "(")
	if parenIdx < 0 {
		return "", 0, false
	}
	id = strings.TrimSpace(rest[:parenIdx])
	scoreStr := rest[parenIdx+1:]

	// scoreStr is now "score/100)" — strip the "/100)" suffix.
	slashIdx := strings.Index(scoreStr, "/100)")
	if slashIdx < 0 {
		return "", 0, false
	}
	scoreStr = scoreStr[:slashIdx]

	s, err := strconv.ParseFloat(scoreStr, 64)
	if err != nil {
		return "", 0, false
	}
	return id, s, true
}

// parseAllScenarios parses the mixed pass/fail slice returned by ValidateFn and
// returns a map of scenario ID → score for all scenarios (passing and failing).
// Malformed or unrecognized entries are silently skipped.
func parseAllScenarios(failures []string) map[string]float64 {
	result := make(map[string]float64)
	for _, entry := range failures {
		firstLine, _, _ := strings.Cut(entry, "\n")
		firstLine = strings.TrimSpace(firstLine)
		id, score, ok := parseScenarioLine(firstLine)
		if !ok {
			continue
		}
		result[id] = score
	}
	return result
}

// detectRegressions compares two snapshots of per-scenario scores and returns
// regressions: scenarios that were at or above threshold in the previous iteration
// and dropped below threshold in the current iteration.
//
// prevScores may be nil or empty (e.g. on the first validated iteration), in which
// case no regressions are reported. Results are sorted by ScenarioID for determinism.
func detectRegressions(prevScores map[string]float64, prevIteration int, currScores map[string]float64, currIteration int, threshold float64) []Regression {
	if len(prevScores) == 0 {
		return nil
	}
	var regressions []Regression
	for id, curr := range currScores {
		prev, ok := prevScores[id]
		if !ok {
			continue
		}
		if prev >= threshold && curr < threshold {
			regressions = append(regressions, Regression{
				ScenarioID:    id,
				PrevScore:     prev,
				CurrScore:     curr,
				PrevIteration: prevIteration,
				CurrIteration: currIteration,
			})
		}
	}
	slices.SortFunc(regressions, func(a, b Regression) int {
		return strings.Compare(a.ScenarioID, b.ScenarioID)
	})
	return regressions
}

// formatRegressions formats a slice of regressions as body text for inclusion in
// iteration feedback. Returns one line per regression, sorted by ScenarioID.
// Returns an empty string when regressions is empty.
// Callers must pass regressions pre-sorted by ScenarioID (as returned by detectRegressions).
func formatRegressions(regressions []Regression) string {
	if len(regressions) == 0 {
		return ""
	}
	var b strings.Builder
	for _, r := range regressions {
		fmt.Fprintf(&b, "scenario '%s' dropped from %.0f → %.0f (iteration %d → %d)\n",
			r.ScenarioID, r.PrevScore, r.CurrScore, r.PrevIteration, r.CurrIteration)
	}
	return b.String()
}
