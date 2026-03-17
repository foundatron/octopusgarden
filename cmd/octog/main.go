package main

import (
	"bufio"
	"context"
	"encoding/json"
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
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-isatty"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"

	"github.com/foundatron/octopusgarden/internal/attractor"
	"github.com/foundatron/octopusgarden/internal/container"
	"github.com/foundatron/octopusgarden/internal/gene"
	"github.com/foundatron/octopusgarden/internal/interview"
	"github.com/foundatron/octopusgarden/internal/lint"
	"github.com/foundatron/octopusgarden/internal/llm"
	"github.com/foundatron/octopusgarden/internal/observability"
	"github.com/foundatron/octopusgarden/internal/paths"
	"github.com/foundatron/octopusgarden/internal/preflight"
	"github.com/foundatron/octopusgarden/internal/scenario"
	"github.com/foundatron/octopusgarden/internal/spec"
	"github.com/foundatron/octopusgarden/internal/store"
	"github.com/foundatron/octopusgarden/internal/ui"
	"github.com/foundatron/octopusgarden/internal/view"
)

// version information set by goreleaser ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// stepPassThreshold is the per-step score below which a step is labeled FAIL
// in validation output and considered failing for detailed feedback. This is
// purely cosmetic — the --threshold flag on validateCmd controls the aggregate
// satisfaction gate.
const stepPassThreshold = 80

var (
	errSpecAndScenariosRequired   = errors.New("--spec and --scenarios are required")
	errScenariosAndTargetRequired = errors.New("--scenarios and either --target or --code are required")
	errCodeAndTargetConflict      = errors.New("--code and --target are mutually exclusive")
	errMissingAnthropicKey        = errors.New("ANTHROPIC_API_KEY not set (use env var, config file, or `octog configure`)")
	errMissingOpenAIKey           = errors.New("OPENAI_API_KEY not set (use env var, config file, or `octog configure`)")
	errNoAPIKey                   = errors.New("no API key found: set ANTHROPIC_API_KEY or OPENAI_API_KEY (env var, config file, or `octog configure`)")
	errAmbiguousProvider          = errors.New("both ANTHROPIC_API_KEY and OPENAI_API_KEY are set; use --provider to disambiguate")
	errBelowThreshold             = errors.New("satisfaction below threshold")
	errInvalidThreshold           = errors.New("--threshold must be between 0 and 100")
	errNoJudgeModelPricing        = errors.New("judge model has no pricing entry")
	errInvalidFormat              = errors.New("--format must be \"text\" or \"json\"")
	errInvalidProvider            = errors.New("--provider must be \"anthropic\" or \"openai\"")
	errSeedAndPromptConflict      = errors.New("--seed and --prompt are mutually exclusive")
	errInvalidParallelScenarios   = errors.New("--parallel-scenarios must be >= 1")
	errInvalidLanguage            = errors.New("--language must be one of: go, python, node, rust, auto")
	errListModelsUnsupported      = errors.New("provider does not support listing models")
	errPreflightFailed            = errors.New("preflight: spec clarity below threshold")
	errScenarioPreflightFailed    = errors.New("preflight: scenario quality below threshold")
	errInvalidPreflightThreshold  = errors.New("preflight threshold must be between 0.0 and 1.0")
	errSourceDirRequired          = errors.New("--source-dir is required")
	errSourceDirNotExist          = errors.New("--source-dir does not exist")
	errSourceDirNotDir            = errors.New("--source-dir is not a directory")
	errNoLanguageDetected         = errors.New("no recognized language in source directory (need go.mod, package.json, Cargo.toml, pyproject.toml, or requirements.txt)")
	errAgenticRequiresAnthropic   = errors.New("--agentic requires AgentClient support; use --provider anthropic")
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLogLevel()}))

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
	case "--help", "-h", "-help":
		printUsage()
		return
	case "--version", "-V", "version":
		fmt.Printf("octog %s (commit %s, built %s)\n", version, commit, date)
		return
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
	case "extract":
		err = extractCmd(ctx, logger, os.Args[2:])
	case "interview":
		err = interviewCmd(ctx, logger, os.Args[2:])
	case "preflight":
		err = preflightCmd(ctx, logger, os.Args[2:])
	case "configure":
		err = configureCmd(ctx, logger, os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1]) //nolint:gosec // G705 false positive: writing to stderr, not an HTTP response
		printUsage()
		os.Exit(1)
	}

	exitOnCmdError(err, os.Args[1], logger)
}

// exitOnCmdError exits with status 1 if err is non-nil and not flag.ErrHelp.
// flag.ErrHelp means the subcommand printed its usage and we should return cleanly.
func exitOnCmdError(err error, cmd string, logger *slog.Logger) {
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return
	}
	logger.Error(cmd+" failed", "error", err)
	os.Exit(1)
}

// parseLogLevel reads LOG_LEVEL from the environment and returns the
// corresponding slog.Level. Defaults to Info on missing or invalid values.
func parseLogLevel() slog.Level {
	var level slog.Level
	if lvl := os.Getenv("LOG_LEVEL"); lvl != "" {
		if err := level.UnmarshalText([]byte(lvl)); err != nil {
			fmt.Fprintf(os.Stderr, "warning: invalid LOG_LEVEL %q, using INFO\n", lvl)
		}
	}
	return level
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `Usage: octog <command> [flags]

OctopusGarden is an autonomous software factory: write a spec and a set of
holdout scenarios, then let the attractor loop generate code, run the
scenarios inside a container, score satisfaction via an LLM judge, and feed
failures back until convergence — zero human code review required.

Typical workflow:
  1. interview   Draft a spec interactively (or write one by hand).
  2. preflight   Verify the spec is clear enough before spending budget.
  3. lint        Catch formatting errors in spec and scenario files.
  4. run         Run the attractor loop: spec + scenarios → converged code.
  5. validate    Re-validate a running service at any time without re-running.
  6. status      Review run history, scores, and costs.

Optional:
  extract    Extract coding patterns from an existing codebase (gene transfusion).
  models     List models available from the configured provider.
  configure  Set API keys in the platform config file.

Commands:
  interview  Interactively draft a spec through conversation
  run        Run the attractor loop to generate software from a spec
  validate   Validate a running service against scenarios
  preflight  Assess spec clarity before running the attractor loop
  status     Show recent runs, scores, and costs
  lint       Check spec and scenario files for errors
  extract    Extract coding patterns from a source directory into a gene file
  models     List available models
  configure  Interactively configure API keys

Prerequisites: Docker (for run/validate), an LLM API key (ANTHROPIC_API_KEY or OPENAI_API_KEY).

Run 'octog <command> --help' for details.
`)
}

