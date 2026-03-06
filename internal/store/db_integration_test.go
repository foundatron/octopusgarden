//go:build integration

package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// newFileStore opens a file-based Store at dir/test.db and registers cleanup.
func newFileStore(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := NewStore(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestFileBasedPersistence(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	// Write a run and iteration via s1, then explicitly close it before reopening.
	s1, err := NewStore(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore s1: %v", err)
	}
	run := Run{
		ID:           "persist-run-1",
		SpecPath:     "/specs/app.md",
		Model:        "claude-sonnet-4-6",
		Threshold:    95.0,
		BudgetUSD:    10.0,
		StartedAt:    now,
		Satisfaction: 98.5,
		Iterations:   4,
		TotalTokens:  20000,
		TotalCostUSD: 0.85,
		Status:       "converged",
		Language:     "go",
	}
	if err := s1.RecordRun(ctx, run); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}
	it := Iteration{
		RunID:        "persist-run-1",
		Iteration:    1,
		Satisfaction: 98.5,
		InputTokens:  10000,
		OutputTokens: 10000,
		CostUSD:      0.85,
		Failures:     []string{},
		CreatedAt:    now,
	}
	if err := s1.RecordIteration(ctx, it); err != nil {
		t.Fatalf("RecordIteration: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close s1: %v", err)
	}

	// Reopen at the same path and read back data.
	s2, err := NewStore(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore reopen: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })

	got, err := s2.GetRun(ctx, "persist-run-1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}

	if got.ID != "persist-run-1" {
		t.Errorf("ID = %q, want %q", got.ID, "persist-run-1")
	}
	if got.SpecPath != "/specs/app.md" {
		t.Errorf("SpecPath = %q, want %q", got.SpecPath, "/specs/app.md")
	}
	if got.Model != "claude-sonnet-4-6" {
		t.Errorf("Model = %q, want %q", got.Model, "claude-sonnet-4-6")
	}
	if got.Threshold != 95.0 {
		t.Errorf("Threshold = %v, want 95.0", got.Threshold)
	}
	if got.Satisfaction != 98.5 {
		t.Errorf("Satisfaction = %v, want 98.5", got.Satisfaction)
	}
	if got.Iterations != 4 {
		t.Errorf("Iterations = %d, want 4", got.Iterations)
	}
	if got.TotalCostUSD != 0.85 {
		t.Errorf("TotalCostUSD = %v, want 0.85", got.TotalCostUSD)
	}
	if got.Status != "converged" {
		t.Errorf("Status = %q, want %q", got.Status, "converged")
	}
	if got.Language != "go" {
		t.Errorf("Language = %q, want %q", got.Language, "go")
	}
	if !got.StartedAt.Equal(now) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, now)
	}

	runs, err := s2.ListRuns(ctx)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("ListRuns count = %d, want 1", len(runs))
	}
	if runs[0].ID != "persist-run-1" {
		t.Errorf("ListRuns[0].ID = %q, want %q", runs[0].ID, "persist-run-1")
	}
}

func TestWALModeEnabled(t *testing.T) {
	s := newFileStore(t, t.TempDir())
	ctx := context.Background()

	var mode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want %q", mode, "wal")
	}
}

func TestConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	const n = 10

	// Open two independent stores to the same file.
	s1 := newFileStore(t, dir)
	s2, err := NewStore(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("NewStore s2: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })

	now := time.Now().Truncate(time.Second)

	// Write N runs concurrently via s1.
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			run := Run{
				ID:        fmt.Sprintf("concurrent-run-%02d", i),
				SpecPath:  "/spec.md",
				Model:     "model",
				Threshold: 95.0,
				StartedAt: now,
				Status:    "converged",
			}
			errs[i] = s1.RecordRun(ctx, run)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d RecordRun: %v", i, err)
		}
	}

	// Read back all runs via s2.
	runs, err := s2.ListRuns(ctx)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != n {
		t.Errorf("ListRuns count = %d, want %d", len(runs), n)
	}
}

func TestConcurrentIterationWrites(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	const n = 10

	s := newFileStore(t, dir)
	now := time.Now().Truncate(time.Second)

	// Insert a parent run.
	run := Run{
		ID:        "parent-run",
		SpecPath:  "/spec.md",
		Model:     "model",
		Threshold: 95.0,
		StartedAt: now,
		Status:    "running",
	}
	if err := s.RecordRun(ctx, run); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}

	// Write N iterations concurrently.
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			it := Iteration{
				RunID:        "parent-run",
				Iteration:    i + 1,
				Satisfaction: float64(50 + i),
				InputTokens:  1000,
				OutputTokens: 500,
				CostUSD:      0.01,
				Failures:     []string{},
				CreatedAt:    now,
			}
			errs[i] = s.RecordIteration(ctx, it)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d RecordIteration: %v", i, err)
		}
	}

	// Verify all N iterations were persisted.
	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM iterations WHERE run_id = 'parent-run'`,
	).Scan(&count); err != nil {
		t.Fatalf("count iterations: %v", err)
	}
	if count != n {
		t.Errorf("iteration count = %d, want %d", count, n)
	}
}
