package attractor

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/foundatron/octopusgarden/internal/llm"
)

// makeFiles builds a map[string]string with n synthetic files plus any extras.
func makeFiles(n int, extras ...string) map[string]string {
	files := make(map[string]string, n+len(extras))
	for i := range n {
		files[fmt.Sprintf("file%02d.go", i)] = fmt.Sprintf("package main // file %d\n", i)
	}
	for _, e := range extras {
		files[e] = "// entry\n"
	}
	return files
}

func TestTriageFilesSkipSmallSets(t *testing.T) {
	a := &Attractor{
		llm: &mockLLMClient{
			generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
				panic("LLM should not be called for small file sets")
			},
		},
	}
	files := makeFiles(3)
	failures := []string{"something broke"}
	got, cost, tokens := a.triageFiles(context.Background(), files, failures, "")
	if len(got) != len(files) {
		t.Errorf("expected all %d files, got %d", len(files), len(got))
	}
	if cost != 0 {
		t.Errorf("expected zero cost, got %f", cost)
	}
	if tokens != 0 {
		t.Errorf("expected zero tokens, got %d", tokens)
	}
}

func TestTriageFilesSkipNoFailures(t *testing.T) {
	a := &Attractor{
		llm: &mockLLMClient{
			generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
				panic("LLM should not be called when there are no failures")
			},
		},
	}
	files := makeFiles(10)
	got, cost, tokens := a.triageFiles(context.Background(), files, nil, "")
	if len(got) != len(files) {
		t.Errorf("expected all %d files, got %d", len(files), len(got))
	}
	if cost != 0 {
		t.Errorf("expected zero cost, got %f", cost)
	}
	if tokens != 0 {
		t.Errorf("expected zero tokens, got %d", tokens)
	}
}

func TestTriageFilesReturnsSubset(t *testing.T) {
	a := &Attractor{
		llm: &mockLLMClient{
			generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
				return llm.GenerateResponse{Content: `["file00.go","file01.go"]`}, nil
			},
		},
	}
	files := makeFiles(10)
	failures := []string{"nil pointer in file00.go"}
	got, _, _ := a.triageFiles(context.Background(), files, failures, "model")
	if _, ok := got["file00.go"]; !ok {
		t.Error("expected file00.go in result")
	}
	if _, ok := got["file01.go"]; !ok {
		t.Error("expected file01.go in result")
	}
	// file05.go was not returned by LLM and is not an entry point.
	if _, ok := got["file05.go"]; ok {
		t.Error("file05.go should not be in result")
	}
}

func TestTriageFilesAlwaysIncludesDockerfile(t *testing.T) {
	a := &Attractor{
		llm: &mockLLMClient{
			generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
				return llm.GenerateResponse{Content: `["file00.go"]`}, nil
			},
		},
	}
	files := makeFiles(10, "Dockerfile")
	failures := []string{"build failed"}
	got, _, _ := a.triageFiles(context.Background(), files, failures, "model")
	if _, ok := got["Dockerfile"]; !ok {
		t.Error("Dockerfile should always be included")
	}
}

func TestTriageFilesAlwaysIncludesEntryPoints(t *testing.T) {
	a := &Attractor{
		llm: &mockLLMClient{
			generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
				return llm.GenerateResponse{Content: `["lib/util.go"]`}, nil
			},
		},
	}
	files := makeFiles(10, "main.go", "go.mod", "Dockerfile", "lib/util.go")
	failures := []string{"compilation error"}
	got, _, _ := a.triageFiles(context.Background(), files, failures, "model")
	for _, ep := range []string{"main.go", "go.mod", "Dockerfile", "lib/util.go"} {
		if _, ok := got[ep]; !ok {
			t.Errorf("expected entry point %q in result", ep)
		}
	}
}

func TestTriageFilesFallbackOnError(t *testing.T) {
	a := &Attractor{
		llm: &mockLLMClient{
			generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
				return llm.GenerateResponse{}, errors.New("network error")
			},
		},
	}
	files := makeFiles(10)
	failures := []string{"something broke"}
	got, cost, tokens := a.triageFiles(context.Background(), files, failures, "model")
	if len(got) != len(files) {
		t.Errorf("expected fallback to all %d files, got %d", len(files), len(got))
	}
	if cost != 0 {
		t.Errorf("expected zero cost on error, got %f", cost)
	}
	if tokens != 0 {
		t.Errorf("expected zero tokens on error, got %d", tokens)
	}
}

func TestTriageFilesUnparseableJSON(t *testing.T) {
	a := &Attractor{
		llm: &mockLLMClient{
			generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
				return llm.GenerateResponse{Content: "not json at all"}, nil
			},
		},
	}
	files := makeFiles(10)
	failures := []string{"something broke"}
	got, cost, tokens := a.triageFiles(context.Background(), files, failures, "model")
	if len(got) != len(files) {
		t.Errorf("expected fallback to all %d files on bad JSON, got %d", len(files), len(got))
	}
	if cost != 0 {
		t.Errorf("expected zero cost on parse error, got %f", cost)
	}
	if tokens != 0 {
		t.Errorf("expected zero tokens on parse error, got %d", tokens)
	}
}

func TestTriageFilesUnknownPaths(t *testing.T) {
	a := &Attractor{
		llm: &mockLLMClient{
			generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
				return llm.GenerateResponse{Content: `["nonexistent.go","file00.go"]`}, nil
			},
		},
	}
	files := makeFiles(10)
	failures := []string{"error in file00.go"}
	got, _, _ := a.triageFiles(context.Background(), files, failures, "model")
	if _, ok := got["nonexistent.go"]; ok {
		t.Error("nonexistent.go should be dropped from result")
	}
	if _, ok := got["file00.go"]; !ok {
		t.Error("file00.go should be in result")
	}
}

func TestTriageFilesCostTracked(t *testing.T) {
	const wantCost = 0.001
	a := &Attractor{
		llm: &mockLLMClient{
			generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
				return llm.GenerateResponse{
					Content: `["file00.go"]`,
					CostUSD: wantCost,
				}, nil
			},
		},
	}
	files := makeFiles(10)
	failures := []string{"error"}
	_, cost, _ := a.triageFiles(context.Background(), files, failures, "model")
	if cost != wantCost {
		t.Errorf("expected cost %f, got %f", wantCost, cost)
	}
}

func TestTriageFilesTokensTracked(t *testing.T) {
	const wantInput = 100
	const wantOutput = 50
	a := &Attractor{
		llm: &mockLLMClient{
			generateFn: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
				return llm.GenerateResponse{
					Content:      `["file00.go"]`,
					InputTokens:  wantInput,
					OutputTokens: wantOutput,
				}, nil
			},
		},
	}
	files := makeFiles(10)
	failures := []string{"error"}
	_, _, tokens := a.triageFiles(context.Background(), files, failures, "model")
	if tokens != wantInput+wantOutput {
		t.Errorf("expected tokens %d, got %d", wantInput+wantOutput, tokens)
	}
}