func runCmd(ctx context.Context, logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	specFlag := fs.String("spec", "", "path to spec file (required)")
	scenariosFlag := fs.String("scenarios", "", "path to scenarios directory (required)")
	provider := fs.String("provider", "", "LLM provider: anthropic or openai (auto-detected from env if omitted)")
	model := fs.String("model", "", "LLM model to use for generation (default: provider-specific)")
	frugalModel := fs.String("frugal-model", "", "cheaper model to start with; escalates to --model after 2 consecutive non-improving iterations")
	judgeModel := fs.String("judge-model", "", "LLM model for satisfaction judging (default: provider-specific)")
	budget := fs.Float64("budget", 5.00, "maximum budget in USD")
	threshold := fs.Float64("threshold", 95, "satisfaction threshold (0-100)")
	language := fs.String("language", "go", "target language: go, python, node, rust, or auto")
	genesFlag := fs.String("genes", "", "path to genes.json file produced by octog extract")
	patchMode := fs.Bool("patch", false, "enable incremental patch mode (iteration 2+ sends only changed files)")
	blockOnRegression := fs.Bool("block-on-regression", false, "block convergence when any scenario regresses below threshold in the current iteration")
	contextBudget := fs.Int("context-budget", 0, "max estimated tokens for spec in system prompt; 0 = unlimited")
	otelEndpoint := fs.String("otel-endpoint", "", "OTLP/HTTP endpoint for tracing (e.g. localhost:4318); disabled if empty")
	skipPreflight := fs.Bool("skip-preflight", false, "skip the spec clarity preflight check")
	preflightThreshold := fs.Float64("preflight-threshold", 0.8, "aggregate clarity score threshold for preflight (0.0–1.0)")
	verbose := fs.Int("v", 0, "verbosity level: 0=quiet, 1=per-scenario summary after each iteration, 2=full step detail with reasoning")
	maxTokensFlag := fs.Int("max-tokens", 0, "max output tokens for generation (0 = auto-scale per model)")
	agenticFlag := fs.Bool("agentic", false, "enable agentic generation mode (multi-turn tool-use)")
	agentMaxTurnsFlag := fs.Int("agent-max-turns", 0, "max tool-use turns per iteration (0 = use attractor default)")
	stratifiedFlag := fs.Bool("stratified", false, "validate scenarios by ascending difficulty tier (1→2→3), converging each tier before advancing")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: octog run [flags]

Run the attractor loop: the LLM generates code from --spec, the code is built
and run in a Docker container, holdout --scenarios are executed against it, an
LLM judge scores each scenario (0-100), and failures are fed back as context
for the next iteration. The loop converges when the aggregate satisfaction
score reaches --threshold (default 95%%) or the --budget is exhausted.

Holdout isolation is enforced: the attractor never sees scenario files during
generation. Only the spec content is provided to the generation model.

Examples:
  # Basic run with spec and scenarios
  octog run --spec spec.md --scenarios scenarios/

  # Tighten budget and lower convergence threshold for rapid prototyping
  octog run --spec spec.md --scenarios scenarios/ --budget 2.00 --threshold 80

  # Apply gene transfusion from an existing Go codebase
  octog run --spec spec.md --scenarios scenarios/ \
    --genes genes.json --patch

  # Agentic generation mode with multi-turn tool use (Anthropic only)
  octog run --spec spec.md --scenarios scenarios/ --agentic

Requires: Docker daemon running.

Spec format (--spec):
  Markdown file. The level-1 heading (# Title) becomes the project name.
  Subsequent sections describe desired behavior. Be explicit about ports,
  endpoints, data models, and error handling -- the spec is the sole input
  to the generation model.
  Optional metadata line "Test-Command: <cmd>" anywhere in the spec runs
  inside the container after health check; non-zero exit triggers re-generation.

Scenario format (--scenarios):
  Directory of YAML files. Each file defines one scenario.
  Required fields: id (string), steps (list).
  Optional: description, type, tier (1-3), weight, satisfaction_criteria,
  setup (steps that run before judged steps; failures are fatal).

  Each step has one action key: request (HTTP), exec (shell), browser,
  grpc, ws, or tui.
  - request: method (GET/POST/PUT/PATCH/DELETE), path (required).
    Optional: headers (map), body (string).
  - exec: command (string), args (list of strings).

  expect: natural-language string scored by the LLM judge (0-100), NOT a
  programmatic assertion. Example: "Status 200. Body contains name 'foo'."

  capture: extract values from responses for later steps.
    source: body (default), header, or status.
    json_path: dot-notation path (e.g. $.id, $.data.items[0].name).
    variable: name to store as. Use {variable_name} in later step paths,
    bodies, headers, and commands for substitution.

  setup: steps listed under setup run before judged steps and are fatal on
  failure. Use for creating test fixtures.
  delay: duration string (e.g. "2s") to wait before a step.
  retry: object with attempts (int), interval (duration), timeout (duration).

Key flags:
  --spec, --scenarios   Required. Paths to spec file and scenarios directory.
  --budget              Max USD to spend (default 5.00).
  --threshold           Convergence target, 0-100 (default 95).
  --model               Generation model (default: provider-specific).
  --judge-model         Scoring model (default: provider-specific).
  --language            Target language: go, python, node, rust, auto.
  --genes               Path to genes.json from 'octog extract'.
  --patch               Send only changed files on iteration 2+.
  --agentic             Enable multi-turn tool-use generation (Anthropic only).
  --stratified          Converge scenarios tier-by-tier (1→2→3).

Exit codes: 0 = converged, 1 = budget exhausted or error.

Flags:
`)
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
	// Validate language flag.
	langForOpts := *language
	if langForOpts == "auto" {
		langForOpts = ""
	} else if _, ok := attractor.LookupLanguage(langForOpts); !ok {
		return errInvalidLanguage
	}

	// Load genes if provided and optionally override language.
	genesGuide, genesLanguage, langForOpts, geneComponents, err := loadGenes(*genesFlag, langForOpts, isFlagSet(fs, "language"), logger)
	if err != nil {
		return err
	}

	// Create LLM client (resolves provider) and apply model defaults.
	clients, err := newLLMClient(*provider, logger)
	if err != nil {
		return err
	}
	if *agenticFlag {
		if _, ok := clients.client.(llm.AgentClient); !ok {
			return errAgenticRequiresAnthropic
		}
	}
	applyModelDefaults(model, judgeModel, clients.provider)

	if err := validateJudgeFlags(*threshold, *judgeModel); err != nil {
		return err
	}

	// Parse spec and run preflight check (validates threshold, parses, and checks clarity).
	parsedSpec, err := parseAndCheckPreflight(ctx, logger, clients.client, *specFlag, *judgeModel, *preflightThreshold, *skipPreflight)
	if err != nil {
		return err
	}

	// Resolve OTEL endpoint: flag → env → empty (disabled).
	endpoint := *otelEndpoint
	if endpoint == "" {
		endpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	}

	return runAttractorLoop(ctx, logger, clients.client, runLoopParams{
		SpecPath:          *specFlag,
		ParsedSpec:        parsedSpec,
		ScenariosPath:     *scenariosFlag,
		Model:             *model,
		FrugalModel:       *frugalModel,
		JudgeModel:        *judgeModel,
		Budget:            *budget,
		Threshold:         *threshold,
		PatchMode:         *patchMode,
		BlockOnRegression: *blockOnRegression,
		ContextBudget:     *contextBudget,
		OTELEndpoint:      endpoint,
		Language:          langForOpts,
		GenesGuide:        genesGuide,
		GeneLanguage:      genesLanguage,
		Verbosity:         *verbose,
		MaxTokens:         *maxTokensFlag,
		Agentic:           *agenticFlag,
		AgentMaxTurns:     *agentMaxTurnsFlag,
		GeneComponents:    geneComponents,
		Stratified:        *stratifiedFlag,
	})
}

// loadGenes loads a genes file if path is non-empty, returning the guide text,
// gene language, the resolved target language, and any structured components.
// If the language flag was not explicitly set, the gene's language overrides langForOpts.
func loadGenes(path, langForOpts string, languageExplicit bool, logger *slog.Logger) (guide, geneLang, resolvedLang string, components []gene.Component, err error) {
	resolvedLang = langForOpts
	if path == "" {
		return "", "", resolvedLang, nil, nil
	}
	g, err := gene.Load(path)
	if err != nil {
		return "", "", "", nil, fmt.Errorf("load genes: %w", err)
	}
	logger.Info("loaded genes", "source", g.Source, "language", g.Language, "tokens", g.TokenCount)

	if !languageExplicit {
		resolvedLang = g.Language
		logger.Info("auto-detected language from genes (override with --language)", "language", resolvedLang)
	}
	if len(g.Components) > 0 {
		logger.Info("loaded gene components for composed convergence", "count", len(g.Components))
	}
	return g.Guide, g.Language, resolvedLang, g.Components, nil
}

// isFlagSet reports whether the named flag was explicitly provided on the command line.
func isFlagSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// runLoopParams bundles the parameters for runAttractorLoop.
type runLoopParams struct {
	SpecPath          string
	ParsedSpec        spec.Spec
	ScenariosPath     string
	Model             string
	FrugalModel       string
	JudgeModel        string
	Budget            float64
	Threshold         float64
	PatchMode         bool
	BlockOnRegression bool
	ContextBudget     int
	OTELEndpoint      string
	Language          string
	GenesGuide        string
	GeneLanguage      string
	Verbosity         int
	MaxTokens         int
	Agentic           bool
	AgentMaxTurns     int
	GeneComponents    []gene.Component
	Stratified        bool
}

func runAttractorLoop(ctx context.Context, logger *slog.Logger, llmClient llm.Client, p runLoopParams) error {
	tp, shutdown, err := observability.InitTracer(ctx, p.OTELEndpoint)
	if err != nil {
		return fmt.Errorf("init tracer: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdown(shutdownCtx)
	}()

	parsedSpec := p.ParsedSpec

	scenarios, err := scenario.LoadDir(p.ScenariosPath)
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
	// Both calls are sequential (attractor loop never overlaps with validate), so
	// no synchronization is needed.
	var currentSession *container.Session
	sessionProvider := attractor.SessionProviderFn(func(session *container.Session) {
		currentSession = session
	})
	sessionGetter := func() *container.Session {
		return currentSession
	}

	// gRPC target provider pattern: same as session provider — attractor sets it,
	// validate closure reads it.
	var currentGRPCTarget string
	grpcTargetProvider := attractor.GRPCTargetProviderFn(func(target string) {
		currentGRPCTarget = target
	})
	grpcTargetGetter := func() string {
		return currentGRPCTarget
	}

	instrumentedLLM := observability.NewTracingLLMClient(llmClient, tp)
	instrumentedContainer := observability.NewTracingContainerManager(containerMgr, tp)
	validateFn := buildValidateFn(scenarios, instrumentedLLM, p.JudgeModel, executorOpts{
		logger:        logger,
		sessionGetter: sessionGetter,
		needsBrowser:  caps.NeedsBrowser,
		needsWS:       caps.NeedsWS,
		needsTUI:      caps.NeedsTUI,
	}, grpcTargetGetter)
	instrumentedValidate := observability.WrapValidateFn(validateFn, tp)

	// Build component validators when gene components are available.
	var componentValidators map[string]attractor.ValidateFn
	if len(p.GeneComponents) > 0 {
		componentValidators = buildComponentValidators(scenarios, instrumentedLLM, p.JudgeModel, executorOpts{
			logger:        logger,
			sessionGetter: sessionGetter,
			needsBrowser:  caps.NeedsBrowser,
			needsWS:       caps.NeedsWS,
			needsTUI:      caps.NeedsTUI,
		}, grpcTargetGetter, tp)
	}

	att := attractor.New(instrumentedLLM, instrumentedContainer, logger, tp)
	opts := attractor.RunOptions{
		Model:               p.Model,
		FrugalModel:         p.FrugalModel,
		JudgeModel:          p.JudgeModel,
		BudgetUSD:           p.Budget,
		Threshold:           p.Threshold,
		PatchMode:           p.PatchMode,
		BlockOnRegression:   p.BlockOnRegression,
		ContextBudget:       p.ContextBudget,
		Language:            p.Language,
		Progress:            progressFn(ctx, logger, st, os.Stderr, p.Verbosity),
		Capabilities:        caps,
		Genes:               p.GenesGuide,
		GeneLanguage:        p.GeneLanguage,
		TestCommand:         parsedSpec.TestCommand,
		MaxTokens:           p.MaxTokens,
		Agentic:             p.Agentic,
		AgentMaxTurns:       p.AgentMaxTurns,
		GeneComponents:      p.GeneComponents,
		ComponentValidators: componentValidators,
		Stratified:          p.Stratified,
	}

	startedAt := time.Now()
	result, err := att.Run(ctx, parsedSpec.RawContent, opts, instrumentedValidate, sessionProvider, grpcTargetProvider)
	if err != nil {
		return fmt.Errorf("attractor run: %w", err)
	}
	finishedAt := time.Now()

	printRunSummary(result)
	recordRun(ctx, logger, st, result, p.SpecPath, p.Model, p.Threshold, p.Budget, startedAt, finishedAt, p.Language)
	printResult(result, p.Language)
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
	switch step.StepType() {
	case "request":
		caps.NeedsHTTP = true
	case "exec":
		caps.NeedsExec = true
	case "browser":
		caps.NeedsBrowser = true
	case "grpc":
		caps.NeedsGRPC = true
	case "ws":
		caps.NeedsHTTP = true
		caps.NeedsWS = true
	case "tui":
		caps.NeedsTUI = true
	}
}

func progressFn(ctx context.Context, logger *slog.Logger, st *store.Store, w io.Writer, verbosity int) func(attractor.IterationProgress) {
	return func(p attractor.IterationProgress) {
		turnsStr := ""
		if p.Turns > 0 {
			turnsStr = fmt.Sprintf("  turns=%d", p.Turns)
		}
		if p.Outcome != attractor.OutcomeValidated {
			_, _ = fmt.Fprintf(w, "iter %d/%d  %s  cost: $%.2f%s  [%s]\n", //nolint:gosec // G705 false positive: writing to injected io.Writer, not an HTTP response
				p.Iteration, p.MaxIterations, p.Outcome,
				p.TotalCostUSD, turnsStr, p.Elapsed.Truncate(time.Second))
		} else {
			_, _ = fmt.Fprintf(w, "iter %d/%d  satisfaction: %.1f/%.1f  cost: $%.2f%s  trend: %s  [%s]\n", //nolint:gosec // G705 false positive: writing to injected io.Writer, not an HTTP response
				p.Iteration, p.MaxIterations, p.Satisfaction, p.Threshold,
				p.TotalCostUSD, turnsStr, p.Trend, p.Elapsed.Truncate(time.Second))
		}

		// p.Failures holds per-scenario feedback strings (both passing and failing scenarios);
		// they are only populated after the validation phase, so we gate on OutcomeValidated.
		if verbosity > 0 && p.Outcome == attractor.OutcomeValidated && len(p.Failures) > 0 {
			for _, f := range p.Failures {
				if verbosity >= 2 {
					_, _ = fmt.Fprintf(w, "  %s\n", f) //nolint:gosec // G705 false positive: writing to injected io.Writer, not an HTTP response
				} else {
					line, _, _ := strings.Cut(f, "\n")
					_, _ = fmt.Fprintf(w, "  %s\n", line) //nolint:gosec // G705 false positive: writing to injected io.Writer, not an HTTP response
				}
			}
		}

		it := store.Iteration{
			RunID:        p.RunID,
			Iteration:    p.Iteration,
			Satisfaction: p.Satisfaction,
			InputTokens:  p.InputTokens,
			OutputTokens: p.OutputTokens,
			CostUSD:      p.IterationCostUSD,
			Failures:     p.Failures,
			CreatedAt:    time.Now(),
		}
		if err := st.RecordIteration(ctx, it); err != nil {
			logger.Warn("failed to record iteration", "error", err)
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

func recordRun(ctx context.Context, logger *slog.Logger, st *store.Store, result *attractor.RunResult, specPath, model string, threshold, budget float64, startedAt, finishedAt time.Time, language string) {
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
		TotalTokens:  result.TotalTokens,
		TotalCostUSD: result.CostUSD,
		Status:       result.Status,
		Language:     language,
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

// validateFlags holds parsed flags for the validate subcommand.
type validateFlags struct {
	scenarios         string
	target            string
	code              string
	healthTimeout     time.Duration
	grpcTarget        string
	provider          string
	judgeModel        string
	threshold         float64
	format            string
	verbose           int
	parallelScenarios int
}

func parseValidateFlags(args []string) (validateFlags, error) {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	vf := validateFlags{}
	fs.StringVar(&vf.scenarios, "scenarios", "", "path to scenarios directory (required)")
	fs.StringVar(&vf.target, "target", "", "target URL to validate against")
	fs.StringVar(&vf.code, "code", "", "path to directory with Dockerfile; manages container lifecycle with restart between scenarios")
	fs.DurationVar(&vf.healthTimeout, "health-timeout", 30*time.Second, "container health check timeout (used with --code)")
	fs.StringVar(&vf.grpcTarget, "grpc-target", "", "gRPC target (host:port) to validate against (required for gRPC scenarios)")
	fs.StringVar(&vf.provider, "provider", "", "LLM provider: anthropic or openai (auto-detected from env if omitted)")
	fs.StringVar(&vf.judgeModel, "judge-model", "", "LLM model for satisfaction judging (default: provider-specific)")
	fs.Float64Var(&vf.threshold, "threshold", 0, "minimum satisfaction score (0-100); non-zero enables exit code 1 on failure")
	fs.StringVar(&vf.format, "format", "text", "output format: text or json")
	fs.IntVar(&vf.verbose, "v", 0, "verbosity level: 0=standard, 1=per-scenario summary, 2=full step detail with judge reasoning")
	fs.IntVar(&vf.parallelScenarios, "parallel-scenarios", 1, "number of scenarios to run concurrently (>1 disables container restart; scenarios share container state)")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: octog validate [flags]

Run scenario files against a live service and score each one via an LLM judge.
Each scenario step is executed (HTTP, exec, browser, gRPC, WebSocket, or TUI),
responses are captured, and the judge assigns a 0-100 satisfaction score. The
aggregate score is printed; exit code 1 is returned if --threshold is set and
the score falls below it.

Unlike 'run', validate does not generate code -- it tests an existing service.
Use --target to point at an already-running service, or --code to let octog
build and manage the container lifecycle itself. For --code, the directory must
contain a Dockerfile. The container is built, started, health-checked on port
8080, then scenarios run against it.

See 'octog run --help' for spec and scenario format documentation.

Examples:
  # Validate against a running service
  octog validate --scenarios scenarios/ --target http://localhost:8080

  # Build and run the container, then validate (restarts between scenarios)
  octog validate --scenarios scenarios/ --code ./myapp/

  # Enforce a minimum score and get JSON output for CI
  octog validate --scenarios scenarios/ --target http://localhost:8080 \
    --threshold 90 --format json

Exit codes: 0 = passed (or no threshold set), 1 = below threshold or error.

Flags:
`)
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return validateFlags{}, err
	}

	if vf.format != "text" && vf.format != "json" {
		return validateFlags{}, errInvalidFormat
	}
	if vf.parallelScenarios < 1 {
		return validateFlags{}, errInvalidParallelScenarios
	}
	if vf.code != "" && vf.target != "" {
		return validateFlags{}, errCodeAndTargetConflict
	}
	if vf.scenarios == "" || (vf.target == "" && vf.code == "") {
		fs.Usage()
		return validateFlags{}, errScenariosAndTargetRequired
	}

	return vf, nil
}

