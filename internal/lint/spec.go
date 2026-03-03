package lint

import (
	"fmt"
	"os"
	"strings"
)

type specHeading struct {
	level int
	text  string
	line  int // 1-based
}

// CheckSpec reads a markdown spec file and returns diagnostics.
func CheckSpec(path string) ([]Diagnostic, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("lint spec: %w", err)
	}

	return lintSpecContent(path, string(data)), nil
}

func lintSpecContent(path, content string) []Diagnostic {
	if strings.TrimSpace(content) == "" {
		return []Diagnostic{{
			File:    path,
			Level:   Error,
			Message: "spec file is empty",
		}}
	}

	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(strings.TrimRight(normalized, "\n"), "\n")
	headings, unclosedFence := extractHeadings(lines)

	var diags []Diagnostic
	if unclosedFence {
		diags = append(diags, Diagnostic{
			File:    path,
			Level:   Warning,
			Message: "unclosed fenced code block (some headings may be ignored)",
		})
	}

	firstL1, hasL1 := findFirstL1(headings)
	if !hasL1 {
		diags = append(diags, Diagnostic{
			File:    path,
			Level:   Error,
			Message: "no level-1 heading found (spec must have a title)",
		})
		return diags
	}
	diags = append(diags, checkDescription(path, lines, headings, firstL1)...)
	diags = append(diags, checkEmptySections(path, lines, headings, firstL1)...)
	diags = append(diags, checkDuplicateHeadings(path, headings)...)
	return diags
}

// extractHeadings returns all headings and whether the file ends inside an unclosed fence.
func extractHeadings(lines []string) ([]specHeading, bool) {
	var headings []specHeading
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
			headings = append(headings, specHeading{level: level, text: text, line: i + 1})
		}
	}
	return headings, inFencedBlock
}

func findFirstL1(headings []specHeading) (specHeading, bool) {
	for _, h := range headings {
		if h.level == 1 {
			return h, true
		}
	}
	return specHeading{}, false
}

func checkDescription(path string, lines []string, headings []specHeading, firstL1 specHeading) []Diagnostic {
	descEnd := len(lines)
	for _, h := range headings {
		if h.line > firstL1.line {
			descEnd = h.line - 1
			break
		}
	}
	desc := collectContent(lines, firstL1.line, descEnd)
	if desc == "" {
		return []Diagnostic{{
			File:    path,
			Line:    firstL1.line,
			Level:   Warning,
			Message: "no description text after title heading",
		}}
	}
	return nil
}

func checkEmptySections(path string, lines []string, headings []specHeading, firstL1 specHeading) []Diagnostic {
	var diags []Diagnostic
	for i, h := range headings {
		if h == firstL1 {
			continue
		}
		contentStart := h.line
		contentEnd := len(lines)
		for j := i + 1; j < len(headings); j++ {
			if headings[j].level <= h.level {
				contentEnd = headings[j].line - 1
				break
			}
		}
		if collectContent(lines, contentStart, contentEnd) == "" {
			diags = append(diags, Diagnostic{
				File:    path,
				Line:    h.line,
				Level:   Warning,
				Message: fmt.Sprintf("section %q has no content", h.text),
			})
		}
	}
	return diags
}

func checkDuplicateHeadings(path string, headings []specHeading) []Diagnostic {
	type levelText struct {
		level int
		text  string
	}
	var diags []Diagnostic
	seen := make(map[levelText]int)
	for _, h := range headings {
		key := levelText{level: h.level, text: h.text}
		if firstLine, ok := seen[key]; ok {
			diags = append(diags, Diagnostic{
				File:    path,
				Line:    h.line,
				Level:   Warning,
				Message: fmt.Sprintf("duplicate heading %q (same level %d, first at line %d)", h.text, h.level, firstLine),
			})
		} else {
			seen[key] = h.line
		}
	}
	return diags
}

// parseHeading returns the heading level and text for a markdown heading line.
// Returns (0, "") if the line is not a heading.
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
	if start >= end || start >= len(lines) {
		return ""
	}
	if end > len(lines) {
		end = len(lines)
	}
	content := strings.Join(lines[start:end], "\n")
	return strings.TrimSpace(content)
}
