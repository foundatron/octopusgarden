package gene

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func validGene() Gene {
	return Gene{
		Version:     1,
		Source:      "https://github.com/example/repo",
		Language:    "go",
		ExtractedAt: time.Date(2025, 6, 15, 10, 30, 0, 0, time.UTC),
		Guide:       "Use net/http for HTTP servers.",
		TokenCount:  42,
	}
}

func TestValidateValid(t *testing.T) {
	if err := Validate(validGene()); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestValidateZeroVersion(t *testing.T) {
	g := validGene()
	g.Version = 0
	if !errors.Is(Validate(g), errInvalidVersion) {
		t.Errorf("Validate() = %v, want %v", Validate(g), errInvalidVersion)
	}
}

func TestValidateEmptyGuide(t *testing.T) {
	g := validGene()
	g.Guide = ""
	if !errors.Is(Validate(g), errEmptyGuide) {
		t.Errorf("Validate() = %v, want %v", Validate(g), errEmptyGuide)
	}
}

func TestValidateUnknownLanguage(t *testing.T) {
	g := validGene()
	g.Language = "ruby"
	if !errors.Is(Validate(g), errUnknownLanguage) {
		t.Errorf("Validate() = %v, want %v", Validate(g), errUnknownLanguage)
	}
}

func TestValidateMissingSource(t *testing.T) {
	g := validGene()
	g.Source = ""
	if err := Validate(g); err != nil {
		t.Errorf("Validate() with empty Source = %v, want nil", err)
	}
}

func TestGeneRoundTrip(t *testing.T) {
	original := validGene()
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var decoded Gene
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if decoded.Version != original.Version {
		t.Errorf("Version = %d, want %d", decoded.Version, original.Version)
	}
	if decoded.Source != original.Source {
		t.Errorf("Source = %q, want %q", decoded.Source, original.Source)
	}
	if decoded.Language != original.Language {
		t.Errorf("Language = %q, want %q", decoded.Language, original.Language)
	}
	if !decoded.ExtractedAt.Equal(original.ExtractedAt) {
		t.Errorf("ExtractedAt = %v, want %v", decoded.ExtractedAt, original.ExtractedAt)
	}
	if decoded.Guide != original.Guide {
		t.Errorf("Guide = %q, want %q", decoded.Guide, original.Guide)
	}
	if decoded.TokenCount != original.TokenCount {
		t.Errorf("TokenCount = %d, want %d", decoded.TokenCount, original.TokenCount)
	}
}

func TestGeneRoundTripAllLanguages(t *testing.T) {
	for _, lang := range []string{"go", "python", "node", "rust"} {
		t.Run(lang, func(t *testing.T) {
			g := validGene()
			g.Language = lang

			data, err := json.Marshal(g)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}

			var decoded Gene
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}

			if decoded.Language != lang {
				t.Errorf("Language = %q, want %q", decoded.Language, lang)
			}
		})
	}
}

func TestSaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gene.json")

	original := validGene()
	if err := Save(path, original); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if loaded.Version != original.Version {
		t.Errorf("Version = %d, want %d", loaded.Version, original.Version)
	}
	if loaded.Source != original.Source {
		t.Errorf("Source = %q, want %q", loaded.Source, original.Source)
	}
	if loaded.Language != original.Language {
		t.Errorf("Language = %q, want %q", loaded.Language, original.Language)
	}
	if !loaded.ExtractedAt.Equal(original.ExtractedAt) {
		t.Errorf("ExtractedAt = %v, want %v", loaded.ExtractedAt, original.ExtractedAt)
	}
	if loaded.Guide != original.Guide {
		t.Errorf("Guide = %q, want %q", loaded.Guide, original.Guide)
	}
	if loaded.TokenCount != original.TokenCount {
		t.Errorf("TokenCount = %d, want %d", loaded.TokenCount, original.TokenCount)
	}
}

func TestSaveFilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gene.json")

	if err := Save(path, validGene()); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("file permissions = %o, want 600", mode)
	}
}

func TestLoadNotFound(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nonexistent.json"))
	if err == nil {
		t.Fatal("Load() expected error for missing file, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Load() error should wrap os.ErrNotExist, got: %v", err)
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() expected error for invalid JSON, got nil")
	}
}

func TestLoadEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() expected error for empty file, got nil")
	}
}

func TestLoadValidates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "invalid.json")
	content := `{"version":0,"source":"x","language":"go","guide":"stuff","token_count":1}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if !errors.Is(err, errInvalidVersion) {
		t.Errorf("Load() error = %v, want %v", err, errInvalidVersion)
	}
}

func TestSaveValidationFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "should-not-exist.json")

	g := validGene()
	g.Guide = ""
	err := Save(path, g)
	if !errors.Is(err, errEmptyGuide) {
		t.Errorf("Save() error = %v, want %v", err, errEmptyGuide)
	}

	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("Save() should not create file on validation failure")
	}
}
