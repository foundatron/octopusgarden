// Package testutil provides shared test helpers for the octopusgarden test
// suite.
//
// RepoRoot walks up from the working directory until it finds a go.mod file and
// returns the containing directory as the repository root. This is used by
// integration tests that need to reference fixture files relative to the
// repository root.
package testutil
