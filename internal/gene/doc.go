// Package gene scans exemplar codebases to extract structural patterns via LLM
// analysis.
//
// Scan walks a source directory, selects high-signal files within a 20K-token
// budget, and returns a ScanResult with file contents grouped by language.
// Analyze submits the scan to an LLM and returns a Gene containing a prose
// coding guide that can be injected into the attractor's system prompt.
// Genes are persisted as JSON files via Save and Load.
package gene
