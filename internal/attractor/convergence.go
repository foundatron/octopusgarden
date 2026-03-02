package attractor

// Trend classifies the direction of score history.
type Trend string

// Trend constants for score trajectory classification.
const (
	TrendImproving  Trend = "improving"
	TrendPlateau    Trend = "plateau"
	TrendRegressing Trend = "regressing"
	TrendConverged  Trend = "converged"
)

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
