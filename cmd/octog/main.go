package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/foundatron/octopusgarden/internal/attractor"
	"github.com/foundatron/octopusgarden/internal/container"
	"github.com/foundatron/octopusgarden/internal/lint"
	"github.com/foundatron/octopusgarden/internal/llm"
	"github.com/foundatron/octopusgarden/internal/observability"
	"github.com/foundatron/octopusgarden/internal/scenario"
	"github.com/foundatron/octopusgarden/internal/spec"
	"github.com/foundatron/octopusgarden/internal/store"
	"github.com/foundatron/octopusgarden/internal/view"
)

// stepPassThreshold is the per-step score below which a step is labeled FAIL
// in validation output. This is purely cosmetic — the --threshold flag on
// validateCmd controls the aggregate satisfaction gate.
const stepPassThreshold = 80

var (
	errSpecAndScenariosRequired   = errors.New("--spec and --scenarios are required")
	errScenariosAndTargetRequired = errors.New("--scenarios and --target are required")
	errMissingAnthropicKey        = errors.New("ANTHROPIC_API_KEY not set (use env var or ~/.octopusgarden/config)")
	errMissingOpenAIKey           = errors.New("OPENAI_API_KEY not set (use env var or ~/.octopusgarden/config)")
	errNoAPIKey                   = errors.New("no API key found: set ANTHROPIC_API_KEY or OPENAI_API_KEY (or use ~/.octopusgarden/config)")
	errAmbiguousProvider          = errors.New("both ANTHROPIC_API_KEY and OPENAI_API_KEY are set; use --provider to disambiguate")
	errBelowThreshold             = errors.New("satisfaction below threshold")
	errInvalidThreshold           = errors.New("--threshold must be between 0 and 100")
	errNoJudgeModelPricing        = errors.New("judge model has no pricing entry")
	errInvalidFormat              = errors.New("--format must be \"text\" or \"json\"")
	errInvalidProvider            = errors.New("--provider must be \"anthropic\" or \"openai\"")
	errListModelsUnsupported      = errors.New("provider does not support listing models")
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if err := loadConfig(logger); err != nil {
		logger.Warn("failed to load config", "error", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "run":
		err = runCmd(ctx, logger, os.Args[2:])
	case "validate":
		err = validateCmd(ctx, logger, os.Args[2:])
	case "status":
		err = statusCmd(ctx, logger, os.Args[2:])
	case "lint":
		err = lintCmd(ctx, logger, os.Args[2:])
	case "models":
		err = modelsCmd(ctx, logger, os.Args[2:])
	case "configure":
		err = configureCmd(ctx, logger, os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1]) //nolint:gosec // G705 false positive: writing to stderr, not an HTTP response
		printUsage()
		os.Exit(1)
	}

	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		logger.Error(os.Args[1]+" failed", "error", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `Usage: octog <command> [flags]

Commands:
  run        Run the attractor loop to generate software from a spec
  validate   Validate a running service against scenarios
  status     Show recent runs, scores, and costs
  lint       Check spec and scenario files for errors
  models     List available models
  configure  Interactively configure API keys

Run 'octog <command> --help' for details.
`)
}

func runCmd(ctx context.Context, logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	specFlag := fs.String("spec", "", "path to spec file (required)")
	scenariosFlag := fs.String("scenarios", "", "path to scenarios directory (required)")
	provider := fs.String("provider", "", "LLM provider: anthropic or openai (auto-detected from env if omitted)")
	model := fs.String("model", "", "LLM model to use for generation (default: provider-specific)")
	judgeModel := fs.String("judge-model", "", "LLM model for satisfaction judging (default: provider-specific)")
	budget := fs.Float64("budget", 5.00, "maximum budget in USD")
	threshold := fs.Float64("threshold", 95, "satisfaction threshold (0-100)")
	patchMode := fs.Bool("patch", false, "enable incremental patch mode (iteration 2+ sends only changed files)")
	contextBudget := fs.Int("context-budget", 0, "max estimated tokens for spec in system prompt; 0 = unlimited")
	otelEndpoint := fs.String("otel-endpoint", "", "OTLP/HTTP endpoint for tracing (e.g. localhost:4318); disabled if empty")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: octog run [flags]\n\nFlags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *specFlag == "" || *scenariosFlag == "" {
		fs.Usage()
		return errSpecAndScenariosRequired
	}

	// Validate threshold range early (before potentially slow LLM client creation).
	if *threshold < 0 || *threshold > 100 {
		return errInvalidThreshold
	}

	// Create LLM client (resolves provider) and apply model defaults.
	clients, err := newLLMClient(*provider, logger)
	if err != nil {
		return err
	}
	applyModelDefaults(model, judgeModel, clients.provider)

	if err := validateJudgeFlags(*threshold, *judgeModel); err != nil {
		return err
	}

	// Resolve OTEL endpoint: flag → env → empty (disabled).
	endpoint := *otelEndpoint
	if endpoint == "" {
		endpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	}

	return runAttractorLoop(ctx, logger, clients.client, *specFlag, *scenariosFlag, *model, *judgeModel, *budget, *threshold, *patchMode, *contextBudget, endpoint)
}

