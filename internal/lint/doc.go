// Package lint provides structural validation for spec markdown and scenario
// YAML files.
//
// CheckSpec validates heading hierarchy and description content of a markdown
// spec. CheckScenario and CheckScenarioDir validate YAML scenario files,
// checking step structure, HTTP methods, JSON path syntax, variable references,
// and duplicate scenario IDs. All findings are returned as Diagnostic values
// with file path, line number, and severity level.
package lint
