package attractor

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/foundatron/octopusgarden/internal/gene"
)

func TestBuildSystemPromptContainsSpec(t *testing.T) {
	spec := "Build a REST API for managing widgets"
	prompt := buildSystemPrompt(spec, ScenarioCapabilities{}, "", "", "")
	if !strings.Contains(prompt, spec) {
		t.Error("system prompt should contain the spec")
	}
}

func TestBuildSystemPromptContainsFewShotExample(t *testing.T) {
	prompt := buildSystemPrompt("some spec", ScenarioCapabilities{}, "go", "", "")

	checks := []string{
		"EXAMPLE",
		"=== FILE: main.go ===",
		"=== FILE: Dockerfile ===",
		"=== END FILE ===",
	}
	for _, want := range checks {
		if !strings.Contains(prompt, want) {
			t.Errorf("system prompt should contain %q", want)
		}
	}
}

func TestBuildSystemPromptDeterministic(t *testing.T) {
	spec := "Build a hello world app"
	a := buildSystemPrompt(spec, ScenarioCapabilities{}, "", "", "")
	b := buildSystemPrompt(spec, ScenarioCapabilities{}, "", "", "")
	if a != b {
		t.Error("buildSystemPrompt should produce identical output for the same spec")
	}
}

func TestBuildMessagesIteration1(t *testing.T) {
	msgs := buildMessages(1, nil)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("expected role user, got %q", msgs[0].Role)
	}
	if !strings.Contains(msgs[0].Content, "Generate the application") {
		t.Error("iteration 1 message should contain generation instruction")
	}
}

func TestBuildMessagesCategorizedHeaders(t *testing.T) {
	history := []iterationFeedback{
		{iteration: 1, kind: feedbackBuildError, message: "Docker build failed: syntax error"},
		{iteration: 2, kind: feedbackValidation, message: "Satisfaction score: 40.0/100\nScenario results:\n✗ api (40/100)"},
	}
	msgs := buildMessages(3, history)
	content := msgs[0].Content

	if !strings.Contains(content, "BUILD FAILURE (iteration 1)") {
		t.Errorf("expected categorized build failure header, got:\n%s", content)
	}
	if !strings.Contains(content, "VALIDATION FAILURES (iteration 2)") {
		t.Errorf("expected categorized validation header, got:\n%s", content)
	}
}

func TestBuildMessagesAllKindHeaders(t *testing.T) {
	tests := []struct {
		kind       string
		wantHeader string
	}{
		{feedbackBuildError, "BUILD FAILURE"},
		{feedbackHealthError, "HEALTH CHECK FAILURE"},
		{feedbackParseError, "PARSE ERROR"},
		{feedbackRunError, "RUN FAILURE"},
		{feedbackValidation, "VALIDATION FAILURES"},
	}

	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			history := []iterationFeedback{
				{iteration: 1, kind: tt.kind, message: "some error"},
			}
			msgs := buildMessages(2, history)
			if !strings.Contains(msgs[0].Content, tt.wantHeader+" (iteration 1)") {
				t.Errorf("expected header %q, got:\n%s", tt.wantHeader, msgs[0].Content)
			}
		})
	}
}

func TestBuildMessagesLimitsHistory(t *testing.T) {
	history := []iterationFeedback{
		{iteration: 1, kind: feedbackValidation, message: "first"},
		{iteration: 2, kind: feedbackValidation, message: "second"},
		{iteration: 3, kind: feedbackValidation, message: "third"},
		{iteration: 4, kind: feedbackValidation, message: "fourth"},
	}
	msgs := buildMessages(5, history)
	content := msgs[0].Content

	if strings.Contains(content, "first") {
		t.Error("should not include oldest entry (only last 3)")
	}
	for _, want := range []string{"second", "third", "fourth"} {
		if !strings.Contains(content, want) {
			t.Errorf("should include %q in feedback", want)
		}
	}
}

func TestTruncateFeedback(t *testing.T) {
	short := "short message"
	if got := truncateFeedback(short, maxFeedbackBytes); got != short {
		t.Errorf("short message should be unchanged, got %q", got)
	}

	long := strings.Repeat("x", maxFeedbackBytes+100)
	got := truncateFeedback(long, maxFeedbackBytes)
	if !strings.HasSuffix(got, "\n[truncated]") {
		t.Error("long message should end with [truncated]")
	}
	if len(got) != maxFeedbackBytes+len("\n[truncated]") {
		t.Errorf("truncated message has wrong length: %d", len(got))
	}
}

func TestTruncateFeedbackExactBoundary(t *testing.T) {
	exact := strings.Repeat("x", maxFeedbackBytes)
	got := truncateFeedback(exact, maxFeedbackBytes)
	if got != exact {
		t.Error("message at exact boundary should not be truncated")
	}
}

func TestFormatValidationFeedback(t *testing.T) {
	result := formatValidationFeedback(72.5, []string{"missing GET /items", "wrong status code"}, fidelityStandard)
	if !strings.Contains(result, "72.5/100") {
		t.Error("should contain score")
	}
	if !strings.Contains(result, "Scenario results:") {
		t.Error("should contain Scenario results header")
	}
	if !strings.Contains(result, "missing GET /items") {
		t.Error("should contain failure detail")
	}
	// Entries should not be prefixed with "- "
	if strings.Contains(result, "- missing GET /items") {
		t.Error("entries should not have '- ' prefix")
	}
}

func TestFormatValidationFeedbackNoFailures(t *testing.T) {
	result := formatValidationFeedback(95.0, nil, fidelityStandard)
	if !strings.Contains(result, "95.0/100") {
		t.Error("should contain score")
	}
	if strings.Contains(result, "Scenario results:") {
		t.Error("should not contain Scenario results header when there are no failures")
	}
}

func TestFormatValidationFeedbackMultiLine(t *testing.T) {
	// Multi-line entries with only failing steps should pass through verbatim under standard fidelity.
	entry := "✗ my-scenario (45/100)\n  ✗ check health (20/100)\n    Reasoning: timeout\n    Observed: got 500"
	result := formatValidationFeedback(45.0, []string{entry}, fidelityStandard)
	if !strings.Contains(result, "Scenario results:") {
		t.Error("should contain Scenario results header")
	}
	if !strings.Contains(result, entry) {
		t.Errorf("multi-line entry should appear verbatim, got:\n%s", result)
	}
	if strings.Contains(result, "- ✗") {
		t.Error("multi-line entry should not have '- ' prefix")
	}
}

