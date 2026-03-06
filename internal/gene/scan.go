package gene

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var (
	errNoFiles = errors.New("gene: no recognizable source files found")
	errNotDir  = errors.New("gene: path is not a directory")
)

const (
	tokenBudget    = 20_000
	readmeMaxLines = 100
)

// ScanResult holds the files selected from an exemplar source directory.
type ScanResult struct {
	Language string
	Files    []SelectedFile
}

// SelectedFile is a single file chosen for pattern extraction.
type SelectedFile struct {
	Path    string
	Content string
	Role    string
}

type fileCandidate struct {
	path string
	size int64
}

type walkCollector struct {
	sourceDir         string
	markers           []string
	readme            string
	dockerfile        string
	entrypoints       []string
	handlerCandidates []fileCandidate
	modelCandidates   []fileCandidate
}

// File classification lookup tables. Read-only after init.
var skipDirs = map[string]bool{
	".git":         true,
	"vendor":       true,
	"node_modules": true,
	"__pycache__":  true,
	".venv":        true,
	"venv":         true,
	"dist":         true,
	"build":        true,
	"target":       true,
	".next":        true,
}

var skipExts = map[string]bool{
	".exe":   true,
	".bin":   true,
	".png":   true,
	".jpg":   true,
	".gif":   true,
	".ico":   true,
	".woff":  true,
	".woff2": true,
	".ttf":   true,
}

var lockFiles = map[string]bool{
	"go.sum":            true,
	"package-lock.json": true,
	"yarn.lock":         true,
	"Cargo.lock":        true,
	"poetry.lock":       true,
}

var markerFiles = map[string]bool{
	"go.mod":           true,
	"package.json":     true,
	"Cargo.toml":       true,
	"pyproject.toml":   true,
	"requirements.txt": true,
	"pom.xml":          true,
	"build.gradle":     true,
}

// markerLang maps marker filenames to detected language. pom.xml and
// build.gradle are markers but have no language mapping (Java/Kotlin are
// not in validLanguages).
var markerLang = map[string]string{
	"go.mod":           "go",
	"package.json":     "node",
	"Cargo.toml":       "rust",
	"pyproject.toml":   "python",
	"requirements.txt": "python",
}

var markerPriority = []string{
	"go.mod",
	"Cargo.toml",
	"pyproject.toml",
	"requirements.txt",
	"package.json",
}

var entrypointPaths = map[string]bool{
	"main.go":      true,
	"index.ts":     true,
	"index.js":     true,
	"app.py":       true,
	"src/main.rs":  true,
	"src/main.py":  true,
	"src/index.ts": true,
}

var handlerDirs = map[string]bool{
	"routes":      true,
	"handlers":    true,
	"controllers": true,
	"routers":     true,
	"api":         true,
	"views":       true,
	"endpoints":   true,
}

var modelDirs = map[string]bool{
	"models":   true,
	"types":    true,
	"schema":   true,
	"entities": true,
	"domain":   true,
}

// Scan walks sourceDir and selects files with the highest architectural signal
// for LLM pattern extraction. It targets approximately tokenBudget tokens of
// content, dropping lower-priority files when the budget is exceeded.
func Scan(ctx context.Context, sourceDir string) (ScanResult, error) {
	info, err := os.Stat(sourceDir)
	if err != nil {
		return ScanResult{}, fmt.Errorf("gene: %w", err)
	}
	if !info.IsDir() {
		return ScanResult{}, errNotDir
	}

	wc := &walkCollector{sourceDir: sourceDir}
	if err := filepath.WalkDir(sourceDir, wc.walkFn(ctx)); err != nil {
		return ScanResult{}, fmt.Errorf("gene: walk: %w", err)
	}

	return wc.buildResult()
}

func (wc *walkCollector) walkFn(ctx context.Context) fs.WalkDirFunc {
	return func(path string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if isSkipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(wc.sourceDir, path)
		if err != nil {
			return fmt.Errorf("relative path for %s: %w", path, err)
		}
		rel = filepath.ToSlash(rel)
		if isSkipFile(rel) {
			return nil
		}
		return wc.classify(rel, d)
	}
}

func (wc *walkCollector) classify(rel string, d fs.DirEntry) error {
	name := filepath.Base(rel)

	if markerFiles[name] {
		wc.markers = append(wc.markers, rel)
	}

	if wc.readme == "" && isReadme(name) {
		wc.readme = rel
	}

	if wc.dockerfile == "" && isDockerfile(rel) {
		wc.dockerfile = rel
	}

	if isEntrypoint(rel) {
		wc.entrypoints = append(wc.entrypoints, rel)
	}

	info, err := d.Info()
	if err != nil {
		return fmt.Errorf("file info for %s: %w", rel, err)
	}
	size := info.Size()

	if inDirSet(rel, handlerDirs) {
		wc.handlerCandidates = append(wc.handlerCandidates, fileCandidate{path: rel, size: size})
	}

	if inDirSet(rel, modelDirs) {
		wc.modelCandidates = append(wc.modelCandidates, fileCandidate{path: rel, size: size})
	}

	return nil
}

func isReadme(name string) bool {
	upper := strings.ToUpper(name)
	return strings.HasPrefix(upper, "README")
}

func isDockerfile(rel string) bool {
	name := filepath.Base(rel)
	return strings.EqualFold(name, "dockerfile")
}

func isSkipDir(name string) bool {
	return skipDirs[name]
}

func isSkipFile(rel string) bool {
	name := filepath.Base(rel)
	ext := filepath.Ext(name)

	if skipExts[ext] {
		return true
	}
	if lockFiles[name] {
		return true
	}
	if isTestFile(name) {
		return true
	}
	if isGeneratedFile(name) {
		return true
	}
	return false
}

