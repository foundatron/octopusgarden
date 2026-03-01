package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/foundatron/octopusgarden/internal/attractor"
	"github.com/foundatron/octopusgarden/internal/container"
	"github.com/foundatron/octopusgarden/internal/llm"
	"github.com/foundatron/octopusgarden/internal/scenario"
	"github.com/foundatron/octopusgarden/internal/spec"
	"github.com/foundatron/octopusgarden/internal/store"
)

const judgeModel = "claude-haiku-4-5-20251001"

var (
	errSpecAndScenariosRequired   = errors.New("--spec and --scenarios are required")
	errScenariosAndTargetRequired = errors.New("--scenarios and --target are required")
	errMissingAPIKey              = errors.New("ANTHROPIC_API_KEY environment variable is required")
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

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
	fmt.Fprintf(os.Stderr, `Usage: octopusgarden <command> [flags]

Commands:
  run        Run the attractor loop to generate software from a spec
  validate   Validate a running service against scenarios
  status     Show recent runs, scores, and costs

Run 'octopusgarden <command> --help' for details.
`)
}

func runCmd(ctx context.Context, logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	specFlag := fs.String("spec", "", "path to spec file (required)")
	scenariosFlag := fs.String("scenarios", "", "path to scenarios directory (required)")
	model := fs.String("model", "claude-sonnet-4-20250514", "LLM model to use for generation")
	budget := fs.Float64("budget", 5.00, "maximum budget in USD")
	threshold := fs.Float64("threshold", 95, "satisfaction threshold (0-100)")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: octopusgarden run [flags]\n\nFlags:\n")
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
		Model:     *model,
		BudgetUSD: *budget,
		Threshold: *threshold,
	}

	startedAt := time.Now()
	result, err := att.Run(ctx, parsedSpec.RawContent, opts, validateFn)
	if err != nil {
		return fmt.Errorf("attractor run: %w", err)
	}
	finishedAt := time.Now()

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

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: octopusgarden validate [flags]\n\nFlags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *scenariosFlag == "" || *target == "" {
		fs.Usage()
		return errScenariosAndTargetRequired
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

	validateFn := buildValidateFn(scenarios, llmClient, logger)
	satisfaction, failures, cost, err := validateFn(ctx, *target)
	if err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	fmt.Printf("Satisfaction: %.1f/100\n", satisfaction)
	fmt.Printf("Cost: $%.4f\n", cost)
	if len(failures) > 0 {
		fmt.Println("Failures:")
		for _, f := range failures {
			fmt.Printf("  - %s\n", f)
		}
	}
	return nil
}

func statusCmd(ctx context.Context, _ *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: octopusgarden status\n\nShow recent runs, scores, and costs.\n")
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

func buildValidateFn(scenarios []scenario.Scenario, llmClient llm.Client, logger *slog.Logger) attractor.ValidateFn {
	return func(ctx context.Context, url string) (float64, []string, float64, error) {
		httpClient := &http.Client{Timeout: 30 * time.Second}
		runner := scenario.NewRunner(url, httpClient, logger)
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
				return 0, nil, 0, fmt.Errorf("score scenario %s: %w", sc.ID, err)
			}
			scored = append(scored, ss)
		}

		agg := scenario.Aggregate(scored)
		return agg.Satisfaction, agg.Failures, agg.TotalCostUSD, nil
	}
}

func printResult(result *attractor.RunResult) {
	fmt.Printf("\nRun complete: %s\n", result.RunID)
	fmt.Printf("  Status:       %s\n", result.Status)
	fmt.Printf("  Iterations:   %d\n", result.Iterations)
	fmt.Printf("  Satisfaction: %.1f%%\n", result.Satisfaction)
	fmt.Printf("  Cost:         $%.4f\n", result.CostUSD)
	fmt.Printf("  Output:       %s\n", result.OutputDir)
}
