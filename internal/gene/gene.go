package gene

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

var (
	errInvalidVersion  = errors.New("version must be greater than zero")
	errEmptyGuide      = errors.New("guide must not be empty")
	errUnknownLanguage = errors.New("unknown language")
)

var validLanguages = map[string]bool{
	"go":     true,
	"python": true,
	"node":   true,
	"rust":   true,
}

// Gene represents an extracted coding guide for a specific language,
// derived from a source repository's patterns and conventions.
type Gene struct {
	Version     int       `json:"version"`
	Source      string    `json:"source"`
	Language    string    `json:"language"`
	ExtractedAt time.Time `json:"extracted_at"`
	Guide       string    `json:"guide"`
	TokenCount  int       `json:"token_count"`
}

// Validate checks that the gene has valid field values.
func Validate(g Gene) error {
	if g.Version < 1 {
		return errInvalidVersion
	}
	if g.Guide == "" {
		return errEmptyGuide
	}
	if !validLanguages[g.Language] {
		return errUnknownLanguage
	}
	return nil
}

// Save marshals the gene as indented JSON and writes it to path with 0600 permissions.
// It validates the gene before writing.
func Save(path string, g Gene) error {
	if err := Validate(g); err != nil {
		return fmt.Errorf("gene: %w", err)
	}
	data, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return fmt.Errorf("gene save marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("gene save: %w", err)
	}
	return nil
}

// Load reads a gene from a JSON file at path, unmarshals it, and validates.
func Load(path string) (Gene, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Gene{}, fmt.Errorf("gene: %w", err)
	}
	var g Gene
	if err := json.Unmarshal(data, &g); err != nil {
		return Gene{}, fmt.Errorf("gene: %w", err)
	}
	if err := Validate(g); err != nil {
		return Gene{}, fmt.Errorf("gene: %w", err)
	}
	return g, nil
}
