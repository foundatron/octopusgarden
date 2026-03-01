package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSchemaCreation(t *testing.T) {
	s := newTestStore(t)

	// Verify both tables exist by querying sqlite_master.
	var count int
	err := s.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('runs', 'iterations')`).Scan(&count)
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 tables, got %d", count)
	}
}

func TestRecordRunAndGetRun(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	run := Run{
		ID:           "abc123",
		SpecPath:     "/tmp/spec.md",
		Model:        "claude-sonnet-4-20250514",
		Threshold:    95.0,
		BudgetUSD:    5.00,
		StartedAt:    now,
		Satisfaction: 87.5,
		Iterations:   3,
		TotalTokens:  15000,
		TotalCostUSD: 0.42,
		Status:       "converged",
	}

	if err := s.RecordRun(ctx, run); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}

	got, err := s.GetRun(ctx, "abc123")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}

	if got.ID != run.ID {
		t.Errorf("ID = %q, want %q", got.ID, run.ID)
	}
	if got.SpecPath != run.SpecPath {
		t.Errorf("SpecPath = %q, want %q", got.SpecPath, run.SpecPath)
	}
	if got.Model != run.Model {
		t.Errorf("Model = %q, want %q", got.Model, run.Model)
	}
	if got.Satisfaction != run.Satisfaction {
		t.Errorf("Satisfaction = %v, want %v", got.Satisfaction, run.Satisfaction)
	}
	if got.Status != run.Status {
		t.Errorf("Status = %q, want %q", got.Status, run.Status)
	}
	if got.FinishedAt != nil {
		t.Errorf("FinishedAt = %v, want nil", got.FinishedAt)
	}
}

func TestUpdateRun(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	run := Run{
		ID:        "run1",
		SpecPath:  "/spec.md",
		Model:     "claude-sonnet-4-20250514",
		Threshold: 95.0,
		BudgetUSD: 5.0,
		StartedAt: now,
		Status:    "running",
	}
	if err := s.RecordRun(ctx, run); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}

	finished := now.Add(2 * time.Minute)
	run.FinishedAt = &finished
	run.Satisfaction = 97.0
	run.Iterations = 5
	run.TotalTokens = 25000
	run.TotalCostUSD = 1.23
	run.Status = "converged"

	if err := s.UpdateRun(ctx, run); err != nil {
		t.Fatalf("UpdateRun: %v", err)
	}

	got, err := s.GetRun(ctx, "run1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}

	if got.Status != "converged" {
		t.Errorf("Status = %q, want %q", got.Status, "converged")
	}
	if got.Satisfaction != 97.0 {
		t.Errorf("Satisfaction = %v, want 97.0", got.Satisfaction)
	}
	if got.FinishedAt == nil {
		t.Fatal("FinishedAt is nil, want non-nil")
	}
}

func TestListRunsOrderingAndLimit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	// Insert 25 runs with ascending started_at.
	for i := range 25 {
		run := Run{
			ID:        fmt.Sprintf("run%02d", i),
			SpecPath:  "/spec.md",
			Model:     "model",
			Threshold: 95,
			StartedAt: base.Add(time.Duration(i) * time.Hour),
			Status:    "converged",
		}
		if err := s.RecordRun(ctx, run); err != nil {
			t.Fatalf("RecordRun %d: %v", i, err)
		}
	}

	runs, err := s.ListRuns(ctx)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}

	if len(runs) != 20 {
		t.Fatalf("len(runs) = %d, want 20", len(runs))
	}

	// Most recent first: run24 should be first.
	if runs[0].ID != "run24" {
		t.Errorf("first run = %q, want %q", runs[0].ID, "run24")
	}
	if runs[19].ID != "run05" {
		t.Errorf("last run = %q, want %q", runs[19].ID, "run05")
	}
}

func TestGetRunNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.GetRun(ctx, "nonexistent")
	if !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("GetRun error = %v, want %v", err, ErrRunNotFound)
	}
}

func TestRecordIterationFailuresJSON(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	// Must have a parent run.
	run := Run{
		ID:        "run1",
		SpecPath:  "/spec.md",
		Model:     "model",
		Threshold: 95,
		StartedAt: now,
		Status:    "running",
	}
	if err := s.RecordRun(ctx, run); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}

	tests := []struct {
		name     string
		failures []string
		want     []string
	}{
		{
			name:     "with failures",
			failures: []string{"POST /items returned 500", "GET /items/1 returned 404"},
			want:     []string{"POST /items returned 500", "GET /items/1 returned 404"},
		},
		{
			name:     "nil failures",
			failures: nil,
			want:     []string{},
		},
		{
			name:     "empty failures",
			failures: []string{},
			want:     []string{},
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			it := Iteration{
				RunID:        "run1",
				Iteration:    i + 1,
				Satisfaction: 75.0,
				InputTokens:  1000,
				OutputTokens: 500,
				CostUSD:      0.05,
				Failures:     tt.failures,
				CreatedAt:    now,
			}

			if err := s.RecordIteration(ctx, it); err != nil {
				t.Fatalf("RecordIteration: %v", err)
			}

			// Read back raw JSON from DB to verify round-trip.
			var failJSON string
			err := s.db.QueryRowContext(ctx,
				`SELECT failures FROM iterations WHERE run_id = ? AND iteration = ?`,
				"run1", i+1,
			).Scan(&failJSON)
			if err != nil {
				t.Fatalf("query failures: %v", err)
			}

			var got []string
			if err := json.Unmarshal([]byte(failJSON), &got); err != nil {
				t.Fatalf("unmarshal failures: %v", err)
			}

			if len(got) != len(tt.want) {
				t.Fatalf("len(failures) = %d, want %d", len(got), len(tt.want))
			}
			for j := range got {
				if got[j] != tt.want[j] {
					t.Errorf("failures[%d] = %q, want %q", j, got[j], tt.want[j])
				}
			}
		})
	}
}

func TestUpdateRunNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.UpdateRun(ctx, Run{ID: "nonexistent", Status: "converged"})
	if !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("UpdateRun error = %v, want %v", err, ErrRunNotFound)
	}
}
