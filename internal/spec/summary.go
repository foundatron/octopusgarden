package spec

import (
	"context"
	"fmt"
	"strings"

	"github.com/foundatron/octopusgarden/internal/llm"
)

// SummarizedSpec holds multi-level summaries of a spec for context budget management.
type SummarizedSpec struct {
	Spec     *Spec
	Sections []SectionSummary // per-section 2-3 sentence summaries
	Outline  string           // headings + one-line descriptions
	Abstract string           // single paragraph
}

// SectionSummary holds a heading and its condensed summary.
type SectionSummary struct {
	Heading string
	Summary string
}

// EstimateTokens returns a rough token count estimate using the len/4 heuristic.
// Note: len() counts bytes, not runes. For predominantly ASCII specs this is accurate;
// for unicode-heavy content (CJK, emoji) this will overestimate byte count per token,
// producing a conservative (higher) token estimate.
func EstimateTokens(text string) int {
	return len(text) / 4
}

// SummarizeResult holds the output of a Summarize call.
type SummarizeResult struct {
	Summary *SummarizedSpec
	CostUSD float64
}

// Summarize calls an LLM to produce all three summary levels in a single request.
// Returns a SummarizeResult with section summaries, outline, abstract, and cost.
func Summarize(ctx context.Context, s *Spec, client llm.Client, model string) (SummarizeResult, error) {
	if len(s.Sections) == 0 {
		return SummarizeResult{
			Summary: &SummarizedSpec{
				Spec:     s,
				Abstract: s.RawContent,
			},
		}, nil
	}

	prompt := buildSummarizePrompt(s)

	resp, err := client.Generate(ctx, llm.GenerateRequest{
		SystemPrompt: "You are a technical writer. Summarize specifications concisely and accurately.",
		Messages:     []llm.Message{{Role: "user", Content: prompt}},
		Model:        model,
	})
	if err != nil {
		return SummarizeResult{}, fmt.Errorf("summarize spec: %w", err)
	}

	ss, err := parseSummarizeResponse(s, resp.Content)
	if err != nil {
		return SummarizeResult{}, err
	}
	return SummarizeResult{Summary: ss, CostUSD: resp.CostUSD}, nil
}

// SelectContent picks the right spec representation for the context budget.
// Returns the fullest representation that fits within budget tokens.
// If failures are provided, failure-relevant sections are expanded to full content.
func SelectContent(ss *SummarizedSpec, budget int, failures []string) string {
	raw := ss.Spec.RawContent

	// Level 1: full spec fits.
	if EstimateTokens(raw) <= budget {
		return raw
	}

	// Level 2: section summaries with failure-relevant sections expanded.
	expanded := expandFailureSections(ss.Spec, ss.Sections, failures)
	if EstimateTokens(expanded) <= budget {
		return expanded
	}

	// Level 3: outline + failure-relevant sections.
	if ss.Outline != "" {
		outlineExpanded := joinNonEmpty(ss.Outline, expandFailureSectionsOnly(ss.Spec, failures))
		if EstimateTokens(outlineExpanded) <= budget {
			return outlineExpanded
		}
	}

	// Level 4: abstract + failure-relevant sections.
	if ss.Abstract != "" {
		abstractExpanded := joinNonEmpty(ss.Abstract, expandFailureSectionsOnly(ss.Spec, failures))
		if EstimateTokens(abstractExpanded) <= budget {
			return abstractExpanded
		}
	}

	// Fallback: abstract alone.
	if ss.Abstract != "" {
		return ss.Abstract
	}

	// Last resort: truncate raw content to fit budget.
	cutoff := budget * 4 // reverse the len/4 heuristic
	if cutoff < len(raw) {
		return raw[:cutoff] + "\n\n[... truncated to fit context budget ...]"
	}
	return raw
}

// expandFailureSections builds content where failure-relevant sections get full content
// and others get their summaries.
func expandFailureSections(s *Spec, summaries []SectionSummary, failures []string) string {
	matched := matchFailureSections(s, failures)

	summaryMap := buildSummaryMap(summaries)

	var b strings.Builder
	for _, sec := range s.Sections {
		if matched[sec.Heading] {
			fmt.Fprintf(&b, "## %s\n%s\n\n", sec.Heading, sec.Content)
		} else if summary, ok := summaryMap[sec.Heading]; ok {
			fmt.Fprintf(&b, "## %s\n%s\n\n", sec.Heading, summary)
		} else {
			fmt.Fprintf(&b, "## %s\n[section content omitted]\n\n", sec.Heading)
		}
	}
	return strings.TrimSpace(b.String())
}