func TestBuildPatchMessagesCategorizedFeedback(t *testing.T) {
	history := []iterationFeedback{
		{iteration: 1, kind: feedbackBuildError, message: "Docker build failed"},
		{iteration: 2, kind: feedbackValidation, message: "Satisfaction score: 50.0/100"},
	}
	bestFiles := map[string]string{
		"main.go":    "package main\n",
		"Dockerfile": "FROM scratch\n",
	}
	msgs := buildPatchMessages(history, bestFiles, 50.0, 0)
	content := msgs[0].Content

	if !strings.Contains(content, "current best version scored 50.0/100") {
		t.Error("should contain best score")
	}
	if !strings.Contains(content, "BUILD FAILURE (iteration 1)") {
		t.Error("should contain categorized build failure header")
	}
	if !strings.Contains(content, "VALIDATION FAILURES (iteration 2)") {
		t.Error("should contain categorized validation header")
	}
	if !strings.Contains(content, "=== FILE: Dockerfile ===") {
		t.Error("should contain best files")
	}
}

func TestBuildPatchMessagesNoHistory(t *testing.T) {
	bestFiles := map[string]string{"main.go": "package main\n"}
	msgs := buildPatchMessages(nil, bestFiles, 70.0, 0)
	content := msgs[0].Content

	if strings.Contains(content, "Failures to fix") {
		t.Error("should not contain failures section when history is empty")
	}
	if !strings.Contains(content, "Output ONLY the files") {
		t.Error("should contain patch instruction")
	}
}

func TestWriteCategorizedFeedbackUnknownKind(t *testing.T) {
	var b strings.Builder
	entries := []iterationFeedback{
		{iteration: 5, kind: "unknown_thing", message: "something happened"},
	}
	writeCategorizedFeedback(&b, entries)
	got := b.String()
	if !strings.Contains(got, "UNKNOWN_THING (iteration 5)") {
		t.Errorf("unknown kind should be uppercased, got:\n%s", got)
	}
}