func isTestFile(name string) bool {
	if strings.HasSuffix(name, "_test.go") {
		return true
	}
	if strings.HasSuffix(name, ".test.ts") || strings.HasSuffix(name, ".test.js") {
		return true
	}
	if strings.HasSuffix(name, ".spec.ts") || strings.HasSuffix(name, ".spec.js") {
		return true
	}
	if strings.HasPrefix(name, "test_") && strings.HasSuffix(name, ".py") {
		return true
	}
	if strings.HasSuffix(name, "_test.py") {
		return true
	}
	return false
}

func isGeneratedFile(name string) bool {
	if strings.HasSuffix(name, ".pb.go") {
		return true
	}
	if strings.HasSuffix(name, ".gen.go") {
		return true
	}
	if strings.Contains(name, "_generated.") {
		return true
	}
	return false
}

func isEntrypoint(rel string) bool {
	if entrypointPaths[rel] {
		return true
	}
	// Match cmd/*/main.go pattern.
	parts := strings.Split(rel, "/")
	if len(parts) == 3 && parts[0] == "cmd" && parts[2] == "main.go" {
		return true
	}
	return false
}

func inDirSet(rel string, dirs map[string]bool) bool {
	dir := filepath.Dir(rel)
	for dir != "." && dir != "/" {
		if dirs[filepath.Base(dir)] {
			return true
		}
		dir = filepath.Dir(dir)
	}
	return false
}

func detectLanguage(markerPaths []string) string {
	found := make(map[string]bool, len(markerPaths))
	for _, p := range markerPaths {
		found[filepath.Base(p)] = true
	}
	for _, m := range markerPriority {
		if found[m] {
			if lang, ok := markerLang[m]; ok {
				return lang
			}
		}
	}
	return ""
}

func (wc *walkCollector) buildResult() (ScanResult, error) {
	files := make([]SelectedFile, 0, len(wc.markers)+4)

	// Markers.
	for _, rel := range wc.markers {
		content, err := readFileContent(filepath.Join(wc.sourceDir, rel))
		if err != nil {
			return ScanResult{}, err
		}
		files = append(files, SelectedFile{Path: rel, Content: content, Role: "marker"})
	}

	// README (truncated).
	if wc.readme != "" {
		content, err := readFileTruncated(filepath.Join(wc.sourceDir, wc.readme), readmeMaxLines)
		if err != nil {
			return ScanResult{}, err
		}
		files = append(files, SelectedFile{Path: wc.readme, Content: content, Role: "readme"})
	}

	// Dockerfile.
	if wc.dockerfile != "" {
		content, err := readFileContent(filepath.Join(wc.sourceDir, wc.dockerfile))
		if err != nil {
			return ScanResult{}, err
		}
		files = append(files, SelectedFile{Path: wc.dockerfile, Content: content, Role: "dockerfile"})
	}

	// Entrypoint: pick first found.
	if len(wc.entrypoints) > 0 {
		rel := wc.entrypoints[0]
		content, err := readFileContent(filepath.Join(wc.sourceDir, rel))
		if err != nil {
			return ScanResult{}, err
		}
		files = append(files, SelectedFile{Path: rel, Content: content, Role: "entrypoint"})
	}

	// Handler: pick largest.
	if best := selectLargest(wc.handlerCandidates); best != nil {
		content, err := readFileContent(filepath.Join(wc.sourceDir, best.path))
		if err != nil {
			return ScanResult{}, err
		}
		files = append(files, SelectedFile{Path: best.path, Content: content, Role: "handler"})
	}

	// Model: pick largest.
	if best := selectLargest(wc.modelCandidates); best != nil {
		content, err := readFileContent(filepath.Join(wc.sourceDir, best.path))
		if err != nil {
			return ScanResult{}, err
		}
		files = append(files, SelectedFile{Path: best.path, Content: content, Role: "model"})
	}

	if len(files) == 0 {
		return ScanResult{}, errNoFiles
	}

	lang := detectLanguage(wc.markers)
	files = enforceTokenBudget(files)

	return ScanResult{Language: lang, Files: files}, nil
}

func selectLargest(candidates []fileCandidate) *fileCandidate {
	if len(candidates) == 0 {
		return nil
	}
	best := &candidates[0]
	for i := 1; i < len(candidates); i++ {
		if candidates[i].size > best.size {
			best = &candidates[i]
		}
	}
	return best
}

func estimateTokens(text string) int {
	return len(text) / 4
}

func enforceTokenBudget(files []SelectedFile) []SelectedFile {
	// Drop in reverse priority: model -> handler -> readme.
	dropOrder := []string{"model", "handler", "readme"}
	for _, role := range dropOrder {
		if totalTokens(files) <= tokenBudget {
			return files
		}
		files = removeRole(files, role)
	}
	return files
}

func totalTokens(files []SelectedFile) int {
	total := 0
	for _, f := range files {
		total += estimateTokens(f.Content)
	}
	return total
}

func removeRole(files []SelectedFile, role string) []SelectedFile {
	result := make([]SelectedFile, 0, len(files))
	for _, f := range files {
		if f.Role != role {
			result = append(result, f)
		}
	}
	return result
}

func readFileContent(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("gene: read %s: %w", path, err)
	}
	return string(data), nil
}

func readFileTruncated(path string, maxLines int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("gene: read %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var b strings.Builder
	scanner := bufio.NewScanner(f)
	count := 0
	for scanner.Scan() {
		if count >= maxLines {
			break
		}
		if count > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(scanner.Text())
		count++
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("gene: scan %s: %w", path, err)
	}
	if count > 0 {
		b.WriteByte('\n')
	}
	return b.String(), nil
}
