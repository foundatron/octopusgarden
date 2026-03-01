package spec

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

var errEmptySpec = errors.New("spec: empty content")

// Parse reads a markdown spec from r and returns a structured Spec.
func Parse(r io.Reader) (Spec, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return Spec{}, fmt.Errorf("parse spec: %w", err)
	}

	raw := string(data)
	if strings.TrimSpace(raw) == "" {
		return Spec{}, errEmptySpec
	}

	// Normalize line endings and split.
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.Split(strings.TrimRight(normalized, "\n"), "\n")

	spec := Spec{RawContent: strings.TrimSpace(normalized)}

	// First pass: find all headings and their line indices.
	// Track fenced code blocks (``` or ~~~) to avoid treating lines inside them as headings.
	type heading struct {
		level int
		text  string
		line  int
	}
	var headings []heading
	inFencedBlock := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFencedBlock = !inFencedBlock
		}
		if inFencedBlock {
			continue
		}
		level, text := parseHeading(line)
		if level > 0 {
			headings = append(headings, heading{level: level, text: text, line: i})
		}
	}

	if len(headings) == 0 {
		// No headings — treat entire content as raw only.
		return spec, nil
	}

	// Title is the first heading.
	spec.Title = headings[0].text

	// Description is text between the title heading and the next heading (or end).
	descEnd := len(lines)
	if len(headings) > 1 {
		descEnd = headings[1].line
	}
	spec.Description = collectContent(lines, headings[0].line+1, descEnd)

	// Build sections from all headings (including the first).
	sections := make([]Section, 0, len(headings))
	for i, h := range headings {
		contentStart := h.line + 1
		// Content ends at the next heading of same or higher (lower number) level, or EOF.
		contentEnd := len(lines)
		for j := i + 1; j < len(headings); j++ {
			if headings[j].level <= h.level {
				contentEnd = headings[j].line
				break
			}
		}
		sections = append(sections, Section{
			Heading: h.text,
			Level:   h.level,
			Content: collectContent(lines, contentStart, contentEnd),
		})
	}
	spec.Sections = sections

	return spec, nil
}

// ParseFile reads a markdown spec file from disk.
func ParseFile(path string) (Spec, error) {
	f, err := os.Open(path)
	if err != nil {
		return Spec{}, fmt.Errorf("parse spec file: %w", err)
	}
	defer func() { _ = f.Close() }()
	return Parse(f)
}

// parseHeading returns the heading level and text for a markdown heading line.
// Returns (0, "") if the line is not a heading or has no text after the hashes.
func parseHeading(line string) (int, string) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "#") {
		return 0, ""
	}
	level := 0
	for _, ch := range trimmed {
		if ch == '#' {
			level++
		} else {
			break
		}
	}
	if level > 6 {
		return 0, ""
	}
	// Must have a space after the hashes.
	rest := trimmed[level:]
	if len(rest) == 0 || rest[0] != ' ' {
		return 0, ""
	}
	text := strings.TrimSpace(rest)
	if text == "" {
		return 0, ""
	}
	return level, text
}

// collectContent joins lines[start:end], trimming leading/trailing blank lines.
func collectContent(lines []string, start, end int) string {
	if start >= end {
		return ""
	}
	content := strings.Join(lines[start:end], "\n")
	return strings.TrimSpace(content)
}
