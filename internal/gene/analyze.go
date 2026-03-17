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
**COMPONENTS** — When the codebase has clear module boundaries (distinct service layers, adapters, or domain objects), identify each as a named component using the format below. Omit this section entirely for simple single-package applications.

**COMPONENT: <name>**
Interface: <what this component exposes to callers>
Patterns: <key implementation patterns>
DependsOn: <comma-separated component names, or none>

Keep the guide under 800 words. Be specific — cite actual file paths, function names, and patterns from the source files provided. Do not include generic advice.`

// Analyze uses an LLM to extract coding patterns from scanned source files,
// producing a Gene guide for use in code generation prompts.
// guidance is optional free-text (or empty string) appended to the system prompt.
func Analyze(ctx context.Context, logger *slog.Logger, client llm.Client, model string, sourceDir string, scan ScanResult, guidance string) (Gene, error) {
	userMsg := buildAnalyzeUserMessage(sourceDir, scan)

	systemPrompt := extractionPrompt
	if guidance != "" {
		systemPrompt = extractionPrompt + "\n\nEXTRACTION GUIDANCE (from user):\n" + guidance
	}

	resp, err := client.Generate(ctx, llm.GenerateRequest{
		SystemPrompt: systemPrompt,
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
		Guidance:    guidance,
		TokenCount:  spec.EstimateTokens(guide),
		Components:  parseComponents(guide),
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

// parseComponents scans a gene guide for **COMPONENT: <name>** headers and extracts
// each component's Interface, Patterns, and DependsOn fields. Returns nil when no
// component headers are found. Component text is left in the guide (intentional; no
// downstream consumer of Components requires stripping it yet).
func parseComponents(guide string) []Component {
	var components []Component
	var current *Component
	inPatterns := false
	pendingField := ""

	for _, line := range strings.Split(guide, "\n") {
		trimmed := strings.TrimRight(line, " \t")

		if name, ok := parseComponentHeader(trimmed); ok {
			if current != nil {
				components = append(components, *current)
			}
			current = &Component{Name: name}
			inPatterns = false
			pendingField = ""
			continue
		}

		if current == nil {
			continue
		}

		if nowInPatterns, nowPending, matched := applyComponentField(current, trimmed); matched {
			inPatterns = nowInPatterns
			pendingField = nowPending
			continue
		}

		if pendingField != "" && trimmed != "" {
			applyPendingLine(current, pendingField, trimmed)
			pendingField = ""
			continue
		}

		if inPatterns && trimmed != "" {
			inPatterns = accumulatePattern(current, trimmed)
		}
	}

	if current != nil {
		components = append(components, *current)
	}

	if len(components) == 0 {
		return nil
	}
	return components
}

// applyPendingLine fills the field named by pendingField from the continuation line.
// A bold header (e.g. **BUILD**) is treated as a section boundary and skipped.
func applyPendingLine(c *Component, pendingField, trimmed string) {
	if strings.HasPrefix(trimmed, "**") {
		return
	}
	switch pendingField {
	case "interface":
		c.Interface = strings.TrimSpace(trimmed)
	case "dependson":
		c.DependsOn = parseDependsOn(strings.TrimSpace(trimmed))
	}
}

// accumulatePattern appends trimmed to c.Patterns and returns whether to remain in pattern-accumulation mode.
// A bold header signals the end of the components section.
func accumulatePattern(c *Component, trimmed string) (inPatterns bool) {
	// A bold header (e.g. **BUILD**) signals end of the components section;
	// stop accumulating to prevent non-component guide text leaking into Patterns.
	if strings.HasPrefix(trimmed, "**") {
		return false
	}
	if c.Patterns == "" {
		c.Patterns = trimmed
	} else {
		c.Patterns += "\n" + trimmed
	}
	return true
}

// parseComponentHeader returns the component name if line matches **COMPONENT: <name>**
// or markdown heading variants like ## COMPONENT: name or ### COMPONENT: name.
func parseComponentHeader(line string) (name string, ok bool) {
	// Strip leading # chars, whitespace, and optional bold markers to normalize
	// heading prefixes (## COMPONENT: / ### COMPONENT: / **COMPONENT:**).
	stripped := strings.TrimLeft(line, "#")
	stripped = strings.TrimLeft(stripped, " \t")
	stripped = strings.TrimPrefix(stripped, "**")

	after, found := strings.CutPrefix(stripped, "COMPONENT:")
	if !found {
		return "", false
	}
	return strings.TrimSpace(strings.TrimSuffix(after, "**")), true
}

// normalizeFieldLine strips markdown list and bold formatting from a field line
// so that applyComponentField can match plain field names regardless of LLM formatting.
// Handles two bold patterns: **Field**: value (bold name, colon outside) and
// **Field:** value (bold name with colon inside).
func normalizeFieldLine(s string) string {
	s = strings.TrimLeft(s, " \t")
	// Strip leading list markers: "- " or "* "
	if strings.HasPrefix(s, "- ") || strings.HasPrefix(s, "* ") {
		s = s[2:]
	}
	s = strings.TrimLeft(s, " \t")
	if strings.HasPrefix(s, "**") {
		s = s[2:]                             // remove opening **
		s = strings.Replace(s, "**:", ":", 1) // handles **Field**: → Field:
		s = strings.Replace(s, "**", "", 1)   // handles **Field:** → Field: (trailing ** gone)
	}
	return s
}

// applyComponentField updates c when line matches a known field prefix.
// Returns (inPatterns, pendingField, matched). pendingField is non-empty when the
// field value was absent (value expected on the next line).
func applyComponentField(c *Component, line string) (inPatterns bool, pendingField string, matched bool) {
	normalized := normalizeFieldLine(line)
	if after, found := strings.CutPrefix(normalized, "Interface:"); found {
		val := strings.TrimSpace(after)
		c.Interface = val
		if val == "" {
			return false, "interface", true
		}
		return false, "", true
	}
	if after, found := strings.CutPrefix(normalized, "Patterns:"); found {
		c.Patterns = strings.TrimSpace(after)
		return true, "", true
	}
	if after, found := strings.CutPrefix(normalized, "DependsOn:"); found {
		val := strings.TrimSpace(after)
		c.DependsOn = parseDependsOn(val)
		if val == "" {
			return false, "dependson", true
		}
		return false, "", true
	}
	return false, "", false
}

// parseDependsOn splits a comma-separated dependency list; returns nil for empty or "none".
func parseDependsOn(val string) []string {
	if val == "" || strings.EqualFold(val, "none") {
		return nil
	}
	parts := strings.Split(val, ",")
	deps := make([]string, 0, len(parts))
	for _, p := range parts {
		if dep := strings.TrimSpace(p); dep != "" {
			deps = append(deps, dep)
		}
	}
	if len(deps) == 0 {
		return nil
	}
	return deps
}

func buildAnalyzeUserMessage(sourceDir string, scan ScanResult) string {
	var b strings.Builder
	b.Grow(256 * len(scan.Files))
	fmt.Fprintf(&b, "Source directory: %s\n", sourceDir)
	fmt.Fprintf(&b, "Detected language: %s\n\n", scan.Language)

	for _, f := range scan.Files {
		fmt.Fprintf(&b, "=== FILE: %s (%s) ===\n%s\n=== END FILE ===\n\n", f.Path, f.Role, f.Content)
	}

	return b.String()
}