func validateCmd(ctx context.Context, logger *slog.Logger, args []string) error {
	vf, err := parseValidateFlags(args)
	if err != nil {
		return err
	}

	clients, err := newLLMClient(vf.provider, logger)
	if err != nil {
		return err
	}
	if vf.judgeModel == "" {
		vf.judgeModel = defaultJudgeModel(clients.provider)
	}

	if err := validateJudgeFlags(vf.threshold, vf.judgeModel); err != nil {
		return err
	}

	scenarios, err := scenario.LoadDir(vf.scenarios)
	if err != nil {
		return fmt.Errorf("load scenarios: %w", err)
	}

	caps := detectCapabilities(scenarios)
	if caps.NeedsGRPC && vf.grpcTarget == "" {
		logger.Warn("scenarios contain gRPC steps but --grpc-target is not set; gRPC steps will fail")
	}

	var targetURL string
	var restartFn attractor.RestartFunc

	if vf.code != "" {
		cs, csErr := setupValidateContainer(ctx, logger, vf.code, vf.healthTimeout)
		if csErr != nil {
			return csErr
		}
		defer cs.cleanup()
		targetURL = cs.url
		restartFn = cs.restart
	} else {
		targetURL = vf.target
		if len(scenarios) > 1 {
			logger.Warn("validating multiple scenarios against --target without container restart; state may accumulate between scenarios")
		}
	}

	agg, err := runAndScore(ctx, scenarios, executorOpts{
		targetURL:     targetURL,
		logger:        logger,
		sessionGetter: func() *container.Session { return nil },
		needsBrowser:  caps.NeedsBrowser,
		needsWS:       caps.NeedsWS,
		needsTUI:      caps.NeedsTUI,
		grpcTarget:    vf.grpcTarget,
	}, clients.client, vf.judgeModel, restartFn, vf.parallelScenarios)
	if err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	return outputValidation(agg, targetURL, vf.threshold, vf.format, vf.verbose, os.Stdout)
}

