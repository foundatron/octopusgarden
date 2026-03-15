package gene

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	err := Validate(g)
	if !errors.Is(err, errInvalidVersion) {
		t.Errorf("Validate() = %v, want %v", err, errInvalidVersion)
	}
}

func TestValidateEmptyGuide(t *testing.T) {
	g := validGene()
	g.Guide = ""
	err := Validate(g)
	if !errors.Is(err, errEmptyGuide) {
		t.Errorf("Validate() = %v, want %v", err, errEmptyGuide)
	}
}

func TestValidateUnknownLanguage(t *testing.T) {
	g := validGene()
	g.Language = "ruby"
	err := Validate(g)
	if !errors.Is(err, errUnknownLanguage) {
		t.Errorf("Validate() = %v, want %v", err, errUnknownLanguage)
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

func TestValidateComponents(t *testing.T) {
	tests := []struct {
		name       string
		components []Component
		wantErr    error
	}{
		{
			name: "valid",
			components: []Component{
				{Name: "A", Interface: "Foo", Patterns: "bar", DependsOn: []string{"B"}},
				{Name: "B", Interface: "Baz", Patterns: "qux"},
			},
			wantErr: nil,
		},
		{
			name:       "empty_name",
			components: []Component{{Name: ""}},
			wantErr:    errEmptyComponentName,
		},
		{
			name:       "duplicate_name",
			components: []Component{{Name: "A"}, {Name: "A"}},
			wantErr:    errDuplicateComponent,
		},
		{
			name:       "missing_dependency",
			components: []Component{{Name: "A", DependsOn: []string{"X"}}},
			wantErr:    errMissingDependency,
		},
		{
			name: "cycle_simple",
			components: []Component{
				{Name: "A", DependsOn: []string{"B"}},
				{Name: "B", DependsOn: []string{"A"}},
			},
			wantErr: errDependencyCycle,
		},
		{
			name: "cycle_longer",
			components: []Component{
				{Name: "A", DependsOn: []string{"B"}},
				{Name: "B", DependsOn: []string{"C"}},
				{Name: "C", DependsOn: []string{"A"}},
			},
			wantErr: errDependencyCycle,
		},
		{
			name:       "self_cycle",
			components: []Component{{Name: "A", DependsOn: []string{"A"}}},
			wantErr:    errDependencyCycle,
		},
		{
			name:       "no_dependencies",
			components: []Component{{Name: "A"}, {Name: "B"}},
			wantErr:    nil,
		},
		{
			name: "case_insensitive_dep",
			components: []Component{
				{Name: "A", DependsOn: []string{"b"}},
				{Name: "B"},
			},
			wantErr: nil,
		},
		{
			name: "whitespace_variant_dep",
			components: []Component{
				{Name: "HTTP Handler", DependsOn: []string{"data store"}},
				{Name: "Data Store"},
			},
			wantErr: nil,
		},
		{
			name: "extra_whitespace_dep",
			components: []Component{
				{Name: "A", DependsOn: []string{"  B  "}},
				{Name: "B"},
			},
			wantErr: nil,
		},
		{
			name:       "duplicate_name_case_insensitive",
			components: []Component{{Name: "Store"}, {Name: "store"}},
			wantErr:    errDuplicateComponent,
		},
		{
			name: "cycle_case_insensitive",
			components: []Component{
				{Name: "A", DependsOn: []string{"b"}},
				{Name: "B", DependsOn: []string{"a"}},
			},
			wantErr: errDependencyCycle,
		},
		{
			name:       "whitespace_only_name",
			components: []Component{{Name: "   "}},
			wantErr:    errEmptyComponentName,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := validGene()
			g.Components = tt.components
			err := Validate(g)
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Validate() = %v, want errors.Is(%v)", err, tt.wantErr)
			}
		})
	}
}

func TestGeneRoundTripWithComponents(t *testing.T) {
	t.Run("with_components", func(t *testing.T) {
		original := validGene()
		original.Components = []Component{
			{Name: "A", Interface: "foo", Patterns: "bar", DependsOn: []string{"B"}},
			{Name: "B", Interface: "baz", Patterns: "qux"},
		}
		data, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
		var decoded Gene
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("Unmarshal() error = %v", err)
		}
		if len(decoded.Components) != 2 {
			t.Fatalf("Components len = %d, want 2", len(decoded.Components))
		}
		if decoded.Components[0].Name != "A" {
			t.Errorf("Components[0].Name = %q, want %q", decoded.Components[0].Name, "A")
		}
		if decoded.Components[1].Name != "B" {
			t.Errorf("Components[1].Name = %q, want %q", decoded.Components[1].Name, "B")
		}
	})

	t.Run("omitempty_nil", func(t *testing.T) {
		original := validGene() // Components is nil
		data, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
		if strings.Contains(string(data), `"components"`) {
			t.Error(`JSON should not contain "components" key when Components is nil`)
		}
	})
}

func TestSaveLoadWithComponents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gene-components.json")

	original := validGene()
	original.Components = []Component{
		{Name: "Handler", Interface: "HTTP handler", Patterns: "net/http", DependsOn: []string{"Service"}},
		{Name: "Service", Interface: "Business logic", Patterns: "pure functions"},
	}

	if err := Save(path, original); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if len(loaded.Components) != 2 {
		t.Fatalf("Components len = %d, want 2", len(loaded.Components))
	}
	if loaded.Components[0].Name != "Handler" {
		t.Errorf("Components[0].Name = %q, want %q", loaded.Components[0].Name, "Handler")
	}
	if loaded.Components[1].Name != "Service" {
		t.Errorf("Components[1].Name = %q, want %q", loaded.Components[1].Name, "Service")
	}
	if len(loaded.Components[0].DependsOn) != 1 || loaded.Components[0].DependsOn[0] != "Service" {
		t.Errorf("Components[0].DependsOn = %v, want [Service]", loaded.Components[0].DependsOn)
	}
}
