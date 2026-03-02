package attractor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Trend classifies the direction of score history.
type Trend string

// Trend constants for score trajectory classification.
const (
	TrendImproving  Trend = "improving"
	TrendPlateau    Trend = "plateau"
	TrendRegressing Trend = "regressing"
	TrendConverged  Trend = "converged"
)

const checkpointFile = "checkpoint.json"

var errNoCheckpoint = errors.New("attractor: no checkpoint found")

// CheckpointMeta holds metadata about a saved checkpoint.
type CheckpointMeta struct {
	Iteration    int       `json:"iteration"`
	Satisfaction float64   `json:"satisfaction"`
	Trend        Trend     `json:"trend"`
	Timestamp    time.Time `json:"timestamp"`
}

// DetectTrend classifies the trajectory of a score history.
// threshold is the convergence threshold, stallLimit defines the lookback window size.
func DetectTrend(history []float64, threshold float64, stallLimit int) Trend {
	if len(history) <= 1 {
		return TrendPlateau
	}
	if stallLimit < 2 {
		stallLimit = 2
	}

	last := history[len(history)-1]
	if last >= threshold {
		return TrendConverged
	}

	// Window = last stallLimit entries (or all if fewer).
	windowStart := len(history) - stallLimit
	if windowStart < 0 {
		windowStart = 0
	}
	window := history[windowStart:]

	// All scores in window identical → plateau.
	allEqual := true
	for _, s := range window[1:] {
		if s != window[0] {
			allEqual = false
			break
		}
	}
	if allEqual {
		return TrendPlateau
	}

	// Baseline = score just before window, or first in window if window covers all.
	var baseline float64
	if windowStart > 0 {
		baseline = history[windowStart-1]
	} else {
		baseline = window[0]
	}

	// Check if last < max in window → regressing (peaked then dropped).
	maxInWindow := window[0]
	for _, s := range window[1:] {
		if s > maxInWindow {
			maxInWindow = s
		}
	}
	if last < maxInWindow {
		return TrendRegressing
	}

	if last > baseline {
		return TrendImproving
	}

	return TrendPlateau
}

// SaveCheckpoint writes generated files and metadata to a checkpoint directory.
func SaveCheckpoint(dir string, files map[string]string, meta CheckpointMeta) error {
	if err := writeFiles(dir, files); err != nil {
		return fmt.Errorf("attractor: save checkpoint files: %w", err)
	}

	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("attractor: marshal checkpoint meta: %w", err)
	}

	metaPath := filepath.Join(dir, checkpointFile)
	if err := os.WriteFile(metaPath, data, 0o600); err != nil {
		return fmt.Errorf("attractor: write checkpoint meta: %w", err)
	}

	return nil
}

// LoadCheckpoint reads a checkpoint directory and returns the files and metadata.
// Returns errNoCheckpoint if the checkpoint.json file does not exist.
func LoadCheckpoint(dir string) (map[string]string, CheckpointMeta, error) {
	metaPath := filepath.Join(dir, checkpointFile)

	data, err := os.ReadFile(metaPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, CheckpointMeta{}, errNoCheckpoint
		}
		return nil, CheckpointMeta{}, fmt.Errorf("attractor: read checkpoint meta: %w", err)
	}

	var meta CheckpointMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, CheckpointMeta{}, fmt.Errorf("attractor: unmarshal checkpoint meta: %w", err)
	}

	files := make(map[string]string)
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return fmt.Errorf("attractor: rel path: %w", err)
		}

		// Skip the checkpoint metadata file.
		if rel == checkpointFile {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("attractor: read file %s: %w", rel, err)
		}

		// Normalize path separators to forward slashes.
		files[strings.ReplaceAll(rel, string(filepath.Separator), "/")] = string(content)
		return nil
	})
	if err != nil {
		return nil, CheckpointMeta{}, fmt.Errorf("attractor: walk checkpoint dir: %w", err)
	}

	return files, meta, nil
}
