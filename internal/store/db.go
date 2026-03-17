package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // Pure-Go SQLite driver (no CGO required)
)

// ErrRunNotFound is returned when a run ID does not exist in the store.
var ErrRunNotFound = errors.New("store: run not found")

// errInvalidIdentifier is returned when a table or column name contains unsafe characters.
var errInvalidIdentifier = errors.New("store: invalid SQL identifier")

// Store provides persistence for attractor runs via SQLite.
type Store struct {
	db *sql.DB
}

// NewStore opens a SQLite database at path and creates tables if needed.
func NewStore(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}

	// Limit to a single connection so PRAGMAs set below apply to all operations.
	// SQLite only allows one concurrent writer; WAL mode handles concurrent reads.
	db.SetMaxOpenConns(1)

	// Set busy_timeout FIRST so the WAL mode transition below will retry
	// for 5s instead of failing instantly with SQLITE_BUSY (261) when
	// another process holds the lock.
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: pragma busy_timeout: %w", err)
	}

	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: pragma wal: %w", err)
	}

	if err := createTables(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: create tables: %w", err)
	}

	return &Store{db: db}, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("store: close: %w", err)
	}
	return nil
}

// RecordRun inserts a new run record.
func (s *Store) RecordRun(ctx context.Context, r Run) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO runs (id, spec_path, model, threshold, budget_usd, started_at, finished_at, satisfaction, iterations, total_tokens, total_cost_usd, status, language)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.SpecPath, r.Model, r.Threshold, r.BudgetUSD,
		r.StartedAt.Format(time.RFC3339), formatNullableTime(r.FinishedAt),
		r.Satisfaction, r.Iterations, r.TotalTokens, r.TotalCostUSD, r.Status, r.Language,
	)
	if err != nil {
		return fmt.Errorf("store: record run: %w", err)
	}
	return nil
}

// UpdateRun updates mutable fields of an existing run.
func (s *Store) UpdateRun(ctx context.Context, r Run) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE runs SET finished_at = ?, satisfaction = ?, iterations = ?, total_tokens = ?, total_cost_usd = ?, status = ?
		 WHERE id = ?`,
		formatNullableTime(r.FinishedAt), r.Satisfaction, r.Iterations, r.TotalTokens, r.TotalCostUSD, r.Status,
		r.ID,
	)
	if err != nil {
		return fmt.Errorf("store: update run: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update run rows affected: %w", err)
	}
	if rows == 0 {
		return ErrRunNotFound
	}
	return nil
}

// RecordIteration inserts an iteration record with JSON-serialized failures.
func (s *Store) RecordIteration(ctx context.Context, it Iteration) error {
	failures := it.Failures
	if failures == nil {
		failures = []string{}
	}
	failJSON, err := json.Marshal(failures)
	if err != nil {
		return fmt.Errorf("store: marshal failures: %w", err)
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO iterations (run_id, iteration, satisfaction, input_tokens, output_tokens, cost_usd, failures, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		it.RunID, it.Iteration, it.Satisfaction, it.InputTokens, it.OutputTokens, it.CostUSD,
		string(failJSON), it.CreatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("store: record iteration: %w", err)
	}
	return nil
}

// ListRuns returns the 20 most recent runs ordered by started_at descending.
func (s *Store) ListRuns(ctx context.Context) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, spec_path, model, threshold, budget_usd, started_at, finished_at, satisfaction, iterations, total_tokens, total_cost_usd, status, language
		 FROM runs ORDER BY started_at DESC LIMIT 20`)
	if err != nil {
		return nil, fmt.Errorf("store: list runs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	runs := make([]Run, 0, 20)
	for rows.Next() {
		r, err := scanRunFrom(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list runs iterate: %w", err)
	}
	return runs, nil
}

// GetRun returns a single run by ID or ErrRunNotFound if missing.
func (s *Store) GetRun(ctx context.Context, id string) (Run, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, spec_path, model, threshold, budget_usd, started_at, finished_at, satisfaction, iterations, total_tokens, total_cost_usd, status, language
		 FROM runs WHERE id = ?`, id)

	r, err := scanRunFrom(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrRunNotFound
	}
	if err != nil {
		return Run{}, fmt.Errorf("store: get run: %w", err)
	}
	return r, nil
}

func createTables(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS runs (
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
		);

		CREATE TABLE IF NOT EXISTS iterations (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id         TEXT NOT NULL REFERENCES runs(id),
			iteration      INTEGER NOT NULL,
			satisfaction   REAL,
			input_tokens   INTEGER,
			output_tokens  INTEGER,
			cost_usd       REAL,
			failures       TEXT,
			created_at     DATETIME NOT NULL
		);
	`)
	if err != nil {
		return err
	}

	// Migrate: add language column to existing databases.
	return addColumnIfMissing(ctx, db, "runs", "language", "TEXT NOT NULL DEFAULT ''")
}

// addColumnIfMissing adds a column to a table if it does not already exist.
// It validates that table and column contain only lowercase letters and underscores
// to prevent SQL injection in DDL statements (SQLite does not support parameterized DDL).
func addColumnIfMissing(ctx context.Context, db *sql.DB, table, column, colDef string) error {
	for _, ident := range []string{table, column} {
		for _, c := range ident {
			if (c < 'a' || c > 'z') && c != '_' {
				return fmt.Errorf("%w: %q", errInvalidIdentifier, ident)
			}
		}
	}
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return fmt.Errorf("addColumnIfMissing pragma: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var dfltValue *string
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dfltValue, &pk); err != nil {
			return fmt.Errorf("addColumnIfMissing scan: %w", err)
		}
		if name == column {
			return nil // column already exists
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("addColumnIfMissing rows: %w", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, colDef)); err != nil {
		return fmt.Errorf("addColumnIfMissing alter: %w", err)
	}
	return nil
}

func formatNullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Format(time.RFC3339)
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanRunFrom(s scanner) (Run, error) {
	var r Run
	var startedAt, finishedAt sql.NullString
	if err := s.Scan(
		&r.ID, &r.SpecPath, &r.Model, &r.Threshold, &r.BudgetUSD,
		&startedAt, &finishedAt,
		&r.Satisfaction, &r.Iterations, &r.TotalTokens, &r.TotalCostUSD, &r.Status, &r.Language,
	); err != nil {
		return Run{}, err
	}
	var err error
	r.StartedAt, err = time.Parse(time.RFC3339, startedAt.String)
	if err != nil {
		return Run{}, fmt.Errorf("store: parse started_at: %w", err)
	}
	if finishedAt.Valid {
		t, err := time.Parse(time.RFC3339, finishedAt.String)
		if err != nil {
			return Run{}, fmt.Errorf("store: parse finished_at: %w", err)
		}
		r.FinishedAt = &t
	}
	return r, nil
}
