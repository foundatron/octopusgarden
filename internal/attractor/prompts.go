package attractor

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/foundatron/octopusgarden/internal/gene"
	"github.com/foundatron/octopusgarden/internal/llm"
)

// Feedback kind constants replace bare strings for categorization.
const (
	feedbackBuildError  = "build_error"
	feedbackHealthError = "health_error"
	feedbackParseError  = "parse_error"
	feedbackRegression  = "regression"
	feedbackRunError    = "run_error"
	feedbackTestError   = "test_error"
	feedbackTruncation  = "truncation"
	feedbackValidation  = "validation"
)

const (
	// maxFeedbackBytes is the maximum size of a single feedback entry before truncation.
	// Increased to 12288 to accommodate detailed per-step scenario output; larger prompts
	// are a deliberate cost tradeoff for richer feedback that drives faster convergence.
	maxFeedbackBytes = 12288
	// maxFeedbackEntries is the number of recent feedback entries included in iteration prompts.
	maxFeedbackEntries = 3
)

// MaxObservedBytes is the maximum bytes of observed output included in per-step failure
// feedback. Longer output is truncated with a … suffix. This is the source of truth
// shared with cmd/octog, which uses it when building ValidateFn failure output.
// fidelityFull leaves observed lines at this bound; fidelityStandard re-truncates
// to observedStandardLimit.
const MaxObservedBytes = 2000

// StepPassPrefix and StepFailPrefix are the line prefixes used by cmd/octog when
// formatting per-step feedback and by filterFailureEntry when parsing those lines.
// Defining them here creates a shared format contract: changing the prefix in one
// place is automatically reflected in the other.
const (
	StepPassPrefix = "  ✓" // 2-space indent + check mark; identifies a passing step line
	StepFailPrefix = "  ✗" // 2-space indent + cross mark; identifies a failing step line
)

// feedbackHeader returns the human-readable header for a feedback kind.
// Unknown kinds fall back to the raw kind string uppercased.
func feedbackHeader(kind string) string {
	switch kind {
	case feedbackBuildError:
		return "BUILD FAILURE"
	case feedbackHealthError:
		return "HEALTH CHECK FAILURE"
	case feedbackParseError:
		return "PARSE ERROR"
	case feedbackRegression:
		return "REGRESSIONS"
	case feedbackRunError:
		return "RUN FAILURE"
	case feedbackTestError:
		return "TEST FAILURE"
	case feedbackTruncation:
		return "TRUNCATION"
	case feedbackValidation:
		return "VALIDATION FAILURES"
	default:
		return strings.ToUpper(kind)
	}
}

// feedbackFidelity controls how much validation detail is included in iteration prompts.
// Higher fidelity means more detail (more tokens, more signal). Zero is the unset value
// and means "use the default truncation limit" (non-validation entries).
type feedbackFidelity int

const (
	fidelityCompact  feedbackFidelity = 1 // scenario summaries only; iterations 1–2
	fidelityStandard feedbackFidelity = 2 // failing step detail, no passing steps; iterations 3–4
	fidelityFull     feedbackFidelity = 3 // all step detail; iterations 5+
)

// compactMaxIter and standardMaxIter define the iteration boundaries for fidelity selection.
// Iterations 1..compactMaxIter use compact fidelity; iterations compactMaxIter+1..standardMaxIter
// use standard fidelity; later iterations use full fidelity.
const (
	compactMaxIter  = 2
	standardMaxIter = 4
)

// determineFidelity returns the appropriate feedback fidelity for the given iteration
// and stall count. Early iterations use compact fidelity to keep prompt cost low;
// stalls escalate one level to provide more debugging signal.
func determineFidelity(iteration, stallCount int) feedbackFidelity {
	var f feedbackFidelity
	switch {
	case iteration <= compactMaxIter:
		f = fidelityCompact
	case iteration <= standardMaxIter:
		f = fidelityStandard
	default:
		f = fidelityFull
	}
	if stallCount >= 2 && f < fidelityFull {
		f++
	}
	return f
}

