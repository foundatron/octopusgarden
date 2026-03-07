package preflight

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/foundatron/octopusgarden/internal/llm"
)

var errMalformedResponse = errors.New("preflight: malformed LLM response")

// Result holds the clarity assessment for a spec.
type Result struct {
	GoalClarity       float64
	ConstraintClarity float64
	SuccessClarity    float64
	AggregateScore    float64
	Pass              bool
	Questions         []string
}

// preflightResponse is the expected JSON structure from the preflight LLM call.
type preflightResponse struct {
	GoalClarity       float64             `json:"goal_clarity"`
	ConstraintClarity float64             `json:"constraint_clarity"`
	SuccessClarity    float64             `json:"success_clarity"`
	Questions         map[string][]string `json:"questions"`
}

// computeAggregate returns a weighted aggregate of the three clarity dimensions.
// Weights: goal 0.4, constraint 0.3, success 0.3.
func computeAggregate(goal, constraint, success float64) float64 {
	return goal*0.4 + constraint*0.3 + success*0.3
}

func buildSystemPrompt() string {
	return `You are a specification clarity analyst. Your job is to assess how well a software specification communicates its requirements to an automated code generator.

Evaluate the spec on three dimensions, each scored from 0.0 (completely unclear) to 1.0 (perfectly clear):

- goal_clarity: Does the spec clearly define WHAT the software should do? Are the core behaviors and purpose unambiguous?
- constraint_clarity: Does the spec clearly define HOW the software should work? Are technical constraints, interfaces, and non-functional requirements specified?
- success_clarity: Does the spec clearly define how to verify success? Are acceptance criteria measurable and testable?

For any dimension scoring below the caller's threshold, provide clarifying questions that, if answered, would raise that dimension's score.

Respond ONLY with valid JSON matching this exact schema:
{
  "goal_clarity": <float 0.0-1.0>,
  "constraint_clarity": <float 0.0-1.0>,
  "success_clarity": <float 0.0-1.0>,
  "questions": {
    "goal": ["question1", "question2"],
    "constraint": ["question1"],
    "success": ["question1"]
  }
}

Example response for a clear spec:
{
  "goal_clarity": 0.95,
  "constraint_clarity": 0.88,
  "success_clarity": 0.92,
  "questions": {}
}

Example response for an unclear spec:
{
  "goal_clarity": 0.4,
  "constraint_clarity": 0.6,
  "success_clarity": 0.3,
  "questions": {
    "goal": ["What are the primary user-facing features?", "What problem does this software solve?"],
    "success": ["How will success be measured?", "What constitutes a passing test?"]
  }
}`
}

func buildUserPrompt(specContent string) string {
	return fmt.Sprintf("Assess the following spec for clarity:\n\n%s", specContent)
}

func parseResponse(raw string) (*preflightResponse, error) {
	extracted := llm.ExtractJSON(raw)
	var r preflightResponse
	if err := json.Unmarshal([]byte(extracted), &r); err != nil {
		return nil, fmt.Errorf("%w: %w", errMalformedResponse, err)
	}
	if r.GoalClarity < 0 || r.GoalClarity > 1 ||
		r.ConstraintClarity < 0 || r.ConstraintClarity > 1 ||
		r.SuccessClarity < 0 || r.SuccessClarity > 1 {
		return nil, fmt.Errorf("%w: scores must be in [0, 1]", errMalformedResponse)
	}
	return &r, nil
}

// Check calls the LLM to assess spec clarity and returns a Result.
// threshold is the aggregate score (0.0–1.0) below which Pass is false and
// questions are surfaced for dimensions scoring below threshold.
func Check(ctx context.Context, client llm.Client, model, specContent string, threshold float64, logger *slog.Logger) (*Result, error) {
	req := llm.GenerateRequest{
		Model:     model,
		MaxTokens: 2048,
		Messages: []llm.Message{
			{Role: "user", Content: buildUserPrompt(specContent)},
		},
		SystemPrompt: buildSystemPrompt(),
		CacheControl: &llm.CacheControl{Type: "ephemeral"},
	}

	resp, err := client.Generate(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("preflight: generate: %w", err)
	}

	logger.Info("preflight LLM call complete",
		"input_tokens", resp.InputTokens,
		"output_tokens", resp.OutputTokens,
		"cost_usd", resp.CostUSD,
		"cache_hit", resp.CacheHit,
	)

	parsed, err := parseResponse(resp.Content)
	if err != nil {
		return nil, err
	}

	agg := computeAggregate(parsed.GoalClarity, parsed.ConstraintClarity, parsed.SuccessClarity)

	// Flatten questions from dimensions scoring below threshold.
	var questions []string
	for _, d := range []struct {
		key   string
		score float64
	}{
		{"goal", parsed.GoalClarity},
		{"constraint", parsed.ConstraintClarity},
		{"success", parsed.SuccessClarity},
	} {
		if d.score < threshold {
			for _, q := range parsed.Questions[d.key] {
				questions = append(questions, fmt.Sprintf("[%s] %s", d.key, q))
			}
		}
	}

	return &Result{
		GoalClarity:       parsed.GoalClarity,
		ConstraintClarity: parsed.ConstraintClarity,
		SuccessClarity:    parsed.SuccessClarity,
		AggregateScore:    agg,
		Pass:              agg >= threshold,
		Questions:         questions,
	}, nil
}
