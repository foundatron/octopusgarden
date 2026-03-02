package attractor

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/foundatron/octopusgarden/internal/container"
	"github.com/foundatron/octopusgarden/internal/llm"
	specpkg "github.com/foundatron/octopusgarden/internal/spec"
)

var errEmptySpec = errors.New("attractor: spec content is empty")

// summarizeModel is the cheap model used for spec summarization.
// Same model as judgeModel in cmd/octog/main.go — both use Haiku for cost efficiency.
const summarizeModel = "claude-haiku-4-5-20251001"

// Status constants for RunResult.
const (
	StatusConverged      = "converged"
	StatusStalled        = "stalled"
	StatusBudgetExceeded = "budget_exceeded"
	StatusMaxIterations  = "max_iterations"
)

// IterationOutcome classifies how a single iteration ended.
type IterationOutcome string

// IterationOutcome constants for progress reporting.
const (
	OutcomeValidated  IterationOutcome = "validated"
	OutcomeBuildFail  IterationOutcome = "build_fail"
	OutcomeRunFail    IterationOutcome = "run_fail"
	OutcomeHealthFail IterationOutcome = "health_fail"
	OutcomeParseFail  IterationOutcome = "parse_fail"
)

// IterationProgress is passed to the progress callback after each iteration completes.
type IterationProgress struct {
	RunID            string
	Iteration        int
	MaxIterations    int
	Outcome          IterationOutcome
	Satisfaction     float64
	BestSatisfaction float64
	Threshold        float64
	Trend            Trend
	IterationCostUSD float64
	TotalCostUSD     float64
	BudgetUSD        float64
	Elapsed          time.Duration
	StallCount       int
}

// ProgressFunc is called synchronously after each iteration completes.
// The attractor loop is single-goroutine — no mutex needed.
// Implementations must return promptly.
type ProgressFunc func(IterationProgress)

// ContainerManager is the interface to Docker container operations.
// *container.Manager satisfies this automatically.
type ContainerManager interface {
	Build(ctx context.Context, dir, tag string) error
	Run(ctx context.Context, tag string) (url string, stop container.StopFunc, err error)
	WaitHealthy(ctx context.Context, url string, timeout time.Duration) error
}

// ValidateFn runs holdout scenarios against a running container and returns results.
// The attractor never imports internal/scenario — the CLI provides this closure.
type ValidateFn func(ctx context.Context, url string) (satisfaction float64, failures []string, cost float64, err error)

// Attractor orchestrates the convergence loop: generate code → build → validate → iterate.
type Attractor struct {
	llm          llm.Client
	containerMgr ContainerManager
	logger       *slog.Logger
}

// RunOptions configures the attractor loop.
type RunOptions struct {
	Model         string
	BudgetUSD     float64       // 0 = unlimited
	Threshold     float64       // default 95
	MaxIterations int           // default 10
	StallLimit    int           // default 3
	WorkspaceDir  string        // default "./workspace"
	HealthTimeout time.Duration // default 30s
	Progress      ProgressFunc  // optional per-iteration callback
	PatchMode     bool          // if true, iteration 2+ sends prev best files + failures
	ContextBudget int           // max estimated tokens for spec in system prompt; 0 = unlimited
}

// RunResult holds the outcome of an attractor run.
type RunResult struct {
	RunID        string
	Iterations   int
	Satisfaction float64
	CostUSD      float64
	OutputDir    string
	Status       string
}

// iterationFeedback captures what happened in one iteration for building the next prompt.
type iterationFeedback struct {
	iteration int
	kind      string // "validation", "build_error", "health_error", "parse_error"
	message   string
}

// runState holds mutable state across iterations of the attractor loop.
type runState struct {
	runID                string
	opts                 RunOptions
	baseDir              string
	bestDir              string
	totalCost            float64
	bestSatisfaction     float64
	stallCount           int
	history              []iterationFeedback
	scoreHistory         []float64
	lastOutcome          IterationOutcome
	lastSatisfaction     float64
	startTime            time.Time
	bestFiles            map[string]string       // files from the best-scoring iteration
	patchActive          bool                    // patch mode currently in effect (may disable on regression)
	patchRegressionCount int                     // consecutive regressions while patch mode active
	summarized           *specpkg.SummarizedSpec // nil if spec fits budget or budget is 0
}