// maxFeedbackForFidelity returns the maximum bytes for a validation feedback entry
// at the given fidelity level.
func maxFeedbackForFidelity(f feedbackFidelity) int {
	switch f {
	case fidelityCompact:
		return 4096
	case fidelityStandard:
		return 12288
	default: // fidelityFull
		return 24576
	}
}

// iterationFeedback captures what happened in one iteration for building the next prompt.
type iterationFeedback struct {
	iteration       int
	kind            string
	message         string
	fidelity        feedbackFidelity   // set for feedbackValidation entries; zero for others
	failedScenarios map[string]float64 // populated for feedbackValidation entries; nil otherwise
}

const systemPromptPrefix = `You are a code generation agent. Your task is to generate a complete, working application based on the following specification.

SPECIFICATION:
`

// genericDepRules contains dependency rules that apply regardless of language.
// Language-specific rules are appended from the LanguageTemplate when a language is specified.
const genericDepRules = `

DEPENDENCY RULES:
- ALWAYS prefer standard library over third-party dependencies
- NEVER generate lock files or checksum files (go.sum, package-lock.json, yarn.lock, Cargo.lock, etc.) — you cannot compute valid hashes; the build will fail
- Let the package manager resolve and verify dependencies at build time`

// geneSectionHeader introduces the gene section in the system prompt.
// The spec-takes-precedence note ensures holdout isolation semantics.
const geneSectionHeader = `

PROVEN PATTERNS (extracted from a working exemplar — synthesize equivalent behavior
adapted to the specification above. Preserve the structural approach and invariants.
The SPECIFICATION always takes precedence over these patterns on any conflict):

`

// buildSystemPrompt creates the system prompt containing the spec.
// This prompt is cached across iterations via CacheControl: ephemeral.
// The suffix is selected based on scenario capabilities and optional language.
// When language is "" (auto), no language-specific examples or dep rules are emitted.
// genes and geneLanguage are optional; when genes is empty, no gene section is emitted.
func buildSystemPrompt(spec string, caps ScenarioCapabilities, language, genes, geneLanguage string) string {
	var b strings.Builder
	b.WriteString(systemPromptPrefix)
	b.WriteString(spec)
	if genes != "" {
		b.WriteString(buildGeneSection(genes, language, geneLanguage))
	}
	b.WriteString(buildCapabilitySuffix(caps, language))
	b.WriteString(buildDepRules(language))
	return b.String()
}

// buildGeneSection formats the gene guide text for inclusion in the system prompt.
// When geneLanguage differs from language, a cross-language adaptation note is appended
// after the gene content, instructing the LLM to preserve invariants while using
// idiomatic constructs in the target language.
func buildGeneSection(genes, language, geneLanguage string) string {
	var b strings.Builder
	b.WriteString(geneSectionHeader)
	b.WriteString(genes)

	if geneLanguage != "" && language != "" && geneLanguage != language {
		sourceName := languageDisplayName(geneLanguage)
		targetName := languageDisplayName(language)
		fmt.Fprintf(&b, "\n\nCROSS-LANGUAGE NOTE: The exemplar above is written in %s. "+
			"You are generating %s. Preserve the invariants, structural patterns, and "+
			"architectural approach while using idiomatic %s constructs, libraries, and conventions.",
			sourceName, targetName, targetName)
	}

	return b.String()
}

// languageDisplayName returns the human-readable display name for a language key.
// Known languages use LanguageTemplate.Name (e.g. "go" → "Go", "node" → "Node.js").
// Unknown languages fall back to the raw key string.
func languageDisplayName(lang string) string {
	if tmpl, ok := LookupLanguage(lang); ok {
		return tmpl.Name
	}
	return lang
}