func runAttractorLoop(ctx context.Context, logger *slog.Logger, llmClient llm.Client, specPath, scenariosPath, model, judgeModel string, budget, threshold float64, patchMode bool, contextBudget int, otelEndpoint string) error {
	tp, shutdown, err := observability.InitTracer(ctx, otelEndpoint)
	if err != nil {
		return fmt.Errorf("init tracer: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdown(shutdownCtx)
	}()

	parsedSpec, err := spec.ParseFile(specPath)
	if err != nil {
		return fmt.Errorf("parse spec: %w", err)
	}

	scenarios, err := scenario.LoadDir(scenariosPath)
	if err != nil {
		return fmt.Errorf("load scenarios: %w", err)
	}

	containerMgr, err := container.NewManager(logger)
	if err != nil {
		return fmt.Errorf("create container manager: %w", err)
	}
	defer func() { _ = containerMgr.Close() }()

	st, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	caps := detectCapabilities(scenarios)

	// Session provider pattern: the attractor sets the current session before
	// calling validate, and the validate closure reads it to create ExecExecutors.
	// Mutex protects against future refactoring that might run the attractor and
	// validation concurrently (runAndScore fans out to 10 goroutines).
	var sessionMu sync.Mutex
	var currentSession *container.Session
	sessionProvider := attractor.SessionProviderFn(func(session *container.Session) {
		sessionMu.Lock()
		currentSession = session
		sessionMu.Unlock()
	})
	sessionGetter := func() *container.Session {
		sessionMu.Lock()
		defer sessionMu.Unlock()
		return currentSession
	}

	instrumentedLLM := observability.NewTracingLLMClient(llmClient, tp)
	instrumentedContainer := observability.NewTracingContainerManager(containerMgr, tp)
	validateFn := buildValidateFn(scenarios, instrumentedLLM, logger, judgeModel, sessionGetter, caps.NeedsBrowser)
	instrumentedValidate := observability.WrapValidateFn(validateFn, tp)

	att := attractor.New(instrumentedLLM, instrumentedContainer, logger, tp)
	opts := attractor.RunOptions{
		Model:         model,
		BudgetUSD:     budget,
		Threshold:     threshold,
		PatchMode:     patchMode,
		ContextBudget: contextBudget,
		Progress:      progressFn(),
		Capabilities:  caps,
	}

	startedAt := time.Now()
	result, err := att.Run(ctx, parsedSpec.RawContent, opts, instrumentedValidate, sessionProvider)
	if err != nil {
		return fmt.Errorf("attractor run: %w", err)
	}
	finishedAt := time.Now()

	printRunSummary(result)
	recordRun(ctx, logger, st, result, specPath, model, threshold, budget, startedAt, finishedAt)
	printResult(result)
	return nil
}

// detectCapabilities inspects loaded scenarios to determine what the container needs.
func detectCapabilities(scenarios []scenario.Scenario) attractor.ScenarioCapabilities {
	var caps attractor.ScenarioCapabilities
	for _, sc := range scenarios {
		for _, step := range sc.Setup {
			detectStepCaps(&caps, step)
		}
		for _, step := range sc.Steps {
			detectStepCaps(&caps, step)
		}
	}
	return caps
}

func detectStepCaps(caps *attractor.ScenarioCapabilities, step scenario.Step) {
	if step.Request != nil {
		caps.NeedsHTTP = true
	}
	if step.Exec != nil {
		caps.NeedsExec = true
	}
	if step.Browser != nil {
		caps.NeedsBrowser = true
	}
}

func progressFn() func(attractor.IterationProgress) {
	return func(p attractor.IterationProgress) {
		if p.Outcome != attractor.OutcomeValidated {
			fmt.Fprintf(os.Stderr, "iter %d/%d  %s  cost: $%.2f  [%s]\n", //nolint:gosec // G705 false positive: writing to stderr, not an HTTP response
				p.Iteration, p.MaxIterations, p.Outcome,
				p.TotalCostUSD, p.Elapsed.Truncate(time.Second))
		} else {
			fmt.Fprintf(os.Stderr, "iter %d/%d  satisfaction: %.1f/%.1f  cost: $%.2f  trend: %s  [%s]\n", //nolint:gosec // G705 false positive: writing to stderr, not an HTTP response
				p.Iteration, p.MaxIterations, p.Satisfaction, p.Threshold,
				p.TotalCostUSD, p.Trend, p.Elapsed.Truncate(time.Second))
		}
	}
}

func printRunSummary(result *attractor.RunResult) {
	if result.Status == attractor.StatusConverged {
		fmt.Fprintf(os.Stderr, "\n✓ Converged after %d iterations ($%.2f total)\n", result.Iterations, result.CostUSD) //nolint:gosec // G705 false positive: writing to stderr, not an HTTP response
	} else {
		fmt.Fprintf(os.Stderr, "\n✗ %s after %d iterations ($%.2f total)\n", result.Status, result.Iterations, result.CostUSD) //nolint:gosec // G705 false positive: writing to stderr, not an HTTP response
	}
}

func recordRun(ctx context.Context, logger *slog.Logger, st *store.Store, result *attractor.RunResult, specPath, model string, threshold, budget float64, startedAt, finishedAt time.Time) {
	run := store.Run{
		ID:           result.RunID,
		SpecPath:     specPath,
		Model:        model,
		Threshold:    threshold,
		BudgetUSD:    budget,
		StartedAt:    startedAt,
		FinishedAt:   &finishedAt,
		Satisfaction: result.Satisfaction,
		Iterations:   result.Iterations,
		TotalCostUSD: result.CostUSD,
		Status:       result.Status,
	}
	if err := st.RecordRun(ctx, run); err != nil {
		logger.Warn("failed to record run", "error", err)
	}
}

// applyModelDefaults sets model and judgeModel to provider-specific defaults if empty.
func applyModelDefaults(model, judgeModel *string, provider string) {
	if *model == "" {
		*model = defaultModel(provider)
	}
	if *judgeModel == "" {
		*judgeModel = defaultJudgeModel(provider)
	}
}

func validateCmd(ctx context.Context, logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	scenariosFlag := fs.String("scenarios", "", "path to scenarios directory (required)")
	target := fs.String("target", "", "target URL to validate against (required)")
	provider := fs.String("provider", "", "LLM provider: anthropic or openai (auto-detected from env if omitted)")
	judgeModel := fs.String("judge-model", "", "LLM model for satisfaction judging (default: provider-specific)")
	threshold := fs.Float64("threshold", 0, "minimum satisfaction score (0-100); non-zero enables exit code 1 on failure")
	format := fs.String("format", "text", "output format: text or json")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: octog validate [flags]\n\nFlags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *format != "text" && *format != "json" {
		return errInvalidFormat
	}

	if *scenariosFlag == "" || *target == "" {
		fs.Usage()
		return errScenariosAndTargetRequired
	}

	// Create LLM client (resolves provider) and apply judge model default.
	clients, err := newLLMClient(*provider, logger)
	if err != nil {
		return err
	}
	if *judgeModel == "" {
		*judgeModel = defaultJudgeModel(clients.provider)
	}

	if err := validateJudgeFlags(*threshold, *judgeModel); err != nil {
		return err
	}

	scenarios, err := scenario.LoadDir(*scenariosFlag)
	if err != nil {
		return fmt.Errorf("load scenarios: %w", err)
	}

	// Exec steps are not supported when validating against an external --target;
	// the nil session causes exec steps to run locally (no container).
	caps := detectCapabilities(scenarios)
	agg, err := runAndScore(ctx, scenarios, *target, clients.client, logger, *judgeModel, func() *container.Session { return nil }, caps.NeedsBrowser)
	if err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	return outputValidation(agg, *target, *threshold, *format)
}

func outputValidation(agg scenario.AggregateResult, target string, threshold float64, format string) error {
	switch format {
	case "json":
		out := view.NewValidateOutput(agg, target, threshold, stepPassThreshold)
		if err := view.WriteJSON(os.Stdout, out); err != nil {
			return fmt.Errorf("write json: %w", err)
		}
	default:
		fprintValidationResult(os.Stdout, agg)
	}

	if threshold > 0 && agg.Satisfaction < threshold {
		return fmt.Errorf("%w: %.1f < %.1f", errBelowThreshold, agg.Satisfaction, threshold)
	}
	return nil
}

func statusCmd(ctx context.Context, _ *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	format := fs.String("format", "text", "output format: text or json")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: octog status [flags]\n\nShow recent runs, scores, and costs.\n\nFlags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *format != "text" && *format != "json" {
		return errInvalidFormat
	}

	storePath, err := resolveStorePath()
	if err != nil {
		return err
	}
	st, err := store.NewStore(ctx, storePath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	runs, err := st.ListRuns(ctx)
	if err != nil {
		return fmt.Errorf("list runs: %w", err)
	}

	if *format == "json" {
		out := view.NewStatusOutput(runs)
		return view.WriteJSON(os.Stdout, out)
	}

	if len(runs) == 0 {
		fmt.Println("No runs recorded yet.")
		return nil
	}

	fmt.Printf("%-10s %-16s %-28s %7s %5s %9s  %s\n",
		"ID", "STATUS", "MODEL", "SCORE", "ITER", "COST", "STARTED")
	for _, r := range runs {
		fmt.Printf("%-10s %-16s %-28s %6.1f%% %5d $%7.4f  %s\n",
			r.ID, r.Status, r.Model, r.Satisfaction, r.Iterations, r.TotalCostUSD,
			r.StartedAt.Format("2006-01-02 15:04"))
	}
	return nil
}

func validateJudgeFlags(threshold float64, judgeModel string) error {
	if threshold < 0 || threshold > 100 {
		return errInvalidThreshold
	}
	if !llm.HasModelPricing(judgeModel) {
		return fmt.Errorf("%s: %w", judgeModel, errNoJudgeModelPricing)
	}
	return nil
}

// modelLister is implemented by LLM clients that can list available models.
type modelLister interface {
	ListModels(ctx context.Context) ([]llm.AvailableModel, error)
}

func modelsCmd(ctx context.Context, logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("models", flag.ContinueOnError)
	provider := fs.String("provider", "", "LLM provider: anthropic or openai (auto-detected from env if omitted)")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: octog models [flags]\n\nList available models.\n\nFlags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	clients, err := newLLMClient(*provider, logger)
	if err != nil {
		return err
	}

	lister, ok := clients.client.(modelLister)
	if !ok {
		return errListModelsUnsupported
	}

	models, err := lister.ListModels(ctx)
	if err != nil {
		return fmt.Errorf("list models: %w", err)
	}

	fmt.Printf("%-40s %-30s %s\n", "ID", "NAME", "CREATED")
	for _, m := range models {
		fmt.Printf("%-40s %-30s %s\n", m.ID, m.DisplayName, m.CreatedAt.Format(time.DateOnly))
	}
	return nil
}

var (
	errLintSpecOrScenarios = errors.New("at least one of --spec or --scenarios is required")
	errLintFailed          = errors.New("lint found errors")
)

func lintCmd(_ context.Context, _ *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("lint", flag.ContinueOnError)
	specFlag := fs.String("spec", "", "path to spec file to lint")
	scenariosFlag := fs.String("scenarios", "", "path to scenarios directory to lint")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: octog lint [flags]\n\nCheck spec and scenario files for errors.\n\nFlags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *specFlag == "" && *scenariosFlag == "" {
		fs.Usage()
		return errLintSpecOrScenarios
	}

	var allDiags []lint.Diagnostic

	if *specFlag != "" {
		diags, err := lint.CheckSpec(*specFlag)
		if err != nil {
			return fmt.Errorf("lint spec: %w", err)
		}
		allDiags = append(allDiags, diags...)
	}

	if *scenariosFlag != "" {
		diags, err := lint.CheckScenarioDir(*scenariosFlag)
		if err != nil {
			return fmt.Errorf("lint scenarios: %w", err)
		}
		allDiags = append(allDiags, diags...)
	}

	for _, d := range allDiags {
		fmt.Fprintln(os.Stderr, d.String()) //nolint:gosec // G705 false positive: writing to stderr, not an HTTP response
	}

	errs, warns := lint.CountByLevel(allDiags)
	if errs == 0 && warns == 0 {
		fmt.Fprintln(os.Stderr, "No issues found.")
		return nil
	}

	fmt.Fprintf(os.Stderr, "\n%d error(s), %d warning(s)\n", errs, warns) //nolint:gosec // G705 false positive: writing to stderr, not an HTTP response
	if errs > 0 {
		return errLintFailed
	}
	return nil
}

func openStore(ctx context.Context) (*store.Store, error) {
	path, err := resolveStorePath()
	if err != nil {
		return nil, err
	}
	return store.NewStore(ctx, path)
}

func resolveStorePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve store path: %w", err)
	}
	dir := filepath.Join(home, ".octopusgarden")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create store dir: %w", err)
	}
	return filepath.Join(dir, "runs.db"), nil
}

