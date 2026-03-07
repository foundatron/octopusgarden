package attractor

import (
	"strings"
	"testing"
)

func TestParseDiagnosis_ValidJSON(t *testing.T) {
	raw := `{"hypotheses":["h1","h2"],"root_causes":["rc1"],"suggested_approach":"Use a different algorithm"}`
	d, err := parseDiagnosis(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(d.Hypotheses) != 2 {
		t.Errorf("expected 2 hypotheses, got %d", len(d.Hypotheses))
	}
	if len(d.RootCauses) != 1 {
		t.Errorf("expected 1 root cause, got %d", len(d.RootCauses))
	}
	if d.SuggestedApproach != "Use a different algorithm" {
		t.Errorf("unexpected suggested_approach: %q", d.SuggestedApproach)
	}
}

func TestParseDiagnosis_InvalidJSON(t *testing.T) {
	_, err := parseDiagnosis("this is not json at all")
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestParseDiagnosis_EmptyApproach(t *testing.T) {
	raw := `{"hypotheses":["h1"],"root_causes":[],"suggested_approach":""}`
	_, err := parseDiagnosis(raw)
	if err == nil {
		t.Fatal("expected error for empty suggested_approach, got nil")
	}
}

func TestParseDiagnosis_JSONInMarkdown(t *testing.T) {
	raw := "```json\n{\"hypotheses\":[],\"root_causes\":[],\"suggested_approach\":\"Try event-driven architecture\"}\n```"
	d, err := parseDiagnosis(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.SuggestedApproach != "Try event-driven architecture" {
		t.Errorf("unexpected suggested_approach: %q", d.SuggestedApproach)
	}
}

func TestParseDiagnosis_EmptyHypotheses(t *testing.T) {
	// Empty hypotheses and root_causes are allowed — only suggested_approach is required.
	raw := `{"hypotheses":[],"root_causes":[],"suggested_approach":"Rewrite the HTTP handler"}`
	d, err := parseDiagnosis(raw)
	if err != nil {
		t.Fatalf("unexpected error for empty hypotheses: %v", err)
	}
	if d.SuggestedApproach != "Rewrite the HTTP handler" {
		t.Errorf("unexpected suggested_approach: %q", d.SuggestedApproach)
	}
}

func TestBuildWonderPrompt_IncludesScoresAndFailures(t *testing.T) {
	history := []iterationFeedback{
		{
			iteration: 1,
			kind:      feedbackValidation,
			message:   "Satisfaction score: 50.0/100\nScenario: auth failed",
			fidelity:  fidelityFull,
		},
	}
	bestFiles := map[string]string{
		"main.go":    "package main\nfunc main() {}",
		"Dockerfile": "FROM scratch",
	}
	scoreHistory := []float64{50.0, 45.0}

	prompt := buildWonderPrompt(history, bestFiles, scoreHistory, false)

	if !strings.Contains(prompt, "50.0/100") {
		t.Error("prompt should contain score history")
	}
	if !strings.Contains(prompt, "45.0/100") {
		t.Error("prompt should contain all scores")
	}
	if !strings.Contains(prompt, "auth failed") {
		t.Error("prompt should contain failure message")
	}
	if !strings.Contains(prompt, "main.go") {
		t.Error("prompt should contain generated code file name")
	}
}

func TestBuildWonderPrompt_IncludesOscillationNotice(t *testing.T) {
	prompt := buildWonderPrompt(nil, nil, nil, true)
	if !strings.Contains(prompt, "OSCILLATION DETECTED") {
		t.Error("prompt should include oscillation notice when oscillating=true")
	}
}

func TestBuildWonderPrompt_NoOscillationNotice(t *testing.T) {
	prompt := buildWonderPrompt(nil, nil, nil, false)
	if strings.Contains(prompt, "OSCILLATION DETECTED") {
		t.Error("prompt should not include oscillation notice when oscillating=false")
	}
}

func TestBuildReflectPrompt_IncludesDiagnosis(t *testing.T) {
	d := Diagnosis{
		Hypotheses:        []string{"wrong auth header"},
		RootCauses:        []string{"token expiry not handled"},
		SuggestedApproach: "Implement token refresh logic",
	}
	prompt := buildReflectPrompt(d, false)

	if !strings.Contains(prompt, "wrong auth header") {
		t.Error("prompt should include hypothesis")
	}
	if !strings.Contains(prompt, "token expiry not handled") {
		t.Error("prompt should include root cause")
	}
	if !strings.Contains(prompt, "Implement token refresh logic") {
		t.Error("prompt should include suggested approach")
	}
}

func TestBuildReflectPrompt_MinimalismFlag(t *testing.T) {
	d := Diagnosis{SuggestedApproach: "fix the handler"}

	withMinimalism := buildReflectPrompt(d, true)
	if !strings.Contains(withMinimalism, "MINIMALISM") {
		t.Error("prompt should include minimalism instruction when minimalism=true")
	}

	withoutMinimalism := buildReflectPrompt(d, false)
	if strings.Contains(withoutMinimalism, "MINIMALISM") {
		t.Error("prompt should not include minimalism instruction when minimalism=false")
	}
}