func TestBuildSystemPromptSuffixSelection(t *testing.T) {
	spec := "Build a sample app"

	tests := []struct {
		name        string
		caps        ScenarioCapabilities
		wantContain []string
		wantAbsent  []string
	}{
		{
			name: "default HTTP only",
			caps: ScenarioCapabilities{},
			wantContain: []string{
				"MUST listen on port 8080",
			},
			wantAbsent: []string{
				"command-line application",
			},
		},
		{
			name: "NeedsHTTP true",
			caps: ScenarioCapabilities{NeedsHTTP: true},
			wantContain: []string{
				"MUST listen on port 8080",
			},
			wantAbsent: []string{
				"command-line application",
			},
		},
		{
			name: "NeedsExec true",
			caps: ScenarioCapabilities{NeedsExec: true},
			wantContain: []string{
				"command-line application",
				"CLI tool",
			},
			wantAbsent: []string{
				"MUST listen on port 8080",
			},
		},
		{
			name: "NeedsHTTP and NeedsExec",
			caps: ScenarioCapabilities{NeedsHTTP: true, NeedsExec: true},
			wantContain: []string{
				"HTTP server AND a command-line tool",
				"MUST listen on port 8080",
			},
		},
		{
			name: "NeedsBrowser only",
			caps: ScenarioCapabilities{NeedsBrowser: true},
			wantContain: []string{
				"MUST listen on port 8080",
			},
			wantAbsent: []string{
				"command-line application",
			},
		},
		{
			name: "NeedsBrowser with NeedsHTTP",
			caps: ScenarioCapabilities{NeedsBrowser: true, NeedsHTTP: true},
			wantContain: []string{
				"MUST listen on port 8080",
			},
		},
		{
			name: "NeedsBrowser with NeedsExec",
			caps: ScenarioCapabilities{NeedsBrowser: true, NeedsExec: true},
			wantContain: []string{
				"HTTP server AND a command-line tool",
				"MUST listen on port 8080",
			},
		},
		{
			name: "NeedsGRPC only",
			caps: ScenarioCapabilities{NeedsGRPC: true},
			wantContain: []string{
				"gRPC server on port 50051",
				"server reflection",
				".proto files",
			},
			wantAbsent: []string{
				"port 8080",
			},
		},
		{
			name: "NeedsHTTP and NeedsGRPC",
			caps: ScenarioCapabilities{NeedsHTTP: true, NeedsGRPC: true},
			wantContain: []string{
				"port 8080",
				"port 50051",
				"server reflection",
			},
		},
		{
			name: "NeedsExec and NeedsGRPC",
			caps: ScenarioCapabilities{NeedsExec: true, NeedsGRPC: true},
			wantContain: []string{
				"CLI",
				"port 50051",
				"server reflection",
			},
		},
		{
			name: "NeedsBrowser and NeedsGRPC",
			caps: ScenarioCapabilities{NeedsBrowser: true, NeedsGRPC: true},
			wantContain: []string{
				"port 8080",
				"port 50051",
				"server reflection",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompt := buildSystemPrompt(spec, tt.caps, "", "", "")
			for _, want := range tt.wantContain {
				if !strings.Contains(prompt, want) {
					t.Errorf("prompt should contain %q", want)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(prompt, absent) {
					t.Errorf("prompt should not contain %q", absent)
				}
			}
		})
	}
}

func TestTruncateFeedbackUTF8Safe(t *testing.T) {
	// Build a string that places a 3-byte UTF-8 rune right at the truncation boundary.
	// U+2603 SNOWMAN = 3 bytes (0xE2 0x98 0x83)
	prefix := strings.Repeat("x", maxFeedbackBytes-1)
	input := prefix + "\u2603" + strings.Repeat("y", 100) // rune straddles boundary
	got := truncateFeedback(input, maxFeedbackBytes)
	if !strings.HasSuffix(got, "\n[truncated]") {
		t.Error("should end with [truncated]")
	}
	// The truncated content (before the marker) must be valid UTF-8.
	trimmed := strings.TrimSuffix(got, "\n[truncated]")
	if !utf8.ValidString(trimmed) {
		t.Error("truncated content should be valid UTF-8")
	}
}

func TestBuildSystemPromptAutoModeNoLanguageBias(t *testing.T) {
	caps := []ScenarioCapabilities{
		{},
		{NeedsHTTP: true},
		{NeedsExec: true},
		{NeedsGRPC: true},
	}
	biasTerms := []string{
		"golang", "go mod", "go build",
		"python", "pip install",
		"node:", "npm install",
		"rust:", "cargo build",
	}

	for _, c := range caps {
		prompt := buildSystemPrompt("some spec", c, "", "", "")
		lower := strings.ToLower(prompt)
		for _, term := range biasTerms {
			if strings.Contains(lower, term) {
				t.Errorf("auto mode (caps=%+v) should not contain language-specific term %q", c, term)
			}
		}
	}
}

func TestBuildSystemPromptWithLanguage(t *testing.T) {
	tests := []struct {
		lang        string
		caps        ScenarioCapabilities
		wantContain []string
		wantAbsent  []string
	}{
		{
			lang: "go",
			caps: ScenarioCapabilities{},
			wantContain: []string{
				"golang:1.24-alpine",
				"main.go",
				"go mod tidy",
			},
			wantAbsent: []string{"python", "node:", "rust:"},
		},
		{
			lang: "go",
			caps: ScenarioCapabilities{NeedsExec: true},
			wantContain: []string{
				"golang:1.24-alpine",
				"os.Args",
			},
		},
		{
			lang: "python",
			caps: ScenarioCapabilities{},
			wantContain: []string{
				"python:3.12-slim",
				"app.py",
				"pip install",
			},
			wantAbsent: []string{"golang", "node:", "rust:"},
		},
		{
			lang: "python",
			caps: ScenarioCapabilities{NeedsExec: true},
			wantContain: []string{
				"python:3.12-slim",
				"argparse",
			},
		},
		{
			lang: "node",
			caps: ScenarioCapabilities{},
			wantContain: []string{
				"node:22-alpine",
				"index.js",
				"npm install",
			},
			wantAbsent: []string{"golang", "python:", "rust:"},
		},
		{
			lang: "rust",
			caps: ScenarioCapabilities{},
			wantContain: []string{
				"rust:1.84-alpine",
				"src/main.rs",
				"cargo build",
			},
			wantAbsent: []string{"golang", "python:", "node:"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.lang+"_"+capsSuffix(tt.caps), func(t *testing.T) {
			prompt := buildSystemPrompt("some spec", tt.caps, tt.lang, "", "")
			for _, want := range tt.wantContain {
				if !strings.Contains(prompt, want) {
					t.Errorf("prompt should contain %q", want)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(prompt, absent) {
					t.Errorf("prompt should not contain %q", absent)
				}
			}
		})
	}
}

func TestBuildSystemPromptGRPCWithLanguage(t *testing.T) {
	tests := []struct {
		lang      string
		wantSetup string
	}{
		{"go", "protoc-gen-go"},
		{"python", "grpcio"},
		{"node", "@grpc/grpc-js"},
		{"rust", "tonic"},
	}

	for _, tt := range tests {
		t.Run(tt.lang, func(t *testing.T) {
			prompt := buildSystemPrompt("some spec", ScenarioCapabilities{NeedsGRPC: true}, tt.lang, "", "")
			if !strings.Contains(prompt, tt.wantSetup) {
				t.Errorf("gRPC prompt for %s should contain %q", tt.lang, tt.wantSetup)
			}
			if !strings.Contains(prompt, "proto/service.proto") {
				t.Errorf("gRPC prompt for %s should contain proto example", tt.lang)
			}
		})
	}
}

func TestBuildSystemPromptWithGenes(t *testing.T) {
	spec := "Build a REST API"
	genes := "// Use repository pattern for data access\nfunc NewRepo() *Repo { ... }"

	prompt := buildSystemPrompt(spec, ScenarioCapabilities{}, "", genes, "")

	if !strings.Contains(prompt, "PROVEN PATTERNS") {
		t.Error("prompt with genes should contain PROVEN PATTERNS header")
	}
	if !strings.Contains(prompt, "SPECIFICATION always takes precedence") {
		t.Error("prompt with genes should contain precedence note")
	}
	if !strings.Contains(prompt, genes) {
		t.Error("prompt should contain gene content verbatim")
	}
}

func TestBuildSystemPromptNoGenes(t *testing.T) {
	spec := "Build a REST API"
	prompt := buildSystemPrompt(spec, ScenarioCapabilities{}, "", "", "")

	if strings.Contains(prompt, "PROVEN PATTERNS") {
		t.Error("empty genes should not include gene section")
	}
}

func TestBuildSystemPromptGeneOrdering(t *testing.T) {
	spec := "Build a REST API"
	genes := "USE_REPO_PATTERN"

	prompt := buildSystemPrompt(spec, ScenarioCapabilities{}, "go", genes, "")

	specIdx := strings.Index(prompt, spec)
	geneIdx := strings.Index(prompt, "PROVEN PATTERNS")
	instrIdx := strings.Index(prompt, "INSTRUCTIONS:")
	depIdx := strings.Index(prompt, "DEPENDENCY RULES:")

	if specIdx >= geneIdx {
		t.Error("spec should appear before gene section")
	}
	if geneIdx >= instrIdx {
		t.Error("gene section should appear before instructions")
	}
	if instrIdx >= depIdx {
		t.Error("instructions should appear before dep rules")
	}
}

func TestBuildGeneSectionSameLanguage(t *testing.T) {
	result := buildGeneSection("some patterns", "go", "go")
	if strings.Contains(result, "CROSS-LANGUAGE NOTE") {
		t.Error("same language should not include cross-language note")
	}
}

func TestBuildGeneSectionCrossLanguage(t *testing.T) {
	result := buildGeneSection("some patterns", "python", "go")
	if !strings.Contains(result, "CROSS-LANGUAGE NOTE") {
		t.Error("cross-language should contain CROSS-LANGUAGE NOTE")
	}
}

func TestBuildGeneSectionCrossLanguageContent(t *testing.T) {
	result := buildGeneSection("some patterns", "python", "go")
	if !strings.Contains(result, "Go") {
		t.Error("note should mention source display name Go")
	}
	if !strings.Contains(result, "Python") {
		t.Error("note should mention target display name Python")
	}
}

func TestBuildGeneSectionCrossLanguagePreserve(t *testing.T) {
	result := buildGeneSection("some patterns", "python", "go")
	if !strings.Contains(result, "invariants") {
		t.Error("note should mention invariants")
	}
	if !strings.Contains(result, "structural patterns") {
		t.Error("note should mention structural patterns")
	}
}

func TestBuildGeneSectionNoGeneLanguage(t *testing.T) {
	result := buildGeneSection("some patterns", "python", "")
	if strings.Contains(result, "CROSS-LANGUAGE NOTE") {
		t.Error("should not include cross-language note when gene language is empty")
	}
}

func TestBuildGeneSectionNoTargetLanguage(t *testing.T) {
	result := buildGeneSection("some patterns", "", "go")
	if strings.Contains(result, "CROSS-LANGUAGE NOTE") {
		t.Error("should not include cross-language note when target language is empty")
	}
}

func TestBuildGeneSectionAllCombinations(t *testing.T) {
	languages := []string{"go", "python", "node", "rust"}
	displayNames := map[string]string{
		"go": "Go", "python": "Python", "node": "Node.js", "rust": "Rust",
	}

	for _, gene := range languages {
		for _, target := range languages {
			name := gene + "_to_" + target
			t.Run(name, func(t *testing.T) {
				result := buildGeneSection("patterns", target, gene)
				hasCrossNote := strings.Contains(result, "CROSS-LANGUAGE NOTE")
				if gene == target && hasCrossNote {
					t.Error("same language should not include cross-language note")
				}
				if gene != target && !hasCrossNote {
					t.Error("different languages should include cross-language note")
				}
				if gene != target && !strings.Contains(result, displayNames[gene]) {
					t.Errorf("note should mention source display name %s", displayNames[gene])
				}
				if gene != target && !strings.Contains(result, displayNames[target]) {
					t.Errorf("note should mention target display name %s", displayNames[target])
				}
			})
		}
	}
}

func TestBuildGeneSectionUnknownLanguage(t *testing.T) {
	result := buildGeneSection("some patterns", "python", "java")
	if !strings.Contains(result, "CROSS-LANGUAGE NOTE") {
		t.Error("unknown gene language should still trigger cross-language note")
	}
	if !strings.Contains(result, "java") {
		t.Error("unknown language should fall back to raw string")
	}
	if !strings.Contains(result, "Python") {
		t.Error("known target language should use display name")
	}
}

func TestLanguageDisplayName(t *testing.T) {
	tests := []struct {
		lang string
		want string
	}{
		{"go", "Go"},
		{"python", "Python"},
		{"node", "Node.js"},
		{"rust", "Rust"},
		{"unknown", "unknown"},
		{"java", "java"},
	}
	for _, tt := range tests {
		t.Run(tt.lang, func(t *testing.T) {
			if got := languageDisplayName(tt.lang); got != tt.want {
				t.Errorf("languageDisplayName(%q) = %q, want %q", tt.lang, got, tt.want)
			}
		})
	}
}

func TestBuildSystemPromptBackwardsCompat(t *testing.T) {
	// Verify that empty genes produces identical output to pre-gene implementation.
	spec := "Build a REST API for widgets"
	caps := ScenarioCapabilities{NeedsHTTP: true}

	prompt := buildSystemPrompt(spec, caps, "go", "", "")
	expected := systemPromptPrefix + spec + buildCapabilitySuffix(caps, "go") + buildDepRules("go")

	if prompt != expected {
		t.Errorf("empty genes should produce identical output to pre-gene construction\ngot:  %s\nwant: %s", prompt, expected)
	}
}

func TestParseFailedScenarios(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		wantKeys []string
		wantNone []string
	}{
		{
			name:     "single failure",
			input:    []string{"✗ move-card (45/100)"},
			wantKeys: []string{"move-card"},
		},
		{
			name:     "passing scenario skipped",
			input:    []string{"✓ add-task (100/100)"},
			wantKeys: []string{},
		},
		{
			name:     "multiple failures",
			input:    []string{"✗ move-card (45/100)", "✗ add-task (30/100)"},
			wantKeys: []string{"move-card", "add-task"},
		},
		{
			name:     "mixed pass and fail",
			input:    []string{"✓ health (100/100)", "✗ create-item (60/100)"},
			wantKeys: []string{"create-item"},
			wantNone: []string{"health"},
		},
		{
			name: "indented sub-lines ignored",
			input: []string{
				"✗ move-card (45/100)\n  ✗ check status (20/100)\n    Reasoning: timeout\n    Observed: got 500",
			},
			wantKeys: []string{"move-card"},
		},
		{
			name:     "empty input",
			input:    []string{},
			wantKeys: []string{},
		},
		{
			name:     "malformed line gracefully skipped",
			input:    []string{"✗ bad-scenario no-parens", "✗ move-card (45/100)"},
			wantKeys: []string{"move-card"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseFailedScenarios(tt.input)
			for _, key := range tt.wantKeys {
				if _, ok := got[key]; !ok {
					t.Errorf("expected key %q in result, got %v", key, got)
				}
			}
			for _, key := range tt.wantNone {
				if _, ok := got[key]; ok {
					t.Errorf("unexpected key %q in result", key)
				}
			}
		})
	}
}

func TestParseFailedScenariosScores(t *testing.T) {
	got := parseFailedScenarios([]string{"✗ move-card (45/100)", "✗ add-task (30/100)"})
	if got["move-card"] != 45 {
		t.Errorf("move-card score: got %v, want 45", got["move-card"])
	}
	if got["add-task"] != 30 {
		t.Errorf("add-task score: got %v, want 30", got["add-task"])
	}
}

// TestScenarioFormatRoundTrip locks the contract between FormatScenarioFailureLine
// (used by cmd/octog to build ValidateFn output) and parseFailedScenarios (used by
// the attractor loop for stall detection). If the format changes in one without
// updating the other, this test catches the regression before it silently degrades
// stall steering to a no-op.
func TestScenarioFormatRoundTrip(t *testing.T) {
	tests := []struct {
		id    string
		score float64
	}{
		{"move-card", 45},
		{"add-task", 30},
		{"scenario with spaces", 0},
		{"edge-case", 100},
	}
	for _, tt := range tests {
		line := FormatScenarioFailureLine(tt.id, tt.score)
		got := parseFailedScenarios([]string{line})
		score, ok := got[tt.id]
		if !ok {
			t.Errorf("id=%q: FormatScenarioFailureLine output %q not parsed by parseFailedScenarios", tt.id, line)
			continue
		}
		if score != tt.score {
			t.Errorf("id=%q: score round-trip: got %v, want %v", tt.id, score, tt.score)
		}
	}
}

func TestBuildSteeringText(t *testing.T) {
	mkFeedback := func(kind string, failed map[string]float64) iterationFeedback {
		return iterationFeedback{kind: kind, failedScenarios: failed}
	}
	mkVal := func(failed map[string]float64) iterationFeedback {
		return mkFeedback(feedbackValidation, failed)
	}
	mkBuild := func() iterationFeedback {
		return mkFeedback(feedbackBuildError, nil)
	}

	tests := []struct {
		name      string
		history   []iterationFeedback
		wantEmpty bool
		wantIDs   []string
	}{
		{
			name:      "no history",
			history:   nil,
			wantEmpty: true,
		},
		{
			name:      "single entry no steering",
			history:   []iterationFeedback{mkVal(map[string]float64{"move-card": 45})},
			wantEmpty: true,
		},
		{
			name: "different scenarios each iteration",
			history: []iterationFeedback{
				mkVal(map[string]float64{"move-card": 45}),
				mkVal(map[string]float64{"add-task": 30}),
			},
			wantEmpty: true,
		},
		{
			name: "same scenario 2 consecutive iterations",
			history: []iterationFeedback{
				mkVal(map[string]float64{"move-card": 50}),
				mkVal(map[string]float64{"move-card": 45}),
			},
			wantEmpty: false,
			wantIDs:   []string{"move-card"},
		},
		{
			name: "3 consecutive same scenario",
			history: []iterationFeedback{
				mkVal(map[string]float64{"move-card": 60}),
				mkVal(map[string]float64{"move-card": 50}),
				mkVal(map[string]float64{"move-card": 45}),
			},
			wantEmpty: false,
			wantIDs:   []string{"move-card"},
		},
		{
			name: "streak broken by passing",
			history: []iterationFeedback{
				mkVal(map[string]float64{"move-card": 50}),
				mkVal(map[string]float64{}), // move-card passed
				mkVal(map[string]float64{"move-card": 45}),
			},
			wantEmpty: true,
		},
		{
			name: "non-validation entries don't break streaks",
			history: []iterationFeedback{
				mkVal(map[string]float64{"move-card": 50}),
				mkBuild(),
				mkVal(map[string]float64{"move-card": 45}),
			},
			wantEmpty: false,
			wantIDs:   []string{"move-card"},
		},
		{
			name: "multiple stalling scenarios",
			history: []iterationFeedback{
				mkVal(map[string]float64{"move-card": 50, "add-task": 40}),
				mkVal(map[string]float64{"move-card": 45, "add-task": 35}),
			},
			wantEmpty: false,
			wantIDs:   []string{"move-card", "add-task"},
		},
		{
			name: "mixed repeated and new",
			history: []iterationFeedback{
				mkVal(map[string]float64{"move-card": 50}),
				mkVal(map[string]float64{"move-card": 45, "add-task": 30}),
			},
			wantEmpty: false,
			wantIDs:   []string{"move-card"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildSteeringText(tt.history)
			if tt.wantEmpty {
				if got != "" {
					t.Errorf("expected empty steering text, got:\n%s", got)
				}
				return
			}
			if got == "" {
				t.Fatal("expected non-empty steering text")
			}
			if !strings.Contains(got, "STALL NOTICE") {
				t.Errorf("steering text should contain STALL NOTICE, got:\n%s", got)
			}
			for _, id := range tt.wantIDs {
				if !strings.Contains(got, id) {
					t.Errorf("steering text should mention scenario %q, got:\n%s", id, got)
				}
			}
		})
	}
}

func TestBuildSteeringTextTrajectory(t *testing.T) {
	mkVal := func(failed map[string]float64) iterationFeedback {
		return iterationFeedback{kind: feedbackValidation, failedScenarios: failed}
	}

	history := []iterationFeedback{
		mkVal(map[string]float64{"move-card": 60}),
		mkVal(map[string]float64{"move-card": 50}),
		mkVal(map[string]float64{"move-card": 45}),
	}
	got := buildSteeringText(history)

	if !strings.Contains(got, "60 → 50 → 45") {
		t.Errorf("steering text should contain score trajectory, got:\n%s", got)
	}
}

func TestBuildMessagesWithSteering(t *testing.T) {
	// Two consecutive validation failures for the same scenario should inject steering.
	history := []iterationFeedback{
		{
			iteration:       1,
			kind:            feedbackValidation,
			message:         "Satisfaction score: 50.0/100\nScenario results:\n✗ move-card (50/100)",
			failedScenarios: map[string]float64{"move-card": 50},
		},
		{
			iteration:       2,
			kind:            feedbackValidation,
			message:         "Satisfaction score: 45.0/100\nScenario results:\n✗ move-card (45/100)",
			failedScenarios: map[string]float64{"move-card": 45},
		},
	}

	msgs := buildMessages(3, history)
	content := msgs[0].Content

	if !strings.Contains(content, "STALL NOTICE") {
		t.Errorf("messages should contain STALL NOTICE when scenario stalls, got:\n%s", content)
	}
	if !strings.Contains(content, "move-card") {
		t.Errorf("messages should mention stalling scenario, got:\n%s", content)
	}
	// Steering should appear before the categorized feedback.
	steerIdx := strings.Index(content, "STALL NOTICE")
	feedbackIdx := strings.Index(content, "VALIDATION FAILURES")
	if steerIdx < 0 || feedbackIdx < 0 {
		t.Errorf("expected both STALL NOTICE and VALIDATION FAILURES in content, got:\n%s", content)
	} else if steerIdx > feedbackIdx {
		t.Errorf("steering text should appear before categorized feedback")
	}
}

func TestBuildMessagesNoSteeringWithoutConsecutive(t *testing.T) {
	// Different scenarios each iteration: no steering expected.
	history := []iterationFeedback{
		{
			iteration:       1,
			kind:            feedbackValidation,
			message:         "Satisfaction score: 50.0/100",
			failedScenarios: map[string]float64{"move-card": 50},
		},
		{
			iteration:       2,
			kind:            feedbackValidation,
			message:         "Satisfaction score: 45.0/100",
			failedScenarios: map[string]float64{"add-task": 45},
		},
	}

	msgs := buildMessages(3, history)
	if strings.Contains(msgs[0].Content, "STALL NOTICE") {
		t.Error("should not inject steering when scenarios differ each iteration")
	}
}

func TestBuildPatchMessagesWithSteering(t *testing.T) {
	history := []iterationFeedback{
		{
			iteration:       1,
			kind:            feedbackValidation,
			message:         "Satisfaction score: 50.0/100\nScenario results:\n✗ add-task (50/100)",
			failedScenarios: map[string]float64{"add-task": 50},
		},
		{
			iteration:       2,
			kind:            feedbackValidation,
			message:         "Satisfaction score: 40.0/100\nScenario results:\n✗ add-task (40/100)",
			failedScenarios: map[string]float64{"add-task": 40},
		},
	}
	bestFiles := map[string]string{"main.go": "package main\n"}

	msgs := buildPatchMessages(history, bestFiles, 50.0, 0)
	content := msgs[0].Content

	if !strings.Contains(content, "STALL NOTICE") {
		t.Errorf("patch messages should contain STALL NOTICE when scenario stalls, got:\n%s", content)
	}
	if !strings.Contains(content, "add-task") {
		t.Errorf("patch messages should mention stalling scenario, got:\n%s", content)
	}
	// Steering should appear before "Failures to fix".
	steerIdx := strings.Index(content, "STALL NOTICE")
	failIdx := strings.Index(content, "Failures to fix")
	if steerIdx < 0 || failIdx < 0 {
		t.Errorf("expected both STALL NOTICE and 'Failures to fix' in content, got:\n%s", content)
	} else if steerIdx > failIdx {
		t.Errorf("steering text should appear before 'Failures to fix' section")
	}
}

func TestDetermineFidelity(t *testing.T) {
	tests := []struct {
		name       string
		iteration  int
		stallCount int
		want       feedbackFidelity
	}{
		{"iter1_no_stall", 1, 0, fidelityCompact},
		{"iter2_no_stall", 2, 0, fidelityCompact},
		{"iter3_no_stall", 3, 0, fidelityStandard},
		{"iter4_no_stall", 4, 0, fidelityStandard},
		{"iter5_no_stall", 5, 0, fidelityFull},
		{"iter10_no_stall", 10, 0, fidelityFull},
		{"iter1_stall2_escalates_to_standard", 1, 2, fidelityStandard},
		{"iter3_stall2_escalates_to_full", 3, 2, fidelityFull},
		{"iter5_stall2_stays_full", 5, 2, fidelityFull},
		{"iter1_stall1_no_escalation", 1, 1, fidelityCompact},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := determineFidelity(tt.iteration, tt.stallCount)
			if got != tt.want {
				t.Errorf("determineFidelity(%d, %d) = %d, want %d", tt.iteration, tt.stallCount, got, tt.want)
			}
		})
	}
}