// validateContainerState holds a running container's URL, restart function, and cleanup.
type validateContainerState struct {
	url     string
	restart attractor.RestartFunc
	cleanup func()
}

// setupValidateContainer builds, starts, and health-checks a container for validate --code.
func setupValidateContainer(ctx context.Context, logger *slog.Logger, codeDir string, healthTimeout time.Duration) (validateContainerState, error) {
	mgr, err := container.NewManager(logger)
	if err != nil {
		return validateContainerState{}, fmt.Errorf("validate: create container manager: %w", err)
	}

	tag := fmt.Sprintf("octog-validate-%d", time.Now().UnixNano())
	if err := mgr.Build(ctx, codeDir, tag); err != nil {
		_ = mgr.Close()
		return validateContainerState{}, fmt.Errorf("validate: build container: %w", err)
	}

	runRes, stop, err := mgr.Run(ctx, tag)
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		mgr.RemoveImage(cleanupCtx, tag)
		_ = mgr.Close()
		return validateContainerState{}, fmt.Errorf("validate: run container: %w", err)
	}

	if err := mgr.WaitHealthy(ctx, runRes.URL, healthTimeout); err != nil {
		stop()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		mgr.RemoveImage(cleanupCtx, tag)
		_ = mgr.Close()
		return validateContainerState{}, fmt.Errorf("validate: health check: %w", err)
	}

	currentStop := stop
	restartFn := func(restartCtx context.Context) (string, error) {
		currentStop()
		currentStop = func() {}
		newRes, newStop, rErr := mgr.Run(restartCtx, tag)
		if rErr != nil {
			return "", fmt.Errorf("validate: restart container: %w", rErr)
		}
		currentStop = newStop
		if hErr := mgr.WaitHealthy(restartCtx, newRes.URL, healthTimeout); hErr != nil {
			newStop()
			currentStop = func() {}
			return "", fmt.Errorf("validate: restart health check: %w", hErr)
		}
		return newRes.URL, nil
	}

	return validateContainerState{
		url:     runRes.URL,
		restart: restartFn,
		cleanup: func() {
			currentStop()
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			mgr.RemoveImage(cleanupCtx, tag)
			_ = mgr.Close()
		},
	}, nil
}

