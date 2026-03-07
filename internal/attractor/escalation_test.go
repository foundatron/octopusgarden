package attractor

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil))
}

func capturingLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

func TestNilEscalation(t *testing.T) {
	e := newEscalationState("", "primary-model", noopLogger())
	if e != nil {
		t.Error("expected nil for empty frugalModel")
	}
}

func TestEscalationStartsAtFrugal(t *testing.T) {
	e := newEscalationState("frugal", "primary", noopLogger())
	if e == nil {
		t.Fatal("expected non-nil escalation state")
	}
	if got := e.currentModel(); got != "frugal" {
		t.Errorf("expected %q, got %q", "frugal", got)
	}
	if e.currentTier != tierFrugal {
		t.Errorf("expected tierFrugal, got %v", e.currentTier)
	}
}

func TestEscalationOnFailure(t *testing.T) {
	e := newEscalationState("frugal", "primary", noopLogger())
	logger := noopLogger()

	e.recordOutcome(false, logger) // failure 1 — no escalation yet
	if e.currentTier != tierFrugal {
		t.Errorf("should still be tierFrugal after 1 failure, got tier %v", e.currentTier)
	}

	e.recordOutcome(false, logger) // failure 2 — should escalate
	if e.currentTier != tierPrimary {
		t.Errorf("expected tierPrimary after 2 failures, got tier %v", e.currentTier)
	}
	if got := e.currentModel(); got != "primary" {
		t.Errorf("expected model %q, got %q", "primary", got)
	}
}

func TestEscalationCeiling(t *testing.T) {
	e := newEscalationState("frugal", "primary", noopLogger())
	logger := noopLogger()

	// Escalate to primary.
	e.recordOutcome(false, logger)
	e.recordOutcome(false, logger)

	if e.currentTier != tierPrimary {
		t.Fatalf("expected tierPrimary, got %v", e.currentTier)
	}

	// More failures should not exceed tierPrimary.
	e.recordOutcome(false, logger)
	e.recordOutcome(false, logger)
	e.recordOutcome(false, logger)

	if e.currentTier != tierPrimary {
		t.Errorf("expected tier to stay at tierPrimary, got %v", e.currentTier)
	}
}

func TestDowngradeOnSuccess(t *testing.T) {
	e := newEscalationState("frugal", "primary", noopLogger())
	logger := noopLogger()

	// Escalate to primary.
	e.recordOutcome(false, logger)
	e.recordOutcome(false, logger)
	if e.currentTier != tierPrimary {
		t.Fatalf("expected tierPrimary, got %v", e.currentTier)
	}

	// 4 improvements — not enough to downgrade.
	for i := range 4 {
		e.recordOutcome(true, logger)
		if e.currentTier != tierPrimary {
			t.Errorf("improvement %d: expected tierPrimary, got %v", i+1, e.currentTier)
		}
	}

	// 5th improvement — should downgrade.
	e.recordOutcome(true, logger)
	if e.currentTier != tierFrugal {
		t.Errorf("expected tierFrugal after 5 improvements, got %v", e.currentTier)
	}
	if got := e.currentModel(); got != "frugal" {
		t.Errorf("expected model %q, got %q", "frugal", got)
	}
}

func TestFailureResetsImprovementCounter(t *testing.T) {
	e := newEscalationState("frugal", "primary", noopLogger())
	logger := noopLogger()

	// Escalate to primary.
	e.recordOutcome(false, logger)
	e.recordOutcome(false, logger)

	// 4 improvements then 1 failure — counter resets.
	for range 4 {
		e.recordOutcome(true, logger)
	}
	e.recordOutcome(false, logger) // resets improvement counter

	if e.currentTier != tierPrimary {
		t.Errorf("expected tierPrimary after reset, got %v", e.currentTier)
	}
	if e.consecutiveImprove != 0 {
		t.Errorf("expected consecutiveImprove=0 after failure, got %d", e.consecutiveImprove)
	}

	// Need 5 fresh improvements to downgrade.
	for i := range 4 {
		e.recordOutcome(true, logger)
		if e.currentTier != tierPrimary {
			t.Errorf("fresh improvement %d: expected tierPrimary, got %v", i+1, e.currentTier)
		}
	}
	e.recordOutcome(true, logger)
	if e.currentTier != tierFrugal {
		t.Errorf("expected tierFrugal after 5 fresh improvements, got %v", e.currentTier)
	}
}

func TestImprovementResetsFailureCounter(t *testing.T) {
	e := newEscalationState("frugal", "primary", noopLogger())
	logger := noopLogger()

	e.recordOutcome(false, logger) // 1 failure
	e.recordOutcome(true, logger)  // resets failure counter

	if e.consecutiveFailures != 0 {
		t.Errorf("expected consecutiveFailures=0 after improvement, got %d", e.consecutiveFailures)
	}
	if e.currentTier != tierFrugal {
		t.Errorf("expected tierFrugal, got %v", e.currentTier)
	}

	// Need 2 fresh failures to escalate.
	e.recordOutcome(false, logger)
	if e.currentTier != tierFrugal {
		t.Errorf("expected tierFrugal after 1 fresh failure, got %v", e.currentTier)
	}
	e.recordOutcome(false, logger)
	if e.currentTier != tierPrimary {
		t.Errorf("expected tierPrimary after 2 fresh failures, got %v", e.currentTier)
	}
}

func TestStateTransitionSequence(t *testing.T) {
	// Table-driven sequence: each row is (improved bool, expectedTier after recordOutcome).
	tests := []struct {
		improved     bool
		expectedTier modelTier
	}{
		// Start at frugal.
		{false, tierFrugal},  // failures=1
		{true, tierFrugal},   // reset, improvements=1
		{false, tierFrugal},  // failures=1
		{false, tierPrimary}, // failures=2 → escalate
		{true, tierPrimary},  // improvements=1
		{true, tierPrimary},  // improvements=2
		{true, tierPrimary},  // improvements=3
		{true, tierPrimary},  // improvements=4
		{true, tierFrugal},   // improvements=5 → downgrade
	}

	e := newEscalationState("frugal", "primary", noopLogger())
	logger := noopLogger()

	for i, tc := range tests {
		e.recordOutcome(tc.improved, logger)
		if e.currentTier != tc.expectedTier {
			t.Errorf("step %d (improved=%v): expected tier %v, got %v", i, tc.improved, tc.expectedTier, e.currentTier)
		}
	}
}

func TestEscalationLogging(t *testing.T) {
	var buf bytes.Buffer
	logger := capturingLogger(&buf)

	e := newEscalationState("frugal", "primary", noopLogger())
	e.recordOutcome(false, logger)
	e.recordOutcome(false, logger) // triggers escalation log

	output := buf.String()
	if !strings.Contains(output, "model escalated") {
		t.Errorf("expected 'model escalated' in log output, got:\n%s", output)
	}
	if !strings.Contains(output, "primary") {
		t.Errorf("expected model name in log output, got:\n%s", output)
	}

	buf.Reset()
	// 5 improvements to trigger downgrade log.
	for range 5 {
		e.recordOutcome(true, logger)
	}
	output = buf.String()
	if !strings.Contains(output, "model downgraded") {
		t.Errorf("expected 'model downgraded' in log output, got:\n%s", output)
	}
	if !strings.Contains(output, "frugal") {
		t.Errorf("expected model name in downgrade log output, got:\n%s", output)
	}
}
