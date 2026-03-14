package attractor

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/foundatron/octopusgarden/internal/llm"
)

// entryPointNames is the set of well-known entry point file base names (lower-cased)
// that are always included in triage results regardless of LLM output.
var entryPointNames = map[string]struct{}{
	"dockerfile":       {},
	"main.go":          {},
	"main.py":          {},
	"main.rs":          {},
	"app.py":           {},
	"app.js":           {},
	"server.js":        {},
	"index.js":         {},
	"index.ts":         {},
	"go.mod":           {},
	"cargo.toml":       {},
	"package.json":     {},
	"requirements.txt": {},
}

// isEntryPoint reports whether path is a well-known entry point file.
func isEntryPoint(path string) bool {
	_, ok := entryPointNames[strings.ToLower(filepath.Base(path))]
	return ok
}

// triageFiles asks the LLM to identify which files from allFiles are relevant to
// the given failures. It returns a filtered map containing only the relevant files
// plus any entry-point files present in allFiles.
//
// Skip conditions (returns allFiles, 0 with no LLM call):
//   - len(allFiles) <= 5
//   - len(failures) == 0
//
// On any error, logs a warning and returns allFiles, 0.
func (a *Attractor) triageFiles(ctx context.Context, allFiles map[string]string, failures []string, model string) (map[string]string, float64) {
	if len(allFiles) <= 5 || len(failures) == 0 {
		return allFiles, 0
	}

	// Build sorted file path list for deterministic prompts.
	paths := slices.Sorted(maps.Keys(allFiles))

	var sb strings.Builder
	sb.WriteString("Files in the current codebase:\n")
	for _, p := range paths {
		sb.WriteString(p)
		sb.WriteString("\n")
	}
	sb.WriteString("\nFailures to fix:\n")
	for _, f := range failures {
		sb.WriteString(f)
		sb.WriteString("\n")
	}
	sb.WriteString("\nReturn ONLY a JSON array of file paths (strings) that are most relevant to fixing these failures, no other text. Include only paths from the list above.")

	resp, err := a.llm.Generate(ctx, llm.GenerateRequest{
		SystemPrompt: "You are a code triage assistant. Respond with a raw JSON array only — no markdown, no explanation.",
		Messages:     []llm.Message{{Role: "user", Content: sb.String()}},
		Model:        model,
		MaxTokens:    1024,
	})
	if err != nil {
		slog.Warn("triage: LLM call failed, using all files", "error", err)
		return allFiles, 0
	}

	cleaned := llm.ExtractJSON(resp.Content)
	var suggested []string
	if jsonErr := json.Unmarshal([]byte(cleaned), &suggested); jsonErr != nil {
		slog.Warn("triage: failed to parse LLM response, using all files", "error", jsonErr)
		return allFiles, resp.CostUSD
	}

	// Build result: LLM-suggested paths that actually exist + entry points from allFiles.
	result := make(map[string]string)
	for _, p := range suggested {
		if content, ok := allFiles[p]; ok {
			result[p] = content
		}
	}
	// Always include entry points present in allFiles.
	for p, content := range allFiles {
		if isEntryPoint(p) {
			result[p] = content
		}
	}

	// Guard: if result is empty (LLM returned nothing useful), fall back to all files.
	if len(result) == 0 {
		slog.Warn("triage: result set empty after filtering, using all files")
		return allFiles, resp.CostUSD
	}

	slog.Debug("triage: file set narrowed", "suggested", len(suggested), "result", len(result), "total", len(allFiles))
	return result, resp.CostUSD
}