func outputValidation(agg scenario.AggregateResult, target string, threshold float64, format string, verbosity int, w io.Writer) error {
	switch format {
	case "json":
		out := view.NewValidateOutput(agg, target, threshold, stepPassThreshold)
		if err := view.WriteJSON(w, out); err != nil {
			return fmt.Errorf("write json: %w", err)
		}
	default:
		fprintValidationResult(w, agg, verbosity)
		if verbosity >= 1 {
			failures := buildDetailedFailures(agg)
			if len(failures) > 0 {
				_, _ = fmt.Fprintln(w, "\nStep detail:")
				for _, f := range failures {
					if verbosity >= 2 {
						_, _ = fmt.Fprintf(w, "  %s\n", f) //nolint:gosec // G705 false positive: writing to injected io.Writer, not an HTTP response
					} else {
						line, _, _ := strings.Cut(f, "\n")
						_, _ = fmt.Fprintf(w, "  %s\n", line) //nolint:gosec // G705 false positive: writing to injected io.Writer, not an HTTP response
					}
				}
			}
		}
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
		fmt.Fprintf(os.Stderr, `Usage: octog status [flags]

Show recent attractor loop runs from the local SQLite history database,
including run ID, status, language, model, satisfaction score, iteration
count, and total cost.

Examples:
  # Show a summary table of all runs
  octog status

  # Emit machine-readable JSON for scripting
  octog status --format json

Flags:
`)
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

	fmt.Printf("%-10s %-16s %-8s %-28s %7s %5s %9s  %s\n",
		"ID", "STATUS", "LANG", "MODEL", "SCORE", "ITER", "COST", "STARTED")
	for _, r := range runs {
		lang := r.Language
		if lang == "" {
			lang = "auto"
		}
		fmt.Printf("%-10s %-16s %-8s %-28s %6.1f%% %5d $%7.4f  %s\n",
			r.ID, r.Status, lang, r.Model, r.Satisfaction, r.Iterations, r.TotalCostUSD,
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

var (
	_ modelLister = (*llm.AnthropicClient)(nil)
	_ modelLister = (*llm.OpenAIClient)(nil)
)

func modelsCmd(ctx context.Context, logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("models", flag.ContinueOnError)
	provider := fs.String("provider", "", "LLM provider: anthropic or openai (auto-detected from env if omitted)")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: octog models [flags]

Query the configured provider's API and list available model IDs, display
names, and creation dates. Useful for choosing values for --model and
--judge-model in 'octog run' and 'octog validate'.

Examples:
  # List Anthropic models (auto-detected from ANTHROPIC_API_KEY)
  octog models

  # List OpenAI models explicitly
  octog models --provider openai

Flags:
`)
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
		fmt.Fprintf(os.Stderr, `Usage: octog lint [flags]

Statically check spec markdown and scenario YAML files for structural and
semantic errors before running the attractor loop. Diagnostics are classified
as errors (block convergence) or warnings (non-fatal). Exits with code 1 if
any errors are found.

At least one of --spec or --scenarios must be provided.

Examples:
  # Lint only the spec file
  octog lint --spec spec.md

  # Lint only the scenarios directory
  octog lint --scenarios scenarios/

  # Lint both spec and scenarios together
  octog lint --spec spec.md --scenarios scenarios/

Exit codes: 0 = no errors, 1 = errors found (warnings alone do not cause failure).

Flags:
`)
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

func extractCmd(ctx context.Context, logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("extract", flag.ContinueOnError)
	sourceDir := fs.String("source-dir", "", "path to source directory to extract patterns from (required)")
	output := fs.String("output", "genes.json", "output file path (use \"-\" for stdout)")
	model := fs.String("model", "", "LLM model to use for extraction (default: provider-specific)")
	provider := fs.String("provider", "", "LLM provider: anthropic or openai (auto-detected from env if omitted)")
	guidanceFlag := fs.String("guidance", "", "extraction guidance for the LLM (use @file.txt to read from file)")
	maxFiles := fs.Int("max-files", 0, "maximum source files to scan (0 for role-based only, positive to backfill additional source files)")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: octog extract [flags]

Scan an existing codebase and extract idiomatic coding patterns into a gene
file (genes.json). The gene file is passed to 'octog run' via --genes to
guide the generation model toward patterns consistent with the exemplar
codebase — a technique called gene transfusion.

How it works:
  1. Scan: select architecturally-significant files from --source-dir.
  2. Analyze: send selected files to an LLM to extract a structured guide.
  3. Save: write the guide + metadata to genes.json.

The guide produced contains sections: PATTERN (architecture), INVARIANTS
(hard rules), EDGE CASES (error handling), STACK (language/framework),
STRUCTURE (directory layout), BOOT (startup), BUILD (Dockerfile/CI), and
optionally COMPONENTS (named module boundaries with interfaces and
dependencies). When passed to 'octog run --genes', the guide is injected
into the generation prompt so generated code follows the exemplar's patterns.

File selection:
  Language is auto-detected from marker files (go.mod, package.json,
  Cargo.toml, pyproject.toml, or requirements.txt).
  By default (--max-files 0), only role-based files are selected:
    - Marker file (go.mod, etc.)
    - README
    - Dockerfile
    - Entrypoint (main.go, index.ts, app.py, src/main.rs)
    - Handler (largest file in routes/, handlers/, controllers/, api/)
    - Model (largest file in models/, types/, schema/, entities/)
  With --max-files N, additional source files are backfilled largest-first
  up to N total files. All selections are capped at a 20,000-token budget.
  Test files, generated files, lock files, and vendor directories are
  excluded automatically.

Cross-language use:
  Genes extracted from one language can guide generation in another.
  For example, extracting from a Go codebase and running with
  --language python preserves architectural invariants while adapting
  to idiomatic Python constructs.

Components:
  For multi-module codebases, the LLM may produce COMPONENT sections
  in the guide, each with an interface, patterns, and dependency list.
  When components are present, 'octog run' can use --stratified to
  converge component-by-component in dependency order.

The --guidance flag steers the LLM toward specific aspects of the codebase;
use @file.txt to read guidance from a file.

Examples:
  # Extract patterns from a Go project
  octog extract --source-dir ./myapp --output genes.json

  # Extract with targeted guidance
  octog extract --source-dir ./myapp --guidance "focus on error handling"

  # Read guidance from a file
  octog extract --source-dir ./myapp --guidance @guidance.txt

  # Include more source files for richer pattern extraction
  octog extract --source-dir ./myapp --max-files 15

  # Write gene output to stdout (for piping or inspection)
  octog extract --source-dir ./myapp --output -

  # Use extracted genes in a run
  octog run --spec spec.md --scenarios scenarios/ --genes genes.json

Flags:
`)
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *sourceDir == "" {
		fs.Usage()
		return errSourceDirRequired
	}

	info, err := os.Stat(*sourceDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errSourceDirNotExist
		}
		return fmt.Errorf("stat source-dir: %w", err)
	}
	if !info.IsDir() {
		return errSourceDirNotDir
	}

	scan, err := gene.Scan(ctx, *sourceDir, gene.ScanOptions{MaxFiles: *maxFiles})
	if err != nil {
		return fmt.Errorf("scan: %w", err)
	}
	if scan.Language == "" {
		return errNoLanguageDetected
	}
	fmt.Fprintf(os.Stderr, "Scanned %d files (%s)\n", len(scan.Files), scan.Language) //nolint:gosec // G705 false positive: writing to stderr, not an HTTP response

	clients, err := newLLMClient(*provider, logger)
	if err != nil {
		return err
	}
	if *model == "" {
		// Use the cheap/judge model for extraction: pattern extraction is a
		// summarization task that doesn't need the expensive generation model,
		// and the judge-tier model (e.g. claude-haiku-4-5) provides sufficient
		// quality at a fraction of the cost.
		*model = defaultJudgeModel(clients.provider)
	}

	guidance, err := resolveGuidance(*guidanceFlag)
	if err != nil {
		return fmt.Errorf("guidance: %w", err)
	}

	g, err := gene.Analyze(ctx, logger, clients.client, *model, *sourceDir, scan, guidance)
	if err != nil {
		return fmt.Errorf("analyze: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Extracted patterns from %s (%s, %d tokens) → %s\n", *sourceDir, g.Language, g.TokenCount, *output) //nolint:gosec // G705 false positive: writing to stderr, not an HTTP response

	if *output == "-" {
		if err := gene.Validate(g); err != nil {
			return fmt.Errorf("gene validate: %w", err)
		}
		data, err := json.MarshalIndent(g, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal gene: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}

	return gene.Save(*output, g)
}

// resolveGuidance resolves the --guidance flag value.
// An empty string returns ("", nil). A value starting with "@" reads the remainder as a file path.
// Otherwise the value is returned as-is.
func resolveGuidance(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, "@") {
		data, err := os.ReadFile(value[1:])
		if err != nil {
			return "", fmt.Errorf("read guidance file: %w", err)
		}
		return strings.TrimSpace(string(data)), nil
	}
	return value, nil
}

// parseAndCheckPreflight parses the spec file and optionally runs a preflight clarity check.
// It validates the preflight threshold, parses the spec, and (if !skip) checks clarity.
// Returns the parsed spec for use by the attractor loop.
func parseAndCheckPreflight(ctx context.Context, logger *slog.Logger, client llm.Client, specPath, judgeModel string, threshold float64, skip bool) (spec.Spec, error) {
	if threshold < 0 || threshold > 1 {
		return spec.Spec{}, errInvalidPreflightThreshold
	}
	parsedSpec, err := spec.ParseFile(specPath)
	if err != nil {
		return spec.Spec{}, fmt.Errorf("parse spec: %w", err)
	}
	if !skip {
		if err := runPreflightCheck(ctx, logger, client, parsedSpec.RawContent, judgeModel, threshold); err != nil {
			return spec.Spec{}, err
		}
	}
	return parsedSpec, nil
}

// runPreflightCheck runs a preflight clarity assessment on the given spec content.
// Returns an error (wrapping errPreflightFailed) if the spec does not pass.
func runPreflightCheck(ctx context.Context, logger *slog.Logger, client llm.Client, specContent, model string, threshold float64) error {
	result, err := preflight.Check(ctx, client, model, specContent, threshold, logger)
	if err != nil {
		return fmt.Errorf("preflight check: %w", err)
	}
	if !result.Pass {
		fmt.Fprintf(os.Stderr, "Preflight: spec clarity below threshold (%.2f < %.2f)\n", //nolint:gosec // G705 false positive: writing to stderr
			result.AggregateScore, threshold)
		for _, q := range result.Questions {
			fmt.Fprintf(os.Stderr, "  ? %s\n", q) //nolint:gosec // G705 false positive: writing to stderr
		}
		return fmt.Errorf("%w (%.2f < %.2f)", errPreflightFailed, result.AggregateScore, threshold)
	}
	logger.Info("preflight passed", "score", result.AggregateScore)
	return nil
}

var errPreflightSpecRequired = errors.New("spec path argument is required")

func preflightCmd(ctx context.Context, logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("preflight", flag.ContinueOnError)
	provider := fs.String("provider", "", "LLM provider: anthropic or openai (auto-detected from env if omitted)")
	judgeModel := fs.String("judge-model", "", "LLM model for clarity assessment (default: provider-specific)")
	threshold := fs.Float64("threshold", 0.8, "aggregate clarity score threshold (0.0–1.0)")
	verbose := fs.Bool("verbose", false, "show per-dimension strengths and gaps")
	scenarios := fs.String("scenarios", "", "directory of scenario YAML files to assess against the spec (both spec and scenario checks always run; use exit code to gate on either)")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: octog preflight [flags] <spec-path>

Assess the clarity and completeness of a spec before spending attractor loop
budget. The LLM judge evaluates multiple dimensions (e.g. scope, ambiguity,
testability) and produces a 0.0-1.0 aggregate score. If the score is below
--threshold, the command exits with code 1 so it can gate CI pipelines.

When --scenarios is provided, scenario quality is assessed in addition to the
spec, using the same threshold.

Examples:
  # Quick clarity check with default threshold (0.8)
  octog preflight spec.md

  # Show per-dimension strengths and gaps
  octog preflight --verbose spec.md

  # Gate on both spec and scenario quality
  octog preflight --scenarios scenarios/ spec.md

Exit codes: 0 = above threshold, 1 = below threshold.

Flags:
`)
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() < 1 {
		fs.Usage()
		return errPreflightSpecRequired
	}
	specPath := fs.Arg(0)

	if *threshold < 0 || *threshold > 1 {
		return errInvalidPreflightThreshold
	}

	parsedSpec, err := spec.ParseFile(specPath)
	if err != nil {
		return fmt.Errorf("parse spec: %w", err)
	}

	clients, err := newLLMClient(*provider, logger)
	if err != nil {
		return err
	}
	if *judgeModel == "" {
		*judgeModel = defaultJudgeModel(clients.provider)
	}

	result, err := preflight.Check(ctx, clients.client, *judgeModel, parsedSpec.RawContent, *threshold, logger)
	if err != nil {
		return err
	}

	fmt.Printf("Preflight results for: %s\n", specPath)
	specErr := printSpecResult(result, *threshold, *verbose)

	if *scenarios == "" {
		return specErr
	}

	fmt.Println()
	scenarioResult, err := preflight.CheckScenarios(ctx, clients.client, *judgeModel, parsedSpec.RawContent, *scenarios, *threshold, logger)
	if err != nil {
		return err
	}
	printScenarioPreflight(scenarioResult, *threshold)

	if specErr != nil {
		return specErr
	}
	if !scenarioResult.Pass {
		return errScenarioPreflightFailed
	}
	return nil
}

// printSpecResult prints spec preflight scores and returns errPreflightFailed if the spec did not pass.
func printSpecResult(result *preflight.Result, threshold float64, verbose bool) error {
	if verbose {
		printPreflightVerbose(result)
	} else {
		fmt.Printf("  Goal clarity:       %.2f\n", result.GoalClarity)
		fmt.Printf("  Constraint clarity: %.2f\n", result.ConstraintClarity)
		fmt.Printf("  Success clarity:    %.2f\n", result.SuccessClarity)
	}
	fmt.Printf("  Aggregate score:    %.2f (threshold: %.2f)\n", result.AggregateScore, threshold)
	if result.Pass {
		fmt.Printf("  Status: PASS\n")
		return nil
	}
	fmt.Printf("  Status: WARN — spec clarity below threshold\n")
	if len(result.Questions) > 0 {
		fmt.Printf("\nSuggested clarifications:\n")
		for _, q := range result.Questions {
			fmt.Printf("  ? %s\n", q)
		}
	}
	return errPreflightFailed
}

func printScenarioPreflight(result *preflight.ScenarioResult, threshold float64) {
	fmt.Printf("Scenario preflight results:\n")
	fmt.Printf("  Coverage:    %.2f\n", result.Coverage)
	fmt.Printf("  Feasibility: %.2f\n", result.Feasibility)
	fmt.Printf("  Isolation:   %.2f\n", result.Isolation)
	fmt.Printf("  Chains:      %.2f\n", result.Chains)
	fmt.Printf("  Aggregate score: %.2f (threshold: %.2f)\n", result.Aggregate, threshold)
	if result.Pass {
		fmt.Printf("  Status: PASS\n")
	} else {
		fmt.Printf("  Status: WARN — scenario quality below threshold\n")
	}
	if len(result.Issues) > 0 {
		fmt.Printf("\nScenario issues:\n")
		for _, issue := range result.Issues {
			fmt.Printf("  [%s/%s] %s\n", issue.Scenario, issue.Dimension, issue.Detail)
		}
	}
}

func printPreflightVerbose(result *preflight.Result) {
	dims := []struct {
		key   string
		label string
		score float64
	}{
		{"goal", "Goal clarity:", result.GoalClarity},
		{"constraint", "Constraint clarity:", result.ConstraintClarity},
		{"success", "Success clarity:", result.SuccessClarity},
	}
	for _, d := range dims {
		fmt.Printf("  %-20s%.2f\n", d.label, d.score)
		strengths := result.Strengths[d.key]
		gaps := result.Gaps[d.key]
		if len(strengths) == 0 && len(gaps) == 0 {
			fmt.Printf("    (no details available)\n")
		} else {
			for _, s := range strengths {
				fmt.Printf("    ✓ %s\n", s)
			}
			for _, g := range gaps {
				fmt.Printf("    ~ %s\n", g)
			}
		}
		fmt.Println()
	}
}

func openStore(ctx context.Context) (*store.Store, error) {
	path, err := resolveStorePath()
	if err != nil {
		return nil, err
	}
	return store.NewStore(ctx, path)
}

func resolveStorePath() (string, error) {
	p, err := paths.StorePath()
	if err != nil {
		return "", fmt.Errorf("resolve store path: %w", err)
	}
	if err := paths.EnsureParentDir(p); err != nil {
		return "", fmt.Errorf("create store dir: %w", err)
	}
	return p, nil
}

// executorOpts captures the parameters for building per-goroutine step executors.
type executorOpts struct {
	targetURL     string
	logger        *slog.Logger
	sessionGetter func() *container.Session
	needsBrowser  bool
	needsWS       bool
	needsTUI      bool
	grpcTarget    string
}

// buildExecutors creates a fresh set of StepExecutors and a cleanup function.
func buildExecutors(opts executorOpts) (map[string]scenario.StepExecutor, func()) {
	httpClient := &http.Client{Timeout: 30 * time.Second}
	executors := map[string]scenario.StepExecutor{
		"request": &scenario.HTTPExecutor{Client: httpClient, BaseURL: opts.targetURL},
		"exec":    &scenario.ExecExecutor{Session: opts.sessionGetter()},
	}
	var closers []func()
	if opts.needsBrowser {
		browserExec := &scenario.BrowserExecutor{BaseURL: opts.targetURL, Logger: opts.logger}
		executors["browser"] = browserExec
		closers = append(closers, browserExec.Close)
	}
	if opts.grpcTarget != "" {
		grpcExec := &scenario.GRPCExecutor{Target: opts.grpcTarget, Logger: opts.logger}
		executors["grpc"] = grpcExec
		closers = append(closers, grpcExec.Close)
	}
	if opts.needsWS {
		wsExec := &scenario.WSExecutor{BaseURL: opts.targetURL, Logger: opts.logger}
		executors["ws"] = wsExec
		closers = append(closers, wsExec.Close)
	}
	registerTUIExecutor(opts, executors, &closers)
	cleanup := func() {
		for _, fn := range closers {
			fn()
		}
	}
	return executors, cleanup
}

func runAndScore(ctx context.Context, scenarios []scenario.Scenario, opts executorOpts, llmClient llm.Client, judgeModel string, restart attractor.RestartFunc, parallelism int) (scenario.AggregateResult, error) {
	if parallelism <= 1 {
		return runAndScoreSequential(ctx, scenarios, opts, llmClient, judgeModel, restart)
	}
	return runAndScoreParallel(ctx, scenarios, opts, llmClient, judgeModel, parallelism)
}

func runAndScoreSequential(ctx context.Context, scenarios []scenario.Scenario, opts executorOpts, llmClient llm.Client, judgeModel string, restart attractor.RestartFunc) (scenario.AggregateResult, error) {
	scored := make([]scenario.ScoredScenario, 0, len(scenarios))

	for i, sc := range scenarios {
		if ctx.Err() != nil {
			return scenario.AggregateResult{}, ctx.Err()
		}
		if i > 0 && restart != nil {
			newURL, err := restart(ctx)
			if err != nil {
				return scenario.AggregateResult{}, fmt.Errorf("restart container: %w", err)
			}
			opts.targetURL = newURL
		}

		executors, cleanup := buildExecutors(opts)
		runner := scenario.NewRunner(executors, opts.logger)
		judge := scenario.NewJudge(llmClient, judgeModel, opts.logger)

		result, runErr := runner.Run(ctx, sc)
		cleanup()
		if runErr != nil {
			weight := 1.0
			if sc.Weight != nil {
				weight = *sc.Weight
			}
			opts.logger.Warn("scenario setup failed", "scenario", sc.ID, "error", runErr)
			scored = append(scored, scenario.ScoredScenario{
				ScenarioID: sc.ID,
				Weight:     weight,
				Score:      0,
			})
			continue
		}

		ss, err := judge.ScoreScenario(ctx, sc, result)
		if err != nil {
			return scenario.AggregateResult{}, fmt.Errorf("score scenario %s: %w", sc.ID, err)
		}

		scored = append(scored, ss)
	}

	return scenario.Aggregate(scored), nil
}

func runAndScoreParallel(ctx context.Context, scenarios []scenario.Scenario, opts executorOpts, llmClient llm.Client, judgeModel string, parallelism int) (scenario.AggregateResult, error) {
	if opts.logger != nil {
		opts.logger.Info("running scenarios in parallel", "count", len(scenarios), "parallelism", parallelism)
	}

	results := make([]scenario.ScoredScenario, len(scenarios))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(parallelism)

	for i, sc := range scenarios {
		g.Go(func() error {
			executors, cleanup := buildExecutors(opts)
			runner := scenario.NewRunner(executors, opts.logger)
			judge := scenario.NewJudge(llmClient, judgeModel, opts.logger)

			result, runErr := runner.Run(gctx, sc)
			cleanup()
			if runErr != nil {
				weight := 1.0
				if sc.Weight != nil {
					weight = *sc.Weight
				}
				if opts.logger != nil {
					opts.logger.Warn("scenario setup failed", "scenario", sc.ID, "error", runErr)
				}
				results[i] = scenario.ScoredScenario{
					ScenarioID: sc.ID,
					Weight:     weight,
					Score:      0,
				}
				return nil
			}

			ss, err := judge.ScoreScenario(gctx, sc, result)
			if err != nil {
				return fmt.Errorf("score scenario %s: %w", sc.ID, err)
			}
			results[i] = ss
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return scenario.AggregateResult{}, err
	}

	return scenario.Aggregate(results), nil
}

// buildComponentValidators creates a map of per-component ValidateFn closures.
// Scenarios are grouped by their Component field in a single pass.
// The "" key maps to scenarios with empty Component (integration scenarios).
func buildComponentValidators(scenarios []scenario.Scenario, llmClient llm.Client, judgeModel string, baseOpts executorOpts, grpcTargetGetter func() string, tp trace.TracerProvider) map[string]attractor.ValidateFn {
	// Group scenarios by component in a single pass.
	grouped := make(map[string][]scenario.Scenario)
	for _, sc := range scenarios {
		grouped[sc.Component] = append(grouped[sc.Component], sc)
	}

	// Component validators always use the full per-component scenario set (maxTier is ignored).
	validators := make(map[string]attractor.ValidateFn, len(grouped))
	for name, group := range grouped {
		validators[name] = observability.WrapValidateFn(
			buildValidateFn(group, llmClient, judgeModel, baseOpts, grpcTargetGetter), tp)
	}
	return validators
}

func buildValidateFn(scenarios []scenario.Scenario, llmClient llm.Client, judgeModel string, baseOpts executorOpts, grpcTargetGetter func() string) attractor.ValidateFn {
	return func(ctx context.Context, url string, restart attractor.RestartFunc, maxTier int) (float64, []string, float64, error) {
		active := scenarios
		if maxTier > 0 {
			filtered := make([]scenario.Scenario, 0, len(scenarios))
			for _, sc := range scenarios {
				if sc.Tier <= maxTier {
					filtered = append(filtered, sc)
				}
			}
			if baseOpts.logger != nil {
				baseOpts.logger.Debug("stratified validation: filtered scenarios", "max_tier", maxTier, "total", len(scenarios), "active", len(filtered))
			}
			active = filtered
		}
		opts := baseOpts
		opts.targetURL = url
		opts.grpcTarget = grpcTargetGetter()
		agg, err := runAndScore(ctx, active, opts, llmClient, judgeModel, restart, 1)
		if err != nil {
			return 0, nil, 0, err
		}
		return agg.Satisfaction, buildDetailedFailures(agg), agg.TotalCostUSD, nil
	}
}

// buildDetailedFailures converts an AggregateResult into a slice of feedback strings
// for the attractor loop. Passing scenarios produce a single summary line; failing
// scenarios expand to include per-step detail with reasoning and observed output for
// failing steps.
func buildDetailedFailures(agg scenario.AggregateResult) []string {
	out := make([]string, 0, len(agg.Scenarios))
	for _, sc := range agg.Scenarios {
		if sc.Score >= float64(stepPassThreshold) {
			out = append(out, fmt.Sprintf("✓ %s (%.0f/100)", sc.ScenarioID, sc.Score))
		} else {
			out = append(out, formatFailedScenario(sc))
		}
	}
	return out
}

// formatFailedScenario formats a failing scenario as a multi-line string with
// per-step detail. Failing steps include reasoning and observed output; passing
// steps within a failing scenario appear as a single-line summary.
//
// The first line is produced by attractor.FormatScenarioFailureLine so that the
// format is defined in a single place shared with internal/attractor.parseFailedScenarios.
func formatFailedScenario(s scenario.ScoredScenario) string {
	var b strings.Builder
	b.WriteString(attractor.FormatScenarioFailureLine(s.ScenarioID, s.Score))
	for _, step := range s.Steps {
		if step.StepScore.Score >= stepPassThreshold {
			fmt.Fprintf(&b, "\n%s %s (%d/100)", attractor.StepPassPrefix, step.StepResult.Description, step.StepScore.Score) //nolint:gosec // G705 false positive: writing to strings.Builder, not an HTTP response
			continue
		}
		fmt.Fprintf(&b, "\n%s %s (%d/100)", attractor.StepFailPrefix, step.StepResult.Description, step.StepScore.Score) //nolint:gosec // G705 false positive: writing to strings.Builder, not an HTTP response
		if step.StepScore.Reasoning != "" {
			fmt.Fprintf(&b, "\n    Reasoning: %s", step.StepScore.Reasoning) //nolint:gosec // G705 false positive: writing to strings.Builder, not an HTTP response
		}
		if step.StepResult.Observed != "" {
			obs := step.StepResult.Observed
			label := "Observed"
			if len(obs) > attractor.MaxObservedBytes {
				obs = truncateObserved(obs, attractor.MaxObservedBytes)
				label = fmt.Sprintf("Observed (%dB)", attractor.MaxObservedBytes)
			}
			fmt.Fprintf(&b, "\n    %s: %s", label, obs) //nolint:gosec // G705 false positive: writing to strings.Builder, not an HTTP response
		}
		for _, d := range step.StepScore.Diagnostics {
			fmt.Fprintf(&b, "\n    [%s] %s", d.Category, d.Detail) //nolint:gosec // G705 false positive: writing to strings.Builder, not an HTTP response
		}
	}
	return b.String()
}

// truncateObserved truncates observed output to max bytes, removing any incomplete
// UTF-8 rune at the cut point and appending a … suffix.
func truncateObserved(s string, max int) string {
	if len(s) <= max {
		return s
	}
	truncated := s[:max]
	for len(truncated) > 0 {
		r, size := utf8.DecodeLastRuneInString(truncated)
		if r != utf8.RuneError || size != 1 {
			break
		}
		truncated = truncated[:len(truncated)-1]
	}
	return truncated + "…"
}

//nolint:gosec // G705 false positive: w is os.Stdout or test buffer, not an HTTP response
func fprintValidationResult(w io.Writer, agg scenario.AggregateResult, verbosity int) {
	_, _ = fmt.Fprintln(w, "Scenarios:")
	for _, sc := range agg.Scenarios {
		_, _ = fmt.Fprintf(w, "  %-30s %5.1f/100  (weight %.1f)\n", sc.ScenarioID, sc.Score, sc.Weight)
		for _, step := range sc.Steps {
			label := "PASS"
			if step.StepScore.Score < stepPassThreshold {
				label = "FAIL"
			}
			_, _ = fmt.Fprintf(w, "    [%s]  %3d  %s\n", label, step.StepScore.Score, step.StepResult.Description)
			if step.StepScore.Reasoning != "" && (label == "FAIL" || verbosity >= 2) {
				_, _ = fmt.Fprintf(w, "           Reasoning: %s\n", step.StepScore.Reasoning)
			}
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

func loadConfig(logger *slog.Logger) error {
	path, err := paths.ConfigFile()
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

	f, err := os.Open(path) //nolint:gosec // G304: path is derived from paths.ConfigFile(), not user input
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

func printResult(result *attractor.RunResult, language string) {
	displayLang := language
	if displayLang == "" {
		displayLang = "auto"
	}
	fmt.Printf("\nRun complete: %s\n", result.RunID)
	fmt.Printf("  Status:       %s\n", result.Status)
	fmt.Printf("  Language:     %s\n", displayLang)
	fmt.Printf("  Iterations:   %d\n", result.Iterations)
	fmt.Printf("  Satisfaction: %.1f%%\n", result.Satisfaction)
	fmt.Printf("  Cost:         $%.4f\n", result.CostUSD)
	fmt.Printf("  Output:       %s\n", result.OutputDir)
}

func interviewCmd(ctx context.Context, logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("interview", flag.ContinueOnError)
	output := fs.String("output", "spec.md", "output file path for the generated spec")
	model := fs.String("model", "", "LLM model to use (default: provider-specific)")
	provider := fs.String("provider", "", "LLM provider: anthropic or openai (auto-detected from env if omitted)")
	prompt := fs.String("prompt", "What would you like to build?", "opening question to start the interview")
	seed := fs.String("seed", "", "path to existing spec file to improve (mutually exclusive with --prompt)")
	scenarios := fs.Bool("scenarios", false, "generate holdout scenario YAML files alongside the spec")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: octog interview [flags]

Start a conversational session to draft a spec file. The LLM asks clarifying
questions to help you articulate requirements, constraints, and acceptance
criteria. The resulting spec is written to --output (default: spec.md).

Use --seed to provide an existing spec for the LLM to improve rather than
starting from scratch. --seed and --prompt are mutually exclusive.

With --scenarios, the interview also generates a set of holdout scenario YAML
files alongside the spec, ready to pass to 'octog run'.

Examples:
  # Start a fresh interview
  octog interview

  # Improve an existing spec
  octog interview --seed existing-spec.md --output improved-spec.md

  # Draft spec and scenarios in one session
  octog interview --scenarios --output spec.md

Flags:
`)
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	promptExplicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "prompt" {
			promptExplicit = true
		}
	})
	if *seed != "" && promptExplicit {
		return errSeedAndPromptConflict
	}

	var seedContent string
	if *seed != "" {
		data, err := os.ReadFile(*seed)
		if err != nil {
			return fmt.Errorf("read seed: %w", err)
		}
		seedContent = string(data)
	}

	// Suppress per-call LLM usage logs during the interview to keep the
	// conversation clean for the user.
	quietLogger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	clients, err := newLLMClient(*provider, quietLogger)
	if err != nil {
		return err
	}
	if *model == "" {
		*model = defaultModel(clients.provider)
	}
	logger.Debug("starting interview", "provider", clients.provider, "model", *model)

	var display interview.Display
	if isatty.IsTerminal(os.Stdout.Fd()) {
		display = ui.NewStyled(os.Stdout, os.Stdout.Fd())
	} else {
		display = ui.NewPlain(os.Stdout)
	}

	return interviewRun(ctx, clients.client, *model, *prompt, *output, seedContent, *scenarios, logger, os.Stdin, display, os.Stderr)
}