func runAndScore(ctx context.Context, scenarios []scenario.Scenario, targetURL string, llmClient llm.Client, logger *slog.Logger, judgeModel string, sessionGetter func() *container.Session, needsBrowser bool) (scenario.AggregateResult, error) {
	type indexedResult struct {
		index int
		ss    scenario.ScoredScenario
	}

	var (
		mu      sync.Mutex
		results = make([]indexedResult, 0, len(scenarios))
	)

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(10)

	for i, sc := range scenarios {
		g.Go(func() error {
			// Each goroutine gets its own Runner (independent variable capture state) and Judge.
			httpClient := &http.Client{Timeout: 30 * time.Second}
			executors := map[string]scenario.StepExecutor{
				"request": &scenario.HTTPExecutor{Client: httpClient, BaseURL: targetURL},
				"exec":    &scenario.ExecExecutor{Session: sessionGetter()},
			}
			if needsBrowser {
				browserExec := &scenario.BrowserExecutor{BaseURL: targetURL, Logger: logger}
				defer browserExec.Close()
				executors["browser"] = browserExec
			}
			runner := scenario.NewRunner(executors, logger)
			judge := scenario.NewJudge(llmClient, judgeModel, logger)

			result, err := runner.Run(gctx, sc)
			if err != nil {
				// If the group context was canceled (another goroutine failed),
				// propagate the cancellation rather than recording a phantom zero.
				if gctx.Err() != nil {
					return gctx.Err()
				}
				weight := 1.0
				if sc.Weight != nil {
					weight = *sc.Weight
				}
				logger.Warn("scenario setup failed", "scenario", sc.ID, "error", err)
				mu.Lock()
				results = append(results, indexedResult{index: i, ss: scenario.ScoredScenario{
					ScenarioID: sc.ID,
					Weight:     weight,
					Score:      0,
				}})
				mu.Unlock()
				return nil
			}

			ss, err := judge.ScoreScenario(gctx, sc, result)
			if err != nil {
				return fmt.Errorf("score scenario %s: %w", sc.ID, err)
			}

			mu.Lock()
			results = append(results, indexedResult{index: i, ss: ss})
			mu.Unlock()
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return scenario.AggregateResult{}, err
	}

	// Sort by original index for deterministic output.
	slices.SortFunc(results, func(a, b indexedResult) int { return a.index - b.index })
	scored := make([]scenario.ScoredScenario, len(results))
	for i, r := range results {
		scored[i] = r.ss
	}

	return scenario.Aggregate(scored), nil
}

func buildValidateFn(scenarios []scenario.Scenario, llmClient llm.Client, logger *slog.Logger, judgeModel string, sessionGetter func() *container.Session, needsBrowser bool) attractor.ValidateFn {
	return func(ctx context.Context, url string) (float64, []string, float64, error) {
		agg, err := runAndScore(ctx, scenarios, url, llmClient, logger, judgeModel, sessionGetter, needsBrowser)
		if err != nil {
			return 0, nil, 0, err
		}
		return agg.Satisfaction, agg.Failures, agg.TotalCostUSD, nil
	}
}

//nolint:gosec // G705 false positive: w is os.Stdout or test buffer, not an HTTP response
func fprintValidationResult(w io.Writer, agg scenario.AggregateResult) {
	_, _ = fmt.Fprintln(w, "Scenarios:")
	for _, sc := range agg.Scenarios {
		_, _ = fmt.Fprintf(w, "  %-30s %5.1f/100  (weight %.1f)\n", sc.ScenarioID, sc.Score, sc.Weight)
		for _, step := range sc.Steps {
			label := "PASS"
			if step.StepScore.Score < stepPassThreshold {
				label = "FAIL"
			}
			_, _ = fmt.Fprintf(w, "    [%s]  %3d  %s\n", label, step.StepScore.Score, step.StepResult.Description)
		}
	}

	_, _ = fmt.Fprintf(w, "\nAggregate satisfaction: %.1f/100\n", agg.Satisfaction)
	_, _ = fmt.Fprintf(w, "Cost: $%.4f\n", agg.TotalCostUSD)

	if len(agg.Failures) > 0 {
		_, _ = fmt.Fprintln(w, "Failures:")
		for _, f := range agg.Failures {
			_, _ = fmt.Fprintf(w, "  - %s\n", f)
		}
	}
}

// configAllowedKeys lists environment variable names that may be set via the config file.
var configAllowedKeys = map[string]bool{
	"ANTHROPIC_API_KEY": true,
	"OPENAI_API_KEY":    true,
	"OPENAI_BASE_URL":   true,
}

// configKeys defines the prompt order for `octog configure`.
var configKeys = []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "OPENAI_BASE_URL"}

// configClearValue is the sentinel input that clears an existing config value.
const configClearValue = "-"

func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve config path: %w", err)
	}
	return filepath.Join(home, ".octopusgarden", "config"), nil
}

