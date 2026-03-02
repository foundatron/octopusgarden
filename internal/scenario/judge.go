package scenario

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/foundatron/octopusgarden/internal/llm"
)

var errStepCountMismatch = errors.New("judge: step count mismatch between scenario and result")

// Judge scores scenario results using an LLM-as-judge.
type Judge struct {
	LLM    llm.Client
	Model  string
	Logger *slog.Logger
}

// NewJudge creates a Judge with the given LLM client and model.
func NewJudge(client llm.Client, model string, logger *slog.Logger) *Judge {
	return &Judge{
		LLM:    client,
		Model:  model,
		Logger: logger,
	}
}

// Score evaluates a single step's response against its expected behavior.
func (j *Judge) Score(ctx context.Context, scenario Scenario, step Step, response HTTPResponse) (StepScore, error) {
	observed := fmt.Sprintf("HTTP %d\nHeaders: %v\nBody:\n%s", response.Status, response.Headers, response.Body)

	userPrompt := llm.SatisfactionJudgeUser
	userPrompt = strings.ReplaceAll(userPrompt, "{scenario_description}", scenario.Description)
	userPrompt = strings.ReplaceAll(userPrompt, "{step_description}", step.Description)
	userPrompt = strings.ReplaceAll(userPrompt, "{expected}", step.Expect)
	userPrompt = strings.ReplaceAll(userPrompt, "{observed}", observed)

	resp, err := j.LLM.Judge(ctx, llm.JudgeRequest{
		SystemPrompt: llm.SatisfactionJudgeSystem,
		UserPrompt:   userPrompt,
		Model:        j.Model,
		CacheControl: &llm.CacheControl{Type: "ephemeral"},
	})
	if err != nil {
		return StepScore{}, fmt.Errorf("judge score: %w", err)
	}

	j.Logger.Debug("step scored",
		"scenario", scenario.ID,
		"step", step.Description,
		"score", resp.Score,
		"cost", resp.CostUSD,
	)

	return StepScore{
		Score:     resp.Score,
		Reasoning: resp.Reasoning,
		Failures:  resp.Failures,
		CostUSD:   resp.CostUSD,
	}, nil
}

// ScoreScenario scores all steps in a scenario result.
func (j *Judge) ScoreScenario(ctx context.Context, scenario Scenario, result Result) (ScoredScenario, error) {
	if len(result.Steps) != len(scenario.Steps) {
		return ScoredScenario{}, fmt.Errorf("%w: scenario has %d steps, result has %d",
			errStepCountMismatch, len(scenario.Steps), len(result.Steps))
	}

	weight := 1.0
	if scenario.Weight != nil {
		weight = *scenario.Weight
	}

	scored := make([]ScoredStep, 0, len(result.Steps))
	var totalScore float64

	for i, stepResult := range result.Steps {
		step := scenario.Steps[i]

		var score StepScore
		if stepResult.Err != nil {
			// Transport failure — score 0 without calling LLM.
			score = StepScore{
				Score:     0,
				Reasoning: fmt.Sprintf("HTTP request failed: %v", stepResult.Err),
			}
		} else {
			var err error
			score, err = j.Score(ctx, scenario, step, stepResult.Response)
			if err != nil {
				return ScoredScenario{}, fmt.Errorf("score step %d: %w", i, err)
			}
		}

		scored = append(scored, ScoredStep{
			StepResult: stepResult,
			StepScore:  score,
		})
		totalScore += float64(score.Score)
	}

	avg := 0.0
	if len(scored) > 0 {
		avg = totalScore / float64(len(scored))
	}

	return ScoredScenario{
		ScenarioID: scenario.ID,
		Weight:     weight,
		Steps:      scored,
		Score:      avg,
	}, nil
}

// Aggregate computes a weighted average satisfaction score across scenarios.
func Aggregate(scenarios []ScoredScenario) AggregateResult {
	if len(scenarios) == 0 {
		return AggregateResult{}
	}

	var totalWeight float64
	var weightedSum float64
	var totalCost float64
	failureSet := make(map[string]struct{})

	for _, sc := range scenarios {
		totalWeight += sc.Weight
		weightedSum += sc.Score * sc.Weight
		for _, step := range sc.Steps {
			totalCost += step.StepScore.CostUSD
			for _, f := range step.StepScore.Failures {
				failureSet[f] = struct{}{}
			}
		}
	}

	satisfaction := 0.0
	if totalWeight > 0 {
		satisfaction = weightedSum / totalWeight
	}

	failures := make([]string, 0, len(failureSet))
	for f := range failureSet {
		failures = append(failures, f)
	}
	slices.Sort(failures)

	return AggregateResult{
		Scenarios:    scenarios,
		Satisfaction: satisfaction,
		TotalCostUSD: totalCost,
		Failures:     failures,
	}
}
