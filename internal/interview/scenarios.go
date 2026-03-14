package interview

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/foundatron/octopusgarden/internal/attractor"
	"github.com/foundatron/octopusgarden/internal/llm"
	"github.com/foundatron/octopusgarden/internal/scenario"
)

// scenarioMaxTokens is the maximum number of tokens to request when generating
// scenario YAML from a spec.
const scenarioMaxTokens = 8192

var (
	errNoValidScenarios    = errors.New("interview: no valid scenarios generated")
	errParseScenarioOutput = errors.New("interview: failed to parse scenario output")
)

// ScenarioGenerator generates holdout validation scenarios from a spec using an LLM.
type ScenarioGenerator struct {
	client llm.Client
	model  string
	log    *slog.Logger
}

// NewScenarioGenerator creates a ScenarioGenerator that uses the given LLM client and model.
func NewScenarioGenerator(client llm.Client, model string, log *slog.Logger) *ScenarioGenerator {
	return &ScenarioGenerator{client: client, model: model, log: log}
}

// Generate generates scenario YAML files from specContent.
// Returns a map of filename to YAML content, the LLM cost in USD, and any error.
func (g *ScenarioGenerator) Generate(ctx context.Context, specContent string) (map[string]string, float64, error) {
	if strings.TrimSpace(specContent) == "" {
		return nil, 0, errEmptySpec
	}

	temp := 0.7
	req := llm.GenerateRequest{
		SystemPrompt: scenarioSystemPrompt,
		Messages:     []llm.Message{{Role: "user", Content: specContent}},
		Model:        g.model,
		MaxTokens:    scenarioMaxTokens,
		Temperature:  &temp,
		CacheControl: &llm.CacheControl{Type: "ephemeral"},
	}

	resp, err := g.client.Generate(ctx, req)
	if err != nil {
		return nil, 0, fmt.Errorf("scenario generator: %w", err)
	}

	raw, err := attractor.ParseFiles(resp.Content)
	if err != nil {
		return nil, resp.CostUSD, fmt.Errorf("%w: %w", errParseScenarioOutput, err)
	}

	valid := make(map[string]string, len(raw))
	for name, content := range raw {
		cleaned := filepath.Base(name)

		if strings.Contains(cleaned, "..") || cleaned == "." {
			g.log.Warn("generated scenario has unsafe filename, skipping", "file", name)
			continue
		}

		if _, loadErr := scenario.Load(strings.NewReader(content)); loadErr != nil {
			g.log.Warn("generated scenario failed validation, skipping", "file", name, "err", loadErr)
			continue
		}

		if existing, collision := valid[cleaned]; collision {
			g.log.Warn("generated scenario filename collision, keeping first",
				"file", name, "cleaned", cleaned, "existing_length", len(existing))
			continue
		}
		valid[cleaned] = content
	}

	if len(valid) == 0 {
		return nil, resp.CostUSD, errNoValidScenarios
	}

	return valid, resp.CostUSD, nil
}