func TestMaxFeedbackForFidelity(t *testing.T) {
	tests := []struct {
		fidelity feedbackFidelity
		want     int
	}{
		{fidelityCompact, 4096},
		{fidelityStandard, 12288},
		{fidelityFull, 24576},
	}
	for _, tt := range tests {
		got := maxFeedbackForFidelity(tt.fidelity)
		if got != tt.want {
			t.Errorf("maxFeedbackForFidelity(%d) = %d, want %d", tt.fidelity, got, tt.want)
		}
	}
}

func TestFormatValidationFeedbackFidelityCompact(t *testing.T) {
	entry := "✗ scenario-a (40/100)\n  ✗ step-one (20/100)\n    Reasoning: timeout\n    Observed: response body\n  ✓ step-two (100/100)\n    Reasoning: ok"
	result := formatValidationFeedback(40.0, []string{entry}, fidelityCompact)

	// Scenario summary line must be present.
	if !strings.Contains(result, "✗ scenario-a (40/100)") {
		t.Error("compact: should contain scenario summary line")
	}
	// No step detail or sub-detail.
	if strings.Contains(result, "step-one") {
		t.Error("compact: should not contain step detail")
	}
	if strings.Contains(result, "Reasoning") {
		t.Error("compact: should not contain Reasoning")
	}
	if strings.Contains(result, "Observed") {
		t.Error("compact: should not contain Observed")
	}
}