// buildCapabilitySuffix assembles the instruction text for a given capability set.
// When a language is specified, a concrete example block is appended.
func buildCapabilitySuffix(caps ScenarioCapabilities, language string) string {
	var b strings.Builder
	b.WriteString("\n\nINSTRUCTIONS:\n")
	b.WriteString(capabilityInstructions(caps))
	b.WriteString("\n- Output each file in this exact format:\n\n")
	b.WriteString("=== FILE: path/to/file ===\nfile content here\n=== END FILE ===\n")

	// Append language-specific example if a language is specified.
	if tmpl, ok := LookupLanguage(language); ok {
		b.WriteString(buildLanguageExample(tmpl, caps))
	}

	b.WriteString("\n- Generate ONLY the file blocks, minimize explanatory text\n")
	b.WriteString(capabilityTrailingInstructions(caps))
	return b.String()
}

// wsSupplementaryInstruction returns the extra line appended when WS is needed.
// It is injected after the primary HTTP instruction, not as a new switch case.
func wsSupplementaryInstruction(caps ScenarioCapabilities) string {
	if caps.NeedsWS {
		return "\n- The HTTP server must also handle WebSocket upgrade requests on port 8080"
	}
	return ""
}

// capabilityInstructions returns the core instruction text for the given capabilities.
func capabilityInstructions(caps ScenarioCapabilities) string {
	needsHTTP := caps.NeedsHTTP || caps.NeedsBrowser
	ws := wsSupplementaryInstruction(caps)
	switch {
	case needsHTTP && caps.NeedsGRPC:
		return `- Generate ALL files needed for a working application that serves both HTTP and gRPC
- Include a Dockerfile that builds and runs the application
- The application MUST listen on port 8080 for HTTP requests
- The application MUST serve gRPC on port 50051
- The gRPC server MUST enable server reflection so clients can discover services at runtime
- Include .proto files defining the service and compile them as part of the Docker build` + ws
	case caps.NeedsExec && caps.NeedsGRPC:
		return `- Generate ALL files needed for a working application that serves both as a CLI tool and a gRPC server
- Include a Dockerfile that builds the application and installs it in PATH
- The application MUST serve gRPC on port 50051
- The gRPC server MUST enable server reflection so clients can discover services at runtime
- The application must also support command-line invocation for CLI operations
- Include .proto files defining the service and compile them as part of the Docker build`
	case caps.NeedsGRPC:
		return `- Generate ALL files needed for a working gRPC application
- Include a Dockerfile that builds and runs the application with a gRPC server on port 50051
- The gRPC server MUST enable server reflection so clients can discover services at runtime
- Include .proto files defining the service and compile them as part of the Docker build
- Install protoc and any language-specific protobuf/gRPC compiler plugins in the Dockerfile`
	case needsHTTP && caps.NeedsExec:
		return `- Generate ALL files needed for a working application that serves both as an HTTP server AND a command-line tool
- Include a Dockerfile that builds the application and installs it in PATH
- The application MUST listen on port 8080 for HTTP requests
- The application must also support command-line invocation for CLI operations` + ws
	case caps.NeedsExec:
		return `- Generate ALL files needed for a working command-line application
- Include a Dockerfile that builds the application. The built binary must be available in PATH inside the container.
- Do NOT start a server or listen on any port. The application is a CLI tool invoked via command-line arguments.`
	default:
		return `- Generate ALL files needed for a working application
- Include a Dockerfile that builds and runs the application on port 8080` + ws
	}
}

// capabilityTrailingInstructions returns the closing instructions specific to each capability mode.
func capabilityTrailingInstructions(caps ScenarioCapabilities) string {
	needsHTTP := caps.NeedsHTTP || caps.NeedsBrowser
	switch {
	case needsHTTP && caps.NeedsGRPC:
		return "- Include all .proto files and configuration files"
	case caps.NeedsExec && caps.NeedsGRPC:
		return "- Include all .proto files and configuration files"
	case caps.NeedsGRPC:
		return "- The application MUST serve gRPC on port 50051\n- Include all .proto files and configuration files"
	case needsHTTP && caps.NeedsExec:
		return "- Include all dependencies and configuration files"
	case caps.NeedsExec:
		return "- The Dockerfile must install the binary to a PATH location (e.g. /usr/local/bin/)\n- Include all dependencies and configuration files"
	default:
		return "- The application MUST listen on port 8080\n- Include all dependencies and configuration files"
	}
}

