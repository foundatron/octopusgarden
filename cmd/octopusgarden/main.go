package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
)

var (
	errSpecAndScenariosRequired   = errors.New("--spec and --scenarios are required")
	errScenariosAndTargetRequired = errors.New("--scenarios and --target are required")
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "run":
		err = runCmd(logger, os.Args[2:])
	case "validate":
		err = validateCmd(logger, os.Args[2:])
	case "status":
		err = statusCmd(logger, os.Args[2:])
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

func runCmd(logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	spec := fs.String("spec", "", "path to spec file (required)")
	scenarios := fs.String("scenarios", "", "path to scenarios directory (required)")
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

	if *spec == "" || *scenarios == "" {
		fs.Usage()
		return errSpecAndScenariosRequired
	}

	logger.Info("run: not yet implemented",
		"spec", *spec,
		"scenarios", *scenarios,
		"model", *model,
		"budget", *budget,
		"threshold", *threshold,
	)
	return nil
}

func validateCmd(logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	scenarios := fs.String("scenarios", "", "path to scenarios directory (required)")
	target := fs.String("target", "", "target URL to validate against (required)")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: octopusgarden validate [flags]\n\nFlags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *scenarios == "" || *target == "" {
		fs.Usage()
		return errScenariosAndTargetRequired
	}

	logger.Info("validate: not yet implemented",
		"scenarios", *scenarios,
		"target", *target,
	)
	return nil
}

func statusCmd(logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: octopusgarden status\n\nShow recent runs, scores, and costs.\n")
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	logger.Info("status: not yet implemented")
	return nil
}