func loadConfig(logger *slog.Logger) error {
	path, err := configPath()
	if err != nil {
		return err
	}

	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat config: %w", err)
	}

	// Warn if file is world-readable.
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		logger.Warn("config file has overly permissive permissions, recommend 0600",
			"path", path, "mode", fmt.Sprintf("%04o", perm))
	}

	f, err := os.Open(path) //nolint:gosec // G304: path is derived from UserHomeDir, not user input
	if err != nil {
		return fmt.Errorf("open config: %w", err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !configAllowedKeys[key] {
			logger.Warn("ignoring unknown config key", "key", key)
			continue
		}
		// Env vars take precedence — only set if not already present.
		if os.Getenv(key) == "" {
			if err := os.Setenv(key, value); err != nil {
				return fmt.Errorf("setenv %s: %w", key, err)
			}
		}
	}
	return scanner.Err()
}

// resolveProvider determines which LLM provider to use based on the --provider
// flag and environment variables. Returns "anthropic" or "openai".
func resolveProvider(provider string) (string, error) {
	if provider != "" {
		switch provider {
		case "anthropic", "openai":
			return provider, nil
		default:
			return "", errInvalidProvider
		}
	}

	hasAnthropic := os.Getenv("ANTHROPIC_API_KEY") != ""
	hasOpenAI := os.Getenv("OPENAI_API_KEY") != ""

	switch {
	case hasAnthropic && hasOpenAI:
		return "", errAmbiguousProvider
	case hasAnthropic:
		return "anthropic", nil
	case hasOpenAI:
		return "openai", nil
	default:
		return "", errNoAPIKey
	}
}