// interviewRun runs the interview conversation and writes the resulting spec to
// outputPath. Separated from interviewCmd for testability.
// When seedContent is non-empty, RunWithSeed is used instead of Run.
// When generateScenarios is true, scenario YAML files are generated and written
// to a scenarios/ directory alongside the spec.
func interviewRun(ctx context.Context, client llm.Client, model, initialPrompt, outputPath, seedContent string, generateScenarios bool, log *slog.Logger, in io.Reader, display interview.Display, errOut io.Writer) error {
	iv := interview.New(client, in, display, errOut, model)
	var (
		spec string
		cost float64
		err  error
	)
	if seedContent != "" {
		spec, cost, err = iv.RunWithSeed(ctx, seedContent)
	} else {
		spec, cost, err = iv.Run(ctx, initialPrompt)
	}
	if err != nil {
		return err
	}

	if err := os.WriteFile(outputPath, []byte(spec), 0o644); err != nil { //nolint:gosec // G306: spec.md is a user-facing document, 0644 is intentional
		return fmt.Errorf("write spec: %w", err)
	}

	specCostStr := fmt.Sprintf("$%.4f", cost)
	if cost == 0 {
		specCostStr = "free"
	}
	fmt.Fprintf(errOut, "Spec written to %s (cost: %s)\n", outputPath, specCostStr) //nolint:gosec,errcheck // G705 false positive: writing to stderr, not an HTTP response

	totalCost := cost
	if generateScenarios {
		scenarioCost, err := writeGeneratedScenarios(ctx, client, model, spec, outputPath, log, errOut)
		if err != nil {
			return err
		}
		totalCost += scenarioCost
	}

	if generateScenarios {
		totalCostStr := fmt.Sprintf("$%.4f", totalCost)
		fmt.Fprintf(errOut, "Total cost: %s\n", totalCostStr) //nolint:gosec,errcheck // G705 false positive: writing to errOut, not an HTTP response
	}
	return nil
}