// buildLanguageExample creates the EXAMPLE block for a specific language and capability.
func buildLanguageExample(tmpl LanguageTemplate, caps ScenarioCapabilities) string {
	needsHTTP := caps.NeedsHTTP || caps.NeedsBrowser

	// For combined capabilities without clear examples, skip the example block.
	switch {
	case needsHTTP && caps.NeedsGRPC,
		caps.NeedsExec && caps.NeedsGRPC,
		needsHTTP && caps.NeedsExec:
		return ""
	}

	var ex ExampleBlock
	switch {
	case caps.NeedsGRPC:
		// gRPC gets a proto example + Dockerfile with gRPC setup, not the standard example.
		return buildGRPCExample(tmpl)
	case caps.NeedsExec:
		ex = tmpl.CLIExample
	default:
		ex = tmpl.HTTPExample
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\nEXAMPLE (showing correct format with two files):\n\n")
	fmt.Fprintf(&b, "=== FILE: %s ===\n%s\n=== END FILE ===\n", ex.EntryFile, ex.EntryContent)
	fmt.Fprintf(&b, "=== FILE: Dockerfile ===\n%s\n=== END FILE ===\n", ex.Dockerfile)
	return b.String()
}

// buildGRPCExample creates a gRPC-specific example block showing proto + Dockerfile structure.
func buildGRPCExample(tmpl LanguageTemplate) string {
	var b strings.Builder
	b.WriteString("\nEXAMPLE (showing correct format with two files):\n\n")
	b.WriteString(`=== FILE: proto/service.proto ===
syntax = "proto3";
package example;

service ExampleService {
  rpc GetItem (GetItemRequest) returns (Item);
}
message GetItemRequest { string id = 1; }
message Item { string id = 1; string name = 2; }
=== END FILE ===
`)
	fmt.Fprintf(&b, "=== FILE: Dockerfile ===\nFROM %s\n%s\nWORKDIR /app\nCOPY . .\nRUN <compile .proto files to generate stubs>\nRUN <install dependencies and build the application>\nCMD [\"./server\"]\n=== END FILE ===\n", tmpl.BaseImage, tmpl.GRPCSetup)
	return b.String()
}

// buildComponentPrompt creates the system prompt for component-scoped generation.
// The spec is placed first (with cache_control: ephemeral on the system message) so that
// the shared prefix is cacheable across components. Component-specific content follows.
func buildComponentPrompt(spec string, component gene.Component, depInterfaces map[string]string, caps ScenarioCapabilities, language string) string {
	var b strings.Builder
	b.WriteString(systemPromptPrefix)
	b.WriteString(spec)

	b.WriteString("\n\nCOMPONENT CONTRACT:\n")
	b.WriteString(component.Interface)

	if component.Patterns != "" {
		b.WriteString("\n\nCOMPONENT PATTERNS:\n")
		b.WriteString(component.Patterns)
	}

	// Only include interfaces for declared dependencies, not all accumulated interfaces.
	if len(component.DependsOn) > 0 && len(depInterfaces) > 0 {
		var depSection strings.Builder
		depNames := slices.Sorted(slices.Values(component.DependsOn))
		for _, name := range depNames {
			if iface, ok := depInterfaces[name]; ok {
				fmt.Fprintf(&depSection, "\n--- %s ---\n%s\n", name, iface)
			}
		}
		if depSection.Len() > 0 {
			b.WriteString("\n\nDEPENDENCY INTERFACES:\n")
			b.WriteString(depSection.String())
		}
	}

	b.WriteString(buildCapabilitySuffix(caps, language))
	b.WriteString(buildDepRules(language))
	return b.String()
}

// buildDepRules returns dependency rules: generic rules plus language-specific rules if applicable.
func buildDepRules(language string) string {
	if tmpl, ok := LookupLanguage(language); ok {
		return genericDepRules + "\n" + tmpl.DepRules
	}
	return genericDepRules
}

// buildMessages constructs the user message for the current iteration.
// Iteration 1 gets a simple "Generate" prompt; subsequent iterations include
// the last 3 failure summaries with categorized headers.
func buildMessages(iter int, history []iterationFeedback) []llm.Message {
	if iter == 1 || len(history) == 0 {
		return []llm.Message{
			{Role: "user", Content: "Generate the application according to the specification. Output all files using the === FILE: path === format."},
		}
	}

	var b strings.Builder
	b.WriteString("The previous attempt did not fully satisfy the specification. Here is the feedback:\n\n")

	// Inject steering for scenarios stalling across consecutive iterations (full history).
	if steeringText := buildSteeringText(history); steeringText != "" {
		b.WriteString(steeringText)
		b.WriteString("\n")
	}

	// Include last 3 feedback entries with categorized headers.
	start := max(len(history)-maxFeedbackEntries, 0)
	writeCategorizedFeedback(&b, history[start:])

	b.WriteString("Please generate a corrected version of the application. Output ALL files using the === FILE: path === format.")

	return []llm.Message{
		{Role: "user", Content: b.String()},
	}
}

// buildPatchMessages constructs the user message for patch mode iterations.
// It includes the previous best files as context and the most recent failures,
// asking the LLM to output only changed files. omittedCount is the number of
// files excluded from bestFiles by triage; when > 0 a note is appended after
// the file blocks.
func buildPatchMessages(history []iterationFeedback, bestFiles map[string]string, bestScore float64, omittedCount int) []llm.Message {
	var b strings.Builder
	fmt.Fprintf(&b, "The current best version scored %.1f/100. Here are all current files:\n\n", bestScore)

	paths := slices.Sorted(maps.Keys(bestFiles))
	for _, p := range paths {
		fmt.Fprintf(&b, "=== FILE: %s ===\n%s=== END FILE ===\n\n", p, bestFiles[p])
	}

	if omittedCount > 0 {
		fmt.Fprintf(&b, "(%d other files not relevant to current failures, not shown)\n\n", omittedCount)
	}

	// Inject steering for scenarios stalling across consecutive iterations (full history).
	if steeringText := buildSteeringText(history); steeringText != "" {
		b.WriteString(steeringText)
		b.WriteString("\n")
	}

	if len(history) > 0 {
		b.WriteString("Failures to fix:\n\n")
		start := max(len(history)-maxFeedbackEntries, 0)
		writeCategorizedFeedback(&b, history[start:])
	}

	b.WriteString("Output ONLY the files that need to change using the === FILE: path === format. ")
	b.WriteString("For files you are not changing, you may emit === UNCHANGED: path === as a comment, but this is optional.")

	return []llm.Message{
		{Role: "user", Content: b.String()},
	}
}

// writeCategorizedFeedback formats feedback entries with human-readable headers.
// Each entry is formatted as: HEADER (iteration N):\nmessage\n\n
// Unknown kinds fall back to the raw kind string uppercased.
// Validation entries use a fidelity-aware byte limit; all others use maxFeedbackBytes.
func writeCategorizedFeedback(b *strings.Builder, entries []iterationFeedback) {
	for _, fb := range entries {
		limit := maxFeedbackBytes
		if fb.fidelity != 0 {
			limit = maxFeedbackForFidelity(fb.fidelity)
		}
		fmt.Fprintf(b, "%s (iteration %d):\n%s\n\n", feedbackHeader(fb.kind), fb.iteration, truncateFeedback(fb.message, limit))
	}
}

// trimUTF8Boundary trims s to at most limit bytes, removing any incomplete UTF-8
// rune at the cut point. Returns the trimmed string without any suffix marker.
func trimUTF8Boundary(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	trimmed := s[:limit]
	for len(trimmed) > 0 {
		r, size := utf8.DecodeLastRuneInString(trimmed)
		if r != utf8.RuneError || size != 1 {
			break
		}
		trimmed = trimmed[:len(trimmed)-1]
	}
	return trimmed
}

// truncateFeedback truncates a feedback message if it exceeds limit bytes.
// Truncation removes any incomplete UTF-8 rune at the cut point.
func truncateFeedback(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return trimUTF8Boundary(s, limit) + "\n[truncated]"
}

// formatValidationFeedback formats validation results into feedback text for the LLM.
// Fidelity controls how much per-scenario detail is included:
//   - compact: scenario summary lines only (cheapest)
//   - standard: failing step detail, observed truncated to 500 bytes; no passing step lines
//   - full: all step detail, observed truncated to 2000 bytes
func formatValidationFeedback(satisfaction float64, failures []string, fidelity feedbackFidelity) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Satisfaction score: %.1f/100\n", satisfaction)
	if len(failures) > 0 {
		b.WriteString("Scenario results:\n")
		for _, f := range failures {
			fmt.Fprintf(&b, "%s\n", filterFailureEntry(f, fidelity))
		}
	}
	return b.String()
}