func (s *runState) result(iter int, status string) *RunResult {
	return &RunResult{
		RunID:        s.runID,
		Iterations:   iter,
		Satisfaction: s.bestSatisfaction,
		CostUSD:      s.totalCost,
		OutputDir:    s.bestDir,
		Status:       status,
	}
}

// buildProgress snapshots current state into an IterationProgress value.
// Trend is based on scoreHistory, which is only updated for validated iterations.
// Non-validated iterations (build/run/health/parse failures) correctly reflect the
// prior trend since no new score was produced.
func (s *runState) buildProgress(iter int, costBefore float64) IterationProgress {
	return IterationProgress{
		RunID:            s.runID,
		Iteration:        iter,
		MaxIterations:    s.opts.MaxIterations,
		Outcome:          s.lastOutcome,
		Satisfaction:     s.lastSatisfaction,
		BestSatisfaction: s.bestSatisfaction,
		Threshold:        s.opts.Threshold,
		Trend:            DetectTrend(s.scoreHistory, s.opts.Threshold, s.opts.StallLimit),
		IterationCostUSD: s.totalCost - costBefore,
		TotalCostUSD:     s.totalCost,
		BudgetUSD:        s.opts.BudgetUSD,
		Elapsed:          time.Since(s.startTime),
		StallCount:       s.stallCount,
	}
}

func (s *runState) budgetExceeded() bool {
	return s.opts.BudgetUSD > 0 && s.totalCost >= s.opts.BudgetUSD
}

func (s *runState) recordStall(iter int, kind, message string) {
	s.stallCount++
	s.history = append(s.history, iterationFeedback{
		iteration: iter,
		kind:      kind,
		message:   message,
	})
}

// New creates an Attractor with the given dependencies.
func New(client llm.Client, containerMgr ContainerManager, logger *slog.Logger) *Attractor {
	return &Attractor{
		llm:          client,
		containerMgr: containerMgr,
		logger:       logger,
	}
}

// Run executes the attractor convergence loop.
// spec is the specification content (never scenario content — holdout isolation).
// validate is a closure provided by the CLI that runs scenarios and judges satisfaction.
// Returns a RunResult for normal termination, or an error for unrecoverable failures.
func (a *Attractor) Run(ctx context.Context, rawSpec string, opts RunOptions, validate ValidateFn) (*RunResult, error) {
	if strings.TrimSpace(rawSpec) == "" {
		return nil, errEmptySpec
	}

	opts = withDefaults(opts)
	s := &runState{
		runID:       generateRunID(),
		opts:        opts,
		startTime:   time.Now(),
		patchActive: opts.PatchMode,
	}
	s.baseDir = filepath.Join(opts.WorkspaceDir, s.runID)
	s.bestDir = filepath.Join(s.baseDir, "best")

	// Conditionally summarize large specs for context budget management.
	if opts.ContextBudget > 0 && specpkg.EstimateTokens(rawSpec) > opts.ContextBudget {
		summarized, summarizeCost := a.trySummarize(ctx, rawSpec)
		s.summarized = summarized
		s.totalCost += summarizeCost
	}

	for iter := 1; iter <= opts.MaxIterations; iter++ {
		a.logger.Info("iteration start", "run_id", s.runID, "iteration", iter, "cost_usd", s.totalCost, "best_satisfaction", s.bestSatisfaction)

		if s.budgetExceeded() {
			return s.result(iter-1, StatusBudgetExceeded), nil
		}

		costBefore := s.totalCost
		result, err := a.iterate(ctx, rawSpec, iter, s, validate)
		if err != nil {
			return nil, err
		}

		if opts.Progress != nil {
			opts.Progress(s.buildProgress(iter, costBefore))
		}

		if result != nil {
			return result, nil
		}
	}

	return s.result(opts.MaxIterations, StatusMaxIterations), nil
}

