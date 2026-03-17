package interview

import (
	"strings"
	"testing"
)

func TestScenarioSystemPromptCLIGuidance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		substring string
	}{
		{"block scalar guidance", "command: |"},
		{"cleanup guidance", "cleanup"},
		{"exit code assertion", "exit code"},
		{"exec example present", "exec:"},
		{"quality criteria self-check section", "Quality Criteria"},
		{"capture chain requirement", "preceding step"},
		{"isolation requirement", "Single behavior per scenario"},
		{"bash -c requirement language", "MUST be wrapped"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !strings.Contains(scenarioSystemPrompt, tc.substring) {
				t.Errorf("scenarioSystemPrompt missing expected substring %q", tc.substring)
			}
		})
	}
}
