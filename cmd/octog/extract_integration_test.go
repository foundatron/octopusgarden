//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/foundatron/octopusgarden/internal/gene"
)

// repoRoot walks up from the working directory to find the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root with go.mod")
		}
		dir = parent
	}
}

func TestExtractEndToEnd(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}

	sourceDir := repoRoot(t)
	outputFile := filepath.Join(t.TempDir(), "genes.json")

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx := context.Background()

	if err := extractCmd(ctx, logger, []string{
		"--source-dir", sourceDir,
		"--output", outputFile,
		"--provider", "anthropic",
	}); err != nil {
		t.Fatalf("extractCmd() error = %v", err)
	}

	g, err := gene.Load(outputFile)
	if err != nil {
		t.Fatalf("gene.Load() error = %v", err)
	}
	if g.Guide == "" {
		t.Error("Guide is empty")
	}
	if g.Language != "go" {
		t.Errorf("Language = %q, want go", g.Language)
	}
	if g.TokenCount <= 0 {
		t.Errorf("TokenCount = %d, want > 0", g.TokenCount)
	}
	t.Logf("language=%s tokens=%d", g.Language, g.TokenCount)
	t.Logf("guide preview:\n%.500s", g.Guide)
}

func TestExtractStdout(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}

	sourceDir := repoRoot(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx := context.Background()

	// Capture stdout while extractCmd writes the gene JSON.
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	cmdErr := extractCmd(ctx, logger, []string{
		"--source-dir", sourceDir,
		"--output", "-",
		"--provider", "anthropic",
	})

	w.Close()
	os.Stdout = origStdout

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}

	if cmdErr != nil {
		t.Fatalf("extractCmd() error = %v", cmdErr)
	}

	var g gene.Gene
	if err := json.Unmarshal(buf.Bytes(), &g); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\noutput:\n%s", err, buf.String())
	}
	if g.Guide == "" {
		t.Error("Guide is empty")
	}
	if g.Language == "" {
		t.Error("Language is empty")
	}
	t.Logf("language=%s tokens=%d", g.Language, g.TokenCount)
}

// TestExtractAllExamples runs extraction against synthetic project directories
// for each supported language and verifies valid gene output.
// Note: examples/ in this repo contain specs/scenarios only (no source code);
// synthetic directories stand in as exemplars for multi-language coverage.
func TestExtractAllExamples(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}

	cases := []struct {
		name     string
		files    map[string]string
		wantLang string
	}{
		{
			name:     "go",
			wantLang: "go",
			files: map[string]string{
				"go.mod": "module example\n\ngo 1.21\n",
				"main.go": `package main

import (
	"fmt"
	"net/http"
)

func main() {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	http.ListenAndServe(":8080", nil)
}
`,
				"Dockerfile": "FROM golang:1.21-alpine AS build\nWORKDIR /app\nCOPY . .\nRUN go build -o server .\nFROM alpine\nCOPY --from=build /app/server /server\nCMD [\"/server\"]\n",
			},
		},
		{
			name:     "node",
			wantLang: "node",
			files: map[string]string{
				"package.json": `{"name":"example","version":"1.0.0","main":"index.js","dependencies":{"express":"^4.18.0"}}`,
				"index.js": `const express = require('express');
const app = express();
app.use(express.json());
app.get('/health', (req, res) => res.json({ status: 'ok' }));
app.listen(3000, () => console.log('listening on 3000'));
`,
				"Dockerfile": "FROM node:20-alpine\nWORKDIR /app\nCOPY package*.json ./\nRUN npm install\nCOPY . .\nCMD [\"node\",\"index.js\"]\n",
			},
		},
		{
			name:     "python",
			wantLang: "python",
			files: map[string]string{
				"requirements.txt": "fastapi==0.110.0\nuvicorn==0.29.0\n",
				"app.py": `from fastapi import FastAPI

app = FastAPI()

@app.get("/health")
def health():
    return {"status": "ok"}
`,
				"Dockerfile": "FROM python:3.12-slim\nWORKDIR /app\nCOPY requirements.txt .\nRUN pip install -r requirements.txt\nCOPY . .\nCMD [\"uvicorn\",\"app:app\",\"--host\",\"0.0.0.0\",\"--port\",\"8000\"]\n",
			},
		},
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx := context.Background()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range tc.files {
				p := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			outputFile := filepath.Join(t.TempDir(), "genes.json")
			if err := extractCmd(ctx, logger, []string{
				"--source-dir", dir,
				"--output", outputFile,
				"--provider", "anthropic",
			}); err != nil {
				t.Fatalf("extractCmd(%s) error = %v", tc.name, err)
			}

			g, err := gene.Load(outputFile)
			if err != nil {
				t.Fatalf("gene.Load() error = %v", err)
			}
			if g.Guide == "" {
				t.Error("Guide is empty")
			}
			if g.Language != tc.wantLang {
				t.Errorf("Language = %q, want %q", g.Language, tc.wantLang)
			}
			t.Logf("%s: language=%s tokens=%d", tc.name, g.Language, g.TokenCount)
		})
	}
}
