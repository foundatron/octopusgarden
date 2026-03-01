package attractor

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/foundatron/octopusgarden/internal/container"
	"github.com/foundatron/octopusgarden/internal/llm"
)

var errEmptySpec = errors.New("attractor: spec content is empty")

// Status constants for RunResult.
const (
	StatusConverged      = "converged"
	StatusStalled        = "stalled"
	StatusBudgetExceeded = "budget_exceeded"
	StatusMaxIterations  = "max_iterations"
)

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
	runID            string
	opts             RunOptions
	baseDir          string
	bestDir          string
	totalCost        float64
	bestSatisfaction float64
	stallCount       int
	history          []iterationFeedback
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
func (a *Attractor) Run(ctx context.Context, spec string, opts RunOptions, validate ValidateFn) (*RunResult, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, errEmptySpec
	}

	opts = withDefaults(opts)
	s := &runState{
		runID:   generateRunID(),
		opts:    opts,
		baseDir: filepath.Join(opts.WorkspaceDir, generateRunID()),
		bestDir: "",
	}
	s.baseDir = filepath.Join(opts.WorkspaceDir, s.runID)
	s.bestDir = filepath.Join(s.baseDir, "best")

	for iter := 1; iter <= opts.MaxIterations; iter++ {
		a.logger.Info("iteration start", "run_id", s.runID, "iteration", iter, "cost_usd", s.totalCost, "best_satisfaction", s.bestSatisfaction)

		if s.budgetExceeded() {
			return s.result(iter-1, StatusBudgetExceeded), nil
		}

		result, err := a.iterate(ctx, spec, iter, s, validate)
		if err != nil {
			return nil, err
		}
		if result != nil {
			return result, nil
		}
	}

	return s.result(opts.MaxIterations, StatusMaxIterations), nil
}

// iterate runs a single iteration of the attractor loop.
// Returns (result, nil) for terminal conditions, (nil, nil) to continue, or (nil, err) for hard errors.
func (a *Attractor) iterate(ctx context.Context, spec string, iter int, s *runState, validate ValidateFn) (*RunResult, error) {
	// Generate code via LLM.
	genResp, err := a.llm.Generate(ctx, llm.GenerateRequest{
		SystemPrompt: buildSystemPrompt(spec),
		Messages:     buildMessages(iter, s.history),
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
		s.recordStall(iter, "parse_error", fmt.Sprintf("Failed to parse generated files: %s", err))
		return a.checkStalled(iter, s), nil
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
		s.recordStall(iter, "build_error", fmt.Sprintf("Docker build failed: %s", err))
		return a.checkStalled(iter, s), nil
	}

	url, stop, err := a.containerMgr.Run(ctx, tag)
	if err != nil {
		a.logger.Warn("container run failed", "iteration", iter, "error", err)
		s.recordStall(iter, "health_error", fmt.Sprintf("Container failed to start: %s", err))
		return a.checkStalled(iter, s), nil
	}

	if err := a.containerMgr.WaitHealthy(ctx, url, s.opts.HealthTimeout); err != nil {
		a.logger.Warn("health check failed", "iteration", iter, "error", err)
		stop()
		s.recordStall(iter, "health_error", fmt.Sprintf("Health check failed: %s", err))
		return a.checkStalled(iter, s), nil
	}

	satisfaction, failures, valCost, err := validate(ctx, url)
	stop()
	if err != nil {
		return nil, fmt.Errorf("attractor: validate iteration %d: %w", iter, err)
	}
	s.totalCost += valCost

	a.logger.Info("iteration result", "iteration", iter, "satisfaction", satisfaction, "failures", len(failures))

	return a.processValidation(iter, satisfaction, failures, files, s)
}

// processValidation handles post-validation logic: convergence, stall detection, checkpoint.
func (a *Attractor) processValidation(iter int, satisfaction float64, failures []string, files map[string]string, s *runState) (*RunResult, error) {
	if satisfaction >= s.opts.Threshold {
		if err := writeFiles(s.bestDir, files); err != nil {
			return nil, fmt.Errorf("attractor: write best files: %w", err)
		}
		result := s.result(iter, StatusConverged)
		result.Satisfaction = satisfaction
		return result, nil
	}

	if satisfaction > s.bestSatisfaction {
		s.bestSatisfaction = satisfaction
		s.stallCount = 0
		if err := writeFiles(s.bestDir, files); err != nil {
			return nil, fmt.Errorf("attractor: write best files: %w", err)
		}
	} else {
		s.stallCount++
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
	b := make([]byte, 4)
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
- Include all dependencies and configuration files`, spec)
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
	if !strings.HasPrefix(absPath, absDir+string(filepath.Separator)) && absPath != absDir {
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
