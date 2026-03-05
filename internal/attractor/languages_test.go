package attractor

import (
	"slices"
	"testing"
)

func TestLookupLanguageKnown(t *testing.T) {
	for _, lang := range []string{"go", "python", "node", "rust"} {
		tmpl, ok := LookupLanguage(lang)
		if !ok {
			t.Errorf("LookupLanguage(%q) should return true", lang)
		}
		if tmpl.Name == "" {
			t.Errorf("LookupLanguage(%q) returned template with empty Name", lang)
		}
	}
}

func TestLookupLanguageUnknown(t *testing.T) {
	_, ok := LookupLanguage("cobol")
	if ok {
		t.Error("LookupLanguage(\"cobol\") should return false")
	}

	_, ok = LookupLanguage("")
	if ok {
		t.Error("LookupLanguage(\"\") should return false")
	}
}

func TestSupportedLanguages(t *testing.T) {
	langs := SupportedLanguages()
	if len(langs) != 4 {
		t.Fatalf("expected 4 supported languages, got %d", len(langs))
	}
	if !slices.IsSorted(langs) {
		t.Errorf("SupportedLanguages should return sorted list, got %v", langs)
	}
	want := []string{"go", "node", "python", "rust"}
	if !slices.Equal(langs, want) {
		t.Errorf("SupportedLanguages = %v, want %v", langs, want)
	}
}

func TestAllTemplatesHaveRequiredFields(t *testing.T) {
	for _, lang := range SupportedLanguages() {
		tmpl, _ := LookupLanguage(lang)
		t.Run(lang, func(t *testing.T) {
			if tmpl.Name == "" {
				t.Error("Name is empty")
			}
			if tmpl.BaseImage == "" {
				t.Error("BaseImage is empty")
			}
			if tmpl.HTTPExample.EntryFile == "" {
				t.Error("HTTPExample.EntryFile is empty")
			}
			if tmpl.HTTPExample.EntryContent == "" {
				t.Error("HTTPExample.EntryContent is empty")
			}
			if tmpl.HTTPExample.Dockerfile == "" {
				t.Error("HTTPExample.Dockerfile is empty")
			}
			if tmpl.CLIExample.EntryFile == "" {
				t.Error("CLIExample.EntryFile is empty")
			}
			if tmpl.CLIExample.EntryContent == "" {
				t.Error("CLIExample.EntryContent is empty")
			}
			if tmpl.CLIExample.Dockerfile == "" {
				t.Error("CLIExample.Dockerfile is empty")
			}
			if tmpl.DepRules == "" {
				t.Error("DepRules is empty")
			}
			if tmpl.GRPCSetup == "" {
				t.Error("GRPCSetup is empty")
			}
		})
	}
}
