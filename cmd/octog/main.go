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
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/foundatron/octopusgarden/internal/attractor"
	"github.com/foundatron/octopusgarden/internal/container"
	"github.com/foundatron/octopusgarden/internal/llm"
	"github.com/foundatron/octopusgarden/internal/scenario"
	"github.com/foundatron/octopusgarden/internal/spec"
	"github.com/foundatron/octopusgarden/internal/store"
)

const judgeModel = "claude-haiku-4-5-20251001"

// stepPassThreshold is the per-step score below which a step is labeled FAIL
// in validation output. This is purely cosmetic — the --threshold flag on
// validateCmd controls the aggregate satisfaction gate.
const stepPassThreshold = 80

var (
	errSpecAndScenariosRequired   = errors.New("--spec and --scenarios are required")
	errScenariosAndTargetRequired = errors.New("--scenarios and --target are required")
	errMissingAPIKey              = errors.New("ANTHROPIC_API_KEY not set (use env var or ~/.octopusgarden/config)")
	errBelowThreshold             = errors.New("satisfaction below threshold")
	errInvalidThreshold           = errors.New("--threshold must be between 0 and 100")
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if err := loadConfig(logger); err != nil {
		logger.Warn("failed to load config", "error", err)
	}

	if !llm.HasModelPricing(judgeModel) {
		logger.Error("judge model has no pricing entry", "model", judgeModel)
		os.Exit(1)
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

Run 'octog <command> --help' for details.
`)
}

func runCmd(ctx context.Context, logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	specFlag := fs.String("spec", "", "path to spec file (required)")
	scenariosFlag := fs.String("scenarios", "", "path to scenarios directory (required)")
	model := fs.String("model", "claude-sonnet-4-20250514", "LLM model to use for generation")
	budget := fs.Float64("budget", 5.00, "maximum budget in USD")
	threshold := fs.Float64("threshold", 95, "satisfaction threshold (0-100)")
	patchMode := fs.Bool("patch", false, "enable incremental patch mode (iteration 2+ sends only changed files)")
	contextBudget := fs.Int("context-budget", 0, "max estimated tokens for spec in system prompt; 0 = unlimited")

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

	// Parse spec.
	parsedSpec, err := spec.ParseFile(*specFlag)
	if err != nil {
		return fmt.Errorf("parse spec: %w", err)
	}

	// Load scenarios.
	scenarios, err := scenario.LoadDir(*scenariosFlag)
	if err != nil {
		return fmt.Errorf("load scenarios: %w", err)
	}

	// Create LLM client.
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return errMissingAPIKey
	}
	llmClient := llm.NewAnthropicClient(apiKey, logger)

	// Create container manager.
	containerMgr, err := container.NewManager(logger)
	if err != nil {
		return fmt.Errorf("create container manager: %w", err)
	}

	// Create store.
	storePath, err := resolveStorePath()
	if err != nil {
		return err
	}
	st, err := store.NewStore(ctx, storePath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	// Build validate function.
	validateFn := buildValidateFn(scenarios, llmClient, logger)

	// Create attractor and run.
	att := attractor.New(llmClient, containerMgr, logger)
	opts := attractor.RunOptions{
		Model:         *model,
		BudgetUSD:     *budget,
		Threshold:     *threshold,
		PatchMode:     *patchMode,
		ContextBudget: *contextBudget,
		Progress: func(p attractor.IterationProgress) {
			if p.Outcome != attractor.OutcomeValidated {
				fmt.Fprintf(os.Stderr, "iter %d/%d  %s  cost: $%.2f  [%s]\n", //nolint:gosec // G705 false positive: writing to stderr, not an HTTP response
					p.Iteration, p.MaxIterations, p.Outcome,
					p.TotalCostUSD, p.Elapsed.Truncate(time.Second))
			} else {
				fmt.Fprintf(os.Stderr, "iter %d/%d  satisfaction: %.1f/%.1f  cost: $%.2f  trend: %s  [%s]\n", //nolint:gosec // G705 false positive: writing to stderr, not an HTTP response
					p.Iteration, p.MaxIterations, p.Satisfaction, p.Threshold,
					p.TotalCostUSD, p.Trend, p.Elapsed.Truncate(time.Second))
			}
		},
	}

	startedAt := time.Now()
	result, err := att.Run(ctx, parsedSpec.RawContent, opts, validateFn)
	if err != nil {
		return fmt.Errorf("attractor run: %w", err)
	}
	finishedAt := time.Now()

	// Print final summary line.
	if result.Status == attractor.StatusConverged {
		fmt.Fprintf(os.Stderr, "\n✓ Converged after %d iterations ($%.2f total)\n", result.Iterations, result.CostUSD) //nolint:gosec // G705 false positive: writing to stderr, not an HTTP response
	} else {
		fmt.Fprintf(os.Stderr, "\n✗ %s after %d iterations ($%.2f total)\n", result.Status, result.Iterations, result.CostUSD) //nolint:gosec // G705 false positive: writing to stderr, not an HTTP response
	}

	// Record result in store.
	run := store.Run{
		ID:           result.RunID,
		SpecPath:     *specFlag,
		Model:        *model,
		Threshold:    *threshold,
		BudgetUSD:    *budget,
		StartedAt:    startedAt,
		FinishedAt:   &finishedAt,
		Satisfaction: result.Satisfaction,
		Iterations:   result.Iterations,
		TotalCostUSD: result.CostUSD,
		Status:       result.Status,
		// TotalTokens not yet tracked by attractor.RunResult
	}
	if err := st.RecordRun(ctx, run); err != nil {
		logger.Warn("failed to record run", "error", err)
	}

	printResult(result)
	return nil
}

func validateCmd(ctx context.Context, logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	scenariosFlag := fs.String("scenarios", "", "path to scenarios directory (required)")
	target := fs.String("target", "", "target URL to validate against (required)")
	threshold := fs.Float64("threshold", 0, "minimum satisfaction score (0-100); non-zero enables exit code 1 on failure")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: octog validate [flags]\n\nFlags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *scenariosFlag == "" || *target == "" {
		fs.Usage()
		return errScenariosAndTargetRequired
	}

	if *threshold < 0 || *threshold > 100 {
		return errInvalidThreshold
	}

	// Load scenarios.
	scenarios, err := scenario.LoadDir(*scenariosFlag)
	if err != nil {
		return fmt.Errorf("load scenarios: %w", err)
	}

	// Create LLM client for judging.
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return errMissingAPIKey
	}
	llmClient := llm.NewAnthropicClient(apiKey, logger)

	agg, err := runAndScore(ctx, scenarios, *target, llmClient, logger)
	if err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	fprintValidationResult(os.Stdout, agg)

	if *threshold > 0 && agg.Satisfaction < *threshold {
		return fmt.Errorf("%w: %.1f < %.1f", errBelowThreshold, agg.Satisfaction, *threshold)
	}
	return nil
}

func statusCmd(ctx context.Context, _ *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: octog status\n\nShow recent runs, scores, and costs.\n")
	}

	if err := fs.Parse(args); err != nil {
		return err
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

func runAndScore(ctx context.Context, scenarios []scenario.Scenario, targetURL string, llmClient llm.Client, logger *slog.Logger) (scenario.AggregateResult, error) {
	httpClient := &http.Client{Timeout: 30 * time.Second}
	runner := scenario.NewRunner(targetURL, httpClient, logger)
	judge := scenario.NewJudge(llmClient, judgeModel, logger)

	scored := make([]scenario.ScoredScenario, 0, len(scenarios))
	for _, sc := range scenarios {
		result, err := runner.Run(ctx, sc)
		if err != nil {
			// Setup failure: score as 0 satisfaction at the scenario's weight.
			weight := 1.0
			if sc.Weight != nil {
				weight = *sc.Weight
			}
			logger.Warn("scenario setup failed", "scenario", sc.ID, "error", err)
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

func buildValidateFn(scenarios []scenario.Scenario, llmClient llm.Client, logger *slog.Logger) attractor.ValidateFn {
	return func(ctx context.Context, url string) (float64, []string, float64, error) {
		agg, err := runAndScore(ctx, scenarios, url, llmClient, logger)
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
}

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
	if perm := info.Mode().Perm(); perm&0o044 != 0 {
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

func printResult(result *attractor.RunResult) {
	fmt.Printf("\nRun complete: %s\n", result.RunID)
	fmt.Printf("  Status:       %s\n", result.Status)
	fmt.Printf("  Iterations:   %d\n", result.Iterations)
	fmt.Printf("  Satisfaction: %.1f%%\n", result.Satisfaction)
	fmt.Printf("  Cost:         $%.4f\n", result.CostUSD)
	fmt.Printf("  Output:       %s\n", result.OutputDir)
}
