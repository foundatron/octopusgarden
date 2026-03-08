// Package store persists attractor run history and per-iteration results in
// SQLite.
//
// NewStore opens or creates a SQLite database using the pure-Go modernc.org/sqlite
// driver, requiring no CGO. The schema contains two tables: runs (metadata and
// final status for each attractor invocation) and iterations (per-iteration
// satisfaction scores, token counts, and costs). ErrRunNotFound is returned
// when a requested run ID does not exist.
package store
