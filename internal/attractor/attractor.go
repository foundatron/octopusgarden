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
	errEmptySpec            = errors.New("attractor: spec content is empty")
	errUnsupportedLanguage  = errors.New("attractor: unsupported language")
	errMinimalismNoMessages = errors.New("attractor: minimalism suffix: no messages to append to")

	_ ContainerManager = (*container.Manager)(nil)
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
type ValidateFn func(ctx context.Context, url string, restart RestartFunc) (satisfaction float64, failures []string, cost float64, err error)

// Attractor orchestrates the convergence loop: generate code → build → validate → iterate.
type Attractor struct {
	llm          llm.Client
	containerMgr ContainerManager
	logger       *slog.Logger
	tracer       trace.Tracer
}

// RunOptions configures the attractor loop.
type RunOptions struct {
	Model             string
	FrugalModel       string               // optional cheaper model to start with; escalates to Model after consecutive failures
	JudgeModel        string               // model used for the wonder phase diagnosis; falls back to Model when empty
	Language          string               // language hint: "go", "python", "node", "rust", or "" (auto)
	BudgetUSD         float64              // 0 = unlimited
	Threshold         float64              // default 95
	MaxIterations     int                  // default 10
	StallLimit        int                  // default 3
	WorkspaceDir      string               // default "./workspace"
	HealthTimeout     time.Duration        // default 30s
	Progress          ProgressFunc         // optional per-iteration callback
	PatchMode         bool                 // if true, iteration 2+ sends prev best files + failures
	BlockOnRegression bool                 // if true, convergence is blocked when per-scenario regressions are detected
	ContextBudget     int                  // max estimated tokens for spec in system prompt; 0 = unlimited
	Capabilities      ScenarioCapabilities // detected from loaded scenarios
	Genes             string               // extracted pattern guide to inject into system prompt (empty = no genes)
	GeneLanguage      string               // source language of the gene exemplar (for cross-language note)
	TestCommand       string               // optional shell command run inside HTTP container after health check; non-zero exit = test_fail
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
	runID                  string
	opts                   RunOptions
	baseDir                string
	bestDir                string
	totalCost              float64
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
		escalation:         newEscalationState(opts.FrugalModel, opts.Model, a.logger),
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

// generateContent produces the LLM output for one iteration.
// When scenarios are stalling (buildSteeringText returns non-empty), it first tries
// the wonder/reflect two-phase process. If that yields output, it is used directly.
// Otherwise it falls back to the standard single-call generation path.
func (a *Attractor) generateContent(ctx context.Context, specContent string, messages []llm.Message, iter int, s *runState) (string, error) {
	if buildSteeringText(s.history) != "" {
		content, err := a.wonderReflect(ctx, specContent, iter, s)
		if err != nil {
			return "", err
		}
		if content != "" {
			return content, nil
		}
	}

	// Normal generation path.
	genResp, err := a.llm.Generate(ctx, llm.GenerateRequest{
		SystemPrompt: buildSystemPrompt(specContent, s.opts.Capabilities, s.opts.Language, s.opts.Genes, s.opts.GeneLanguage),
		Messages:     messages,
		Model:        s.currentModel(),
		CacheControl: &llm.CacheControl{Type: "ephemeral"},
	})
	if err != nil {
		return "", fmt.Errorf("attractor: generate iteration %d: %w", iter, err)
	}
	s.totalCost += genResp.CostUSD
	s.lastInputTokens = genResp.InputTokens
	s.lastOutputTokens = genResp.OutputTokens
	return genResp.Content, nil
}

// wonderReflect runs a two-phase wonder/reflect process when scenarios are stalling.
// Wonder phase: uses the judge model at high temperature to diagnose why attempts are failing.
// Reflect phase: uses the generator model at low temperature to produce new code from the diagnosis.
// Returns the reflect output (non-empty means use it instead of normal generation).
// Returns ("", nil) to signal graceful fallback to normal generation.
func (a *Attractor) wonderReflect(ctx context.Context, rawSpec string, iter int, s *runState) (string, error) {
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
			return "", fmt.Errorf("attractor: wonder phase iteration %d: %w", iter, err)
		}
		a.logger.Warn("wonder/reflect: wonder phase failed, falling back to normal generation",
			"iteration", iter, "error", err)
		return "", nil
	}
	s.totalCost += wonderResp.CostUSD
	a.logger.Debug("wonder phase complete", "iteration", iter, "cost_usd", wonderResp.CostUSD)

	// Check budget before proceeding to reflect phase.
	if s.budgetExceeded() {
		a.logger.Debug("budget exceeded after wonder phase, skipping reflect", "iteration", iter)
		return "", nil
	}

	diagnosis, err := parseDiagnosis(wonderResp.Content)
	if err != nil {
		a.logger.Warn("wonder/reflect: failed to parse diagnosis, falling back to normal generation",
			"iteration", iter, "error", err)
		return "", nil
	}

	// Determine whether minimalism prompting should be included.
	minimalism := len(s.scoreHistory) > 0 && s.scoreHistory[len(s.scoreHistory)-1] > minimalismThreshold

	reflectPrompt := buildReflectPrompt(diagnosis, minimalism)
	reflectTemp := reflectTemperature
	reflectResp, err := a.llm.Generate(ctx, llm.GenerateRequest{
		SystemPrompt: buildSystemPrompt(rawSpec, s.opts.Capabilities, s.opts.Language, s.opts.Genes, s.opts.GeneLanguage),
		Messages:     []llm.Message{{Role: "user", Content: reflectPrompt}},
		Model:        opts.Model, // always use primary model; reflect crafts the next steering prompt
		Temperature:  &reflectTemp,
	})
	if err != nil {
		// Context cancellation is a hard error; other LLM errors fall back to normal generation.
		if ctx.Err() != nil {
			return "", fmt.Errorf("attractor: reflect phase iteration %d: %w", iter, err)
		}
		a.logger.Warn("wonder/reflect: reflect phase failed, falling back to normal generation",
			"iteration", iter, "error", err)
		return "", nil
	}
	s.totalCost += reflectResp.CostUSD
	s.lastInputTokens = wonderResp.InputTokens + reflectResp.InputTokens
	s.lastOutputTokens = wonderResp.OutputTokens + reflectResp.OutputTokens
	a.logger.Debug("reflect phase complete", "iteration", iter, "cost_usd", reflectResp.CostUSD)

	return reflectResp.Content, nil
}