// iterate runs a single iteration of the attractor loop.
// Returns (result, nil) for terminal conditions, (nil, nil) to continue, or (nil, err) for hard errors.
func (a *Attractor) iterate(ctx context.Context, rawSpec string, iter int, s *runState, validate ValidateFn) (*RunResult, error) {
	// Select spec content: use summarized view when available to respect context budget.
	// SelectContent returns full spec if it fits the budget, so iteration 1 (no failures)
	// still gets maximum detail. Iteration 2+ gets failure-relevant sections expanded.
	specContent := rawSpec
	if s.summarized != nil {
		var failures []string
		if iter > 1 {
			failures = extractFailureStrings(s.history)
		}
		specContent = specpkg.SelectContent(s.summarized, s.opts.ContextBudget, failures)
	}

	// Build messages: patch mode sends previous best files + failures.
	var messages []llm.Message
	if s.patchActive && s.bestFiles != nil && iter > 1 {
		messages = buildPatchMessages(s.history, s.bestFiles, s.bestSatisfaction)
	} else {
		messages = buildMessages(iter, s.history)
	}

	// Generate code via LLM.
	genResp, err := a.llm.Generate(ctx, llm.GenerateRequest{
		SystemPrompt: buildSystemPrompt(specContent),
		Messages:     messages,
		Model:        s.opts.Model,
		CacheControl: &llm.CacheControl{Type: "ephemeral"},
	})
	if err != nil {
		return nil, fmt.Errorf("attractor: generate iteration %d: %w", iter, err)
	}
	s.totalCost += genResp.CostUSD

	// Parse files from LLM output.
	files, err := ParseFiles(genResp.Content)
	if err != nil {
		a.logger.Warn("parse files failed", "iteration", iter, "error", err)
		s.lastOutcome = OutcomeParseFail
		s.lastSatisfaction = 0
		s.recordStall(iter, "parse_error", fmt.Sprintf("Failed to parse generated files: %s", err))
		return a.checkStalled(iter, s), nil
	}

	// In patch mode, merge new output over previous best to carry forward unchanged files.
	if s.patchActive && s.bestFiles != nil && iter > 1 {
		files = MergeFiles(files, s.bestFiles)
	}

	// Write files to iteration directory.
	iterDir := filepath.Join(s.baseDir, fmt.Sprintf("iter_%d", iter))
	if err := writeFiles(iterDir, files); err != nil {
		return nil, fmt.Errorf("attractor: write files iteration %d: %w", iter, err)
	}

	// Build, run, and validate.
	return a.buildRunValidate(ctx, iter, iterDir, files, s, validate)
}

// buildRunValidate handles the Docker build → run → health check → validate pipeline.
func (a *Attractor) buildRunValidate(ctx context.Context, iter int, iterDir string, files map[string]string, s *runState, validate ValidateFn) (*RunResult, error) {
	tag := fmt.Sprintf("og-%s-iter%d", s.runID, iter)
	if err := a.containerMgr.Build(ctx, iterDir, tag); err != nil {
		a.logger.Warn("build failed", "iteration", iter, "error", err)
		s.lastOutcome = OutcomeBuildFail
		s.lastSatisfaction = 0
		s.recordStall(iter, "build_error", fmt.Sprintf("Docker build failed: %s", err))
		return a.checkStalled(iter, s), nil
	}

	url, stop, err := a.containerMgr.Run(ctx, tag)
	if err != nil {
		a.logger.Warn("container run failed", "iteration", iter, "error", err)
		s.lastOutcome = OutcomeRunFail
		s.lastSatisfaction = 0
		s.recordStall(iter, "run_error", fmt.Sprintf("Container failed to start: %s", err))
		return a.checkStalled(iter, s), nil
	}
	defer stop()

	if err := a.containerMgr.WaitHealthy(ctx, url, s.opts.HealthTimeout); err != nil {
		a.logger.Warn("health check failed", "iteration", iter, "error", err)
		s.lastOutcome = OutcomeHealthFail
		s.lastSatisfaction = 0
		s.recordStall(iter, "health_error", fmt.Sprintf("Health check failed: %s", err))
		return a.checkStalled(iter, s), nil
	}

	satisfaction, failures, valCost, err := validate(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("attractor: validate iteration %d: %w", iter, err)
	}
	s.totalCost += valCost

	a.logger.Info("iteration result", "iteration", iter, "satisfaction", satisfaction, "failures", len(failures))

	return a.processValidation(iter, satisfaction, failures, files, s)
}

