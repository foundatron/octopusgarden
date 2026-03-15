package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
		Language:     "python",
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
	if got.Language != run.Language {
		t.Errorf("Language = %q, want %q", got.Language, run.Language)
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

func TestAddColumnIfMissing(t *testing.T) {
	ctx := context.Background()

	t.Run("column_already_exists", func(t *testing.T) {
		s := newTestStore(t)
		err := addColumnIfMissing(ctx, s.db, "runs", "language", "TEXT NOT NULL DEFAULT ''")
		if err != nil {
			t.Fatalf("addColumnIfMissing (already exists) = %v, want nil", err)
		}
		// Verify the table still works.
		if err := s.RecordRun(ctx, Run{
			ID:        "idempotent",
			SpecPath:  "/spec.md",
			Model:     "m",
			Threshold: 95,
			StartedAt: time.Now(),
			Status:    "running",
		}); err != nil {
			t.Fatalf("RecordRun after idempotent migration: %v", err)
		}
	})

	t.Run("column_missing_gets_added", func(t *testing.T) {
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("sql.Open: %v", err)
		}
		defer func() { _ = db.Close() }()

		// Create runs table without language column.
		_, err = db.ExecContext(ctx, `CREATE TABLE runs (
			id             TEXT PRIMARY KEY,
			spec_path      TEXT NOT NULL,
			model          TEXT NOT NULL,
			threshold      REAL NOT NULL,
			budget_usd     REAL,
			started_at     DATETIME NOT NULL,
			finished_at    DATETIME,
			satisfaction   REAL,
			iterations     INTEGER,
			total_tokens   INTEGER,
			total_cost_usd REAL,
			status         TEXT NOT NULL
		)`)
		if err != nil {
			t.Fatalf("CREATE TABLE: %v", err)
		}

		if err := addColumnIfMissing(ctx, db, "runs", "language", "TEXT NOT NULL DEFAULT ''"); err != nil {
			t.Fatalf("addColumnIfMissing = %v, want nil", err)
		}

		// Verify column exists via PRAGMA.
		rows, err := db.QueryContext(ctx, "PRAGMA table_info(runs)")
		if err != nil {
			t.Fatalf("PRAGMA table_info: %v", err)
		}
		defer func() { _ = rows.Close() }()
		found := false
		for rows.Next() {
			var cid, notNull, pk int
			var name, typ string
			var dflt *string
			if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
				t.Fatalf("scan pragma row: %v", err)
			}
			if name == "language" {
				found = true
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("pragma rows: %v", err)
		}
		if !found {
			t.Fatal("language column not found after addColumnIfMissing")
		}

		// Verify insert/select with the new column works.
		_, err = db.ExecContext(ctx, `INSERT INTO runs (id, spec_path, model, threshold, budget_usd, started_at, satisfaction, iterations, total_tokens, total_cost_usd, status, language) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			"r1", "/s.md", "m", 95.0, nil, time.Now().Format(time.RFC3339), 0.0, 0, 0, 0.0, "running", "go",
		)
		if err != nil {
			t.Fatalf("INSERT with language column: %v", err)
		}
		var lang string
		if err := db.QueryRowContext(ctx, "SELECT language FROM runs WHERE id = 'r1'").Scan(&lang); err != nil {
			t.Fatalf("SELECT language: %v", err)
		}
		if lang != "go" {
			t.Errorf("language = %q, want %q", lang, "go")
		}
	})

	// Identifier validation is a pure string check that returns before any DB call,
	// so all cases can share a single store.
	identTests := []struct {
		name    string
		table   string
		column  string
		wantErr error // nil means expect success
	}{
		{"invalid_table_name", "runs; DROP TABLE runs--", "language", errInvalidIdentifier},
		{"invalid_column_name_uppercase", "runs", "Language", errInvalidIdentifier},
		{"invalid_column_name_digits", "runs", "col1", errInvalidIdentifier},
		{"invalid_column_name_hyphen", "runs", "my-col", errInvalidIdentifier},
		{"valid_underscore_name", "runs", "some_col", nil},
	}
	sv := newTestStore(t)
	for _, tt := range identTests {
		t.Run(tt.name, func(t *testing.T) {
			err := addColumnIfMissing(ctx, sv.db, tt.table, tt.column, "TEXT")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}

	t.Run("empty_table_name", func(t *testing.T) {
		s := newTestStore(t)
		// Empty string bypasses the identifier character check (range is a no-op),
		// but the resulting SQL is invalid and fails at the SQLite level.
		err := addColumnIfMissing(ctx, s.db, "", "x", "TEXT")
		if err == nil {
			t.Fatal("expected non-nil error for empty table name, got nil")
		}
	})
}

func TestScanRunFromInvalidTime(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name       string
		startedAt  string
		finishedAt string
		wantMsg    string
	}{
		{
			name:       "invalid_started_at",
			startedAt:  "not-a-timestamp",
			finishedAt: "",
			wantMsg:    "parse started_at",
		},
		{
			name:       "invalid_finished_at",
			startedAt:  time.Now().UTC().Format(time.RFC3339),
			finishedAt: "garbage",
			wantMsg:    "parse finished_at",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			id := "bad-time-" + tt.name

			var finishedAtVal any
			if tt.finishedAt != "" {
				finishedAtVal = tt.finishedAt
			}

			_, err := s.db.ExecContext(ctx,
				`INSERT INTO runs (id, spec_path, model, threshold, budget_usd, started_at, finished_at, satisfaction, iterations, total_tokens, total_cost_usd, status, language)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				id, "/spec.md", "model", 95.0, 0.0,
				tt.startedAt, finishedAtVal,
				0.0, 0, 0, 0.0, "running", "",
			)
			if err != nil {
				t.Fatalf("raw INSERT: %v", err)
			}

			_, err = s.GetRun(ctx, id)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.wantMsg)
			}
		})
	}
}
