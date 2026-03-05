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

const systemPromptSuffixHTTP = `

INSTRUCTIONS:
- Generate ALL files needed for a working application
- Include a Dockerfile that builds and runs the application on port 8080
- Output each file in this exact format:

=== FILE: path/to/file ===
file content here
=== END FILE ===

EXAMPLE (showing correct format with two files):

=== FILE: main.go ===
package main

import "net/http"

func main() {
	http.ListenAndServe(":8080", nil)
}
=== END FILE ===
=== FILE: Dockerfile ===
FROM golang:1.22-alpine
WORKDIR /app
COPY go.mod ./
COPY . .
RUN go mod tidy
RUN go build -o server .
CMD ["./server"]
=== END FILE ===

- Generate ONLY the file blocks, minimize explanatory text
- The application MUST listen on port 8080
- Include all dependencies and configuration files`

const systemPromptSuffixCLI = `

INSTRUCTIONS:
- Generate ALL files needed for a working command-line application
- Include a Dockerfile that builds the application. The built binary must be available in PATH inside the container.
- Do NOT start a server or listen on any port. The application is a CLI tool invoked via command-line arguments.
- Output each file in this exact format:

=== FILE: path/to/file ===
file content here
=== END FILE ===

EXAMPLE (showing correct format with two files):

=== FILE: main.go ===
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: myapp <command>")
		os.Exit(1)
	}
	fmt.Println("Hello from", os.Args[1])
}
=== END FILE ===
=== FILE: Dockerfile ===
FROM golang:1.22-alpine
WORKDIR /app
COPY go.mod ./
COPY . .
RUN go mod tidy
RUN go build -o /usr/local/bin/myapp .
=== END FILE ===

- Generate ONLY the file blocks, minimize explanatory text
- The Dockerfile must install the binary to a PATH location (e.g. /usr/local/bin/)
- Include all dependencies and configuration files`

const systemPromptSuffixBoth = `

INSTRUCTIONS:
- Generate ALL files needed for a working application that serves both as an HTTP server AND a command-line tool
- Include a Dockerfile that builds the application and installs it in PATH
- The application MUST listen on port 8080 for HTTP requests
- The application must also support command-line invocation for CLI operations
- Output each file in this exact format:

=== FILE: path/to/file ===
file content here
=== END FILE ===

- Generate ONLY the file blocks, minimize explanatory text
- Include all dependencies and configuration files`

const systemPromptSuffixGRPC = `

INSTRUCTIONS:
- Generate ALL files needed for a working gRPC application
- Include a Dockerfile that builds and runs the application with a gRPC server on port 50051
- The gRPC server MUST enable server reflection (import "google.golang.org/grpc/reflection"; reflection.Register(srv))
- Include .proto files and generate Go stubs in the Dockerfile (protoc + protoc-gen-go + protoc-gen-go-grpc)
- Do NOT generate go.sum — use go mod tidy in the Dockerfile
- Output each file in this exact format:

=== FILE: path/to/file ===
file content here
=== END FILE ===

- Generate ONLY the file blocks, minimize explanatory text
- The application MUST serve gRPC on port 50051
- Include all .proto files, generated code, and configuration files`

const systemPromptSuffixHTTPAndGRPC = `

INSTRUCTIONS:
- Generate ALL files needed for a working application that serves both HTTP and gRPC
- Include a Dockerfile that builds and runs the application
- The application MUST listen on port 8080 for HTTP requests
- The application MUST serve gRPC on port 50051
- The gRPC server MUST enable server reflection (import "google.golang.org/grpc/reflection"; reflection.Register(srv))
- Include .proto files and generate Go stubs in the Dockerfile (protoc + protoc-gen-go + protoc-gen-go-grpc)
- Do NOT generate go.sum — use go mod tidy in the Dockerfile
- Output each file in this exact format:

=== FILE: path/to/file ===
file content here
=== END FILE ===

- Generate ONLY the file blocks, minimize explanatory text
- Include all .proto files, generated code, and configuration files`

const systemPromptSuffixCLIAndGRPC = `

INSTRUCTIONS:
- Generate ALL files needed for a working application that serves both as a CLI tool and a gRPC server
- Include a Dockerfile that builds the application and installs it in PATH
- The application MUST serve gRPC on port 50051
- The gRPC server MUST enable server reflection (import "google.golang.org/grpc/reflection"; reflection.Register(srv))
- The application must also support command-line invocation for CLI operations
- Include .proto files and generate Go stubs in the Dockerfile (protoc + protoc-gen-go + protoc-gen-go-grpc)
- Do NOT generate go.sum — use go mod tidy in the Dockerfile
- Output each file in this exact format:

=== FILE: path/to/file ===
file content here
=== END FILE ===

- Generate ONLY the file blocks, minimize explanatory text
- Include all .proto files, generated code, and configuration files`

const dependencyRules = `

DEPENDENCY RULES:
- ALWAYS prefer standard library over third-party dependencies. For Go: use net/http (not gorilla/mux), use crypto/rand or math/rand for UUIDs (not google/uuid), etc.
- NEVER generate lock files or checksum files (go.sum, package-lock.json, yarn.lock, etc.) — you cannot compute valid hashes; the build will fail
- For Go: generate only go.mod with no "require" block (or minimal requires). In the Dockerfile, COPY all source files first, THEN run "go mod tidy" to resolve dependencies, THEN build. Example Dockerfile order: COPY go.mod ./ then COPY . . then RUN go mod tidy then RUN go build
- For Node.js: generate only package.json; use "npm install" in the Dockerfile
- For Python: generate only requirements.txt; use "pip install" in the Dockerfile
- Let the package manager resolve and verify dependencies at build time`

// buildSystemPrompt creates the system prompt containing the spec.
// This prompt is cached across iterations via CacheControl: ephemeral.
// The suffix is selected based on scenario capabilities.
func buildSystemPrompt(spec string, caps ScenarioCapabilities) string {
	suffix := selectPromptSuffix(caps)
	return systemPromptPrefix + spec + suffix + dependencyRules
}

func selectPromptSuffix(caps ScenarioCapabilities) string {
	needsHTTP := caps.NeedsHTTP || caps.NeedsBrowser
	switch {
	case needsHTTP && caps.NeedsGRPC:
		return systemPromptSuffixHTTPAndGRPC
	case caps.NeedsExec && caps.NeedsGRPC:
		return systemPromptSuffixCLIAndGRPC
	case caps.NeedsGRPC:
		return systemPromptSuffixGRPC
	case needsHTTP && caps.NeedsExec:
		return systemPromptSuffixBoth
	case caps.NeedsExec:
		return systemPromptSuffixCLI
	default:
		return systemPromptSuffixHTTP
	}
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
