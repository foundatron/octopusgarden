// Package spec parses markdown specification files into structured section
// trees.
//
// Parse accepts an io.Reader and ParseFile accepts a file path; both return a
// Spec with a title, description, and ordered slice of Section values.
// Summarize generates an LLM summary for large specs to stay within context
// budgets, and SelectContent returns either the full spec or a failure-focused
// view based on available token budget.
package spec
