package attractor

import (
	"testing"
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
