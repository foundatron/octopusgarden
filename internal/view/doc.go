// Package view defines JSON-serializable output types for CLI commands.
//
// NewValidateOutput converts an AggregateResult and threshold configuration
// into a ValidateOutput suitable for structured JSON output. WriteJSON encodes
// any value as indented JSON to the provided writer. The types in this package
// are the stable serialization format for the validate and status subcommands.
package view
