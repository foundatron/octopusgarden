package scenario

import (
	"errors"
	"strings"
	"testing"
)

const sampleYAML = `
id: items-crud
description: "Create, read, update, and delete items"
type: api
weight: 1.0
setup:
  - description: "Create a test item"
    request:
      method: POST
      path: /items
      body: { "name": "test item", "description": "for testing" }
    capture:
      - name: item_id
        jsonpath: $.id
steps:
  - description: "Read the created item"
    request:
      method: GET
      path: /items/{item_id}
    expect: "Returns the item with name 'test item' and a valid ID"
  - description: "Update the item"
    request:
      method: PUT
      path: /items/{item_id}
      body: { "name": "updated item" }
    expect: "Returns the updated item with name 'updated item'"
satisfaction_criteria: |
  All CRUD operations work correctly with appropriate status codes.
`

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantID  string
		wantErr error
		check   func(t *testing.T, s Scenario)
	}{
		{
			name:   "valid scenario",
			input:  sampleYAML,
			wantID: "items-crud",
			check: func(t *testing.T, s Scenario) {
				t.Helper()
				if s.Description != "Create, read, update, and delete items" {
					t.Errorf("Description = %q", s.Description)
				}
				if s.Type != "api" {
					t.Errorf("Type = %q, want %q", s.Type, "api")
				}
				if len(s.Setup) != 1 {
					t.Errorf("len(Setup) = %d, want 1", len(s.Setup))
				}
				if len(s.Steps) != 2 {
					t.Errorf("len(Steps) = %d, want 2", len(s.Steps))
				}
				if s.SatisfactionCriteria == "" {
					t.Error("SatisfactionCriteria is empty")
				}
			},
		},
		{
			name:   "weight defaults to 1.0",
			input:  "id: no-weight\ndescription: test\ntype: api\nsteps: []\n",
			wantID: "no-weight",
			check: func(t *testing.T, s Scenario) {
				t.Helper()
				if s.Weight == nil || *s.Weight != 1.0 {
					t.Errorf("Weight = %v, want 1.0", s.Weight)
				}
			},
		},
		{
			name:   "explicit weight zero preserved",
			input:  "id: zero-weight\ndescription: test\ntype: api\nweight: 0\nsteps: []\n",
			wantID: "zero-weight",
			check: func(t *testing.T, s Scenario) {
				t.Helper()
				if s.Weight == nil || *s.Weight != 0 {
					t.Errorf("Weight = %v, want 0", s.Weight)
				}
			},
		},
		{
			name:  "capture and jsonpath fields",
			input: sampleYAML,
			check: func(t *testing.T, s Scenario) {
				t.Helper()
				if len(s.Setup) == 0 {
					t.Fatal("no setup steps")
				}
				captures := s.Setup[0].Capture
				if len(captures) != 1 {
					t.Fatalf("len(Capture) = %d, want 1", len(captures))
				}
				if captures[0].Name != "item_id" {
					t.Errorf("Capture.Name = %q, want %q", captures[0].Name, "item_id")
				}
				if captures[0].JSONPath != "$.id" {
					t.Errorf("Capture.JSONPath = %q, want %q", captures[0].JSONPath, "$.id")
				}
			},
		},
		{
			name:    "missing id",
			input:   "description: no id\ntype: api\n",
			wantErr: errMissingID,
		},
		{
			name:    "empty content",
			input:   "",
			wantErr: errEmptyScenario,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Load(strings.NewReader(tt.input))
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantID != "" && got.ID != tt.wantID {
				t.Errorf("ID = %q, want %q", got.ID, tt.wantID)
			}
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

func TestLoadFile(t *testing.T) {
	got, err := LoadFile("testdata/items-crud.yaml")
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if got.ID != "items-crud" {
		t.Errorf("ID = %q, want %q", got.ID, "items-crud")
	}
	if got.Weight == nil || *got.Weight != 1.0 {
		t.Errorf("Weight = %v, want 1.0", got.Weight)
	}
}

func TestLoadFile_notFound(t *testing.T) {
	_, err := LoadFile("testdata/nonexistent.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadDir(t *testing.T) {
	scenarios, err := LoadDir("testdata")
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(scenarios) != 2 {
		t.Fatalf("len(scenarios) = %d, want 2", len(scenarios))
	}
	// Should be sorted by ID.
	if scenarios[0].ID != "items-crud" {
		t.Errorf("scenarios[0].ID = %q, want %q", scenarios[0].ID, "items-crud")
	}
	if scenarios[1].ID != "validation" {
		t.Errorf("scenarios[1].ID = %q, want %q", scenarios[1].ID, "validation")
	}
}

func TestLoadDir_notFound(t *testing.T) {
	_, err := LoadDir("testdata/nonexistent")
	if err == nil {
		t.Fatal("expected error for missing directory")
	}
}

func TestLoad_requestFields(t *testing.T) {
	got, err := LoadFile("testdata/validation.yaml")
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(got.Steps) < 1 {
		t.Fatal("expected at least one step")
	}
	req := got.Steps[0].Request
	if req == nil {
		t.Fatal("expected non-nil request on step 0")
	}
	if req.Method != "POST" {
		t.Errorf("Method = %q, want %q", req.Method, "POST")
	}
	if req.Path != "/items" {
		t.Errorf("Path = %q, want %q", req.Path, "/items")
	}
	if req.Headers["Content-Type"] != "application/json" {
		t.Errorf("Content-Type header = %q, want %q", req.Headers["Content-Type"], "application/json")
	}
	if req.Body == nil {
		t.Error("Body is nil, expected parsed YAML value")
	}
}

func TestComponentFieldRoundTrip(t *testing.T) {
	const yaml = `
id: models-crud
description: "test component field"
component: models
steps:
  - description: "check something"
    request:
      method: GET
      path: /items
    expect: "works"
`
	sc, err := Load(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if sc.Component != "models" {
		t.Errorf("Component = %q, want %q", sc.Component, "models")
	}

	// Empty component should also work.
	const yamlNoComponent = `
id: integration-test
description: "no component"
steps:
  - description: "check"
    request:
      method: GET
      path: /
    expect: "ok"
`
	sc2, err := Load(strings.NewReader(yamlNoComponent))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if sc2.Component != "" {
		t.Errorf("Component = %q, want empty", sc2.Component)
	}
}