// processValidation handles post-validation logic: convergence, stall detection, checkpoint.
func (a *Attractor) processValidation(iter int, satisfaction float64, failures []string, files map[string]string, s *runState) (*RunResult, error) {
	s.lastOutcome = OutcomeValidated
	s.lastSatisfaction = satisfaction
	s.scoreHistory = append(s.scoreHistory, satisfaction)

	if satisfaction >= s.opts.Threshold {
		if err := writeFiles(s.bestDir, files); err != nil {
			return nil, fmt.Errorf("attractor: write best files: %w", err)
		}
		s.bestSatisfaction = satisfaction
		s.bestFiles = maps.Clone(files)
		return s.result(iter, StatusConverged), nil
	}

	if satisfaction > s.bestSatisfaction {
		s.bestSatisfaction = satisfaction
		s.bestFiles = maps.Clone(files)
		s.stallCount = 0
		s.patchRegressionCount = 0
		if err := writeFiles(s.bestDir, files); err != nil {
			return nil, fmt.Errorf("attractor: write best files: %w", err)
		}
	} else {
		s.stallCount++
		a.trackPatchRegression(iter, satisfaction, s)
	}

	s.history = append(s.history, iterationFeedback{
		iteration: iter,
		kind:      "validation",
		message:   formatValidationFeedback(satisfaction, failures),
	})

	if s.stallCount >= s.opts.StallLimit {
		return s.result(iter, StatusStalled), nil
	}

	if s.budgetExceeded() {
		return s.result(iter, StatusBudgetExceeded), nil
	}

	return nil, nil
}

// trackPatchRegression increments the regression counter when patch mode is active
// and satisfaction drops below the best. After 2 consecutive regressions, patch mode
// is disabled for the remainder of the run.
func (a *Attractor) trackPatchRegression(iter int, satisfaction float64, s *runState) {
	if !s.patchActive || satisfaction >= s.bestSatisfaction {
		return
	}
	s.patchRegressionCount++
	if s.patchRegressionCount >= 2 {
		a.logger.Info("patch mode disabled after consecutive regressions",
			"iteration", iter, "regression_count", s.patchRegressionCount)
		s.patchActive = false
	}
}

// checkStalled returns a stalled result if the stall limit is reached, nil otherwise.
func (a *Attractor) checkStalled(iter int, s *runState) *RunResult {
	if s.stallCount >= s.opts.StallLimit {
		return s.result(iter, StatusStalled)
	}
	return nil
}

// withDefaults fills in zero-value fields with sensible defaults.
func withDefaults(opts RunOptions) RunOptions {
	if opts.Threshold == 0 {
		opts.Threshold = 95
	}
	if opts.MaxIterations == 0 {
		opts.MaxIterations = 10
	}
	if opts.StallLimit == 0 {
		opts.StallLimit = 3
	}
	if opts.WorkspaceDir == "" {
		opts.WorkspaceDir = "./workspace"
	}
	if opts.HealthTimeout == 0 {
		opts.HealthTimeout = 30 * time.Second
	}
	return opts
}

// generateRunID returns a short random hex string for use as a run identifier.
func generateRunID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// buildSystemPrompt creates the system prompt containing the spec.
// This prompt is cached across iterations via CacheControl: ephemeral.
func buildSystemPrompt(spec string) string {
	return fmt.Sprintf(`You are a code generation agent. Your task is to generate a complete, working application based on the following specification.

SPECIFICATION:
%s

INSTRUCTIONS:
- Generate ALL files needed for a working application
- Include a Dockerfile that builds and runs the application on port 8080
- Output each file in this exact format:

=== FILE: path/to/file ===
file content here
=== END FILE ===

- Generate ONLY the file blocks, minimize explanatory text
- The application MUST listen on port 8080
- Include all dependencies and configuration files

DEPENDENCY RULES:
- ALWAYS prefer standard library over third-party dependencies. For Go: use net/http (not gorilla/mux), use crypto/rand or math/rand for UUIDs (not google/uuid), etc.
- NEVER generate lock files or checksum files (go.sum, package-lock.json, yarn.lock, etc.) — you cannot compute valid hashes; the build will fail
- For Go: generate only go.mod with no "require" block (or minimal requires). In the Dockerfile, COPY all source files first, THEN run "go mod tidy" to resolve dependencies, THEN build. Example Dockerfile order: COPY go.mod ./ then COPY . . then RUN go mod tidy then RUN go build
- For Node.js: generate only package.json; use "npm install" in the Dockerfile
- For Python: generate only requirements.txt; use "pip install" in the Dockerfile
- Let the package manager resolve and verify dependencies at build time`, spec)
}