// iterate runs a single iteration of the attractor loop.
// Returns (result, nil) for terminal conditions, (nil, nil) to continue, or (nil, err) for hard errors.
func (a *Attractor) iterate(ctx context.Context, rawSpec string, iter int, s *runState, validate ValidateFn) (*RunResult, error) {
	s.lastImproved = false // reset at start of each iteration; set true only by processValidation on strict improvement

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

	// Inject minimalism suffix when the previous validated score is above the threshold.
	// This discourages adding complexity when the solution is already close to passing.
	if err := applyMinimalismSuffix(messages, s.scoreHistory, s.history); err != nil {
		return nil, err
	}

	// Inject oscillation steering when the last 4 code hashes form an A→B→A→B pattern.
	// Appended after applyMinimalismSuffix for highest salience at the end of the message.
	if detectOscillation(s.codeHashes) {
		messages[len(messages)-1].Content += "\n\n" + buildOscillationSteering()
	}

	// Generate code: wonder/reflect on stall, normal generation otherwise.
	generatedContent, err := a.generateContent(ctx, specContent, messages, iter, s)
	if err != nil {
		return nil, err
	}
	s.lastFailures = nil

	// Parse files from LLM output.
	files, err := ParseFiles(generatedContent)
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

	// Record the hash of the merged file set for oscillation detection.
	s.codeHashes = append(s.codeHashes, hashFiles(files))

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
	var containerID string

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

	var restartFn RestartFunc
	switch {
	case caps.NeedsGRPC:
		res, err := a.startGRPCContainer(ctx, iter, tag, caps, s)
		if err != nil {
			return nil, err
		}
		if res.stop == nil {
			return res.stalled, nil
		}
		defer res.stop()
		url = res.url
		containerID = res.containerID
		s.grpcTargetProvider(res.grpcTarget)
		defer s.grpcTargetProvider("") // clear after validation
	case caps.NeedsHTTP || caps.NeedsBrowser || !caps.NeedsExec:
		// If only HTTP needed, or no capabilities detected (legacy), use Run + WaitHealthy.
		res, err := a.startHTTPContainer(ctx, iter, tag, s.opts.HealthTimeout, s)
		if err != nil {
			return nil, err
		}
		if res.stop == nil {
			return res.stalled, nil
		}
		defer res.stop()
		url = res.url
		containerID = res.containerID
		restartFn = res.restart
	}

	// Run test command before validation when configured and an HTTP container is available.
	if skip, stall, err := a.runTestCommand(ctx, iter, containerID, s); err != nil || skip {
		return stall, err
	}

	satisfaction, failures, valCost, err := validate(ctx, url, restartFn)
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