// writeGeneratedScenarios generates scenario YAML files and writes them to a
// scenarios/ directory alongside outputPath. Returns the LLM cost incurred.
func writeGeneratedScenarios(ctx context.Context, client llm.Client, model, specContent, outputPath string, log *slog.Logger, errOut io.Writer) (float64, error) {
	gen := interview.NewScenarioGenerator(client, model, log)
	files, cost, err := gen.Generate(ctx, specContent)
	if err != nil {
		return cost, fmt.Errorf("generate scenarios: %w", err)
	}

	dir := filepath.Join(filepath.Dir(outputPath), "scenarios")
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: scenarios/ is user-facing output directory
		return cost, fmt.Errorf("create scenarios dir: %w", err)
	}

	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil { //nolint:gosec // G306: scenario files are user-facing documents
			return cost, fmt.Errorf("write scenario %s: %w", name, err)
		}
	}

	fmt.Fprintf(errOut, "Scenarios written to %s (%d file(s))\n", dir, len(files)) //nolint:gosec,errcheck // G705 false positive: writing to errOut (io.Writer), not an HTTP response
	return cost, nil
}

func configureCmd(_ context.Context, _ *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("configure", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: octog configure

Interactively set API keys and other settings. Values are written to the
platform-native config file in KEY=VALUE format. Environment variables take
precedence over config file values at runtime.

Config file location:
  macOS:   ~/Library/Application Support/octopusgarden/config
  Linux:   ~/.config/octopusgarden/config (or $XDG_CONFIG_HOME/octopusgarden/config)
  Override: set OCTOG_CONFIG_DIR to use a custom path

Example:
  octog configure
`)
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	cfgPath, err := paths.ConfigFile()
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
	f, err := os.Open(path) //nolint:gosec // G304: path derives from paths.ConfigFile(), not user input
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
