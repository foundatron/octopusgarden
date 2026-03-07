package attractor

import (
	"strings"
	"testing"
	"unicode/utf8"
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
	if got := truncateFeedback(short); got != short {
		t.Errorf("short message should be unchanged, got %q", got)
	}

	long := strings.Repeat("x", maxFeedbackBytes+100)
	got := truncateFeedback(long)
	if !strings.HasSuffix(got, "\n[truncated]") {
		t.Error("long message should end with [truncated]")
	}
	if len(got) != maxFeedbackBytes+len("\n[truncated]") {
		t.Errorf("truncated message has wrong length: %d", len(got))
	}
}

func TestTruncateFeedbackExactBoundary(t *testing.T) {
	exact := strings.Repeat("x", maxFeedbackBytes)
	got := truncateFeedback(exact)
	if got != exact {
		t.Error("message at exact boundary should not be truncated")
	}
}

func TestFormatValidationFeedback(t *testing.T) {
	result := formatValidationFeedback(72.5, []string{"missing GET /items", "wrong status code"})
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
	result := formatValidationFeedback(95.0, nil)
	if !strings.Contains(result, "95.0/100") {
		t.Error("should contain score")
	}
	if strings.Contains(result, "Scenario results:") {
		t.Error("should not contain Scenario results header when there are no failures")
	}
}

func TestFormatValidationFeedbackMultiLine(t *testing.T) {
	// Multi-line entries should pass through verbatim without any "- " prefix added.
	entry := "✗ my-scenario (45/100)\n  ✗ check health (20/100)\n    Reasoning: timeout\n    Observed: got 500"
	result := formatValidationFeedback(45.0, []string{entry})
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
	msgs := buildPatchMessages(history, bestFiles, 50.0)
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
	msgs := buildPatchMessages(nil, bestFiles, 70.0)
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
	got := truncateFeedback(input)
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

	msgs := buildPatchMessages(history, bestFiles, 50.0)
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

// capsSuffix returns a short string describing capabilities for test names.
func capsSuffix(caps ScenarioCapabilities) string {
	switch {
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
