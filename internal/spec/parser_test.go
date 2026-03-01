package spec

import (
	"errors"
	"strings"
	"testing"
)

const sampleSpec = `# Items REST API

A simple REST API for managing items with CRUD operations.

## Endpoints

### POST /items

Create a new item with a name and description.

### GET /items/{id}

Retrieve a single item by its ID.

## Data Model

Each item has an id, name, description, and created_at timestamp.
`

func TestParse(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		wantTitle     string
		wantDesc      string
		wantSections  int
		wantErr       error
		checkSections func(t *testing.T, sections []Section)
	}{
		{
			name:         "valid spec",
			input:        sampleSpec,
			wantTitle:    "Items REST API",
			wantDesc:     "A simple REST API for managing items with CRUD operations.",
			wantSections: 5,
			checkSections: func(t *testing.T, sections []Section) {
				t.Helper()
				// First section is the title itself.
				if sections[0].Heading != "Items REST API" {
					t.Errorf("first section heading = %q, want %q", sections[0].Heading, "Items REST API")
				}
				if sections[0].Level != 1 {
					t.Errorf("first section level = %d, want 1", sections[0].Level)
				}
				// "Endpoints" section at level 2.
				if sections[1].Heading != "Endpoints" {
					t.Errorf("second section heading = %q, want %q", sections[1].Heading, "Endpoints")
				}
				if sections[1].Level != 2 {
					t.Errorf("second section level = %d, want 2", sections[1].Level)
				}
				// Level-3 headings are nested under Endpoints.
				if sections[2].Heading != "POST /items" {
					t.Errorf("third section heading = %q, want %q", sections[2].Heading, "POST /items")
				}
				if sections[2].Level != 3 {
					t.Errorf("third section level = %d, want 3", sections[2].Level)
				}
			},
		},
		{
			name:    "empty input",
			input:   "",
			wantErr: errEmptySpec,
		},
		{
			name:    "whitespace only",
			input:   "   \n\n  \t  \n",
			wantErr: errEmptySpec,
		},
		{
			name:         "title extraction",
			input:        "# My Project\n\nSome description here.\n",
			wantTitle:    "My Project",
			wantDesc:     "Some description here.",
			wantSections: 1,
		},
		{
			name:         "multi-level sections",
			input:        "# Top\n\n## Second\n\nContent A\n\n### Third\n\nContent B\n\n## Another Second\n\nContent C\n",
			wantTitle:    "Top",
			wantSections: 4,
			checkSections: func(t *testing.T, sections []Section) {
				t.Helper()
				// "Second" level=2, content goes until next level<=2 heading.
				if sections[1].Heading != "Second" {
					t.Errorf("sections[1].Heading = %q, want %q", sections[1].Heading, "Second")
				}
				if sections[1].Level != 2 {
					t.Errorf("sections[1].Level = %d, want 2", sections[1].Level)
				}
				// "Third" section.
				if sections[2].Heading != "Third" {
					t.Errorf("sections[2].Heading = %q, want %q", sections[2].Heading, "Third")
				}
				if sections[2].Level != 3 {
					t.Errorf("sections[2].Level = %d, want 3", sections[2].Level)
				}
				if sections[2].Content != "Content B" {
					t.Errorf("sections[2].Content = %q, want %q", sections[2].Content, "Content B")
				}
				// "Another Second" section.
				if sections[3].Heading != "Another Second" {
					t.Errorf("sections[3].Heading = %q, want %q", sections[3].Heading, "Another Second")
				}
				if sections[3].Content != "Content C" {
					t.Errorf("sections[3].Content = %q, want %q", sections[3].Content, "Content C")
				}
			},
		},
		{
			name:         "raw content preserved",
			input:        "# Title\n\nBody text\n\n## Section\n\nMore text\n",
			wantSections: 2,
		},
		{
			name:         "no headings",
			input:        "Just some plain text\nwith multiple lines\n",
			wantTitle:    "",
			wantSections: 0,
		},
		{
			name:         "bare hash ignored",
			input:        "#\n\nSome text\n\n# Real Title\n\nDescription\n",
			wantTitle:    "Real Title",
			wantSections: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(strings.NewReader(tt.input))
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantTitle != "" && got.Title != tt.wantTitle {
				t.Errorf("Title = %q, want %q", got.Title, tt.wantTitle)
			}
			if tt.wantDesc != "" && got.Description != tt.wantDesc {
				t.Errorf("Description = %q, want %q", got.Description, tt.wantDesc)
			}
			if len(got.Sections) != tt.wantSections {
				t.Errorf("len(Sections) = %d, want %d", len(got.Sections), tt.wantSections)
			}
			// RawContent should be the whitespace-trimmed input.
			if got.RawContent != strings.TrimSpace(tt.input) {
				t.Errorf("RawContent = %q, want %q", got.RawContent, strings.TrimSpace(tt.input))
			}
			if tt.checkSections != nil {
				tt.checkSections(t, got.Sections)
			}
		})
	}
}

func TestParseFile(t *testing.T) {
	got, err := ParseFile("testdata/sample.md")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if got.Title != "Items REST API" {
		t.Errorf("Title = %q, want %q", got.Title, "Items REST API")
	}
	if len(got.Sections) == 0 {
		t.Error("expected sections from sample.md")
	}
	if got.RawContent == "" {
		t.Error("expected RawContent to be populated")
	}
}

func TestParseFile_notFound(t *testing.T) {
	_, err := ParseFile("testdata/nonexistent.md")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