func TestFormatValidationFeedbackFidelityStandard(t *testing.T) {
	entry := "✗ scenario-a (40/100)\n  ✗ fail-step (20/100)\n    Reasoning: timeout\n    Observed: observed-data\n  ✓ pass-step (100/100)\n    Reasoning: all good"
	result := formatValidationFeedback(40.0, []string{entry}, fidelityStandard)

	// Scenario summary present.
	if !strings.Contains(result, "✗ scenario-a (40/100)") {
		t.Error("standard: should contain scenario summary")
	}
	// Failing step detail present.
	if !strings.Contains(result, "fail-step") {
		t.Error("standard: should contain failing step")
	}
	if !strings.Contains(result, "Reasoning: timeout") {
		t.Error("standard: should contain reasoning for failing step")
	}
	if !strings.Contains(result, "observed-data") {
		t.Error("standard: should contain observed content for failing step")
	}
	// Passing step and its sub-detail stripped.
	if strings.Contains(result, "pass-step") {
		t.Error("standard: should not contain passing step")
	}
	if strings.Contains(result, "all good") {
		t.Error("standard: should not contain passing step reasoning")
	}
}

func TestFormatValidationFeedbackFidelityFull(t *testing.T) {
	entry := "✗ scenario-a (40/100)\n  ✗ fail-step (20/100)\n    Reasoning: bad\n  ✓ pass-step (100/100)\n    Reasoning: ok"
	result := formatValidationFeedback(40.0, []string{entry}, fidelityFull)

	// Full keeps everything.
	if !strings.Contains(result, "fail-step") {
		t.Error("full: should contain failing step")
	}
	if !strings.Contains(result, "pass-step") {
		t.Error("full: should contain passing step")
	}
	if !strings.Contains(result, "Reasoning: ok") {
		t.Error("full: should contain passing step reasoning")
	}
}

