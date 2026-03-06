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
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/foundatron/octopusgarden/internal/container"
	"github.com/foundatron/octopusgarden/internal/llm"
	specpkg "github.com/foundatron/octopusgarden/internal/spec"
)

var (
	errEmptySpec           = errors.New("attractor: spec content is empty")
	errUnsupportedLanguage = errors.New("attractor: unsupported language")
)

// summarizeModel is the cheap model used for spec summarization.
// Haiku is also the default --judge-model for cost efficiency.
const summarizeModel = "claude-haiku-4-5"

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
	InputTokens      int
	OutputTokens     int
	Failures         []string
}

// ProgressFunc is called synchronously after each iteration completes.
// The attractor loop is single-goroutine — no mutex needed.
// Implementations must return promptly.
type ProgressFunc func(IterationProgress)

// ScenarioCapabilities describes what the loaded scenarios need from the container.
type ScenarioCapabilities struct {
	NeedsHTTP    bool // any scenario has request steps
	NeedsExec    bool // any scenario has exec steps
	NeedsBrowser bool // any scenario has browser steps
	NeedsGRPC    bool // any scenario has grpc steps
	NeedsWS      bool // any scenario has ws steps (modifier on NeedsHTTP — same port 8080)
}

// ContainerManager is the interface to Docker container operations.
// *container.Manager satisfies this automatically.
type ContainerManager interface {
	Build(ctx context.Context, dir, tag string) error
	Run(ctx context.Context, tag string) (url string, stop container.StopFunc, err error)
	RunMultiPort(ctx context.Context, tag string, extraPorts []string) (container.RunResult, container.StopFunc, error)
	WaitHealthy(ctx context.Context, url string, timeout time.Duration) error
	WaitPort(ctx context.Context, addr string, timeout time.Duration) error
	StartSession(ctx context.Context, tag string) (session *container.Session, stop container.StopFunc, err error)
}

// ValidateFn runs holdout scenarios against a running container and returns results.
// The attractor never imports internal/scenario — the CLI provides this closure.
type ValidateFn func(ctx context.Context, url string) (satisfaction float64, failures []string, cost float64, err error)

// Attractor orchestrates the convergence loop: generate code → build → validate → iterate.
type Attractor struct {
	llm          llm.Client
	containerMgr ContainerManager
	logger       *slog.Logger
	tracer       trace.Tracer
}

