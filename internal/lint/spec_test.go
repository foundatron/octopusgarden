package lint

import (
	"strings"
	"testing"
)

func TestCheckSpecContent(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		wantErrors int
		wantWarns  int
		wantMsg    string // substring match on first diagnostic
	}{
		{
			name:       "valid spec",
			content:    "# My API\n\nA great API.\n\n## Endpoints\n\nGET /items\n",
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name:       "empty file",
			content:    "",
			wantErrors: 1,
			wantMsg:    "empty",
		},
		{
			name:       "whitespace only",
			content:    "   \n\n  \t\n",
			wantErrors: 1,
			wantMsg:    "empty",
		},
		{
			name:       "no level-1 heading",
			content:    "## Section\n\nSome text.\n",
			wantErrors: 1,
			wantMsg:    "no level-1 heading",
		},
		{
			name:       "no description after title",
			content:    "# Title\n## Section\n\nContent here.\n",
			wantErrors: 0,
			wantWarns:  1,
			wantMsg:    "no description",
		},
		{
			name:       "empty section",
			content:    "# Title\n\nDescription.\n\n## Empty Section\n\n## Full Section\n\nContent.\n",
			wantErrors: 0,
			wantWarns:  1,
			wantMsg:    "no content",
		},
		{
			name:       "duplicate heading same level",
			content:    "# Title\n\nDesc.\n\n## Endpoints\n\nGET /a\n\n## Endpoints\n\nGET /b\n",
			wantErrors: 0,
			wantWarns:  1,
			wantMsg:    "duplicate heading",
		},
		{
			name:       "duplicate heading different level ok",
			content:    "# Title\n\nDesc.\n\n## Foo\n\nContent.\n\n### Foo\n\nSub content.\n",
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name:       "heading in fenced code block ignored",
			content:    "# Title\n\nDesc.\n\n## Real Section\n\nContent.\n\n```\n# Not a heading\n```\n",
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name:       "heading in tilde fence ignored",
			content:    "# Title\n\nDesc.\n\n## Real Section\n\nContent.\n\n~~~\n# Not a heading\n~~~\n",
			wantErrors: 0,
			wantWarns:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diags := lintSpecContent("test.md", tt.content)
			errs, warns := CountByLevel(diags)

			if errs != tt.wantErrors {
				t.Errorf("got %d errors, want %d; diags: %v", errs, tt.wantErrors, diags)
			}
			if warns != tt.wantWarns {
				t.Errorf("got %d warnings, want %d; diags: %v", warns, tt.wantWarns, diags)
			}
			if tt.wantMsg != "" && len(diags) > 0 {
				found := false
				for _, d := range diags {
					if strings.Contains(d.Message, tt.wantMsg) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("no diagnostic contains %q; got: %v", tt.wantMsg, diags)
				}
			}
		})
	}
}

func TestCheckSpecFile(t *testing.T) {
	// Test with the actual example spec.
	diags, err := CheckSpec("../../examples/hello-api/spec.md")
	if err != nil {
		t.Fatalf("CheckSpec: %v", err)
	}
	if HasErrors(diags) {
		t.Errorf("example spec should have no errors; got: %v", diags)
	}
}

func TestCheckSpecFileNotFound(t *testing.T) {
	_, err := CheckSpec("nonexistent.md")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}
