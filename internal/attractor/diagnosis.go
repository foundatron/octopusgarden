package attractor

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// wonderTemperature is the sampling temperature for the wonder (diagnosis) phase.
// Higher temperature encourages diverse hypotheses.
const wonderTemperature = 0.8

// reflectTemperature is the sampling temperature for the reflect (generation) phase.
// Lower temperature encourages coherent, focused code output.
const reflectTemperature = 0.4

// Diagnosis holds the structured output of the wonder phase.
type Diagnosis struct {
	Hypotheses        []string `json:"hypotheses"`
	RootCauses        []string `json:"root_causes"`
	SuggestedApproach string   `json:"suggested_approach"`
}

var errEmptySuggestedApproach = errors.New("attractor: diagnosis missing required suggested_approach")

// parseDiagnosis parses a Diagnosis from raw LLM output.
// Strips markdown code fences before unmarshalling.
// Only SuggestedApproach is required; empty hypotheses and root_causes are allowed.
func parseDiagnosis(raw string) (Diagnosis, error) {
	cleaned := extractJSONFromText(raw)
	var d Diagnosis
	if err := json.Unmarshal([]byte(cleaned), &d); err != nil {
		return Diagnosis{}, fmt.Errorf("attractor: parse diagnosis JSON: %w", err)
	}
	if strings.TrimSpace(d.SuggestedApproach) == "" {
		return Diagnosis{}, errEmptySuggestedApproach
	}
	return d, nil
}

// extractJSONFromText strips markdown code fences from LLM output to get raw JSON.
// Handles ```json\n...\n``` and ```\n...\n``` patterns.
// This mirrors the extractJSON function in internal/llm/json.go (unexported there).
func extractJSONFromText(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
		s = strings.TrimSpace(s)
	}
	return s
}

// buildWonderPrompt constructs the prompt for the wonder (diagnosis) phase.
// It receives iteration feedback, the best generated files seen so far, and the
// score history. Oscillation detection data is incorporated when available.
// Holdout isolation is preserved: only generated code is included, never scenario files.
func buildWonderPrompt(history []iterationFeedback, bestFiles map[string]string, scoreHistory []float64, oscillating bool) string {
	var b strings.Builder
	b.WriteString("You are a debugging expert. Analyze why the following code generation attempts are failing.\n\n")

	if len(scoreHistory) > 0 {
		b.WriteString("SCORE HISTORY:\n")
		for i, s := range scoreHistory {
			fmt.Fprintf(&b, "  Iteration %d: %.1f/100\n", i+1, s)
		}
		b.WriteString("\n")
	}

	if oscillating {
		b.WriteString("OSCILLATION DETECTED: The generator is alternating between two implementations without progress. This is a key signal for your diagnosis.\n\n")
	}

	b.WriteString("RECENT FAILURES:\n")
	if len(history) == 0 {
		b.WriteString("  (no history available)\n")
	} else {
		start := max(len(history)-maxFeedbackEntries, 0)
		for _, fb := range history[start:] {
			fmt.Fprintf(&b, "%s (iteration %d):\n%s\n\n", feedbackHeader(fb.kind), fb.iteration, truncateFeedback(fb.message, maxFeedbackForFidelity(fidelityFull)))
		}
	}

	if len(bestFiles) > 0 {
		b.WriteString("BEST GENERATED CODE SO FAR:\n")
		paths := slices.Sorted(maps.Keys(bestFiles))
		for _, p := range paths {
			fmt.Fprintf(&b, "=== FILE: %s ===\n%s=== END FILE ===\n\n", p, truncateFeedback(bestFiles[p], 4096))
		}
	}

	b.WriteString(`Respond with a JSON object containing your diagnosis:
{
  "hypotheses": ["<possible cause 1>", "<possible cause 2>"],
  "root_causes": ["<confirmed root cause>"],
  "suggested_approach": "<concrete description of a fundamentally different implementation approach to try>"
}

The suggested_approach must be specific and actionable — describe the new architecture or algorithm to use, not just "try something different". Only suggested_approach is required; hypotheses and root_causes may be empty arrays if not applicable.`)

	return b.String()
}

// buildReflectPrompt constructs the prompt for the reflect (generation) phase.
// It incorporates the diagnosis from the wonder phase and optionally includes
// the minimalism instruction when the score is already above minimalismThreshold.
func buildReflectPrompt(diagnosis Diagnosis, minimalism bool) string {
	var b strings.Builder
	b.WriteString("Based on the following diagnosis of why previous attempts failed, generate a new implementation.\n\n")

	b.WriteString("DIAGNOSIS:\n")
	if len(diagnosis.Hypotheses) > 0 {
		b.WriteString("Hypotheses:\n")
		for _, h := range diagnosis.Hypotheses {
			fmt.Fprintf(&b, "- %s\n", h)
		}
	}
	if len(diagnosis.RootCauses) > 0 {
		b.WriteString("Root causes:\n")
		for _, rc := range diagnosis.RootCauses {
			fmt.Fprintf(&b, "- %s\n", rc)
		}
	}
	fmt.Fprintf(&b, "Suggested approach: %s\n", diagnosis.SuggestedApproach)

	if minimalism {
		b.WriteString("\nMINIMALISM REQUIREMENT: The current solution is already scoring above 80%. Implement the SMALLEST possible change based on the diagnosis above. Do not refactor working parts.\n")
	}

	b.WriteString("\nGenerate the complete corrected application. Output ALL files using the === FILE: path === format.")

	return b.String()
}
