package attractor

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/foundatron/octopusgarden/internal/llm"
)

// Feedback kind constants replace bare strings for categorization.
const (
	feedbackBuildError  = "build_error"
	feedbackHealthError = "health_error"
	feedbackParseError  = "parse_error"
	feedbackRunError    = "run_error"
	feedbackValidation  = "validation"
)

const (
	// maxFeedbackBytes is the maximum size of a single feedback entry before truncation.
	maxFeedbackBytes = 4096
	// maxFeedbackEntries is the number of recent feedback entries included in iteration prompts.
	maxFeedbackEntries = 3
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
	case feedbackRunError:
		return "RUN FAILURE"
	case feedbackValidation:
		return "VALIDATION FAILURES"
	default:
		return strings.ToUpper(kind)
	}
}

// iterationFeedback captures what happened in one iteration for building the next prompt.
type iterationFeedback struct {
	iteration int
	kind      string
	message   string
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
// asking the LLM to output only changed files.
func buildPatchMessages(history []iterationFeedback, bestFiles map[string]string, bestScore float64) []llm.Message {
	var b strings.Builder
	fmt.Fprintf(&b, "The current best version scored %.1f/100. Here are all current files:\n\n", bestScore)

	paths := slices.Sorted(maps.Keys(bestFiles))
	for _, p := range paths {
		fmt.Fprintf(&b, "=== FILE: %s ===\n%s=== END FILE ===\n\n", p, bestFiles[p])
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
func writeCategorizedFeedback(b *strings.Builder, entries []iterationFeedback) {
	for _, fb := range entries {
		fmt.Fprintf(b, "%s (iteration %d):\n%s\n\n", feedbackHeader(fb.kind), fb.iteration, truncateFeedback(fb.message))
	}
}

// truncateFeedback truncates a feedback message if it exceeds maxFeedbackBytes.
// Truncation removes any incomplete UTF-8 rune at the cut point.
func truncateFeedback(s string) string {
	if len(s) <= maxFeedbackBytes {
		return s
	}
	truncated := s[:maxFeedbackBytes]
	// Remove any incomplete UTF-8 sequence at the end.
	for len(truncated) > 0 {
		r, size := utf8.DecodeLastRuneInString(truncated)
		if r != utf8.RuneError || size != 1 {
			break
		}
		truncated = truncated[:len(truncated)-1]
	}
	return truncated + "\n[truncated]"
}

// formatValidationFeedback formats validation results into feedback text for the LLM.
func formatValidationFeedback(satisfaction float64, failures []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Satisfaction score: %.1f/100\n", satisfaction)
	if len(failures) > 0 {
		b.WriteString("Failures:\n")
		for _, f := range failures {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}
	return b.String()
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
