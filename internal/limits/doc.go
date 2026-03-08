// Package limits defines shared size constraints used across packages.
//
// It exists as a leaf package (no internal imports) so that both container
// and scenario can import it without creating a circular dependency.
package limits
