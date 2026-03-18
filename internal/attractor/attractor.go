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
	"github.com/foundatron/octopusgarden/internal/gene"
	"github.com/foundatron/octopusgarden/internal/llm"
	specpkg "github.com/foundatron/octopusgarden/internal/spec"
)

var (
	errEmptySpec            = errors.New("attractor: spec content is empty")
	errUnsupportedLanguage  = errors.New("attractor: unsupported language")
	errMinimalismNoMessages = errors.New("attractor: minimalism suffix: no messages to append to")
	errAgentClientRequired  = errors.New("attractor: agentic mode requires AgentClient implementation")
	errComponentFallback    = errors.New("attractor: component convergence failed, falling back to monolithic")
	errComponentStalled     = errors.New("attractor: component stalled")
	errComponentBudget      = errors.New("attractor: budget exceeded during component convergence")
	errComponentBuildFail   = errors.New("attractor: component build failed")
	errComponentSessionFail = errors.New("attractor: component session start failed")
	errComponentStartFail   = errors.New("attractor: component container start failed")
	errUnknownDependency    = errors.New("attractor: component depends on unknown component")

	_ ContainerManager = (*container.Manager)(nil)
)

// summarizeModel is the cheap model used for spec summarization.
// Haiku is also the default --judge-model for cost efficiency.
const summarizeModel = "claude-haiku-4-5"

// defaultAgentMaxTurns is the default maximum number of agent turns per iteration
// when no AgentMaxTurns is specified in RunOptions.
const defaultAgentMaxTurns = 50

// maxStratifiedTier is the highest difficulty tier in stratified validation mode.
// Tiers progress 1 -> 2 -> 3; convergence at tier 3 ends the run.
const maxStratifiedTier = 3

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
	OutcomeTestFail   IterationOutcome = "test_fail"
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
	Model            string // model used for generation in this iteration
	Turns            int    // number of agent turns used (0 for non-agentic iterations)
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
	NeedsTUI     bool // any scenario has tui steps (Unix only)
}

// ContainerManager is the interface to Docker container operations.
// *container.Manager satisfies this automatically.
type ContainerManager interface {
	Build(ctx context.Context, dir, tag string) error
	Run(ctx context.Context, tag string) (container.RunResult, container.StopFunc, error)
	RunMultiPort(ctx context.Context, tag string, extraPorts []string) (container.RunResult, container.StopFunc, error)
	RunTest(ctx context.Context, containerID, command string) (container.ExecResult, error)
	WaitHealthy(ctx context.Context, url string, timeout time.Duration) error
	WaitPort(ctx context.Context, addr string, timeout time.Duration) error
	StartSession(ctx context.Context, tag string) (session *container.Session, stop container.StopFunc, err error)
}

// RestartFunc stops the current container, starts a fresh one, and returns the new URL.
// It is provided to ValidateFn so that a scenario can trigger a clean restart between runs.
type RestartFunc func(ctx context.Context) (newURL string, err error)

// ValidateFn runs holdout scenarios against a running container and returns results.
// The attractor never imports internal/scenario — the CLI provides this closure.
// restart may be called to stop the current container and start a fresh one between scenarios.
// restart is nil for gRPC and exec-only paths that do not support container restart.
// maxTier, when > 0, restricts validation to scenarios with Tier <= maxTier (stratified mode).
// maxTier == 0 means run all scenarios (non-stratified or component validators).
type ValidateFn func(ctx context.Context, url string, restart RestartFunc, maxTier int) (satisfaction float64, failures []string, cost float64, err error)

// Attractor orchestrates the convergence loop: generate code → build → validate → iterate.
type Attractor struct {
	llm          llm.Client
	containerMgr ContainerManager
	logger       *slog.Logger
	tracer       trace.Tracer
}

// RunOptions configures the attractor loop.
type RunOptions struct {
	Model               string
	FrugalModel         string                // optional cheaper model to start with; escalates to Model after consecutive failures
	JudgeModel          string                // model used for the wonder phase diagnosis; falls back to Model when empty
	Language            string                // language hint: "go", "python", "node", "rust", or "" (auto)
	BudgetUSD           float64               // 0 = unlimited
	Threshold           float64               // default 95
	MaxIterations       int                   // default 10
	StallLimit          int                   // default 3
	WorkspaceDir        string                // default "./workspace"
	HealthTimeout       time.Duration         // default 30s
	Progress            ProgressFunc          // optional per-iteration callback
	PatchMode           bool                  // if true, iteration 2+ sends prev best files + failures
	BlockOnRegression   bool                  // if true, convergence is blocked when per-scenario regressions are detected
	ContextBudget       int                   // max estimated tokens for spec in system prompt; 0 = unlimited
	Capabilities        ScenarioCapabilities  // detected from loaded scenarios
	Genes               string                // extracted pattern guide to inject into system prompt (empty = no genes)
	GeneLanguage        string                // source language of the gene exemplar (for cross-language note)
	TestCommand         string                // optional shell command run inside HTTP container after health check; non-zero exit = test_fail
	MaxTokens           int                   // max output tokens for generation; 0 = auto-scale per model
	Agentic             bool                  // if true, use AgentLoop for code generation (tool-use mode)
	AgentMaxTurns       int                   // max turns per AgentLoop call; 0 = default (50 when Agentic is true)
	Stratified          bool                  // if true, validate by ascending difficulty tier (1→2→3), converging each before advancing
	GeneComponents      []gene.Component      // structured component decomposition from gene extraction
	ComponentValidators map[string]ValidateFn // per-component validators; "" key = integration validator
}

// RunResult holds the outcome of an attractor run.
type RunResult struct {
	RunID        string
	Iterations   int
	Satisfaction float64
	CostUSD      float64
	TotalTokens  int
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
	runID                  string
	opts                   RunOptions
	baseDir                string
	bestDir                string
	totalCost              float64
	totalTokens            int
	bestSatisfaction       float64
	stallCount             int
	history                []iterationFeedback
	scoreHistory           []float64
	lastOutcome            IterationOutcome
	lastSatisfaction       float64
	lastImproved           bool // true when the last validated iteration beat bestSatisfaction
	lastInputTokens        int
	lastOutputTokens       int
	lastFailures           []string
	startTime              time.Time
	bestFiles              map[string]string       // files from the best-scoring iteration
	patchActive            bool                    // patch mode currently in effect (may disable on regression)
	patchRegressionCount   int                     // consecutive regressions while patch mode active
	summarized             *specpkg.SummarizedSpec // nil if spec fits budget or budget is 0
	sessionProvider        SessionProviderFn       // callback to set the current session
	grpcTargetProvider     GRPCTargetProviderFn    // callback to set the gRPC target
	scenarioScores         map[string]float64      // per-scenario scores from the last validated iteration
	scenarioScoreIteration int                     // iteration number corresponding to scenarioScores
	codeHashes             []string                // SHA-256 hashes of generated file sets, in iteration order
	escalation             *escalationState        // nil when FrugalModel is empty (escalation disabled)
	lastTurns              int                     // number of agent turns used in the last iteration (0 for non-agentic)
	activeTier             int                     // 0 = non-stratified; 1-3 = current difficulty tier in stratified mode
}