// expandFailureSectionsOnly returns only the full content of failure-matched sections.
func expandFailureSectionsOnly(s *Spec, failures []string) string {
	matched := matchFailureSections(s, failures)

	var b strings.Builder
	for _, sec := range s.Sections {
		if matched[sec.Heading] {
			fmt.Fprintf(&b, "## %s\n%s\n\n", sec.Heading, sec.Content)
		}
	}
	return strings.TrimSpace(b.String())
}

// matchFailureSections returns a set of section headings that match any failure string
// via case-insensitive substring matching.
func matchFailureSections(s *Spec, failures []string) map[string]bool {
	lowerFailures := make([]string, len(failures))
	for i, f := range failures {
		lowerFailures[i] = strings.ToLower(f)
	}

	matched := make(map[string]bool)
	for _, sec := range s.Sections {
		heading := strings.ToLower(sec.Heading)
		for _, lf := range lowerFailures {
			if strings.Contains(lf, heading) || strings.Contains(heading, lf) {
				matched[sec.Heading] = true
				break
			}
		}
	}
	return matched
}

// joinNonEmpty joins two strings with a double newline, omitting the separator
// if either part is empty.
func joinNonEmpty(a, b string) string {
	if b == "" {
		return a
	}
	if a == "" {
		return b
	}
	return a + "\n\n" + b
}

func buildSummaryMap(summaries []SectionSummary) map[string]string {
	m := make(map[string]string, len(summaries))
	for _, ss := range summaries {
		m[ss.Heading] = ss.Summary
	}
	return m
}

func buildSummarizePrompt(s *Spec) string {
	var b strings.Builder
	b.WriteString("Summarize the following specification at three levels of detail.\n\n")
	b.WriteString("SPECIFICATION:\n")
	b.WriteString(s.RawContent)
	b.WriteString("\n\n")
	b.WriteString("Respond in EXACTLY this format with these delimiters:\n\n")
	b.WriteString("=== SECTION SUMMARIES ===\n")
	b.WriteString("For each section heading in the spec, write:\n")
	b.WriteString("### <heading>\n")
	b.WriteString("<2-3 sentence summary>\n\n")
	b.WriteString("=== OUTLINE ===\n")
	b.WriteString("List each heading with a one-line description.\n\n")
	b.WriteString("=== ABSTRACT ===\n")
	b.WriteString("A single paragraph summarizing the entire spec.\n")
	return b.String()
}

func parseSummarizeResponse(s *Spec, response string) (*SummarizedSpec, error) {
	result := &SummarizedSpec{Spec: s}

	// Parse section summaries.
	sectionsBlock := extractBlock(response, "=== SECTION SUMMARIES ===", "=== OUTLINE ===")
	if sectionsBlock != "" {
		result.Sections = parseSectionSummaries(sectionsBlock)
	}

	// Parse outline.
	result.Outline = strings.TrimSpace(extractBlock(response, "=== OUTLINE ===", "=== ABSTRACT ==="))

	// Parse abstract.
	if _, after, found := strings.Cut(response, "=== ABSTRACT ==="); found {
		result.Abstract = strings.TrimSpace(after)
	}

	return result, nil
}

func extractBlock(text, startDelim, endDelim string) string {
	_, after, found := strings.Cut(text, startDelim)
	if !found {
		return ""
	}
	before, _, found := strings.Cut(after, endDelim)
	if !found {
		return after
	}
	return before
}

func parseSectionSummaries(block string) []SectionSummary {
	var summaries []SectionSummary
	lines := strings.Split(block, "\n")

	var current *SectionSummary
	var contentLines []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "### ") {
			if current != nil {
				current.Summary = strings.TrimSpace(strings.Join(contentLines, "\n"))
				summaries = append(summaries, *current)
			}
			heading := strings.TrimSpace(trimmed[4:])
			current = &SectionSummary{Heading: heading}
			contentLines = nil
		} else if current != nil {
			contentLines = append(contentLines, line)
		}
	}
	if current != nil {
		current.Summary = strings.TrimSpace(strings.Join(contentLines, "\n"))
		summaries = append(summaries, *current)
	}

	return summaries
}