// TestObservedStandardLimitBelowMax guards the invariant that observedStandardLimit
// (the re-truncation limit for fidelityStandard) is strictly less than MaxObservedBytes
// (the limit used by cmd/octog when building observed output). If MaxObservedBytes
// changes, this test will catch a silent drift before it affects feedback quality.
func TestObservedStandardLimitBelowMax(t *testing.T) {
	if observedStandardLimit >= MaxObservedBytes {
		t.Errorf("observedStandardLimit (%d) must be < MaxObservedBytes (%d); update one of them to restore the invariant",
			observedStandardLimit, MaxObservedBytes)
	}
}

func TestObservedTruncationUTF8Safe(t *testing.T) {
	// Place a 3-byte UTF-8 rune (SNOWMAN U+2603) at the truncation boundary (500 bytes).
	obsPrefix := strings.Repeat("x", observedStandardLimit-1)
	obsContent := obsPrefix + "\u2603" + strings.Repeat("y", 100)
	entry := "✗ scenario-a (40/100)\n  ✗ fail-step (20/100)\n    Observed: " + obsContent
	result := formatValidationFeedback(40.0, []string{entry}, fidelityStandard)

	// Result must be valid UTF-8.
	if !utf8.ValidString(result) {
		t.Error("standard: observed truncation must produce valid UTF-8")
	}
	// The truncation marker must be present.
	if !strings.Contains(result, "…") {
		t.Error("standard: truncated observed line should end with …")
	}
}

// TestFidelityRoundTrip constructs failure strings in the canonical format produced by
// cmd/octog (FormatScenarioFailureLine + indented step detail) and verifies that each
// fidelity level filters as expected. This guards the coupling between filterFailureEntry
// and the format produced by formatFailedScenario in cmd/octog.
func TestFidelityRoundTrip(t *testing.T) {
	// Build a canonical failure entry: one failing step and one passing step.
	scenarioLine := FormatScenarioFailureLine("round-trip", 50)
	failStep := "  ✗ failing step (30/100)\n    Reasoning: broke\n    Observed: " + strings.Repeat("a", 100) + "\n    [missing_endpoint] POST /users returned 404"
	passStep := "  ✓ passing step (100/100)\n    Reasoning: fine"
	entry := scenarioLine + "\n" + failStep + "\n" + passStep

	t.Run("compact", func(t *testing.T) {
		result := formatValidationFeedback(50.0, []string{entry}, fidelityCompact)
		if !strings.Contains(result, scenarioLine) {
			t.Error("compact: must contain scenario summary")
		}
		if strings.Contains(result, "failing step") {
			t.Error("compact: must not contain step detail")
		}
		if strings.Contains(result, "passing step") {
			t.Error("compact: must not contain passing step")
		}
		if strings.Contains(result, "[missing_endpoint]") {
			t.Error("compact: must not contain diagnostic lines")
		}
	})

	t.Run("standard", func(t *testing.T) {
		result := formatValidationFeedback(50.0, []string{entry}, fidelityStandard)
		if !strings.Contains(result, scenarioLine) {
			t.Error("standard: must contain scenario summary")
		}
		if !strings.Contains(result, "failing step") {
			t.Error("standard: must contain failing step detail")
		}
		if !strings.Contains(result, "Reasoning: broke") {
			t.Error("standard: must contain failing step reasoning")
		}
		if strings.Contains(result, "passing step") {
			t.Error("standard: must not contain passing step")
		}
		if strings.Contains(result, "Reasoning: fine") {
			t.Error("standard: must not contain passing step reasoning")
		}
		if !strings.Contains(result, "[missing_endpoint] POST /users returned 404") {
			t.Error("standard: must contain diagnostic lines under failing step")
		}
	})

	t.Run("full", func(t *testing.T) {
		result := formatValidationFeedback(50.0, []string{entry}, fidelityFull)
		if !strings.Contains(result, "failing step") {
			t.Error("full: must contain failing step")
		}
		if !strings.Contains(result, "passing step") {
			t.Error("full: must contain passing step")
		}
		if !strings.Contains(result, "Reasoning: fine") {
			t.Error("full: must contain passing step reasoning")
		}
		if !strings.Contains(result, "[missing_endpoint] POST /users returned 404") {
			t.Error("full: must contain diagnostic lines")
		}
	})
}