type llmClients struct {
	client   llm.Client
	provider string
}

// newLLMClient creates the appropriate LLM client based on the resolved provider.
func newLLMClient(provider string, logger *slog.Logger) (llmClients, error) {
	resolved, err := resolveProvider(provider)
	if err != nil {
		return llmClients{}, err
	}

	switch resolved {
	case "openai":
		apiKey := os.Getenv("OPENAI_API_KEY")
		if apiKey == "" {
			return llmClients{}, errMissingOpenAIKey
		}
		baseURL := os.Getenv("OPENAI_BASE_URL")
		zeroCost := baseURL != "" // local endpoints like Ollama have no billing
		client := llm.NewOpenAIClient(apiKey, baseURL, zeroCost, logger)
		return llmClients{client: client, provider: resolved}, nil
	default: // anthropic
		apiKey := os.Getenv("ANTHROPIC_API_KEY")
		if apiKey == "" {
			return llmClients{}, errMissingAnthropicKey
		}
		client := llm.NewAnthropicClient(apiKey, logger)
		return llmClients{client: client, provider: resolved}, nil
	}
}

// defaultModel returns the default generation model for the given provider.
func defaultModel(provider string) string {
	if provider == "openai" {
		return "gpt-5.2"
	}
	return "claude-sonnet-4-6"
}

