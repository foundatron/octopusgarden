package gene

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foundatron/octopusgarden/internal/spec"
)

func writeTestFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findRole(files []SelectedFile, role string) *SelectedFile {
	for i := range files {
		if files[i].Role == role {
			return &files[i]
		}
	}
	return nil
}

func TestScanGoProject(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example\n")
	writeTestFile(t, dir, "main.go", "package main\nfunc main() {}\n")
	writeTestFile(t, dir, "handlers/user.go", "package handlers\n")
	writeTestFile(t, dir, "Dockerfile", "FROM golang:1.24\n")

	res, err := Scan(context.Background(), dir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if res.Language != "go" {
		t.Errorf("Language = %q, want %q", res.Language, "go")
	}
	if len(res.Files) != 4 {
		t.Errorf("len(Files) = %d, want 4", len(res.Files))
	}

	roles := map[string]bool{}
	for _, f := range res.Files {
		roles[f.Role] = true
	}
	for _, want := range []string{"marker", "entrypoint", "handler", "dockerfile"} {
		if !roles[want] {
			t.Errorf("missing role %q", want)
		}
	}
}

func TestScanNodeProject(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "package.json", `{"name":"test"}`)
	writeTestFile(t, dir, "index.js", "console.log('hi')\n")
	writeTestFile(t, dir, "routes/items.js", "module.exports = {}\n")

	res, err := Scan(context.Background(), dir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if res.Language != "node" {
		t.Errorf("Language = %q, want %q", res.Language, "node")
	}
}

func TestScanPythonProject(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "requirements.txt", "flask\n")
	writeTestFile(t, dir, "app.py", "from flask import Flask\n")
	writeTestFile(t, dir, "routers/auth.py", "def login(): pass\n")

	res, err := Scan(context.Background(), dir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if res.Language != "python" {
		t.Errorf("Language = %q, want %q", res.Language, "python")
	}
}

func TestScanRustProject(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "Cargo.toml", "[package]\nname = \"test\"\n")
	writeTestFile(t, dir, "src/main.rs", "fn main() {}\n")

	res, err := Scan(context.Background(), dir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if res.Language != "rust" {
		t.Errorf("Language = %q, want %q", res.Language, "rust")
	}
}

func TestScanSelectsLargestHandler(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example\n")
	small := "package routes\n" + strings.Repeat("// line\n", 10)
	large := "package routes\n" + strings.Repeat("// line\n", 100)
	writeTestFile(t, dir, "routes/small.go", small)
	writeTestFile(t, dir, "routes/large.go", large)

	res, err := Scan(context.Background(), dir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	h := findRole(res.Files, "handler")
	if h == nil {
		t.Fatal("no handler file selected")
	}
	if h.Path != "routes/large.go" {
		t.Errorf("handler Path = %q, want %q", h.Path, "routes/large.go")
	}
}

func TestScanSkipsTestFiles(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example\n")
	writeTestFile(t, dir, "handlers/handler.go", "package handlers\n")
	writeTestFile(t, dir, "handlers/handler_test.go", "package handlers\n")

	res, err := Scan(context.Background(), dir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	for _, f := range res.Files {
		if strings.Contains(f.Path, "_test.go") {
			t.Errorf("test file %q should not be selected", f.Path)
		}
	}
}

func TestScanSkipsVendor(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example\n")
	writeTestFile(t, dir, "main.go", "package main\n")
	writeTestFile(t, dir, "vendor/lib/main.go", "package lib\n")

	res, err := Scan(context.Background(), dir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	for _, f := range res.Files {
		if strings.HasPrefix(f.Path, "vendor/") {
			t.Errorf("vendor file %q should not be selected", f.Path)
		}
	}
}

func TestScanSkipsNodeModules(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "package.json", `{"name":"test"}`)
	writeTestFile(t, dir, "index.js", "console.log('hi')\n")
	writeTestFile(t, dir, "node_modules/express/index.js", "module.exports = {}\n")

	res, err := Scan(context.Background(), dir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	for _, f := range res.Files {
		if strings.HasPrefix(f.Path, "node_modules/") {
			t.Errorf("node_modules file %q should not be selected", f.Path)
		}
	}
}

func TestScanSkipsLockFiles(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example\n")
	writeTestFile(t, dir, "go.sum", "hash\n")

	res, err := Scan(context.Background(), dir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	for _, f := range res.Files {
		if f.Path == "go.sum" {
			t.Error("go.sum should not be selected")
		}
	}
}

func TestScanSkipsBinaryFiles(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example\n")
	writeTestFile(t, dir, "main.go", "package main\n")
	writeTestFile(t, dir, "app.exe", "MZ\x00")

	res, err := Scan(context.Background(), dir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	for _, f := range res.Files {
		if f.Path == "app.exe" {
			t.Error("app.exe should not be selected")
		}
	}
}

func TestScanReadmeTruncation(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example\n")
	lines := make([]string, 0, 200)
	for i := range 200 {
		lines = append(lines, fmt.Sprintf("Line %d", i+1))
	}
	writeTestFile(t, dir, "README.md", strings.Join(lines, "\n")+"\n")

	res, err := Scan(context.Background(), dir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	rm := findRole(res.Files, "readme")
	if rm == nil {
		t.Fatal("no readme file selected")
	}
	got := strings.Count(rm.Content, "\n")
	if got != readmeMaxLines {
		t.Errorf("readme lines = %d, want %d", got, readmeMaxLines)
	}
}

func TestScanTokenBudget(t *testing.T) {
	dir := t.TempDir()
	// Each char is ~0.25 tokens, so 40K chars = ~10K tokens.
	bigContent := strings.Repeat("x", 40_000)
	writeTestFile(t, dir, "go.mod", "module example\n")
	writeTestFile(t, dir, "README.md", strings.Repeat("readme line\n", 100))
	writeTestFile(t, dir, "main.go", "package main\nfunc main() {}\n")
	writeTestFile(t, dir, "handlers/user.go", bigContent)
	writeTestFile(t, dir, "models/user.go", bigContent)

	res, err := Scan(context.Background(), dir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	total := 0
	for _, f := range res.Files {
		total += spec.EstimateTokens(f.Content)
	}
	if total > tokenBudget {
		t.Errorf("total tokens = %d, want <= %d", total, tokenBudget)
	}
	// Model should be dropped first, then handler, then readme.
	if findRole(res.Files, "model") != nil {
		t.Error("model should have been dropped")
	}
}

func TestScanEmptyDir(t *testing.T) {
	dir := t.TempDir()
	_, err := Scan(context.Background(), dir)
	if !errors.Is(err, errNoFiles) {
		t.Errorf("Scan() error = %v, want %v", err, errNoFiles)
	}
}

func TestScanDirNotExist(t *testing.T) {
	_, err := Scan(context.Background(), filepath.Join(t.TempDir(), "nonexistent"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Scan() error = %v, want os.ErrNotExist", err)
	}
}

func TestScanNestedEntryPoint(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example\n")
	writeTestFile(t, dir, "cmd/server/main.go", "package main\nfunc main() {}\n")

	res, err := Scan(context.Background(), dir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	ep := findRole(res.Files, "entrypoint")
	if ep == nil {
		t.Fatal("no entrypoint file selected")
	}
	if ep.Path != "cmd/server/main.go" {
		t.Errorf("entrypoint Path = %q, want %q", ep.Path, "cmd/server/main.go")
	}
}

func TestScanMultipleMarkers(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example\n")
	writeTestFile(t, dir, "package.json", `{"name":"test"}`)

	res, err := Scan(context.Background(), dir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if res.Language != "go" {
		t.Errorf("Language = %q, want %q (go.mod has higher priority)", res.Language, "go")
	}
}

func TestScanNoMarkerFiles(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "app.py", "print('hello')\n")

	res, err := Scan(context.Background(), dir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if res.Language != "" {
		t.Errorf("Language = %q, want empty", res.Language)
	}
}

func TestScanDockerfileVariants(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example\n")
	writeTestFile(t, dir, "docker/Dockerfile", "FROM golang:1.24\n")

	res, err := Scan(context.Background(), dir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	df := findRole(res.Files, "dockerfile")
	if df == nil {
		t.Fatal("no dockerfile found")
	}
	if df.Path != "docker/Dockerfile" {
		t.Errorf("dockerfile Path = %q, want %q", df.Path, "docker/Dockerfile")
	}
}

func TestScanSkipsLargeFiles(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example\n")
	writeTestFile(t, dir, "main.go", "package main\nfunc main() {}\n")

	// Create a file just over 1MB in a handler directory.
	bigContent := strings.Repeat("x", maxFileSize+1)
	writeTestFile(t, dir, "handlers/huge.go", bigContent)

	res, err := Scan(context.Background(), dir)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	h := findRole(res.Files, "handler")
	if h != nil && h.Content != "" {
		t.Errorf("large file should have empty content, got %d bytes", len(h.Content))
	}
}

func TestScanNotDir(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(f, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Scan(context.Background(), f)
	if !errors.Is(err, errNotDir) {
		t.Errorf("Scan() error = %v, want %v", err, errNotDir)
	}
}
