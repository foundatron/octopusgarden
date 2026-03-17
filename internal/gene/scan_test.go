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

	res, err := Scan(context.Background(), dir, ScanOptions{})
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

	res, err := Scan(context.Background(), dir, ScanOptions{})
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

	res, err := Scan(context.Background(), dir, ScanOptions{})
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

	res, err := Scan(context.Background(), dir, ScanOptions{})
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

	res, err := Scan(context.Background(), dir, ScanOptions{})
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

	res, err := Scan(context.Background(), dir, ScanOptions{})
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

	res, err := Scan(context.Background(), dir, ScanOptions{})
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

	res, err := Scan(context.Background(), dir, ScanOptions{})
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

	res, err := Scan(context.Background(), dir, ScanOptions{})
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

	res, err := Scan(context.Background(), dir, ScanOptions{})
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

	res, err := Scan(context.Background(), dir, ScanOptions{})
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

	res, err := Scan(context.Background(), dir, ScanOptions{})
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
	_, err := Scan(context.Background(), dir, ScanOptions{})
	if !errors.Is(err, errNoFiles) {
		t.Errorf("Scan() error = %v, want %v", err, errNoFiles)
	}
}

func TestScanDirNotExist(t *testing.T) {
	_, err := Scan(context.Background(), filepath.Join(t.TempDir(), "nonexistent"), ScanOptions{})
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Scan() error = %v, want os.ErrNotExist", err)
	}
}

func TestScanNestedEntryPoint(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example\n")
	writeTestFile(t, dir, "cmd/server/main.go", "package main\nfunc main() {}\n")

	res, err := Scan(context.Background(), dir, ScanOptions{})
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

	res, err := Scan(context.Background(), dir, ScanOptions{})
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

	res, err := Scan(context.Background(), dir, ScanOptions{})
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

	res, err := Scan(context.Background(), dir, ScanOptions{})
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

	res, err := Scan(context.Background(), dir, ScanOptions{})
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
	_, err := Scan(context.Background(), f, ScanOptions{})
	if !errors.Is(err, errNotDir) {
		t.Errorf("Scan() error = %v, want %v", err, errNotDir)
	}
}

func TestScanMaxFilesBackfill(t *testing.T) {
	// A project with only a marker file and several plain .go files that don't
	// match any handler/model/entrypoint heuristics. With MaxFiles: 10, the
	// plain files should be backfilled as "source" role.
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example\n")
	writeTestFile(t, dir, "pkg/alpha/alpha.go", "package alpha\n"+strings.Repeat("// line\n", 20))
	writeTestFile(t, dir, "pkg/beta/beta.go", "package beta\n"+strings.Repeat("// line\n", 10))
	writeTestFile(t, dir, "pkg/gamma/gamma.go", "package gamma\n"+strings.Repeat("// line\n", 5))

	res, err := Scan(context.Background(), dir, ScanOptions{MaxFiles: 10})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	roles := map[string]int{}
	for _, f := range res.Files {
		roles[f.Role]++
	}
	if roles["marker"] != 1 {
		t.Errorf("marker count = %d, want 1", roles["marker"])
	}
	if roles["source"] != 3 {
		t.Errorf("source count = %d, want 3", roles["source"])
	}
}

func TestScanMaxFilesZero(t *testing.T) {
	// MaxFiles: 0 (zero value) means no backfill — only role-based files.
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example\n")
	writeTestFile(t, dir, "pkg/alpha/alpha.go", "package alpha\n")
	writeTestFile(t, dir, "pkg/beta/beta.go", "package beta\n")

	res, err := Scan(context.Background(), dir, ScanOptions{})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	for _, f := range res.Files {
		if f.Role == "source" {
			t.Errorf("unexpected source-role file %q with MaxFiles: 0", f.Path)
		}
	}
	// Only the marker should be present.
	if len(res.Files) != 1 {
		t.Errorf("len(Files) = %d, want 1", len(res.Files))
	}
}

func TestScanMaxFilesExceedsCandidates(t *testing.T) {
	// MaxFiles larger than available files: all files included, no error.
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example\n")
	writeTestFile(t, dir, "pkg/a.go", "package pkg\n")
	writeTestFile(t, dir, "pkg/b.go", "package pkg\n")
	writeTestFile(t, dir, "pkg/c.go", "package pkg\n")

	res, err := Scan(context.Background(), dir, ScanOptions{MaxFiles: 20})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	// 1 marker + 3 source = 4 total.
	if len(res.Files) != 4 {
		t.Errorf("len(Files) = %d, want 4", len(res.Files))
	}
}

func TestScanMaxFilesTokenBudget(t *testing.T) {
	// Large source files should be trimmed before role-based files when over budget.
	dir := t.TempDir()
	// Each char ~0.25 tokens; 40K chars ~10K tokens.
	bigContent := strings.Repeat("x", 40_000)
	writeTestFile(t, dir, "go.mod", "module example\n")
	writeTestFile(t, dir, "main.go", "package main\nfunc main() {}\n")
	writeTestFile(t, dir, "pkg/big1.go", bigContent)
	writeTestFile(t, dir, "pkg/big2.go", bigContent)
	writeTestFile(t, dir, "pkg/big3.go", bigContent)

	res, err := Scan(context.Background(), dir, ScanOptions{MaxFiles: 10})
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
	// The marker (go.mod) and entrypoint (main.go) must survive.
	if findRole(res.Files, "marker") == nil {
		t.Error("marker should survive budget enforcement")
	}
	if findRole(res.Files, "entrypoint") == nil {
		t.Error("entrypoint should survive budget enforcement")
	}
}

func TestScanSourceFileSortOrder(t *testing.T) {
	// Largest source files should be selected first when MaxFiles limits count.
	dir := t.TempDir()
	writeTestFile(t, dir, "go.mod", "module example\n")
	small := "package pkg\n" + strings.Repeat("// s\n", 5)
	large := "package pkg\n" + strings.Repeat("// l\n", 50)
	writeTestFile(t, dir, "pkg/small.go", small)
	writeTestFile(t, dir, "pkg/large.go", large)

	// MaxFiles: 2 means 1 marker + 1 source (the largest).
	res, err := Scan(context.Background(), dir, ScanOptions{MaxFiles: 2})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}

	var sourceFiles []SelectedFile
	for _, f := range res.Files {
		if f.Role == "source" {
			sourceFiles = append(sourceFiles, f)
		}
	}
	if len(sourceFiles) != 1 {
		t.Fatalf("source file count = %d, want 1", len(sourceFiles))
	}
	if sourceFiles[0].Path != "pkg/large.go" {
		t.Errorf("selected source = %q, want %q", sourceFiles[0].Path, "pkg/large.go")
	}
}
