package attractor

import (
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

var (
	errNoFiles       = errors.New("attractor: no files found in output")
	errPathTraversal = errors.New("attractor: path traversal detected")
)

const (
	filePrefix      = "=== FILE: "
	fileSuffix      = " ==="
	fileEnd         = "=== END FILE ==="
	unchangedPrefix = "=== UNCHANGED: "
	unchangedSuffix = " ==="
)

// ParseResult holds the output of ParseFilesWithMetadata, including metadata
// about files that were opened but never closed (dropped due to truncation or
// a new header appearing before the end marker).
type ParseResult struct {
	Files        map[string]string
	DroppedFiles []string // paths opened but never closed (discarded)
	Truncated    bool     // output ended mid-block (last file was never closed)
}

// ParseFiles extracts file blocks from LLM output.
// Format: === FILE: path === ... === END FILE ===
// Text between blocks is ignored. Unclosed blocks are skipped.
// Returns a map of path → content.
func ParseFiles(output string) (map[string]string, error) {
	result, err := ParseFilesWithMetadata(output)
	if err != nil {
		return nil, err
	}
	return result.Files, nil
}

// ParseFilesWithMetadata extracts file blocks from LLM output, returning metadata
// about dropped and truncated files alongside the successfully parsed files.
func ParseFilesWithMetadata(output string) (ParseResult, error) {
	files := make(map[string]string)
	var droppedFiles []string
	lines := strings.Split(output, "\n")

	var currentPath string
	var currentContent strings.Builder

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Only skip UNCHANGED markers outside file blocks — inside a block
		// the line is literal content and must be preserved.
		if currentPath == "" && isUnchangedMarker(trimmed) {
			continue
		}

		path, ok := extractFilePath(trimmed)
		if ok {
			if err := validatePath(path); err != nil {
				return ParseResult{}, err
			}
			// If we were already in a block, discard it (unclosed).
			if currentPath != "" {
				droppedFiles = append(droppedFiles, currentPath)
			}
			currentPath = path
			currentContent.Reset()
			continue
		}

		if trimmed == fileEnd && currentPath != "" {
			files[currentPath] = normalizeTrailingNewline(currentContent.String())
			currentPath = ""
			currentContent.Reset()
			continue
		}

		if currentPath != "" {
			currentContent.WriteString(line)
			currentContent.WriteByte('\n')
		}
	}

	truncated := currentPath != ""
	if truncated {
		droppedFiles = append(droppedFiles, currentPath)
	}

	if len(files) == 0 {
		return ParseResult{}, errNoFiles
	}
	return ParseResult{
		Files:        files,
		DroppedFiles: droppedFiles,
		Truncated:    truncated,
	}, nil
}

// extractFilePath returns the path from a === FILE: path === header line.
// Returns ("", false) if the line is not a file header or the path is empty.
func extractFilePath(trimmed string) (string, bool) {
	if !strings.HasPrefix(trimmed, filePrefix) || !strings.HasSuffix(trimmed, fileSuffix) {
		return "", false
	}
	path := strings.TrimSpace(trimmed[len(filePrefix) : len(trimmed)-len(fileSuffix)])
	if path == "" {
		return "", false
	}
	return path, true
}

// isUnchangedMarker returns true for === UNCHANGED: path === advisory markers.
// These are skipped during parsing — carry-forward is handled by mergeFiles.
func isUnchangedMarker(trimmed string) bool {
	return strings.HasPrefix(trimmed, unchangedPrefix) && strings.HasSuffix(trimmed, unchangedSuffix)
}

// validatePath rejects paths containing traversal components.
func validatePath(path string) error {
	if strings.HasPrefix(path, "/") {
		return errPathTraversal
	}
	cleaned := filepath.Clean(path)
	if slices.Contains(strings.Split(cleaned, string(filepath.Separator)), "..") {
		return errPathTraversal
	}
	return nil
}

// normalizeTrailingNewline ensures content ends with exactly one newline.
func normalizeTrailingNewline(s string) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return "\n"
	}
	return s + "\n"
}

// mergeFiles merges new LLM output into the previous best file set.
// Files present in newFiles replace their counterparts in prevFiles.
// Files present in prevFiles but absent from newFiles are carried forward unchanged.
// The result is a new map — prevFiles and newFiles are never mutated.
func mergeFiles(newFiles, prevFiles map[string]string) map[string]string {
	merged := make(map[string]string, len(prevFiles)+len(newFiles))
	maps.Copy(merged, prevFiles)
	maps.Copy(merged, newFiles)
	return merged
}