// defaultJudgeModel returns the default judge model for the given provider.
func defaultJudgeModel(provider string) string {
	if provider == "openai" {
		return "gpt-5-nano"
	}
	return "claude-haiku-4-5"
}

func printResult(result *attractor.RunResult) {
	fmt.Printf("\nRun complete: %s\n", result.RunID)
	fmt.Printf("  Status:       %s\n", result.Status)
	fmt.Printf("  Iterations:   %d\n", result.Iterations)
	fmt.Printf("  Satisfaction: %.1f%%\n", result.Satisfaction)
	fmt.Printf("  Cost:         $%.4f\n", result.CostUSD)
	fmt.Printf("  Output:       %s\n", result.OutputDir)
}

func configureCmd(_ context.Context, _ *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("configure", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: octog configure\n\nInteractively configure API keys and settings.\n")
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	cfgPath, err := configPath()
	if err != nil {
		return err
	}

	return configureInteractive(os.Stdin, os.Stdout, cfgPath)
}

func configureInteractive(r io.Reader, w io.Writer, cfgPath string) error {
	values, originalLines, err := readConfigFile(cfgPath)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(w, "\nOctopusGarden Configuration\nConfig file: %s\n\n", cfgPath)

	scanner := bufio.NewScanner(r)
	newValues := make(map[string]string, len(configKeys))
	maps.Copy(newValues, values)

	for _, key := range configKeys {
		current := values[key]
		prompt := "not set"
		if current != "" {
			prompt = maskValue(current)
		}
		_, _ = fmt.Fprintf(w, "%s [%s]: ", key, prompt)
		if !scanner.Scan() {
			// EOF — keep all remaining values as-is.
			break
		}
		input := strings.TrimSpace(scanner.Text())
		switch {
		case input == configClearValue:
			delete(newValues, key)
		case input != "":
			newValues[key] = input
		}
		// Empty input (Enter) keeps the existing value.
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read input: %w", err)
	}

	_, _ = fmt.Fprintln(w)

	// Warn if no API key is configured.
	if newValues["ANTHROPIC_API_KEY"] == "" && newValues["OPENAI_API_KEY"] == "" {
		_, _ = fmt.Fprintln(w, "Warning: no API key configured. Run 'octog configure' to add one.")
		_, _ = fmt.Fprintln(w)
	}

	if err := writeConfigFile(cfgPath, newValues, originalLines); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(w, "Configuration saved to %s\n", cfgPath)
	return nil
}

// maskValue masks a config value for display. Values 16+ chars show first4...last4,
// shorter values show ****.
func maskValue(value string) string {
	if len(value) >= 16 {
		return value[:4] + "..." + value[len(value)-4:]
	}
	return "****"
}

// readConfigFile reads a config file and returns a map of known key-value pairs
// plus the original lines (for comment/ordering preservation). Returns an empty
// map and nil lines if the file does not exist.
func readConfigFile(path string) (map[string]string, []string, error) {
	f, err := os.Open(path) //nolint:gosec // G304: path derives from configPath()/UserHomeDir
	if errors.Is(err, fs.ErrNotExist) {
		return make(map[string]string), nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("open config: %w", err)
	}
	defer func() { _ = f.Close() }()

	values := make(map[string]string)
	var lines []string

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		lines = append(lines, line)

		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("read config: %w", err)
	}
	return values, lines, nil
}