func TestBuildMinimalismSuffix(t *testing.T) {
	tests := []struct {
		name            string
		score           float64
		failedScenarios map[string]float64
		wantEmpty       bool
		wantContains    []string
	}{
		{
			name:            "normal case with two failing scenarios",
			score:           85,
			failedScenarios: map[string]float64{"auth": 60, "health": 40},
			wantContains:    []string{"85%", "SMALLEST", "auth", "health", "60%", "40%"},
		},
		{
			name:            "nil failedScenarios",
			score:           85,
			failedScenarios: nil,
			wantEmpty:       true,
		},
		{
			name:            "empty failedScenarios map",
			score:           85,
			failedScenarios: map[string]float64{},
			wantEmpty:       true,
		},
		{
			name:            "score exactly 80 with failures",
			score:           80,
			failedScenarios: map[string]float64{"create": 70},
			wantContains:    []string{"80%", "SMALLEST", "create"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildMinimalismSuffix(tt.score, tt.failedScenarios)
			if tt.wantEmpty {
				if got != "" {
					t.Errorf("expected empty string, got %q", got)
				}
				return
			}
			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("expected suffix to contain %q, got:\n%s", want, got)
				}
			}
		})
	}
}

// capsSuffix returns a short string describing capabilities for test names.
func capsSuffix(caps ScenarioCapabilities) string {
	switch {
	case caps.NeedsTUI:
		return "tui"
	case caps.NeedsHTTP:
		return "http"
	case caps.NeedsExec:
		return "cli"
	case caps.NeedsGRPC:
		return "grpc"
	default:
		return "default"
	}
}

func TestBuildAgenticSystemPrompt(t *testing.T) {
	spec := "Build a REST API for managing widgets"
	prompt := buildAgenticSystemPrompt(spec, ScenarioCapabilities{NeedsHTTP: true}, "go", "", "")

	if !strings.Contains(prompt, spec) {
		t.Error("agentic system prompt should contain the spec")
	}
	if !strings.Contains(prompt, "write_file") {
		t.Error("agentic system prompt should mention write_file tool")
	}
	// Must NOT contain the === FILE: format instruction.
	if strings.Contains(prompt, "=== FILE:") {
		t.Error("agentic system prompt must not contain === FILE: format instruction")
	}
	// Must NOT contain the EXAMPLE block.
	if strings.Contains(prompt, "=== FILE: main.go ===") {
		t.Error("agentic system prompt must not contain language example block")
	}
}

func TestBuildAgenticSystemPromptContainsGenes(t *testing.T) {
	genes := "Use dependency injection pattern"
	prompt := buildAgenticSystemPrompt("some spec", ScenarioCapabilities{}, "", genes, "")
	if !strings.Contains(prompt, genes) {
		t.Error("agentic system prompt should include gene content when provided")
	}
}

func TestBuildAgenticMessages_Iteration1(t *testing.T) {
	msgs := buildAgenticMessages(1, nil)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("expected role user, got %q", msgs[0].Role)
	}
	if !strings.Contains(msgs[0].Content, "write_file") {
		t.Error("iteration 1 message should mention write_file tool")
	}
	// Must NOT contain the === FILE: format instruction.
	if strings.Contains(msgs[0].Content, "=== FILE:") {
		t.Error("agentic message must not contain === FILE: format instruction")
	}
}

func TestBuildAgenticMessages_Iteration2(t *testing.T) {
	history := []iterationFeedback{
		{iteration: 1, kind: feedbackValidation, message: "Satisfaction score: 50.0/100\nneeds work"},
	}
	msgs := buildAgenticMessages(2, history)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "write_file") {
		t.Error("iteration 2 message should mention write_file tool")
	}
	if !strings.Contains(msgs[0].Content, "previous attempt") {
		t.Error("iteration 2 message should reference previous attempt")
	}
	// Must NOT contain the === FILE: format instruction.
	if strings.Contains(msgs[0].Content, "=== FILE:") {
		t.Error("agentic message must not contain === FILE: format instruction")
	}
}

func TestBuildAgenticPatchMessages(t *testing.T) {
	bestFiles := map[string]string{
		"main.go":    "package main\n",
		"Dockerfile": "FROM scratch\n",
	}
	history := []iterationFeedback{
		{iteration: 1, kind: feedbackValidation, message: "Satisfaction score: 60.0/100\nmissing endpoint"},
	}
	msgs := buildAgenticPatchMessages(history, bestFiles, 60.0)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	content := msgs[0].Content

	// Should list file paths, not file content.
	if !strings.Contains(content, "main.go") {
		t.Error("patch message should list main.go path")
	}
	if !strings.Contains(content, "Dockerfile") {
		t.Error("patch message should list Dockerfile path")
	}
	// Should mention read_file for inspection.
	if !strings.Contains(content, "read_file") {
		t.Error("patch message should mention read_file for inspecting files")
	}
	// Should mention write_file for output.
	if !strings.Contains(content, "write_file") {
		t.Error("patch message should mention write_file for output")
	}
	// Must NOT embed full file content.
	if strings.Contains(content, "package main") {
		t.Error("agentic patch message must not embed file content inline")
	}
}

func TestBuildPatchMessagesOmittedCount(t *testing.T) {
	bestFiles := map[string]string{"main.go": "package main\n"}
	msgs := buildPatchMessages(nil, bestFiles, 80.0, 15)
	content := msgs[0].Content
	if !strings.Contains(content, "(15 other files not relevant to current failures, not shown)") {
		t.Errorf("should contain omitted-files notice, got:\n%s", content)
	}
}

func TestBuildPatchMessagesZeroOmitted(t *testing.T) {
	bestFiles := map[string]string{"main.go": "package main\n"}
	msgs := buildPatchMessages(nil, bestFiles, 80.0, 0)
	content := msgs[0].Content
	if strings.Contains(content, "other files not relevant") {
		t.Errorf("should not contain omitted-files notice when omittedCount=0, got:\n%s", content)
	}
}

func TestBuildComponentPrompt_IncludesContractAndPatterns(t *testing.T) {
	comp := gene.Component{
		Name:      "routes",
		Interface: "HTTP handler interface for /api routes",
		Patterns:  "Use chi router with middleware chain",
	}
	prompt := buildComponentPrompt("my spec", comp, nil, "go")
	if !strings.Contains(prompt, "COMPONENT CONTRACT:") {
		t.Error("prompt should contain COMPONENT CONTRACT section")
	}
	if !strings.Contains(prompt, comp.Interface) {
		t.Error("prompt should contain component interface text")
	}
	if !strings.Contains(prompt, "COMPONENT PATTERNS:") {
		t.Error("prompt should contain COMPONENT PATTERNS section")
	}
	if !strings.Contains(prompt, comp.Patterns) {
		t.Error("prompt should contain component patterns text")
	}
}