// RunOptions configures the attractor loop.
type RunOptions struct {
	Model         string
	Language      string               // language hint: "go", "python", "node", "rust", or "" (auto)
	BudgetUSD     float64              // 0 = unlimited
	Threshold     float64              // default 95
	MaxIterations int                  // default 10
	StallLimit    int                  // default 3
	WorkspaceDir  string               // default "./workspace"
	HealthTimeout time.Duration        // default 30s
	Progress      ProgressFunc         // optional per-iteration callback
	PatchMode     bool                 // if true, iteration 2+ sends prev best files + failures
	ContextBudget int                  // max estimated tokens for spec in system prompt; 0 = unlimited
	Capabilities  ScenarioCapabilities // detected from loaded scenarios
	Genes         string               // extracted pattern guide to inject into system prompt (empty = no genes)
	GeneLanguage  string               // source language of the gene exemplar (for cross-language note)
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

// SessionProviderFn is called by the attractor to set the current container session.
// The CLI captures this and passes it to the ExecExecutor.
type SessionProviderFn func(session *container.Session)

// GRPCTargetProviderFn is called by the attractor to set the current gRPC target address.
// The CLI captures this and passes it to the GRPCExecutor.
type GRPCTargetProviderFn func(target string)

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
	lastInputTokens      int
	lastOutputTokens     int
	lastFailures         []string
	startTime            time.Time
	bestFiles            map[string]string       // files from the best-scoring iteration
	patchActive          bool                    // patch mode currently in effect (may disable on regression)
	patchRegressionCount int                     // consecutive regressions while patch mode active
	summarized           *specpkg.SummarizedSpec // nil if spec fits budget or budget is 0
	sessionProvider      SessionProviderFn       // callback to set the current session
	grpcTargetProvider   GRPCTargetProviderFn    // callback to set the gRPC target
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
		InputTokens:      s.lastInputTokens,
		OutputTokens:     s.lastOutputTokens,
		Failures:         s.lastFailures,
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
// A nil TracerProvider defaults to a noop provider (zero overhead).
func New(client llm.Client, containerMgr ContainerManager, logger *slog.Logger, tp trace.TracerProvider) *Attractor {
	if tp == nil {
		tp = noop.NewTracerProvider()
	}
	return &Attractor{
		llm:          client,
		containerMgr: containerMgr,
		logger:       logger,
		tracer:       tp.Tracer("octog/attractor"),
	}
}

// Run executes the attractor convergence loop.
// spec is the specification content (never scenario content — holdout isolation).
// validate is a closure provided by the CLI that runs scenarios and judges satisfaction.
// sessionProvider is an optional callback called before validation to set the container session.
// grpcTargetProvider is an optional callback called before validation to set the gRPC target.
// Returns a RunResult for normal termination, or an error for unrecoverable failures.
func (a *Attractor) Run(ctx context.Context, rawSpec string, opts RunOptions, validate ValidateFn, sessionProvider SessionProviderFn, grpcTargetProvider GRPCTargetProviderFn) (*RunResult, error) {
	if strings.TrimSpace(rawSpec) == "" {
		return nil, errEmptySpec
	}
	if opts.Language != "" {
		if _, ok := LookupLanguage(opts.Language); !ok {
			return nil, fmt.Errorf("%w: %q (supported: %v)", errUnsupportedLanguage, opts.Language, SupportedLanguages())
		}
	}

	if sessionProvider == nil {
		sessionProvider = func(_ *container.Session) {} // no-op
	}
	if grpcTargetProvider == nil {
		grpcTargetProvider = func(_ string) {} // no-op
	}

	opts = withDefaults(opts)
	s := &runState{
		runID:              generateRunID(),
		opts:               opts,
		startTime:          time.Now(),
		patchActive:        opts.PatchMode,
		sessionProvider:    sessionProvider,
		grpcTargetProvider: grpcTargetProvider,
	}
	s.baseDir = filepath.Join(opts.WorkspaceDir, s.runID)
	s.bestDir = filepath.Join(s.baseDir, "best")

	ctx, runSpan := a.tracer.Start(ctx, "attractor.run", trace.WithAttributes(
		attribute.String("run_id", s.runID),
		attribute.Float64("budget_usd", opts.BudgetUSD),
		attribute.Float64("threshold", opts.Threshold),
		attribute.Int("max_iterations", opts.MaxIterations),
	))
	defer runSpan.End()

	// Conditionally summarize large specs for context budget management.
	if opts.ContextBudget > 0 && specpkg.EstimateTokens(rawSpec) > opts.ContextBudget {
		summarized, summarizeCost := a.trySummarize(ctx, rawSpec)
		s.summarized = summarized
		s.totalCost += summarizeCost
	}

	for iter := 1; iter <= opts.MaxIterations; iter++ {
		a.logger.Info("iteration start", "run_id", s.runID, "iteration", iter, "cost_usd", s.totalCost, "best_satisfaction", s.bestSatisfaction)

		if s.budgetExceeded() {
			result := s.result(iter-1, StatusBudgetExceeded)
			a.setRunSpanAttrs(runSpan, result)
			return result, nil
		}

		costBefore := s.totalCost

		iterCtx, iterSpan := a.tracer.Start(ctx, "attractor.iteration", trace.WithAttributes(
			attribute.String("run_id", s.runID),
			attribute.Int("iteration", iter),
		))
		result, err := a.iterate(iterCtx, rawSpec, iter, s, validate)
		if err != nil {
			iterSpan.RecordError(err)
			iterSpan.SetStatus(codes.Error, err.Error())
			iterSpan.End()
			runSpan.RecordError(err)
			runSpan.SetStatus(codes.Error, err.Error())
			return nil, err
		}
		iterSpan.SetAttributes(
			attribute.String("outcome", string(s.lastOutcome)),
			attribute.Float64("satisfaction", s.lastSatisfaction),
			attribute.Float64("iteration_cost_usd", s.totalCost-costBefore),
		)
		iterSpan.End()

		if opts.Progress != nil {
			opts.Progress(s.buildProgress(iter, costBefore))
		}

		if result != nil {
			a.setRunSpanAttrs(runSpan, result)
			return result, nil
		}
	}

	result := s.result(opts.MaxIterations, StatusMaxIterations)
	a.setRunSpanAttrs(runSpan, result)
	return result, nil
}

// setRunSpanAttrs sets final attributes on the attractor.run span.
func (a *Attractor) setRunSpanAttrs(span trace.Span, result *RunResult) {
	span.SetAttributes(
		attribute.String("status", result.Status),
		attribute.Int("iterations", result.Iterations),
		attribute.Float64("satisfaction", result.Satisfaction),
		attribute.Float64("cost_usd", result.CostUSD),
	)
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
		SystemPrompt: buildSystemPrompt(specContent, s.opts.Capabilities, s.opts.Language, s.opts.Genes, s.opts.GeneLanguage),
		Messages:     messages,
		Model:        s.opts.Model,
		CacheControl: &llm.CacheControl{Type: "ephemeral"},
	})
	if err != nil {
		return nil, fmt.Errorf("attractor: generate iteration %d: %w", iter, err)
	}
	s.totalCost += genResp.CostUSD
	s.lastInputTokens = genResp.InputTokens
	s.lastOutputTokens = genResp.OutputTokens
	s.lastFailures = nil

	// Parse files from LLM output.
	files, err := ParseFiles(genResp.Content)
	if err != nil {
		a.logger.Warn("parse files failed", "iteration", iter, "error", err)
		s.lastOutcome = OutcomeParseFail
		s.lastSatisfaction = 0
		s.recordStall(iter, feedbackParseError, fmt.Sprintf("Failed to parse generated files: %s", err))
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

// buildRunValidate handles the Docker build → container setup → validate pipeline.
// Capabilities drive the container strategy:
//   - Always: build image, start session container
//   - NeedsHTTP: also Run (expose port 8080), WaitHealthy
//   - NeedsExec: pass session to validate via sessionProvider
func (a *Attractor) buildRunValidate(ctx context.Context, iter int, iterDir string, files map[string]string, s *runState, validate ValidateFn) (*RunResult, error) {
	tag := fmt.Sprintf("og-%s-iter%d", s.runID, iter)
	if err := a.containerMgr.Build(ctx, iterDir, tag); err != nil {
		a.logger.Warn("build failed", "iteration", iter, "error", err)
		s.lastOutcome = OutcomeBuildFail
		s.lastSatisfaction = 0
		s.recordStall(iter, feedbackBuildError, fmt.Sprintf("Docker build failed: %s", err))
		return a.checkStalled(iter, s), nil
	}

	caps := s.opts.Capabilities
	var url string

	// When both HTTP and exec capabilities are needed, two containers run from the same image:
	// - Session container: runs "sleep infinity" for docker exec commands
	// - HTTP container: runs the image's CMD to serve HTTP on port 8080
	if caps.NeedsExec {
		session, stopSession, err := a.containerMgr.StartSession(ctx, tag)
		if err != nil {
			a.logger.Warn("session start failed", "iteration", iter, "error", err)
			s.lastOutcome = OutcomeRunFail
			s.lastSatisfaction = 0
			s.recordStall(iter, feedbackRunError, fmt.Sprintf("Session container failed to start: %s", err))
			return a.checkStalled(iter, s), nil
		}
		defer stopSession()
		s.sessionProvider(session)
		defer s.sessionProvider(nil) // clear session after validation
	}

	switch {
	case caps.NeedsGRPC:
		res, err := a.startGRPCContainer(ctx, iter, tag, caps, s)
		if err != nil {
			return nil, err
		}
		if res.stalled != nil {
			return res.stalled, nil
		}
		defer res.stop()
		url = res.url
		s.grpcTargetProvider(res.grpcTarget)
		defer s.grpcTargetProvider("") // clear after validation
	case caps.NeedsHTTP || caps.NeedsBrowser || !caps.NeedsExec:
		// If only HTTP needed, or no capabilities detected (legacy), use Run + WaitHealthy.
		var stop container.StopFunc
		var err error
		url, stop, err = a.containerMgr.Run(ctx, tag)
		if err != nil {
			a.logger.Warn("container run failed", "iteration", iter, "error", err)
			s.lastOutcome = OutcomeRunFail
			s.lastSatisfaction = 0
			s.recordStall(iter, feedbackRunError, fmt.Sprintf("Container failed to start: %s", err))
			return a.checkStalled(iter, s), nil
		}
		defer stop()

		if err := a.containerMgr.WaitHealthy(ctx, url, s.opts.HealthTimeout); err != nil {
			a.logger.Warn("health check failed", "iteration", iter, "error", err)
			s.lastOutcome = OutcomeHealthFail
			s.lastSatisfaction = 0
			s.recordStall(iter, feedbackHealthError, fmt.Sprintf("Health check failed: %s", err))
			return a.checkStalled(iter, s), nil
		}
	}

	satisfaction, failures, valCost, err := validate(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("attractor: validate iteration %d: %w", iter, err)
	}
	s.totalCost += valCost
	s.lastFailures = failures

	a.logger.Info("iteration result", "iteration", iter, "satisfaction", satisfaction, "failures", len(failures))

	return a.processValidation(iter, satisfaction, failures, files, s)
}

// grpcContainerResult holds the outputs of startGRPCContainer.
type grpcContainerResult struct {
	url        string
	stop       container.StopFunc
	grpcTarget string
	stalled    *RunResult // non-nil if startup failed (stall, not error)
}

// startGRPCContainer launches a container with gRPC port exposed and waits for readiness.
// The caller must defer result.stop() when result.stalled is nil.
func (a *Attractor) startGRPCContainer(ctx context.Context, iter int, tag string, caps ScenarioCapabilities, s *runState) (grpcContainerResult, error) {
	runResult, stop, err := a.containerMgr.RunMultiPort(ctx, tag, []string{container.DefaultGRPCPort})
	if err != nil {
		a.logger.Warn("container run failed", "iteration", iter, "error", err)
		s.lastOutcome = OutcomeRunFail
		s.lastSatisfaction = 0
		s.recordStall(iter, feedbackRunError, fmt.Sprintf("Container failed to start: %s", err))
		return grpcContainerResult{stalled: a.checkStalled(iter, s)}, nil
	}

	grpcTarget := runResult.ExtraPorts[container.DefaultGRPCPort]
	if stallResult := a.waitGRPCHealth(ctx, iter, caps, runResult.URL, grpcTarget, s); stallResult != nil {
		stop()
		return grpcContainerResult{stalled: stallResult}, nil
	}

	return grpcContainerResult{url: runResult.URL, stop: stop, grpcTarget: grpcTarget}, nil
}

// waitGRPCHealth waits for either HTTP or gRPC port readiness depending on capabilities.
// Returns a stall result if health check fails, nil on success.
func (a *Attractor) waitGRPCHealth(ctx context.Context, iter int, caps ScenarioCapabilities, url, grpcTarget string, s *runState) *RunResult {
	needsHTTP := caps.NeedsHTTP || caps.NeedsBrowser
	if needsHTTP {
		if err := a.containerMgr.WaitHealthy(ctx, url, s.opts.HealthTimeout); err != nil {
			a.logger.Warn("health check failed", "iteration", iter, "error", err)
			s.lastOutcome = OutcomeHealthFail
			s.lastSatisfaction = 0
			s.recordStall(iter, feedbackHealthError, fmt.Sprintf("Health check failed: %s", err))
			return a.checkStalled(iter, s)
		}
		return nil
	}
	if grpcTarget != "" {
		if err := a.containerMgr.WaitPort(ctx, grpcTarget, s.opts.HealthTimeout); err != nil {
			a.logger.Warn("gRPC port health check failed", "iteration", iter, "error", err)
			s.lastOutcome = OutcomeHealthFail
			s.lastSatisfaction = 0
			s.recordStall(iter, feedbackHealthError, fmt.Sprintf("gRPC port health check failed: %s", err))
			return a.checkStalled(iter, s)
		}
	}
	return nil
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
		kind:      feedbackValidation,
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
