package attractor

import (
	"errors"
	"strings"
)

var (
	errNoFiles       = errors.New("attractor: no files found in output")
	errPathTraversal = errors.New("attractor: path traversal detected")
)

const (
	filePrefix = "=== FILE: "
	fileSuffix = " ==="
	fileEnd    = "=== END FILE ==="
)

// ParseFiles extracts file blocks from LLM output.
// Format: === FILE: path === ... === END FILE ===
// Text between blocks is ignored. Unclosed blocks are skipped.
// Returns a map of path → content.
func ParseFiles(output string) (map[string]string, error) {
	files := make(map[string]string)
	lines := strings.Split(output, "\n")

	var currentPath string
	var currentContent strings.Builder

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, filePrefix) && strings.HasSuffix(trimmed, fileSuffix) {
			// Extract path from === FILE: path ===
			path := trimmed[len(filePrefix) : len(trimmed)-len(fileSuffix)]
			path = strings.TrimSpace(path)

			if path == "" {
				continue
			}
			if err := validatePath(path); err != nil {
				return nil, err
			}

			// If we were already in a block, discard it (unclosed).
			currentPath = path
			currentContent.Reset()
			continue
		}

		if trimmed == fileEnd {
			if currentPath != "" {
				content := normalizeTrailingNewline(currentContent.String())
				files[currentPath] = content
				currentPath = ""
				currentContent.Reset()
			}
			continue
		}

		if currentPath != "" {
			currentContent.WriteString(line)
			currentContent.WriteByte('\n')
		}
	}

	if len(files) == 0 {
		return nil, errNoFiles
	}
	return files, nil
}

// validatePath rejects paths containing traversal components.
func validatePath(path string) error {
	if strings.Contains(path, "..") {
		return errPathTraversal
	}
	if strings.HasPrefix(path, "/") {
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