// observedStandardLimit is the observed content byte limit for fidelityStandard.
// fidelityFull uses maxObservedBytes (2000), but we cannot import it from cmd/octog;
// the observed content in failure strings is already bounded by cmd/octog's truncateObserved,
// so fidelityFull leaves lines as-is and only fidelityStandard needs re-truncation here.
const observedStandardLimit = 500

// filterFailureEntry applies fidelity-based line filtering to a single failure entry string.
// compact: keep only scenario summary lines (0-indent); strip all indented lines.
// standard: keep failing step detail; strip passing step lines and their sub-detail;
//
//	truncate Observed content to observedStandardLimit.
//
// full: keep everything as-is.
func filterFailureEntry(entry string, fidelity feedbackFidelity) string {
	if fidelity == fidelityFull {
		return entry
	}
	lines := strings.Split(entry, "\n")
	out := make([]string, 0, len(lines))
	inPassingStep := false
	for _, line := range lines {
		if !strings.HasPrefix(line, " ") {
			// Scenario summary line (0-indent) — always keep.
			out = append(out, line)
			inPassingStep = false
			continue
		}
		if fidelity == fidelityCompact {
			continue // strip all indented lines
		}
		// fidelityStandard: keep failing step detail; strip passing step lines.
		if strings.HasPrefix(line, StepPassPrefix) {
			inPassingStep = true
			continue
		}
		if strings.HasPrefix(line, StepFailPrefix) {
			inPassingStep = false
		}
		if inPassingStep {
			continue // sub-detail of a passing step
		}
		if strings.HasPrefix(line, "    Observed") {
			line = truncateObservedLine(line, observedStandardLimit)
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// truncateObservedLine truncates the content portion of an "    Observed: ..." line to
// maxBytes, removing any incomplete UTF-8 rune at the cut and appending a … suffix.
func truncateObservedLine(line string, maxBytes int) string {
	colonIdx := strings.Index(line, ": ")
	if colonIdx < 0 {
		return line
	}
	content := line[colonIdx+2:]
	if len(content) <= maxBytes {
		return line
	}
	return line[:colonIdx+2] + trimUTF8Boundary(content, maxBytes) + "…"
}

// extractFailureStrings pulls failure messages from history for section matching.
func extractFailureStrings(history []iterationFeedback) []string {
	failures := make([]string, 0, len(history))
	for _, fb := range history {
		if fb.message != "" {
			failures = append(failures, fb.message)
		}
	}
	return failures
}

// failScenarioPrefix is the prefix that identifies a failing scenario summary line.
// FormatScenarioFailureLine produces lines with this prefix;
// parseFailedScenarios expects it. Both must agree on this constant.
const failScenarioPrefix = "✗ "

// FormatScenarioFailureLine returns the first-line summary of a failing scenario
// in the canonical format "✗ id (score/100)" that parseFailedScenarios can parse.
// cmd/octog uses this when building ValidateFn failure output so that the formatter
// and the parser share a single definition — changing one without the other is caught
// by TestScenarioFormatRoundTrip.
func FormatScenarioFailureLine(id string, score float64) string {
	return fmt.Sprintf("%s%s (%.0f/100)", failScenarioPrefix, id, score)
}

// parseFailedScenarios parses the mixed pass/fail slice returned by ValidateFn into a
// map of scenario ID → score for failing scenarios only.
//
// Each element is a full scenario result string (possibly multi-line). Passing entries
// start with "✓"; failing entries start with the canonical failScenarioPrefix on the
// first line, as produced by FormatScenarioFailureLine in cmd/octog.
// Indented sub-lines (step detail) are part of the same element and are ignored here.
func parseFailedScenarios(failures []string) map[string]float64 {
	var result map[string]float64
	for _, entry := range failures {
		// Take only the first line of each entry (sub-lines are step detail).
		firstLine, _, _ := strings.Cut(entry, "\n")
		firstLine = strings.TrimSpace(firstLine)

		// Parse the line first; malformed or unrecognized entries are skipped.
		id, score, ok := parseScenarioLine(firstLine)
		if !ok {
			continue
		}
		// Only collect failing scenarios (✗ prefix); skip passing ones (✓ prefix).
		if !strings.HasPrefix(firstLine, failScenarioPrefix) {
			continue
		}
		if result == nil {
			result = make(map[string]float64)
		}
		result[id] = score
	}
	return result
}

// buildSteeringText inspects the full iteration history and returns a steering notice
// for scenarios that have failed in 2 or more consecutive validation iterations.
// Non-validation entries (build/health/parse/run errors) do not break failure streaks.
// Returns an empty string when no stalling scenarios are detected.
func buildSteeringText(history []iterationFeedback) string {
	streaks := make(map[string]int)
	scoreHistory := make(map[string][]float64)

	for _, fb := range history {
		if fb.kind != feedbackValidation {
			continue
		}
		// Reset streaks for scenarios that did not fail in this validation entry.
		for id := range streaks {
			if _, failed := fb.failedScenarios[id]; !failed {
				delete(streaks, id)
				delete(scoreHistory, id)
			}
		}
		// Increment streaks for scenarios that failed in this entry.
		for id, score := range fb.failedScenarios {
			streaks[id]++
			scoreHistory[id] = append(scoreHistory[id], score)
		}
	}

	// Collect scenarios stalling across 2+ consecutive iterations.
	stalling := make([]string, 0, len(streaks))
	for id, streak := range streaks {
		if streak >= 2 {
			stalling = append(stalling, id)
		}
	}
	if len(stalling) == 0 {
		return ""
	}
	slices.Sort(stalling)

	var b strings.Builder
	b.WriteString("STALL NOTICE: The following scenario(s) have failed in multiple consecutive attempts. ")
	b.WriteString("Try a fundamentally different implementation approach for each:\n")
	for _, id := range stalling {
		fmt.Fprintf(&b, "- %s (score: %s/100, failing %d iterations in a row)\n", id, formatScoreTrajectory(scoreHistory[id]), streaks[id])
	}
	return b.String()
}

// minimalismThreshold is the score above which the minimalism suffix is injected.
// When the previous iteration scored above this level, the LLM is instructed to
// implement the SMALLEST possible fix rather than adding new complexity.
const minimalismThreshold = 80.0

// buildMinimalismSuffix returns an instruction suffix that discourages over-engineering
// when the solution is already scoring above minimalismThreshold. Returns "" when
// failedScenarios is nil or empty (no suffix needed).
func buildMinimalismSuffix(score float64, failedScenarios map[string]float64) string {
	if len(failedScenarios) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nMINIMALISM REQUIREMENT: Current score is %.0f%%. Implement the SMALLEST possible change to fix these failing scenarios:\n", score)
	names := slices.Sorted(maps.Keys(failedScenarios))
	for _, name := range names {
		fmt.Fprintf(&b, "- %s: %.0f%%\n", name, failedScenarios[name])
	}
	return b.String()
}

// buildAgenticSystemPrompt creates the system prompt for agentic (tool-use) code generation.
// Structurally similar to buildSystemPrompt but replaces the file-format suffix with
// tool-use instructions via buildAgenticCapabilitySuffix.
func buildAgenticSystemPrompt(spec string, caps ScenarioCapabilities, language, genes, geneLanguage string) string {
	var b strings.Builder
	b.WriteString(systemPromptPrefix)
	b.WriteString(spec)
	if genes != "" {
		b.WriteString(buildGeneSection(genes, language, geneLanguage))
	}
	b.WriteString(buildAgenticCapabilitySuffix(caps, language))
	b.WriteString(buildDepRules(language))
	return b.String()
}

// buildAgenticCapabilitySuffix assembles instruction text for agentic tool-use mode.
// Uses the same capability instructions as buildCapabilitySuffix but replaces the
// === FILE: === format instructions with write_file tool-use instructions.
// No language example block is included.
func buildAgenticCapabilitySuffix(caps ScenarioCapabilities, language string) string {
	var b strings.Builder
	b.WriteString("\n\nINSTRUCTIONS:\n")
	b.WriteString(capabilityInstructions(caps))
	b.WriteString("\n- Use the write_file tool to create each file in the workspace")
	if _, ok := LookupLanguage(language); ok {
		b.WriteString("\n- Use read_file to inspect existing files and list_files to see what is present")
	}
	b.WriteString("\n- Write ALL required files; do not skip any file needed for a working application\n")
	b.WriteString("- Minimize explanatory text; focus on writing the files\n")
	b.WriteString(capabilityTrailingInstructions(caps))
	return b.String()
}

// buildAgenticMessages constructs the user message for agentic generation.
// Iteration 1 gets a simple "Generate" prompt using tool calls.
// Subsequent iterations include failure feedback.
func buildAgenticMessages(iter int, history []iterationFeedback) []llm.Message {
	if iter == 1 || len(history) == 0 {
		return []llm.Message{
			{Role: "user", Content: "Generate the application according to the specification. Use the write_file tool to create each file."},
		}
	}

	var b strings.Builder
	b.WriteString("The previous attempt did not fully satisfy the specification. Here is the feedback:\n\n")

	if steeringText := buildSteeringText(history); steeringText != "" {
		b.WriteString(steeringText)
		b.WriteString("\n")
	}

	start := max(len(history)-maxFeedbackEntries, 0)
	writeCategorizedFeedback(&b, history[start:])

	b.WriteString("Please generate a corrected version of the application. Use the write_file tool to create ALL required files.")

	return []llm.Message{
		{Role: "user", Content: b.String()},
	}
}

// buildAgenticPatchMessages constructs the user message for agentic patch mode.
// Lists current files as paths only (agent can read_file to inspect content).
// Includes failure feedback and asks the agent to use write_file to update files.
func buildAgenticPatchMessages(history []iterationFeedback, bestFiles map[string]string, bestScore float64) []llm.Message {
	var b strings.Builder
	fmt.Fprintf(&b, "The current best version scored %.1f/100. Current files in the workspace:\n\n", bestScore)

	paths := slices.Sorted(maps.Keys(bestFiles))
	for _, p := range paths {
		fmt.Fprintf(&b, "- %s\n", p)
	}
	b.WriteString("\nUse read_file to inspect any file you need to review before making changes.\n\n")

	if steeringText := buildSteeringText(history); steeringText != "" {
		b.WriteString(steeringText)
		b.WriteString("\n")
	}

	if len(history) > 0 {
		b.WriteString("Here is the feedback:\n\n")
		start := max(len(history)-maxFeedbackEntries, 0)
		writeCategorizedFeedback(&b, history[start:])
	}

	b.WriteString("Use the write_file tool to write any files that need to change. ")
	b.WriteString("You only need to write files that require modification; unchanged files are already present.")

	return []llm.Message{
		{Role: "user", Content: b.String()},
	}
}

// formatScoreTrajectory formats a slice of scores as "50 → 45 → 40".
func formatScoreTrajectory(scores []float64) string {
	parts := make([]string, len(scores))
	for i, s := range scores {
		parts[i] = fmt.Sprintf("%.0f", s)
	}
	return strings.Join(parts, " → ")
}
