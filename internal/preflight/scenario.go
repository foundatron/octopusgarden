package preflight

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/foundatron/octopusgarden/internal/llm"
)

// maxScenarioTotalBytes is the maximum combined size of all scenario files
// accepted by CheckScenarios. Prevents context-window overruns and unexpected costs.
const maxScenarioTotalBytes = 5 * 1024 * 1024 // 5 MB

var errScenariosTooLarge = errors.New("preflight: total scenario file size exceeds 5 MB limit")

var (
	errMalformedScenarioResponse = errors.New("preflight: malformed scenario LLM response")
	errNoScenarios               = errors.New("preflight: no YAML scenario files found in directory")
)

// ScenarioIssue describes a specific problem found in a scenario.
type ScenarioIssue struct {
	Scenario  string `json:"scenario"`
	Dimension string `json:"dimension"`
	Detail    string `json:"detail"`
}

// ScenarioResult holds the quality assessment for a set of scenarios.
type ScenarioResult struct {
	Coverage    float64
	Feasibility float64
	Isolation   float64
	Chains      float64
	Aggregate   float64
	Pass        bool
	Issues      []ScenarioIssue
}

// scenarioPreflightResponse is the expected JSON structure from the scenario preflight LLM call.
type scenarioPreflightResponse struct {
	Coverage    float64         `json:"coverage"`
	Feasibility float64         `json:"feasibility"`
	Isolation   float64         `json:"isolation"`
	Chains      float64         `json:"chains"`
	Issues      []ScenarioIssue `json:"issues"`
}

func buildScenarioSystemPrompt() string {
	return `You are a scenario quality analyst. Your job is to assess how well a set of test scenarios covers and exercises a software specification.

Evaluate the scenarios on four dimensions, each scored from 0.0 (completely inadequate) to 1.0 (excellent):

- coverage: Do the scenarios collectively exercise all behaviors described in the spec? Are happy paths, edge cases, and failure modes represented? Scope your coverage evaluation to behavior that is testable via the step types present in the scenarios — for example, exec-only suites should be scored against CLI-observable behavior such as exit codes, stdout/stderr, and file system effects. Flag genuine gaps only when the spec describes behavior that the available step types could reasonably test.
- feasibility: Are the scenarios executable as written? Do steps reference valid endpoints, actions, and data that an implementation could satisfy?
- isolation: Does each scenario test one coherent behavior? Are scenarios free from hidden dependencies on each other's side effects?
- chains: For multi-step scenarios, do step sequences form coherent chains? Are captures and variable substitutions used correctly?

For any issues found, report them with the scenario name, affected dimension, and a concise actionable description.

Respond ONLY with valid JSON matching this exact schema:
{
  "coverage": <float 0.0-1.0>,
  "feasibility": <float 0.0-1.0>,
  "isolation": <float 0.0-1.0>,
  "chains": <float 0.0-1.0>,
  "issues": [
    {"scenario": "<name>", "dimension": "<coverage|feasibility|isolation|chains>", "detail": "<actionable description>"}
  ]
}

If there are no issues, set "issues" to an empty array [].`
}

func buildScenarioUserPrompt(specContent string, scenarioYAMLs map[string]string) string {
	var sb strings.Builder
	sb.WriteString("Assess the following scenarios against the spec.\n\n")
	sb.WriteString("## Spec\n\n")
	sb.WriteString(specContent)
	sb.WriteString("\n\n## Scenarios\n\n")

	names := make([]string, 0, len(scenarioYAMLs))
	for name := range scenarioYAMLs {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		fmt.Fprintf(&sb, "### %s\n\n", name)
		sb.WriteString("```yaml\n")
		sb.WriteString(scenarioYAMLs[name])
		sb.WriteString("\n```\n\n")
	}

	return sb.String()
}

func parseScenarioResponse(raw string) (*scenarioPreflightResponse, error) {
	extracted := llm.ExtractJSON(raw)
	var r scenarioPreflightResponse
	if err := json.Unmarshal([]byte(extracted), &r); err != nil {
		return nil, fmt.Errorf("%w: %w", errMalformedScenarioResponse, err)
	}
	if r.Coverage < 0 || r.Coverage > 1 ||
		r.Feasibility < 0 || r.Feasibility > 1 ||
		r.Isolation < 0 || r.Isolation > 1 ||
		r.Chains < 0 || r.Chains > 1 {
		return nil, fmt.Errorf("%w: scores must be in [0, 1]", errMalformedScenarioResponse)
	}
	validDimensions := map[string]bool{
		"coverage": true, "feasibility": true, "isolation": true, "chains": true,
	}
	for _, issue := range r.Issues {
		if issue.Dimension != "" && !validDimensions[issue.Dimension] {
			return nil, fmt.Errorf("%w: unknown dimension %q", errMalformedScenarioResponse, issue.Dimension)
		}
	}
	if r.Issues == nil {
		r.Issues = []ScenarioIssue{}
	}
	return &r, nil
}

// computeScenarioAggregate returns the unweighted average of the four scenario quality dimensions.
func computeScenarioAggregate(coverage, feasibility, isolation, chains float64) float64 {
	return (coverage + feasibility + isolation + chains) / 4.0
}

// CheckScenarios calls the LLM to assess scenario quality against the spec and returns a ScenarioResult.
// threshold is the aggregate score (0.0–1.0) below which Pass is false.
func CheckScenarios(ctx context.Context, client llm.Client, model, specContent, scenarioDir string, threshold float64, logger *slog.Logger) (*ScenarioResult, error) {
	entries, err := os.ReadDir(scenarioDir)
	if err != nil {
		return nil, fmt.Errorf("read scenario dir: %w", err)
	}

	scenarioYAMLs := make(map[string]string)
	var totalBytes int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		// Skip symlinks to prevent reading files outside the scenario directory.
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(scenarioDir, name))
		if err != nil {
			return nil, fmt.Errorf("read scenario file %s: %w", name, err)
		}
		totalBytes += int64(len(data))
		if totalBytes > maxScenarioTotalBytes {
			return nil, fmt.Errorf("%w", errScenariosTooLarge)
		}
		scenarioYAMLs[name] = string(data)
	}

	if len(scenarioYAMLs) == 0 {
		return nil, errNoScenarios
	}

	req := llm.GenerateRequest{
		Model:     model,
		MaxTokens: 2048,
		Messages: []llm.Message{
			{Role: "user", Content: buildScenarioUserPrompt(specContent, scenarioYAMLs)},
		},
		SystemPrompt: buildScenarioSystemPrompt(),
		CacheControl: &llm.CacheControl{Type: "ephemeral"},
	}

	resp, err := client.Generate(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("scenario preflight: generate: %w", err)
	}

	logger.Info("scenario preflight LLM call complete",
		"input_tokens", resp.InputTokens,
		"output_tokens", resp.OutputTokens,
		"cost_usd", resp.CostUSD,
		"cache_hit", resp.CacheHit,
	)

	parsed, err := parseScenarioResponse(resp.Content)
	if err != nil {
		return nil, err
	}

	agg := computeScenarioAggregate(parsed.Coverage, parsed.Feasibility, parsed.Isolation, parsed.Chains)

	return &ScenarioResult{
		Coverage:    parsed.Coverage,
		Feasibility: parsed.Feasibility,
		Isolation:   parsed.Isolation,
		Chains:      parsed.Chains,
		Aggregate:   agg,
		Pass:        agg >= threshold,
		Issues:      parsed.Issues,
	}, nil
}