// buildMessages constructs the user message for the current iteration.
// Iteration 1 gets a simple "Generate" prompt; subsequent iterations include
// the last 3 failure summaries for context.
func buildMessages(iter int, history []iterationFeedback) []llm.Message {
	if iter == 1 || len(history) == 0 {
		return []llm.Message{
			{Role: "user", Content: "Generate the application according to the specification. Output all files using the === FILE: path === format."},
		}
	}

	var b strings.Builder
	b.WriteString("The previous attempt did not fully satisfy the specification. Here is the feedback:\n\n")

	// Include last 3 feedback entries.
	start := max(len(history)-3, 0)
	for _, fb := range history[start:] {
		fmt.Fprintf(&b, "--- Iteration %d (%s) ---\n%s\n\n", fb.iteration, fb.kind, fb.message)
	}

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
		start := max(len(history)-3, 0)
		for _, fb := range history[start:] {
			fmt.Fprintf(&b, "--- Iteration %d (%s) ---\n%s\n\n", fb.iteration, fb.kind, fb.message)
		}
	}

	b.WriteString("Output ONLY the files that need to change using the === FILE: path === format. ")
	b.WriteString("For files you are not changing, you may emit === UNCHANGED: path === as a comment, but this is optional.")

	return []llm.Message{
		{Role: "user", Content: b.String()},
	}
}

// writeFiles writes the parsed file map to the given directory.
// Validates that all paths resolve within the directory to prevent path traversal.
func writeFiles(dir string, files map[string]string) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("attractor: abs dir: %w", err)
	}

	for path, content := range files {
		if err := writeOneFile(absDir, path, content); err != nil {
			return err
		}
	}
	return nil
}

// writeOneFile writes a single file to the given base directory, validating the path is contained.
func writeOneFile(absDir, path, content string) error {
	fullPath := filepath.Join(absDir, path)
	absPath, err := filepath.Abs(fullPath)
	if err != nil {
		return fmt.Errorf("attractor: abs path %s: %w", path, err)
	}
	if !strings.HasPrefix(absPath, absDir+string(filepath.Separator)) {
		return fmt.Errorf("%w: %s escapes workspace", errPathTraversal, path)
	}

	if err := os.MkdirAll(filepath.Dir(absPath), 0o750); err != nil {
		return fmt.Errorf("attractor: mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(absPath, []byte(content), 0o600); err != nil {
		return fmt.Errorf("attractor: write %s: %w", path, err)
	}
	return nil
}

// trySummarize attempts to parse and summarize the spec.
// Returns (nil, 0) on failure (non-fatal). Cost is returned even on partial success.
func (a *Attractor) trySummarize(ctx context.Context, rawSpec string) (*specpkg.SummarizedSpec, float64) {
	parsed, err := specpkg.Parse(strings.NewReader(rawSpec))
	if err != nil {
		a.logger.Warn("failed to parse spec for summarization", "error", err)
		return nil, 0
	}
	result, err := specpkg.Summarize(ctx, &parsed, a.llm, summarizeModel)
	if err != nil {
		a.logger.Warn("failed to summarize spec, using full spec", "error", err)
		return nil, 0
	}
	return result.Summary, result.CostUSD
}

// extractFailureStrings pulls failure messages from history for section matching.
func extractFailureStrings(history []iterationFeedback) []string {
	var failures []string
	for _, fb := range history {
		if fb.message != "" {
			failures = append(failures, fb.message)
		}
	}
	return failures
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