// writeConfigFile writes config values to the given path, preserving comments and
// unknown keys from originalLines. Known keys are updated in-place; new keys are
// appended at the end in configKeys order. Creates the parent directory (0700) if needed.
// Note: existing key lines are normalized to KEY=VALUE format (whitespace around = is removed).
func writeConfigFile(path string, values map[string]string, originalLines []string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	// Track which keys we've written so we can append new ones.
	written := make(map[string]bool)

	var out []string
	for _, line := range originalLines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			out = append(out, line)
			continue
		}
		key, _, ok := strings.Cut(trimmed, "=")
		if !ok {
			out = append(out, line)
			continue
		}
		key = strings.TrimSpace(key)

		if v, exists := values[key]; exists {
			out = append(out, key+"="+v)
			written[key] = true
		} else {
			// Key was cleared or is unknown-but-absent — only drop known keys.
			if configAllowedKeys[key] {
				// Cleared — omit the line.
				written[key] = true
			} else {
				// Unknown key — pass through.
				out = append(out, line)
			}
		}
	}

	// Append new keys that weren't in the original file, in configKeys order.
	for _, key := range configKeys {
		if written[key] {
			continue
		}
		if v, exists := values[key]; exists {
			out = append(out, key+"="+v)
		}
	}

	content := strings.Join(out, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}
