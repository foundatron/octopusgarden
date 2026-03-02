package attractor

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDetectTrend(t *testing.T) {
	tests := []struct {
		name       string
		history    []float64
		threshold  float64
		stallLimit int
		want       Trend
	}{
		{
			name:       "empty history",
			history:    nil,
			threshold:  95,
			stallLimit: 3,
			want:       TrendPlateau,
		},
		{
			name:       "single entry",
			history:    []float64{50},
			threshold:  95,
			stallLimit: 3,
			want:       TrendPlateau,
		},
		{
			name:       "converged at threshold",
			history:    []float64{50, 60, 95},
			threshold:  95,
			stallLimit: 3,
			want:       TrendConverged,
		},
		{
			name:       "converged above threshold",
			history:    []float64{50, 60, 100},
			threshold:  95,
			stallLimit: 3,
			want:       TrendConverged,
		},
		{
			name:       "improving",
			history:    []float64{40, 50, 60, 70},
			threshold:  95,
			stallLimit: 3,
			want:       TrendImproving,
		},
		{
			name:       "plateau flat scores",
			history:    []float64{50, 50, 50},
			threshold:  95,
			stallLimit: 3,
			want:       TrendPlateau,
		},
		{
			name:       "regressing",
			history:    []float64{40, 60, 80, 70},
			threshold:  95,
			stallLimit: 3,
			want:       TrendRegressing,
		},
		{
			name:       "improving then plateau within window",
			history:    []float64{40, 60, 60, 60},
			threshold:  95,
			stallLimit: 3,
			want:       TrendPlateau,
		},
		{
			name:       "stall limit larger than history",
			history:    []float64{40, 50},
			threshold:  95,
			stallLimit: 10,
			want:       TrendImproving,
		},
		{
			name:       "regressing from peak",
			history:    []float64{40, 60, 80, 60, 50},
			threshold:  95,
			stallLimit: 3,
			want:       TrendRegressing,
		},
		{
			name:       "improving with baseline before window",
			history:    []float64{30, 40, 50, 60, 70},
			threshold:  95,
			stallLimit: 3,
			want:       TrendImproving,
		},
		{
			name:       "stall limit zero clamped",
			history:    []float64{40, 50, 60},
			threshold:  95,
			stallLimit: 0,
			want:       TrendImproving,
		},
		{
			name:       "stall limit one clamped",
			history:    []float64{40, 50, 60},
			threshold:  95,
			stallLimit: 1,
			want:       TrendImproving,
		},
		{
			name:       "two entry regression",
			history:    []float64{80, 70},
			threshold:  95,
			stallLimit: 3,
			want:       TrendRegressing,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectTrend(tt.history, tt.threshold, tt.stallLimit)
			if got != tt.want {
				t.Errorf("DetectTrend(%v, %.0f, %d) = %q, want %q", tt.history, tt.threshold, tt.stallLimit, got, tt.want)
			}
		})
	}
}

func TestSaveLoadCheckpoint(t *testing.T) {
	dir := t.TempDir()

	files := map[string]string{
		"main.go":    "package main\n\nfunc main() {}\n",
		"Dockerfile": "FROM scratch\n",
	}
	meta := CheckpointMeta{
		Iteration:    3,
		Satisfaction: 85.5,
		Trend:        TrendImproving,
		Timestamp:    time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
	}

	if err := SaveCheckpoint(dir, files, meta); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}

	gotFiles, gotMeta, err := LoadCheckpoint(dir)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}

	if gotMeta.Iteration != meta.Iteration {
		t.Errorf("iteration = %d, want %d", gotMeta.Iteration, meta.Iteration)
	}
	if gotMeta.Satisfaction != meta.Satisfaction {
		t.Errorf("satisfaction = %f, want %f", gotMeta.Satisfaction, meta.Satisfaction)
	}
	if gotMeta.Trend != meta.Trend {
		t.Errorf("trend = %q, want %q", gotMeta.Trend, meta.Trend)
	}
	if !gotMeta.Timestamp.Equal(meta.Timestamp) {
		t.Errorf("timestamp = %v, want %v", gotMeta.Timestamp, meta.Timestamp)
	}

	for path, content := range files {
		got, ok := gotFiles[path]
		if !ok {
			t.Errorf("missing file %q in loaded checkpoint", path)
			continue
		}
		if got != content {
			t.Errorf("file %q content = %q, want %q", path, got, content)
		}
	}
	if len(gotFiles) != len(files) {
		t.Errorf("loaded %d files, want %d", len(gotFiles), len(files))
	}
}

func TestLoadCheckpointNotFound(t *testing.T) {
	dir := t.TempDir()

	_, _, err := LoadCheckpoint(dir)
	if !errors.Is(err, errNoCheckpoint) {
		t.Fatalf("expected errNoCheckpoint, got %v", err)
	}
}

func TestCheckpointWithSubdirectories(t *testing.T) {
	dir := t.TempDir()

	files := map[string]string{
		"cmd/server/main.go":  "package main\n",
		"internal/handler.go": "package internal\n",
		"Dockerfile":          "FROM golang:1.22\n",
	}
	meta := CheckpointMeta{
		Iteration:    5,
		Satisfaction: 90,
		Trend:        TrendPlateau,
		Timestamp:    time.Date(2025, 2, 1, 12, 0, 0, 0, time.UTC),
	}

	if err := SaveCheckpoint(dir, files, meta); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}

	gotFiles, gotMeta, err := LoadCheckpoint(dir)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}

	if gotMeta.Iteration != meta.Iteration {
		t.Errorf("iteration = %d, want %d", gotMeta.Iteration, meta.Iteration)
	}

	for path, content := range files {
		got, ok := gotFiles[path]
		if !ok {
			t.Errorf("missing file %q in loaded checkpoint", path)
			continue
		}
		if got != content {
			t.Errorf("file %q content = %q, want %q", path, got, content)
		}
	}
	if len(gotFiles) != len(files) {
		t.Errorf("loaded %d files, want %d", len(gotFiles), len(files))
	}
}

func TestSaveLoadCheckpointEmptyFiles(t *testing.T) {
	dir := t.TempDir()

	meta := CheckpointMeta{
		Iteration:    1,
		Satisfaction: 0,
		Trend:        TrendPlateau,
		Timestamp:    time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	if err := SaveCheckpoint(dir, map[string]string{}, meta); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}

	gotFiles, gotMeta, err := LoadCheckpoint(dir)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if len(gotFiles) != 0 {
		t.Errorf("expected 0 files, got %d", len(gotFiles))
	}
	if gotMeta.Iteration != 1 {
		t.Errorf("iteration = %d, want 1", gotMeta.Iteration)
	}
}

func TestLoadCheckpointCorrupt(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "checkpoint.json"), []byte("{invalid"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := LoadCheckpoint(dir)
	if err == nil {
		t.Fatal("expected error for corrupt checkpoint")
	}
}
