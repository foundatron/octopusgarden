//go:build integration

package gene

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/foundatron/octopusgarden/internal/llm"
)

func TestAnalyzeRealLLM(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}

	// Find repo root.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root with go.mod")
		}
		dir = parent
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := llm.NewAnthropicClient(apiKey, logger)

	scan, err := Scan(context.Background(), dir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	g, err := Analyze(context.Background(), logger, client, "claude-haiku-4-5", dir, scan)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	if g.Guide == "" {
		t.Error("Guide is empty")
	}
	if g.Language != "go" {
		t.Errorf("Language = %q, want %q", g.Language, "go")
	}
	if g.TokenCount <= 0 {
		t.Errorf("TokenCount = %d, want > 0", g.TokenCount)
	}
	if g.TokenCount > 1500 {
		t.Errorf("TokenCount = %d, want < 1500", g.TokenCount)
	}
}