// currentModel returns the model to use for generation, respecting escalation state.
// Returns opts.Model directly when escalation is disabled.
func (s *runState) currentModel() string {
	if s.escalation != nil {
		return s.escalation.currentModel()
	}
	return s.opts.Model
}

func (s *runState) result(iter int, status string) *RunResult {
	return &RunResult{
		RunID:        s.runID,
		Iterations:   iter,
		Satisfaction: s.bestSatisfaction,
		CostUSD:      s.totalCost,
		TotalTokens:  s.totalTokens,
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
		Model:            s.currentModel(),
		Turns:            s.lastTurns,
	}
}

func (s *runState) budgetExceeded() bool {
	return s.opts.BudgetUSD > 0 && s.totalCost >= s.opts.BudgetUSD
}

// newRunState creates a runState for the monolithic loop, optionally seeding
// history with composed failure context so the first iteration sees it.
func newRunState(opts RunOptions, composedCost float64, composedContext string, sessionProvider SessionProviderFn, grpcTargetProvider GRPCTargetProviderFn, logger *slog.Logger) *runState {
	s := &runState{
		runID:              generateRunID(),
		opts:               opts,
		startTime:          time.Now(),
		patchActive:        opts.PatchMode,
		sessionProvider:    sessionProvider,
		grpcTargetProvider: grpcTargetProvider,
		escalation:         newEscalationState(opts.FrugalModel, opts.Model, logger),
		totalCost:          composedCost,
		activeTier:         initialActiveTier(opts.Stratified),
	}
	if composedContext != "" {
		s.history = append(s.history, iterationFeedback{
			iteration: 0,
			kind:      feedbackBuildError,
			message:   composedContext,
		})
	}
	return s
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
	if err := validateRunInputs(rawSpec, opts); err != nil {
		return nil, err
	}

	if sessionProvider == nil {
		sessionProvider = func(_ *container.Session) {} // no-op
	}
	if grpcTargetProvider == nil {
		grpcTargetProvider = func(_ string) {} // no-op
	}

	opts = withDefaults(opts)

	// Try composed convergence when gene components and component validators are available.
	// composedCost carries costs from the composed attempt even on fallback, so the
	// monolithic loop starts with an accurate budget accounting.
	// composedContext carries error context from the composed attempt for seeding monolithic history.
	composedResult, composedCost, composedContext, err := a.tryComposed(ctx, rawSpec, opts, sessionProvider, grpcTargetProvider)
	if err != nil {
		return nil, err
	}
	if composedResult != nil {
		return composedResult, nil
	}

	s := newRunState(opts, composedCost, composedContext, sessionProvider, grpcTargetProvider, a.logger)
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
		a.logger.Debug("iteration start", "run_id", s.runID, "iteration", iter, "cost_usd", s.totalCost, "best_satisfaction", s.bestSatisfaction)

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

		if s.escalation != nil {
			s.escalation.recordOutcome(s.lastImproved, a.logger)
		}

		// In stratified mode, converging a tier advances to the next; otherwise return result.
		if result != nil && !a.advanceTierIfNeeded(result, s) {
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

// validateRunInputs validates inputs to Run before the loop starts.
func validateRunInputs(rawSpec string, opts RunOptions) error {
	if strings.TrimSpace(rawSpec) == "" {
		return errEmptySpec
	}
	if opts.Language != "" {
		if _, ok := LookupLanguage(opts.Language); !ok {
			return fmt.Errorf("%w: %q (supported: %v)", errUnsupportedLanguage, opts.Language, SupportedLanguages())
		}
	}
	return nil
}

// tryComposed attempts composed convergence if gene components and validators are available.
// Returns (nil, 0, "", nil) to fall through to the monolithic loop.
// The returned cost reflects LLM and validation spend during the composed attempt,
// even when falling back, so callers can seed the monolithic budget correctly.
// composedContext is non-empty when falling back with error context for the monolithic loop.
func (a *Attractor) tryComposed(ctx context.Context, rawSpec string, opts RunOptions, sessionProvider SessionProviderFn, grpcTargetProvider GRPCTargetProviderFn) (*RunResult, float64, string, error) {
	if len(opts.GeneComponents) == 0 || len(opts.ComponentValidators) == 0 {
		return nil, 0, "", nil
	}
	if opts.Agentic {
		a.logger.Warn("composed convergence skipped: not supported in agentic mode, falling back to monolithic")
		return nil, 0, "", nil
	}
	result, cost, err := a.runComposed(ctx, rawSpec, opts, sessionProvider, grpcTargetProvider)
	if err != nil && !errors.Is(err, errComponentFallback) {
		return nil, cost, "", err
	}
	if result != nil {
		return result, cost, "", nil
	}
	a.logger.Info("composed convergence failed, falling back to monolithic loop")
	composedContext := ""
	if err != nil {
		composedContext = fmt.Sprintf("A prior composed build attempt failed: %s", err)
	}
	return nil, cost, composedContext, nil
}

// Component mini-loop tuning constants.
const (
	// componentMiniLoopMaxIter is the maximum iterations for a per-component convergence mini-loop.
	componentMiniLoopMaxIter = 5
	// componentMiniLoopStallLimit is the stall limit for per-component mini-loops.
	componentMiniLoopStallLimit = 2
)

// runComposed attempts composed convergence: converge each component independently
// in topological order, then validate the composed result with integration scenarios.
// Returns the accumulated cost even on fallback so callers can track total spend.
func (a *Attractor) runComposed(ctx context.Context, rawSpec string, opts RunOptions, sessionProvider SessionProviderFn, grpcTargetProvider GRPCTargetProviderFn) (*RunResult, float64, error) {
	sorted, err := topoSort(opts.GeneComponents)
	if err != nil {
		return nil, 0, fmt.Errorf("attractor: composed convergence: %w", err)
	}

	s := &runState{
		runID:              generateRunID(),
		opts:               opts,
		startTime:          time.Now(),
		sessionProvider:    sessionProvider,
		grpcTargetProvider: grpcTargetProvider,
	}
	s.baseDir = filepath.Join(opts.WorkspaceDir, s.runID)
	s.bestDir = filepath.Join(s.baseDir, "best")

	ctx, composedSpan := a.tracer.Start(ctx, "attractor.composed.run", trace.WithAttributes(
		attribute.String("run_id", s.runID),
		attribute.Int("components", len(sorted)),
	))
	defer composedSpan.End()

	accumulatedFiles := make(map[string]string)
	componentFiles := make(map[string]map[string]string, len(sorted))
	depInterfaces := make(map[string]string, len(sorted))

	for _, comp := range sorted {
		if s.budgetExceeded() {
			composedSpan.SetStatus(codes.Error, "budget exceeded")
			return nil, s.totalCost, errComponentFallback
		}

		compFiles, compErr := a.convergeComponent(ctx, rawSpec, comp, depInterfaces, accumulatedFiles, s)
		if compErr != nil {
			a.logger.Warn("component convergence failed, falling back to monolithic",
				"component", comp.Name, "error", compErr)
			composedSpan.RecordError(compErr)
			composedSpan.SetStatus(codes.Error, compErr.Error())
			return nil, s.totalCost, errComponentFallback
		}
		componentFiles[comp.Name] = compFiles
		maps.Copy(accumulatedFiles, compFiles)
		depInterfaces[comp.Name] = comp.Interface
	}

	// Merge all component files into composed output (later topo order wins on overlap).
	composedFiles := mergeComponentFiles(sorted, componentFiles, a.logger)

	// Ensure build infrastructure exists — components generate only source code,
	// so Dockerfile and go.mod may be missing. Synthesize from the language template.
	ensureBuildInfrastructure(composedFiles, s.opts.Language, s.opts.Capabilities, a.logger)

	result, err := a.validateComposed(ctx, composedFiles, s)
	if err != nil {
		composedSpan.RecordError(err)
		if !errors.Is(err, errComponentFallback) {
			composedSpan.SetStatus(codes.Error, err.Error())
		}
		return nil, s.totalCost, err
	}
	composedSpan.SetAttributes(attribute.Float64("cost_usd", s.totalCost))
	return result, s.totalCost, nil
}

// mergeComponentFiles merges files from all components in topo order. Later components
// win on file path conflicts. Overlaps are logged.
func mergeComponentFiles(sorted []gene.Component, componentFiles map[string]map[string]string, logger *slog.Logger) map[string]string {
	composed := make(map[string]string)
	for _, comp := range sorted {
		for path, content := range componentFiles[comp.Name] {
			if _, exists := composed[path]; exists {
				logger.Debug("file overlap in composed merge, later component wins",
					"path", path, "component", comp.Name)
			}
			composed[path] = content
		}
	}
	return composed
}

// ensureBuildInfrastructure adds a synthetic Dockerfile (and go.mod for Go) to composedFiles
// when components did not generate them. Components generate only source code; build
// infrastructure is synthesized deterministically from the language template.
func ensureBuildInfrastructure(composedFiles map[string]string, language string, caps ScenarioCapabilities, logger *slog.Logger) {
	if _, hasDockerfile := composedFiles["Dockerfile"]; hasDockerfile {
		return
	}

	tmpl, ok := LookupLanguage(language)
	if !ok {
		logger.Warn("no language template for synthetic Dockerfile", "language", language)
		return
	}

	// Select the Dockerfile template matching the app's capability profile.
	// TUI and CLI apps need the binary installed in PATH for exec steps;
	// HTTP apps use CMD to start the server.
	composedFiles["Dockerfile"] = selectExampleBlock(tmpl, caps).Dockerfile
	logger.Info("synthesized Dockerfile for composed build", "language", language)

	// For Go, ensure go.mod exists so `go mod tidy` can resolve dependencies.
	if language == "go" {
		if _, hasGoMod := composedFiles["go.mod"]; !hasGoMod {
			composedFiles["go.mod"] = "module app\n\ngo 1.25\n"
			logger.Info("synthesized go.mod for composed build")
		}
	}
}

// validateComposed writes composed files, builds, runs, and validates with integration scenarios.
func (a *Attractor) validateComposed(ctx context.Context, composedFiles map[string]string, s *runState) (*RunResult, error) {
	ctx, span := a.tracer.Start(ctx, "attractor.composed.validate", trace.WithAttributes(
		attribute.String("run_id", s.runID),
	))
	defer span.End()

	integrationDir := filepath.Join(s.baseDir, "composed")
	if err := writeFiles(integrationDir, composedFiles); err != nil {
		return nil, fmt.Errorf("attractor: write composed files: %w", err)
	}

	integrationValidate := s.opts.ComponentValidators[""]
	if integrationValidate == nil {
		// No integration scenarios: composed result is accepted.
		s.bestSatisfaction = 100
		s.bestFiles = maps.Clone(composedFiles)
		if err := writeFiles(s.bestDir, composedFiles); err != nil {
			return nil, fmt.Errorf("attractor: write best composed: %w", err)
		}
		span.SetAttributes(attribute.String("outcome", "accepted_no_integration"))
		return s.result(0, StatusConverged), nil
	}

	tag := fmt.Sprintf("og-%s-composed", s.runID)
	if err := a.containerMgr.Build(ctx, integrationDir, tag); err != nil {
		a.logger.Warn("composed build failed", "error", err)
		return nil, errComponentFallback
	}

	caps := s.opts.Capabilities

	if caps.NeedsExec || caps.NeedsTUI {
		session, stopSession, err := a.containerMgr.StartSession(ctx, tag)
		if err != nil {
			return nil, errComponentFallback
		}
		defer stopSession()
		s.sessionProvider(session)
		defer s.sessionProvider(nil)
	}

	url, stop, err := a.startComposedContainer(ctx, tag, caps, s)
	if err != nil {
		return nil, err
	}
	if stop != nil {
		defer stop()
	}

	satisfaction, failures, valCost, err := integrationValidate(ctx, url, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("attractor: integration validate: %w", err)
	}
	s.totalCost += valCost

	span.SetAttributes(
		attribute.Float64("satisfaction", satisfaction),
		attribute.Float64("cost_usd", valCost),
	)

	if satisfaction >= s.opts.Threshold {
		s.bestSatisfaction = satisfaction
		s.bestFiles = maps.Clone(composedFiles)
		if wErr := writeFiles(s.bestDir, composedFiles); wErr != nil {
			return nil, fmt.Errorf("attractor: write best composed: %w", wErr)
		}
		return s.result(0, StatusConverged), nil
	}

	a.logger.Info("integration validation failed", "satisfaction", satisfaction, "failures_count", len(failures))
	span.SetStatus(codes.Error, "integration validation failed")
	return nil, errComponentFallback
}

// startComposedContainer starts the appropriate container type for integration validation.
func (a *Attractor) startComposedContainer(ctx context.Context, tag string, caps ScenarioCapabilities, s *runState) (url string, stop func(), err error) {
	switch {
	case caps.NeedsGRPC:
		res, gErr := a.startGRPCContainer(ctx, 0, tag, caps, s)
		if gErr != nil {
			return "", nil, errComponentFallback
		}
		if res.stop == nil {
			return "", nil, errComponentFallback
		}
		s.grpcTargetProvider(res.grpcTarget)
		return res.url, func() { res.stop(); s.grpcTargetProvider("") }, nil
	case caps.NeedsTUI && !caps.NeedsHTTP && !caps.NeedsGRPC:
		// TUI-only: no container service needed.
		return "", nil, nil
	case caps.NeedsHTTP || caps.NeedsBrowser || !caps.NeedsExec:
		res, hErr := a.startHTTPContainer(ctx, 0, tag, s.opts.HealthTimeout, s)
		if hErr != nil {
			return "", nil, errComponentFallback
		}
		if res.stop == nil {
			return "", nil, errComponentFallback
		}
		return res.url, res.stop, nil
	default:
		return "", nil, nil
	}
}

// componentLoopState holds local mutable state for a component mini-loop.
// Isolated from the shared runState to prevent corruption (only totalCost is shared).
type componentLoopState struct {
	history    []iterationFeedback
	stallCount int
	bestScore  float64
	bestFiles  map[string]string
}

// convergeComponent runs a mini convergence loop for a single component.
// Uses local state for history/stallCount/etc. Only totalCost accumulates into s.
func (a *Attractor) convergeComponent(ctx context.Context, rawSpec string, comp gene.Component, depInterfaces map[string]string, baseFiles map[string]string, s *runState) (map[string]string, error) {
	ctx, span := a.tracer.Start(ctx, "attractor.composed.component", trace.WithAttributes(
		attribute.String("component", comp.Name),
		attribute.String("run_id", s.runID),
	))
	defer span.End()

	validate := s.opts.ComponentValidators[comp.Name]
	cs := &componentLoopState{}

	a.logger.Info("converging component", "component", comp.Name, "max_iterations", componentMiniLoopMaxIter)

	for iter := 1; iter <= componentMiniLoopMaxIter; iter++ {
		if s.budgetExceeded() {
			return nil, fmt.Errorf("%w: component %q", errComponentBudget, comp.Name)
		}
		files, err := a.componentIteration(ctx, rawSpec, comp, depInterfaces, baseFiles, validate, iter, cs, s)
		if err != nil {
			return nil, err
		}
		if files != nil {
			return files, nil
		}
		if cs.stallCount >= componentMiniLoopStallLimit {
			return nil, fmt.Errorf("%w: component %q", errComponentStalled, comp.Name)
		}
	}

	if cs.bestFiles != nil {
		a.logger.Info("component exhausted iterations, using best", "component", comp.Name, "best_score", cs.bestScore)
		return cs.bestFiles, nil
	}
	return nil, fmt.Errorf("%w: component %q", errComponentStalled, comp.Name)
}

// componentIteration runs one iteration of the component mini-loop.
// Returns (files, nil) on convergence, (nil, nil) to continue, or (nil, err) on hard error.
func (a *Attractor) componentIteration(ctx context.Context, rawSpec string, comp gene.Component, depInterfaces map[string]string, baseFiles map[string]string, validate ValidateFn, iter int, cs *componentLoopState, s *runState) (map[string]string, error) {
	systemPrompt := buildComponentPrompt(rawSpec, comp, depInterfaces, s.opts.Language)
	genResp, err := a.llm.Generate(ctx, llm.GenerateRequest{
		SystemPrompt: systemPrompt,
		Messages:     buildMessages(iter, cs.history),
		MaxTokens:    s.opts.MaxTokens,
		Model:        s.opts.Model,
		CacheControl: &llm.CacheControl{Type: "ephemeral"},
	})
	if err != nil {
		return nil, fmt.Errorf("attractor: generate component %q iteration %d: %w", comp.Name, iter, err)
	}
	s.totalCost += genResp.CostUSD
	s.totalTokens += genResp.InputTokens + genResp.OutputTokens

	files, parseErr := ParseFiles(genResp.Content)
	if parseErr != nil {
		cs.stallCount++
		cs.history = append(cs.history, iterationFeedback{
			iteration: iter, kind: feedbackParseError,
			message: fmt.Sprintf("Failed to parse generated files: %s", parseErr),
		})
		return nil, nil
	}

	buildContext := MergeFiles(files, baseFiles)
	iterDir := filepath.Join(s.baseDir, fmt.Sprintf("comp_%s_iter_%d", comp.Name, iter))
	if err := writeFiles(iterDir, buildContext); err != nil {
		return nil, fmt.Errorf("attractor: write component %q files: %w", comp.Name, err)
	}

	if validate == nil {
		a.logger.Info("component converged (no validator)", "component", comp.Name, "iteration", iter)
		return files, nil
	}

	return a.evaluateComponent(ctx, iterDir, comp.Name, iter, files, validate, cs, s)
}

// evaluateComponent builds, validates, and records results for one component iteration.
func (a *Attractor) evaluateComponent(ctx context.Context, iterDir, compName string, iter int, files map[string]string, validate ValidateFn, cs *componentLoopState, s *runState) (map[string]string, error) {
	satisfaction, failures, err := a.buildAndValidateComponent(ctx, iterDir, compName, iter, validate, s)
	if err != nil {
		cs.stallCount++
		cs.history = append(cs.history, iterationFeedback{
			iteration: iter, kind: feedbackBuildError, message: err.Error(),
		})
		return nil, nil
	}

	if satisfaction >= s.opts.Threshold {
		a.logger.Info("component converged", "component", compName, "iteration", iter, "satisfaction", satisfaction)
		return files, nil
	}

	if satisfaction > cs.bestScore {
		cs.bestScore = satisfaction
		cs.bestFiles = maps.Clone(files)
		cs.stallCount = 0
	} else {
		cs.stallCount++
	}

	fidelity := determineFidelity(iter, cs.stallCount)
	cs.history = append(cs.history, iterationFeedback{
		iteration:       iter,
		kind:            feedbackValidation,
		message:         formatValidationFeedback(satisfaction, failures, fidelity),
		fidelity:        fidelity,
		failedScenarios: parseFailedScenarios(failures),
	})
	return nil, nil
}

// buildAndValidateComponent builds a container and runs validation for a component.
// Returns satisfaction, failures, and error (error for build/run/health failures).
func (a *Attractor) buildAndValidateComponent(ctx context.Context, iterDir, compName string, iter int, validate ValidateFn, s *runState) (float64, []string, error) {
	tag := fmt.Sprintf("og-%s-comp-%s-%d", s.runID, compName, iter)
	if err := a.containerMgr.Build(ctx, iterDir, tag); err != nil {
		return 0, nil, fmt.Errorf("%w: %w", errComponentBuildFail, err)
	}

	if s.opts.Capabilities.NeedsExec || s.opts.Capabilities.NeedsTUI {
		session, stopSession, err := a.containerMgr.StartSession(ctx, tag)
		if err != nil {
			return 0, nil, fmt.Errorf("%w: %w", errComponentSessionFail, err)
		}
		defer stopSession()
		s.sessionProvider(session)
		defer s.sessionProvider(nil)
	}

	url, stop, err := a.startComposedContainer(ctx, tag, s.opts.Capabilities, s)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %w", errComponentStartFail, err)
	}
	if stop != nil {
		defer stop()
	}

	satisfaction, failures, valCost, valErr := validate(ctx, url, nil, 0)
	if valErr != nil {
		return 0, nil, fmt.Errorf("attractor: validate component %q: %w", compName, valErr)
	}
	s.totalCost += valCost
	return satisfaction, failures, nil
}

// generateContent produces the LLM output for one iteration.
// When scenarios are stalling (buildSteeringText returns non-empty), it first tries
// the wonder/reflect two-phase process. If that yields output, it is used directly.
// Otherwise it falls back to the standard single-call generation path.
// Returns the full GenerateResponse so callers can inspect FinishReason.
func (a *Attractor) generateContent(ctx context.Context, specContent string, messages []llm.Message, iter int, s *runState) (llm.GenerateResponse, error) {
	if buildSteeringText(s.history) != "" {
		content, finishReason, wrCost, err := a.wonderReflect(ctx, specContent, iter, s)
		if err != nil {
			return llm.GenerateResponse{}, err
		}
		if content != "" {
			// Wonder/reflect path: construct a synthetic response.
			// Token counts and cost are already recorded as side effects in wonderReflect.
			return llm.GenerateResponse{
				Content:      content,
				FinishReason: finishReason,
				InputTokens:  s.lastInputTokens,
				OutputTokens: s.lastOutputTokens,
				CostUSD:      wrCost,
			}, nil
		}
	}

	// Normal generation path.
	genResp, err := a.llm.Generate(ctx, llm.GenerateRequest{
		SystemPrompt: buildSystemPrompt(specContent, s.opts.Capabilities, s.opts.Language, s.opts.Genes, s.opts.GeneLanguage),
		Messages:     messages,
		MaxTokens:    s.opts.MaxTokens,
		Model:        s.currentModel(),
		CacheControl: &llm.CacheControl{Type: "ephemeral"},
	})
	if err != nil {
		return llm.GenerateResponse{}, fmt.Errorf("attractor: generate iteration %d: %w", iter, err)
	}
	s.totalCost += genResp.CostUSD
	s.totalTokens += genResp.InputTokens + genResp.OutputTokens
	s.lastInputTokens = genResp.InputTokens
	s.lastOutputTokens = genResp.OutputTokens
	return genResp, nil
}

// wonderReflect runs a two-phase wonder/reflect process when scenarios are stalling.
// Wonder phase: uses the judge model at high temperature to diagnose why attempts are failing.
// Reflect phase: uses the generator model at low temperature to produce new code from the diagnosis.
// Returns the reflect output (non-empty means use it instead of normal generation),
// the reflect phase's finish reason, and the combined cost of both phases.
// Returns ("", "", 0, nil) to signal graceful fallback to normal generation.
func (a *Attractor) wonderReflect(ctx context.Context, rawSpec string, iter int, s *runState) (content, finishReason string, combinedCost float64, err error) {
	opts := s.opts

	// Resolve judge model — fall back to the primary generation model when unset.
	// Always use the primary model (not the current frugal tier) so diagnostics retain
	// full capability during the exact window where the frugal model is struggling.
	judgeModel := opts.JudgeModel
	if judgeModel == "" {
		a.logger.Debug("wonder/reflect: no judge model set, falling back to primary model", "model", opts.Model)
		judgeModel = opts.Model
	}

	oscillating := detectOscillation(s.codeHashes)
	wonderPrompt := buildWonderPrompt(s.history, s.bestFiles, s.scoreHistory, oscillating)

	wonderTemp := wonderTemperature
	wonderResp, err := a.llm.Generate(ctx, llm.GenerateRequest{
		SystemPrompt: buildSystemPrompt(rawSpec, s.opts.Capabilities, s.opts.Language, s.opts.Genes, s.opts.GeneLanguage),
		Messages:     []llm.Message{{Role: "user", Content: wonderPrompt}},
		Model:        judgeModel,
		Temperature:  &wonderTemp,
	})
	if err != nil {
		// Context cancellation is a hard error; other LLM errors fall back to normal generation.
		if ctx.Err() != nil {
			return "", "", 0, fmt.Errorf("attractor: wonder phase iteration %d: %w", iter, err)
		}
		a.logger.Warn("wonder/reflect: wonder phase failed, falling back to normal generation",
			"iteration", iter, "error", err)
		return "", "", 0, nil
	}
	s.totalCost += wonderResp.CostUSD
	s.totalTokens += wonderResp.InputTokens + wonderResp.OutputTokens
	a.logger.Debug("wonder phase complete", "iteration", iter, "cost_usd", wonderResp.CostUSD)

	// Check budget before proceeding to reflect phase.
	if s.budgetExceeded() {
		a.logger.Debug("budget exceeded after wonder phase, skipping reflect", "iteration", iter)
		return "", "", 0, nil
	}

	diagnosis, err := parseDiagnosis(wonderResp.Content)
	if err != nil {
		a.logger.Warn("wonder/reflect: failed to parse diagnosis, falling back to normal generation",
			"iteration", iter, "error", err)
		return "", "", 0, nil
	}

	// Determine whether minimalism prompting should be included.
	minimalism := len(s.scoreHistory) > 0 && s.scoreHistory[len(s.scoreHistory)-1] > minimalismThreshold

	reflectPrompt := buildReflectPrompt(diagnosis, minimalism)
	reflectTemp := reflectTemperature
	reflectResp, err := a.llm.Generate(ctx, llm.GenerateRequest{
		SystemPrompt: buildSystemPrompt(rawSpec, s.opts.Capabilities, s.opts.Language, s.opts.Genes, s.opts.GeneLanguage),
		Messages:     []llm.Message{{Role: "user", Content: reflectPrompt}},
		MaxTokens:    s.opts.MaxTokens,
		Model:        opts.Model, // always use primary model; reflect crafts the next steering prompt
		Temperature:  &reflectTemp,
	})
	if err != nil {
		// Context cancellation is a hard error; other LLM errors fall back to normal generation.
		if ctx.Err() != nil {
			return "", "", 0, fmt.Errorf("attractor: reflect phase iteration %d: %w", iter, err)
		}
		a.logger.Warn("wonder/reflect: reflect phase failed, falling back to normal generation",
			"iteration", iter, "error", err)
		return "", "", 0, nil
	}
	s.totalCost += reflectResp.CostUSD
	s.totalTokens += reflectResp.InputTokens + reflectResp.OutputTokens
	s.lastInputTokens = wonderResp.InputTokens + reflectResp.InputTokens
	s.lastOutputTokens = wonderResp.OutputTokens + reflectResp.OutputTokens
	a.logger.Debug("reflect phase complete", "iteration", iter, "cost_usd", reflectResp.CostUSD)

	// Return the reflect phase's FinishReason intentionally: wonder output is diagnostic
	// (not code), so only the reflect phase's truncation status matters for downstream handling.
	return reflectResp.Content, reflectResp.FinishReason, wonderResp.CostUSD + reflectResp.CostUSD, nil
}

// iterate runs a single iteration of the attractor loop.
// Returns (result, nil) for terminal conditions, (nil, nil) to continue, or (nil, err) for hard errors.
func (a *Attractor) iterate(ctx context.Context, rawSpec string, iter int, s *runState, validate ValidateFn) (*RunResult, error) {
	s.lastImproved = false // reset at start of each iteration; set true only by processValidation on strict improvement
	s.lastTurns = 0

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

	// Compute iterDir once before the generation branch.
	iterDir := filepath.Join(s.baseDir, fmt.Sprintf("iter_%d", iter))

	var files map[string]string

	if s.opts.Agentic {
		// Agentic path: use AgentLoop for tool-use code generation.
		// The handler writes files directly to iterDir; no ParseFiles/writeFiles needed.
		var agentErr error
		files, agentErr = a.generateAgentic(ctx, specContent, iterDir, iter, s)
		if errors.Is(agentErr, errNoFiles) {
			a.logger.Warn("agentic generation produced no files", "iteration", iter)
			s.lastOutcome = OutcomeParseFail
			s.lastSatisfaction = 0
			s.recordStall(iter, feedbackParseError, "Agent produced no files")
			return a.checkStalled(iter, s), nil
		}
		if agentErr != nil {
			return nil, agentErr
		}
	} else {
		// Standard single-call generation path.
		// stall is non-nil when stall limit reached; stdFiles is nil on any parse failure.
		stall, stdFiles, stdErr := a.generateStandard(ctx, specContent, iterDir, iter, s)
		if stdErr != nil || stall != nil || stdFiles == nil {
			return stall, stdErr
		}
		files = stdFiles
	}

	s.lastFailures = nil

	// Record the hash of the file set for oscillation detection.
	s.codeHashes = append(s.codeHashes, hashFiles(files))

	// Build, run, and validate.
	return a.buildRunValidate(ctx, iter, iterDir, files, s, validate)
}

// generateAgentic runs the AgentLoop to generate files via tool use.
// iterDir must be computed by the caller before this is called.
// If patch mode is active (iter > 1 and bestFiles non-nil), pre-seeds iterDir with the current
// best files so the agent can read_file them.
// Returns the generated file map, or errNoFiles if the agent wrote no files.
//
// Wonder/reflect is intentionally skipped in agentic mode. The agent's multi-turn tool-use loop
// already handles self-correction through iterative read_file/write_file cycles — it can observe
// its own output and refine it within a single AgentLoop call. Injecting a separate wonder/reflect
// diagnosis pass would duplicate this capability and send an extra Generate call that bypasses the
// agent's stateful context.
func (a *Attractor) generateAgentic(ctx context.Context, specContent, iterDir string, iter int, s *runState) (map[string]string, error) {
	agentClient, ok := a.llm.(llm.AgentClient)
	if !ok {
		return nil, errAgentClientRequired
	}

	patching := s.patchActive && s.bestFiles != nil && iter > 1

	// Pre-seed iterDir with best files in patch mode so the agent can read them.
	if patching {
		if err := writeFiles(iterDir, s.bestFiles); err != nil {
			return nil, fmt.Errorf("attractor: pre-seed agentic iterDir iteration %d: %w", iter, err)
		}
	}

	var messages []llm.Message
	if patching {
		messages = buildAgenticPatchMessages(s.history, s.bestFiles, s.bestSatisfaction)
	} else {
		messages = buildAgenticMessages(iter, s.history)
	}

	if err := applyMinimalismSuffix(messages, s.scoreHistory, s.history); err != nil {
		return nil, err
	}

	if detectOscillation(s.codeHashes) {
		messages[len(messages)-1].Content += "\n\n" + buildOscillationSteering()
	}

	handler, err := newAgentToolHandler(iterDir, a.logger)
	if err != nil {
		return nil, fmt.Errorf("attractor: create agentic tool handler iteration %d: %w", iter, err)
	}

	// In patch mode, pre-populate the handler's file map with the seeded files so that
	// hashFiles() and bestFiles tracking operate on the complete file set. The agent's
	// write_file calls overwrite entries for files it modifies; files it doesn't touch remain.
	if patching {
		maps.Copy(handler.files, s.bestFiles)
	}

	resp, err := agentClient.AgentLoop(ctx, llm.AgentRequest{
		SystemPrompt: buildAgenticSystemPrompt(specContent, s.opts.Capabilities, s.opts.Language, s.opts.Genes, s.opts.GeneLanguage),
		Messages:     messages,
		Tools:        agentTools(),
		Model:        s.currentModel(),
		MaxTurns:     s.opts.AgentMaxTurns,
		CacheControl: &llm.CacheControl{Type: "ephemeral"},
	}, handler.Handle)
	if err != nil {
		return nil, fmt.Errorf("attractor: agent loop iteration %d: %w", iter, err)
	}

	s.totalCost += resp.TotalCost
	s.totalTokens += resp.InputTokens + resp.OutputTokens
	s.lastInputTokens = resp.InputTokens
	s.lastOutputTokens = resp.OutputTokens
	s.lastTurns = resp.Turns

	files := handler.Files()

	a.logger.Debug("agentic generation complete",
		"iteration", iter,
		"turns", resp.Turns,
		"cost_usd", resp.TotalCost,
		"files_written", len(files),
	)

	if len(files) == 0 {
		return nil, errNoFiles
	}
	return files, nil
}

// generateStandard runs the standard single-call generation path for one iteration.
// Returns (stall, files, nil) when generation succeeds, (stall, nil, nil) when a stall
// is recorded (parse failure), or (nil, nil, err) on a hard error.
func (a *Attractor) generateStandard(ctx context.Context, specContent, iterDir string, iter int, s *runState) (*RunResult, map[string]string, error) {
	patching := s.patchActive && s.bestFiles != nil && iter > 1

	// Build messages: patch mode sends previous best files + failures.
	var messages []llm.Message
	if patching {
		relevantFiles, triageCost, triageTokens := a.triageFiles(ctx, s.bestFiles, s.lastFailures, s.opts.JudgeModel)
		s.totalCost += triageCost
		s.totalTokens += triageTokens
		omitted := len(s.bestFiles) - len(relevantFiles)
		messages = buildPatchMessages(s.history, relevantFiles, s.bestSatisfaction, omitted)
	} else {
		messages = buildMessages(iter, s.history)
	}

	// Inject minimalism suffix when the previous validated score is above the threshold.
	if err := applyMinimalismSuffix(messages, s.scoreHistory, s.history); err != nil {
		return nil, nil, err
	}

	// Inject oscillation steering when the last 4 code hashes form an A→B→A→B pattern.
	if detectOscillation(s.codeHashes) {
		messages[len(messages)-1].Content += "\n\n" + buildOscillationSteering()
	}

	// Generate code: wonder/reflect on stall, normal generation otherwise.
	genResp, err := a.generateContent(ctx, specContent, messages, iter, s)
	if err != nil {
		return nil, nil, err
	}

	// Parse files from LLM output.
	parseResult, parseErr := ParseFilesWithMetadata(genResp.Content)
	if parseErr != nil {
		// When the output was truncated, surface truncation as the feedback kind
		// instead of a generic parse error -- this gives the LLM actionable signal.
		if genResp.FinishReason == "max_tokens" {
			a.logger.Warn("parse files failed due to truncation", "iteration", iter, "error", parseErr)
			s.lastOutcome = OutcomeParseFail
			s.lastSatisfaction = 0
			s.recordStall(iter, feedbackTruncation, "Output truncated at model limit -- response was cut off")
			return a.checkStalled(iter, s), nil, nil
		}
		a.logger.Warn("parse files failed", "iteration", iter, "error", parseErr)
		s.lastOutcome = OutcomeParseFail
		s.lastSatisfaction = 0
		s.recordStall(iter, feedbackParseError, fmt.Sprintf("Failed to parse generated files: %s", parseErr))
		return a.checkStalled(iter, s), nil, nil
	}
	files := parseResult.Files
	a.logFileInventory(iter, parseResult, genResp.FinishReason)

	// In patch mode, merge new output over previous best to carry forward unchanged files.
	if patching {
		files = MergeFiles(files, s.bestFiles)
	}

	// Write files to iteration directory.
	if err := writeFiles(iterDir, files); err != nil {
		return nil, nil, fmt.Errorf("attractor: write files iteration %d: %w", iter, err)
	}

	return nil, files, nil
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
		s.recordStall(iter, feedbackBuildError, fmt.Sprintf("Docker build failed: %s", stripBuildNoise(err.Error())))
		return a.checkStalled(iter, s), nil
	}

	caps := s.opts.Capabilities
	var url string
	var containerID string

	// When exec or TUI capabilities are needed, a session container runs from the same image:
	// - Session container: runs "sleep infinity" for docker exec commands (exec) and
	//   docker exec -it commands via PTY (TUI)
	// - HTTP container (if needed): runs the image's CMD to serve HTTP on port 8080
	if caps.NeedsExec || caps.NeedsTUI {
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

	svc, svcErr := a.startServiceContainer(ctx, iter, tag, caps, s)
	if svcErr != nil {
		return nil, svcErr
	}
	if svc.stalled != nil {
		return svc.stalled, nil
	}
	if svc.stop != nil {
		defer svc.stop()
	}
	url = svc.url
	containerID = svc.containerID
	restartFn := svc.restartFn

	// Run test command before validation when configured and an HTTP container is available.
	if skip, stall, err := a.runTestCommand(ctx, iter, containerID, s); err != nil || skip {
		return stall, err
	}

	satisfaction, failures, valCost, err := validate(ctx, url, restartFn, s.activeTier)
	if err != nil {
		return nil, fmt.Errorf("attractor: validate iteration %d: %w", iter, err)
	}
	s.totalCost += valCost
	s.lastFailures = failures

	a.logIterationResult(iter, satisfaction, failures, s.opts.Threshold)

	return a.processValidation(iter, satisfaction, failures, files, s)
}

// runTestCommand executes the test command against the HTTP container when configured.
// Returns (skip=true, stall, nil) when the test failed — caller must not call validate.
// Returns (skip=false, nil, nil) when the test passed or test command is empty.
// Returns (skip=false, nil, err) on hard error.
func (a *Attractor) runTestCommand(ctx context.Context, iter int, containerID string, s *runState) (skip bool, stall *RunResult, err error) {
	if s.opts.TestCommand == "" {
		return false, nil, nil
	}
	if containerID == "" {
		a.logger.Debug("skipping test command: no HTTP container", "iteration", iter)
		return false, nil, nil
	}
	execRes, execErr := a.containerMgr.RunTest(ctx, containerID, s.opts.TestCommand)
	if execErr != nil {
		return false, nil, fmt.Errorf("attractor: run test iteration %d: %w", iter, execErr)
	}
	if execRes.ExitCode != 0 {
		output := execRes.Stdout
		if output != "" && execRes.Stderr != "" {
			output += "\n"
		}
		output += execRes.Stderr
		a.logger.Warn("test command failed", "iteration", iter, "exit_code", execRes.ExitCode)
		s.lastOutcome = OutcomeTestFail
		s.lastSatisfaction = 0
		s.recordStall(iter, feedbackTestError, fmt.Sprintf("Test command exited %d:\n%s", execRes.ExitCode, truncateFeedback(output, maxFeedbackBytes)))
		return true, a.checkStalled(iter, s), nil
	}
	return false, nil, nil
}

// serviceContainerResult holds the outputs of startServiceContainer.
type serviceContainerResult struct {
	url         string
	containerID string
	restartFn   RestartFunc
	stop        func()     // nil when no service container is needed (TUI-only, exec-only)
	stalled     *RunResult // non-nil when startup failed and stall limit is reached
}

// startServiceContainer selects and starts the appropriate service container based on capabilities.
// It returns a serviceContainerResult whose stop function (if non-nil) must be deferred by the caller.
func (a *Attractor) startServiceContainer(ctx context.Context, iter int, tag string, caps ScenarioCapabilities, s *runState) (serviceContainerResult, error) {
	switch {
	case caps.NeedsGRPC:
		res, err := a.startGRPCContainer(ctx, iter, tag, caps, s)
		if err != nil {
			return serviceContainerResult{}, err
		}
		if res.stop == nil {
			return serviceContainerResult{stalled: res.stalled}, nil
		}
		s.grpcTargetProvider(res.grpcTarget)
		return serviceContainerResult{
			url:         res.url,
			containerID: res.containerID,
			stop:        func() { res.stop(); s.grpcTargetProvider("") },
		}, nil
	case caps.NeedsTUI && !caps.NeedsHTTP && !caps.NeedsGRPC:
		// TUI-only: steps run locally via PTY, no container service needed.
		return serviceContainerResult{}, nil
	case caps.NeedsHTTP || caps.NeedsBrowser || !caps.NeedsExec:
		// If only HTTP needed, or no capabilities detected (legacy), use Run + WaitHealthy.
		res, err := a.startHTTPContainer(ctx, iter, tag, s.opts.HealthTimeout, s)
		if err != nil {
			return serviceContainerResult{}, err
		}
		if res.stop == nil {
			return serviceContainerResult{stalled: res.stalled}, nil
		}
		return serviceContainerResult{
			url:         res.url,
			containerID: res.containerID,
			restartFn:   res.restart,
			stop:        res.stop,
		}, nil
	}
	return serviceContainerResult{}, nil
}

// httpContainerResult holds the outputs of startHTTPContainer.
type httpContainerResult struct {
	url         string
	stop        func() // non-nil on success; nil when startup failed (safe to use as failure sentinel)
	restart     RestartFunc
	containerID string
	stalled     *RunResult // non-nil when startup failed and stall limit is reached
}

// startHTTPContainer launches a container, waits for HTTP health, and builds a RestartFunc.
// When startup fails, result.stop is nil and the container has already been stopped.
// The caller must defer result.stop() only when result.stop is non-nil.
// The stop function and restart function share a mutable reference so that restarting
// updates the cleanup target without changing the deferred call site.
func (a *Attractor) startHTTPContainer(ctx context.Context, iter int, tag string, healthTimeout time.Duration, s *runState) (httpContainerResult, error) {
	runRes, stop, err := a.containerMgr.Run(ctx, tag)
	if err != nil {
		a.logger.Warn("container run failed", "iteration", iter, "error", err)
		s.lastOutcome = OutcomeRunFail
		s.lastSatisfaction = 0
		s.recordStall(iter, feedbackRunError, fmt.Sprintf("Container failed to start: %s", err))
		return httpContainerResult{stalled: a.checkStalled(iter, s)}, nil
	}

	if err := a.containerMgr.WaitHealthy(ctx, runRes.URL, healthTimeout); err != nil {
		stop()
		a.logger.Warn("health check failed", "iteration", iter, "error", err)
		s.lastOutcome = OutcomeHealthFail
		s.lastSatisfaction = 0
		s.recordStall(iter, feedbackHealthError, fmt.Sprintf("Health check failed: %s", err))
		return httpContainerResult{stalled: a.checkStalled(iter, s)}, nil
	}

	currentStop := stop
	restartFn := func(restartCtx context.Context) (string, error) {
		currentStop()
		currentStop = func() {} // prevent double-stop if Run fails
		newRes, newStop, runErr := a.containerMgr.Run(restartCtx, tag)
		if runErr != nil {
			return "", fmt.Errorf("attractor: restart container: %w", runErr)
		}
		currentStop = newStop
		if hErr := a.containerMgr.WaitHealthy(restartCtx, newRes.URL, healthTimeout); hErr != nil {
			newStop()
			currentStop = func() {}
			return "", fmt.Errorf("attractor: restart health check: %w", hErr)
		}
		return newRes.URL, nil
	}

	return httpContainerResult{
		url:         runRes.URL,
		containerID: runRes.ContainerID,
		stop:        func() { currentStop() },
		restart:     restartFn,
	}, nil
}

// grpcContainerResult holds the outputs of startGRPCContainer.
type grpcContainerResult struct {
	url         string
	stop        container.StopFunc
	grpcTarget  string
	containerID string
	stalled     *RunResult // non-nil if startup failed (stall, not error)
}

// startGRPCContainer launches a container with gRPC port exposed and waits for readiness.
// When startup fails, result.stop is nil and the container has already been stopped.
// The caller must defer result.stop() only when result.stop is non-nil.
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

	return grpcContainerResult{url: runResult.URL, stop: stop, grpcTarget: grpcTarget, containerID: runResult.ContainerID}, nil
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

// logFileInventory logs file counts, sizes, and any truncation/drop warnings after parsing.
func (a *Attractor) logFileInventory(iter int, result ParseResult, finishReason string) {
	totalBytes := 0
	for _, content := range result.Files {
		totalBytes += len(content)
	}
	a.logger.Info("files parsed", "iteration", iter, "count", len(result.Files), "total_bytes", totalBytes)

	for path, content := range result.Files {
		a.logger.Debug("parsed file", "iteration", iter, "path", path, "bytes", len(content))
	}

	if len(result.DroppedFiles) > 0 {
		a.logger.Warn("incomplete files dropped", "iteration", iter, "dropped", result.DroppedFiles, "finish_reason", finishReason)
	}
	if result.Truncated && finishReason == "max_tokens" {
		a.logger.Warn("output truncated, files lost", "iteration", iter, "dropped", result.DroppedFiles)
	}
}

func (a *Attractor) logIterationResult(iter int, satisfaction float64, failures []string, threshold float64) {
	if satisfaction < threshold && len(failures) > 0 {
		summaries := make([]string, 0, len(failures))
		for _, f := range failures {
			line, _, _ := strings.Cut(f, "\n")
			summaries = append(summaries, line)
		}
		a.logger.Info("iteration result", "iteration", iter, "satisfaction", satisfaction, "failing", strings.Join(summaries, "; "))
	} else {
		a.logger.Info("iteration result", "iteration", iter, "satisfaction", satisfaction)
	}
}

// processValidation handles post-validation logic: convergence, stall detection, checkpoint.
func (a *Attractor) processValidation(iter int, satisfaction float64, failures []string, files map[string]string, s *runState) (*RunResult, error) {
	s.lastOutcome = OutcomeValidated
	s.lastSatisfaction = satisfaction
	s.scoreHistory = append(s.scoreHistory, satisfaction)

	// Detect per-scenario regressions before the convergence check.
	currentScores := parseAllScenarios(failures)
	if len(failures) > 0 && len(currentScores) == 0 {
		// No scenario lines parsed from non-empty output — likely a format change in
		// validator output. Log at debug level to aid diagnosis of unexpected
		// "no regressions detected" situations.
		a.logger.Debug("parseAllScenarios: non-empty failures slice yielded zero parsed scenarios; regression detection disabled for this iteration", "iteration", iter, "entry_count", len(failures))
	}
	regressions := detectRegressions(s.scenarioScores, s.scenarioScoreIteration, currentScores, iter, s.opts.Threshold)
	if len(regressions) > 0 {
		body := formatRegressions(regressions)
		s.history = append(s.history, iterationFeedback{
			iteration: iter,
			kind:      feedbackRegression,
			message:   body,
		})
		a.logger.Info("scenario regressions detected", "iteration", iter, "count", len(regressions))
	}

	// Full replacement (not merge) avoids stale scores for renamed or removed scenarios.
	s.scenarioScores = currentScores
	s.scenarioScoreIteration = iter

	// Converge only when at/above threshold and not blocked by a regression.
	converge := satisfaction >= s.opts.Threshold
	if converge && s.opts.BlockOnRegression && len(regressions) > 0 {
		converge = false
	}
	if converge {
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
		s.lastImproved = true
		if err := writeFiles(s.bestDir, files); err != nil {
			return nil, fmt.Errorf("attractor: write best files: %w", err)
		}
	} else {
		s.stallCount++
		a.trackPatchRegression(iter, satisfaction, s)
	}

	fidelity := determineFidelity(iter, s.stallCount)
	slog.Debug("feedback fidelity", "iteration", iter, "fidelity", fidelity, "stall_count", s.stallCount)
	s.history = append(s.history, iterationFeedback{
		iteration:       iter,
		kind:            feedbackValidation,
		message:         formatValidationFeedback(satisfaction, failures, fidelity),
		fidelity:        fidelity,
		failedScenarios: parseFailedScenarios(failures),
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

// lastValidationFailures returns the failedScenarios map from the most recent
// feedbackValidation entry in history, scanning backwards. Returns nil when no
// validation entry exists.
func lastValidationFailures(history []iterationFeedback) map[string]float64 {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].kind == feedbackValidation {
			return history[i].failedScenarios
		}
	}
	return nil
}

// applyMinimalismSuffix appends a minimalism instruction to the last message when the
// most recent validated score exceeds minimalismThreshold. It is a no-op when
// scoreHistory is empty or the score is at or below the threshold.
//
// Invariant: scoreHistory is only appended after a successful validation, which requires
// at least one prior iteration and therefore a non-empty messages slice. The guard
// below defends against that invariant ever being violated.
func applyMinimalismSuffix(messages []llm.Message, scoreHistory []float64, history []iterationFeedback) error {
	n := len(scoreHistory)
	if n == 0 {
		return nil
	}
	last := scoreHistory[n-1]
	if last <= minimalismThreshold {
		return nil
	}
	suffix := buildMinimalismSuffix(last, lastValidationFailures(history))
	if suffix == "" {
		return nil
	}
	if len(messages) == 0 {
		return errMinimalismNoMessages
	}
	messages[len(messages)-1].Content += suffix
	return nil
}

// checkStalled returns a stalled result if the stall limit is reached, nil otherwise.
func (a *Attractor) checkStalled(iter int, s *runState) *RunResult {
	if s.stallCount >= s.opts.StallLimit {
		return s.result(iter, StatusStalled)
	}
	return nil
}

// advanceTierIfNeeded checks whether stratified mode should advance to the next tier
// after a convergence result. Returns true when the tier was advanced (caller should continue
// the loop); returns false when the result should be returned to the caller.
func (a *Attractor) advanceTierIfNeeded(result *RunResult, s *runState) bool {
	if result.Status != StatusConverged || s.activeTier == 0 || s.activeTier >= maxStratifiedTier {
		return false
	}
	s.activeTier++
	a.logger.Info("tier advanced", "tier", s.activeTier, "prev_satisfaction", result.Satisfaction)
	s.resetForTierAdvancement()
	if s.escalation != nil {
		s.escalation = newEscalationState(s.opts.FrugalModel, s.opts.Model, a.logger)
	}
	return true
}

// resetForTierAdvancement resets per-tier mutable state when stratified mode advances to the
// next difficulty tier. Fields that must persist across tiers (bestFiles, bestDir, totalCost,
// codeHashes, runID, startTime) are intentionally NOT reset here.
func (s *runState) resetForTierAdvancement() {
	s.bestSatisfaction = 0
	s.stallCount = 0
	s.scoreHistory = nil
	s.history = nil
	s.patchActive = s.opts.PatchMode
	s.patchRegressionCount = 0
	s.scenarioScores = nil
	s.scenarioScoreIteration = 0
	s.lastFailures = nil
	s.lastOutcome = ""
	s.lastSatisfaction = 0
	s.lastImproved = false
	s.lastTurns = 0
}

// initialActiveTier returns the starting activeTier for a run. In stratified mode tiers
// begin at 1; non-stratified mode uses 0 (no tier filtering).
func initialActiveTier(stratified bool) int {
	if stratified {
		return 1
	}
	return 0
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
	if opts.Agentic && opts.AgentMaxTurns == 0 {
		opts.AgentMaxTurns = defaultAgentMaxTurns
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
