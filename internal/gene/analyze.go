package gene

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/foundatron/octopusgarden/internal/llm"
	"github.com/foundatron/octopusgarden/internal/spec"
)

var errEmptyExtraction = errors.New("gene: LLM returned empty extraction")

const extractionPrompt = `You are a senior software architect analyzing an exemplar codebase.

Extract the key patterns, conventions, and architectural decisions that a code generator should follow when building new software in this language/stack.

Respond with a concise guide covering these sections:

**PATTERN** — Primary architectural pattern (e.g. layered MVC, hexagonal, serverless).
**INVARIANTS** — Hard rules the codebase always follows (naming, error handling, auth, validation).
**EDGE CASES** — How the code handles errors, timeouts, retries, missing data, and concurrency.
**STACK** — Language version, framework, key libraries, and why they were chosen.
**STRUCTURE** — Directory layout and what goes where.
**BOOT** — How the application starts: entry point, config loading, dependency wiring, server listen.
**BUILD** — Build tool, Dockerfile strategy, CI commands, and how to run locally.

Keep the guide under 800 words. Be specific — cite actual file paths, function names, and patterns from the source files provided. Do not include generic advice.`

// Analyze uses an LLM to extract coding patterns from scanned source files,
// producing a Gene guide for use in code generation prompts.
func Analyze(ctx context.Context, logger *slog.Logger, client llm.Client, model string, sourceDir string, scan ScanResult) (Gene, error) {
	userMsg := buildAnalyzeUserMessage(sourceDir, scan)

	resp, err := client.Generate(ctx, llm.GenerateRequest{
		SystemPrompt: extractionPrompt,
		Messages:     []llm.Message{{Role: "user", Content: userMsg}},
		Model:        model,
		CacheControl: &llm.CacheControl{Type: "ephemeral"},
	})
	if err != nil {
		return Gene{}, fmt.Errorf("gene analyze: %w", err)
	}

	guide := strings.TrimSpace(resp.Content)
	if guide == "" {
		return Gene{}, errEmptyExtraction
	}

	g := Gene{
		Version:     1,
		Source:      sourceDir,
		Language:    scan.Language,
		ExtractedAt: time.Now(),
		Guide:       guide,
		TokenCount:  spec.EstimateTokens(guide),
	}

	logger.Info("gene extraction",
		"model", model,
		"input_tokens", resp.InputTokens,
		"output_tokens", resp.OutputTokens,
		"cost_usd", resp.CostUSD,
		"guide_tokens", g.TokenCount,
	)

	return g, nil
}

func buildAnalyzeUserMessage(sourceDir string, scan ScanResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Source directory: %s\n", sourceDir)
	fmt.Fprintf(&b, "Detected language: %s\n\n", scan.Language)

	for _, f := range scan.Files {
		fmt.Fprintf(&b, "=== FILE: %s (%s) ===\n%s\n=== END FILE ===\n\n", f.Path, f.Role, f.Content)
	}

	return b.String()
}