func TestBuildComponentPrompt_IncludesDependencyInterfaces(t *testing.T) {
	comp := gene.Component{
		Name:      "routes",
		Interface: "HTTP handler interface",
		DependsOn: []string{"models"},
	}
	depInterfaces := map[string]string{
		"models": "type User struct { ID int; Name string }",
	}
	prompt := buildComponentPrompt("my spec", comp, depInterfaces, "")
	if !strings.Contains(prompt, "DEPENDENCY INTERFACES:") {
		t.Error("prompt should contain DEPENDENCY INTERFACES section")
	}
	if !strings.Contains(prompt, "--- models ---") {
		t.Error("prompt should contain models dependency header")
	}
	if !strings.Contains(prompt, depInterfaces["models"]) {
		t.Error("prompt should contain dependency interface text")
	}
}

func TestBuildComponentPrompt_NoDependencies(t *testing.T) {
	comp := gene.Component{
		Name:      "models",
		Interface: "Data models",
	}
	prompt := buildComponentPrompt("my spec", comp, nil, "")
	if strings.Contains(prompt, "DEPENDENCY INTERFACES:") {
		t.Error("prompt should not contain DEPENDENCY INTERFACES when there are no deps")
	}
}

func TestBuildComponentPrompt_FiltersToOnlyDeclaredDeps(t *testing.T) {
	comp := gene.Component{
		Name:      "routes",
		Interface: "HTTP handler interface",
		DependsOn: []string{"models"},
	}
	depInterfaces := map[string]string{
		"models": "type User struct { ID int; Name string }",
		"auth":   "type AuthService interface { Verify(token string) bool }",
	}
	prompt := buildComponentPrompt("my spec", comp, depInterfaces, "")
	if !strings.Contains(prompt, "--- models ---") {
		t.Error("prompt should contain declared dependency models")
	}
	if strings.Contains(prompt, "--- auth ---") {
		t.Error("prompt should not contain undeclared dependency auth")
	}
}

func TestBuildComponentPrompt_SpecFirst(t *testing.T) {
	spec := "Build a REST API"
	comp := gene.Component{
		Name:      "routes",
		Interface: "HTTP handlers",
	}
	prompt := buildComponentPrompt(spec, comp, nil, "")
	specIdx := strings.Index(prompt, spec)
	contractIdx := strings.Index(prompt, "COMPONENT CONTRACT:")
	if specIdx < 0 || contractIdx < 0 {
		t.Fatal("prompt missing spec or contract section")
	}
	if specIdx >= contractIdx {
		t.Error("spec should appear before component contract (caching correctness)")
	}
}

func TestBuildComponentPrompt_NoDockerfileInstructions(t *testing.T) {
	comp := gene.Component{
		Name:      "routes",
		Interface: "HTTP handler interface",
	}
	prompt := buildComponentPrompt("my spec", comp, nil, "go")
	if !strings.Contains(prompt, "Do NOT generate a Dockerfile, dependency manifests, or build infrastructure") {
		t.Error("component prompt should instruct not to generate Dockerfile or build infrastructure")
	}
	// Should not contain "Include a Dockerfile" (the monolithic instruction).
	if strings.Contains(prompt, "Include a Dockerfile") {
		t.Error("component prompt should not contain monolithic Dockerfile instruction")
	}
}

func TestTUICapabilityInstructions(t *testing.T) {
	tests := []struct {
		name        string
		caps        ScenarioCapabilities
		wantContain []string
		wantAbsent  []string
	}{
		{
			name: "NeedsTUI only",
			caps: ScenarioCapabilities{NeedsTUI: true},
			wantContain: []string{
				"terminal user interface (TUI)",
				"Bubble Tea",
				"Do NOT start an HTTP server",
			},
			wantAbsent: []string{
				"CLI tool invoked via command-line arguments",
				"MUST listen on port 8080",
			},
		},
		{
			name: "NeedsTUI and NeedsExec",
			caps: ScenarioCapabilities{NeedsTUI: true, NeedsExec: true},
			wantContain: []string{
				"terminal user interface (TUI)",
				"Bubble Tea",
				"CLI subcommands",
				"Do NOT start an HTTP server",
			},
			wantAbsent: []string{
				"CLI tool invoked via command-line arguments",
				"MUST listen on port 8080",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := capabilityInstructions(tt.caps)
			for _, want := range tt.wantContain {
				if !strings.Contains(got, want) {
					t.Errorf("capabilityInstructions should contain %q, got:\n%s", want, got)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("capabilityInstructions should not contain %q", absent)
				}
			}
		})
	}
}

func TestTUITrailingInstructions(t *testing.T) {
	got := capabilityTrailingInstructions(ScenarioCapabilities{NeedsTUI: true})
	if !strings.Contains(got, "full-screen TUI") {
		t.Errorf("trailing instructions for TUI should mention full-screen TUI, got:\n%s", got)
	}
	if !strings.Contains(got, "PATH location") {
		t.Errorf("trailing instructions for TUI should mention PATH, got:\n%s", got)
	}
}

func TestTUILanguageExample(t *testing.T) {
	tmpl, ok := LookupLanguage("go")
	if !ok {
		t.Fatal("go language template not found")
	}

	// TUI-only should use TUI example.
	tuiExample := buildLanguageExample(tmpl, ScenarioCapabilities{NeedsTUI: true})
	if !strings.Contains(tuiExample, "charm.land/bubbletea/v2") {
		t.Error("TUI example should contain bubbletea v2 import")
	}
	if !strings.Contains(tuiExample, "tea.NewProgram") {
		t.Error("TUI example should contain tea.NewProgram")
	}

	// TUI+Exec should skip example (combined capability).
	tuiExecExample := buildLanguageExample(tmpl, ScenarioCapabilities{NeedsTUI: true, NeedsExec: true})
	if tuiExecExample != "" {
		t.Errorf("TUI+Exec should skip example block, got:\n%s", tuiExecExample)
	}

	// Exec-only should still use CLI example, not TUI.
	execExample := buildLanguageExample(tmpl, ScenarioCapabilities{NeedsExec: true})
	if strings.Contains(execExample, "bubbletea") {
		t.Error("Exec-only example should not contain bubbletea")
	}
	if !strings.Contains(execExample, "os.Args") {
		t.Error("Exec-only example should contain os.Args")
	}
}

func TestTUISystemPromptIntegration(t *testing.T) {
	prompt := buildSystemPrompt("TUI app spec", ScenarioCapabilities{NeedsTUI: true, NeedsExec: true}, "go", "", "")
	if !strings.Contains(prompt, "terminal user interface") {
		t.Error("system prompt with TUI caps should contain TUI instructions")
	}
	if strings.Contains(prompt, "CLI tool invoked via command-line arguments") {
		t.Error("system prompt with TUI+Exec should not fall through to exec-only instructions")
	}
}
