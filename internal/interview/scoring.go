package interview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/foundatron/octopusgarden/internal/llm"
)

var (
	errEmptySpec            = errors.New("spec content is empty")
	errMalformedResponse    = errors.New("malformed scoring response")
	errIncompleteDimensions = errors.New("incomplete dimensions in scoring response")
)

// dimensionOrder defines the canonical output order for spec-completeness dimensions.
// Weights must sum to 1.0. Always iterate this slice (never a map) to guarantee
// stable ordering in CompletenessResult.Dimensions.
var dimensionOrder = []struct {
	name   string
	weight float64
}{
	{"behavioral_completeness", 0.25},
	{"interface_precision", 0.25},
	{"defaults_and_boundaries", 0.20},
	{"acceptance_criteria", 0.20},
	{"economy", 0.10},
}

// DimensionScore holds the score and gap details for a single completeness dimension.
type DimensionScore struct {
	Name   string
	Score  int
	Weight float64
	Gaps   []string
}

// CompletenessResult is the output of a spec-completeness scoring call.
type CompletenessResult struct {
	Dimensions []DimensionScore
	Overall    int
	CostUSD    float64
}

// Scorer scores spec completeness using an LLM judge.
type Scorer struct {
	client llm.Client
	model  string
}

// NewScorer creates a Scorer that uses the given LLM client and model.
func NewScorer(client llm.Client, model string) *Scorer {
	return &Scorer{client: client, model: model}
}

// scoringResponse is the expected JSON structure from the scoring LLM.
type scoringResponse struct {
	Dimensions []struct {
		Name  string   `json:"name"`
		Score int      `json:"score"`
		Gaps  []string `json:"gaps"`
	} `json:"dimensions"`
}

// Score evaluates the completeness of specContent and returns a CompletenessResult.
func (s *Scorer) Score(ctx context.Context, specContent string) (CompletenessResult, error) {
	if strings.TrimSpace(specContent) == "" {
		return CompletenessResult{}, errEmptySpec
	}

	var zeroTemp float64
	req := llm.GenerateRequest{
		SystemPrompt: scoringSystemPrompt,
		Messages:     []llm.Message{{Role: "user", Content: specContent}},
		Model:        s.model,
		MaxTokens:    4096,
		Temperature:  &zeroTemp,
		CacheControl: &llm.CacheControl{Type: "ephemeral"},
	}

	resp, err := s.client.Generate(ctx, req)
	if err != nil {
		return CompletenessResult{}, fmt.Errorf("scorer: %w", err)
	}

	cleaned := llm.ExtractJSON(resp.Content)
	var sr scoringResponse
	if err := json.Unmarshal([]byte(cleaned), &sr); err != nil {
		return CompletenessResult{}, fmt.Errorf("%w: %w", errMalformedResponse, err)
	}

	// Require exactly the known dimensions -- no more, no less.
	if len(sr.Dimensions) != len(dimensionOrder) {
		return CompletenessResult{}, errIncompleteDimensions
	}

	// Index response by name for exact lookup.
	idxByName := make(map[string]int, len(sr.Dimensions))
	for i, d := range sr.Dimensions {
		idxByName[d.Name] = i
	}

	// Iterate dimensionOrder (not a map) to guarantee stable output ordering.
	dimensions := make([]DimensionScore, 0, len(dimensionOrder))
	for _, dim := range dimensionOrder {
		idx, ok := idxByName[dim.name]
		if !ok {
			return CompletenessResult{}, errIncompleteDimensions
		}
		d := sr.Dimensions[idx]
		dimensions = append(dimensions, DimensionScore{
			Name:   dim.name,
			Score:  min(max(d.Score, 0), 100),
			Weight: dim.weight,
			Gaps:   d.Gaps,
		})
	}

	return CompletenessResult{
		Dimensions: dimensions,
		Overall:    computeOverall(dimensions),
		CostUSD:    resp.CostUSD,
	}, nil
}

func computeOverall(dimensions []DimensionScore) int {
	var sum float64
	for _, d := range dimensions {
		sum += float64(d.Score) * d.Weight
	}
	return int(math.Round(sum))
}
