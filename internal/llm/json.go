package llm

import "strings"

// judgeResult is the expected JSON structure from the judge LLM.
type judgeResult struct {
	Score     int      `json:"score"`
	Reasoning string   `json:"reasoning"`
	Failures  []string `json:"failures"`
}

// extractJSON strips markdown code fences from LLM output to get raw JSON.
// Handles ```json\n...\n``` and ```\n...\n``` patterns.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		// Strip opening fence (with optional language tag).
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		// Strip closing fence.
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
		s = strings.TrimSpace(s)
	}
	return s
}
