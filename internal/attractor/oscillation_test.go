package attractor

import (
	"strings"
	"testing"
)

func TestHashFiles(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		files := map[string]string{"main.go": "package main"}
		h1 := hashFiles(files)
		h2 := hashFiles(files)
		if h1 != h2 {
			t.Errorf("expected same hash, got %q and %q", h1, h2)
		}
	})

	t.Run("different content", func(t *testing.T) {
		h1 := hashFiles(map[string]string{"main.go": "package main"})
		h2 := hashFiles(map[string]string{"main.go": "package other"})
		if h1 == h2 {
			t.Error("expected different hashes for different content")
		}
	})

	t.Run("order independent", func(t *testing.T) {
		h1 := hashFiles(map[string]string{"a": "1", "b": "2"})
		h2 := hashFiles(map[string]string{"b": "2", "a": "1"})
		if h1 != h2 {
			t.Errorf("expected same hash regardless of map iteration order, got %q and %q", h1, h2)
		}
	})

	t.Run("empty file set", func(t *testing.T) {
		h := hashFiles(map[string]string{})
		if h == "" {
			t.Error("expected non-empty hash for empty file set")
		}
	})

	t.Run("nil map", func(t *testing.T) {
		h := hashFiles(nil)
		if h == "" {
			t.Error("expected non-empty hash for nil map")
		}
		if h != hashFiles(map[string]string{}) {
			t.Error("expected nil map to produce same hash as empty map")
		}
	})

	t.Run("single file", func(t *testing.T) {
		h := hashFiles(map[string]string{"file.go": "content"})
		if h == "" {
			t.Error("expected non-empty hash for single file")
		}
	})
}

func TestDetectOscillation(t *testing.T) {
	cases := []struct {
		name   string
		hashes []string
		want   bool
	}{
		{"empty", []string{}, false},
		{"one hash", []string{"A"}, false},
		{"two hashes", []string{"A", "B"}, false},
		{"three hashes ABA", []string{"A", "B", "A"}, false},
		{"four hashes no oscillation", []string{"A", "B", "C", "D"}, false},
		{"four hashes partial match", []string{"A", "B", "A", "C"}, false},
		{"four hashes ABAB", []string{"A", "B", "A", "B"}, true},
		{"five hashes oscillation at end", []string{"C", "A", "B", "A", "B"}, true},
		{"four identical hashes", []string{"A", "A", "A", "A"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectOscillation(tc.hashes)
			if got != tc.want {
				t.Errorf("detectOscillation(%v) = %v, want %v", tc.hashes, got, tc.want)
			}
		})
	}
}

func TestBuildOscillationSteering(t *testing.T) {
	text := buildOscillationSteering()
	if !strings.Contains(text, "OSCILLATION DETECTED") {
		t.Error("expected steering text to contain \"OSCILLATION DETECTED\"")
	}
	if strings.Contains(text, "STALL NOTICE") {
		t.Error("expected steering text to not contain \"STALL NOTICE\"")
	}
}
