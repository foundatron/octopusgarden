package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckScenarioContent(t *testing.T) {
	tests := []struct {
		name       string
		yaml       string
		wantErrors int
		wantWarns  int
		wantMsg    string // substring match on any diagnostic
	}{
		{
			name: "valid scenario",
			yaml: `id: test-scenario
description: A valid scenario
type: functional
steps:
  - description: Get items
    request:
      method: GET
      path: /items
    expect: "Status 200"
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name:       "empty file",
			yaml:       "",
			wantErrors: 1,
			wantMsg:    "empty",
		},
		{
			name: "missing id",
			yaml: `steps:
  - description: A step
    request:
      method: GET
      path: /items
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "missing required field: id",
		},
		{
			name: "invalid id pattern",
			yaml: `id: Invalid_ID
steps:
  - description: A step
    request:
      method: GET
      path: /items
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "must match pattern",
		},
		{
			name: "missing steps",
			yaml: `id: test
`,
			wantErrors: 1,
			wantMsg:    "missing required field: steps",
		},
		{
			name: "empty steps",
			yaml: `id: test
steps: []
`,
			wantErrors: 1,
			wantMsg:    "non-empty array",
		},
		{
			name: "missing step type",
			yaml: `id: test
steps:
  - description: A step
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "exactly one of request or exec is required",
		},
		{
			name: "missing method",
			yaml: `id: test
steps:
  - description: A step
    request:
      path: /items
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "request missing required field: method",
		},
		{
			name: "invalid method",
			yaml: `id: test
steps:
  - description: A step
    request:
      method: INVALID
      path: /items
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "invalid HTTP method",
		},
		{
			name: "missing path",
			yaml: `id: test
steps:
  - description: A step
    request:
      method: GET
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "request missing required field: path",
		},
		{
			name: "invalid type",
			yaml: `id: test
type: unknown
steps:
  - description: A step
    request:
      method: GET
      path: /items
    expect: "ok"
`,
			wantErrors: 0,
			wantWarns:  1,
			wantMsg:    "not one of",
		},
		{
			name: "negative weight",
			yaml: `id: test
weight: -1.0
steps:
  - description: A step
    request:
      method: GET
      path: /items
    expect: "ok"
`,
			wantErrors: 0,
			wantWarns:  1,
			wantMsg:    "weight must be positive",
		},
		{
			name: "missing expect warning",
			yaml: `id: test
steps:
  - description: A step
    request:
      method: GET
      path: /items
`,
			wantErrors: 0,
			wantWarns:  1,
			wantMsg:    "missing expect",
		},
		{
			name: "missing description warning",
			yaml: `id: test
steps:
  - request:
      method: GET
      path: /items
    expect: "ok"
`,
			wantErrors: 0,
			wantWarns:  1,
			wantMsg:    "missing description",
		},
		{
			name: "capture missing name",
			yaml: `id: test
steps:
  - description: A step
    request:
      method: GET
      path: /items
    expect: "ok"
    capture:
      - jsonpath: $.id
`,
			wantErrors: 1,
			wantMsg:    "capture missing required field: name",
		},
		{
			name: "capture missing jsonpath",
			yaml: `id: test
steps:
  - description: A step
    request:
      method: GET
      path: /items
    expect: "ok"
    capture:
      - name: item_id
`,
			wantErrors: 1,
			wantMsg:    "capture missing required field: jsonpath",
		},
		{
			name: "capture invalid name",
			yaml: `id: test
steps:
  - description: A step
    request:
      method: GET
      path: /items
    expect: "ok"
    capture:
      - name: "123bad"
        jsonpath: $.id
`,
			wantErrors: 1,
			wantMsg:    "capture name",
		},
		{
			name: "capture invalid jsonpath",
			yaml: `id: test
steps:
  - description: A step
    request:
      method: GET
      path: /items
    expect: "ok"
    capture:
      - name: item_id
        jsonpath: id
`,
			wantErrors: 1,
			wantMsg:    "invalid jsonpath",
		},
		{
			name: "variable referenced but not captured",
			yaml: `id: test
steps:
  - description: Get item
    request:
      method: GET
      path: /items/{item_id}
    expect: "ok"
`,
			wantErrors: 0,
			wantWarns:  1,
			wantMsg:    "never captured",
		},
		{
			name: "variable captured in setup used in steps",
			yaml: `id: test
setup:
  - description: Create item
    request:
      method: POST
      path: /items
    capture:
      - name: item_id
        jsonpath: $.id
steps:
  - description: Get item
    request:
      method: GET
      path: /items/{item_id}
    expect: "ok"
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "capture shadows earlier",
			yaml: `id: test
setup:
  - description: Create first
    request:
      method: POST
      path: /items
    capture:
      - name: item_id
        jsonpath: $.id
steps:
  - description: Create second
    request:
      method: POST
      path: /items
    expect: "ok"
    capture:
      - name: item_id
        jsonpath: $.id
`,
			wantErrors: 0,
			wantWarns:  1,
			wantMsg:    "shadows",
		},
		{
			name: "valid setup step no expect warning",
			yaml: `id: test
setup:
  - description: Create item
    request:
      method: POST
      path: /items
    capture:
      - name: item_id
        jsonpath: $.id
steps:
  - description: Get item
    request:
      method: GET
      path: /items/{item_id}
    expect: "ok"
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "valid exec step",
			yaml: `id: test
steps:
  - description: Run command
    exec:
      command: echo hello
    expect: "outputs hello"
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "exec missing command field",
			yaml: `id: test
steps:
  - description: Run command
    exec: {}
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "exec missing required field: command",
		},
		{
			name: "exec empty command",
			yaml: `id: test
steps:
  - description: Run command
    exec:
      command: ""
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "exec command must not be empty",
		},
		{
			name: "both request and exec",
			yaml: `id: test
steps:
  - description: Ambiguous step
    request:
      method: GET
      path: /items
    exec:
      command: echo hello
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "both request and exec",
		},
		{
			name: "exec with variable reference",
			yaml: `id: test
setup:
  - description: Get token
    request:
      method: POST
      path: /login
    capture:
      - name: token
        jsonpath: $.token
steps:
  - description: Use token
    exec:
      command: "curl -H 'Authorization: {token}' http://localhost/api"
    expect: "ok"
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "exec with uncaptured variable",
			yaml: `id: test
steps:
  - description: Use variable
    exec:
      command: echo {missing_var}
    expect: "ok"
`,
			wantErrors: 0,
			wantWarns:  1,
			wantMsg:    "never captured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diags := lintScenarioContent("test.yaml", []byte(tt.yaml))
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

func TestCheckScenarioDir(t *testing.T) {
	// Test with the actual example scenarios.
	diags, err := CheckScenarioDir("../../scenarios/examples/hello-api")
	if err != nil {
		t.Fatalf("CheckScenarioDir: %v", err)
	}
	if HasErrors(diags) {
		t.Errorf("example scenarios should have no errors; got: %v", diags)
	}
}

func TestCheckScenarioDirDuplicateIDs(t *testing.T) {
	dir := t.TempDir()

	content1 := `id: duplicate
steps:
  - description: Step 1
    request:
      method: GET
      path: /items
    expect: "ok"
`
	content2 := `id: duplicate
steps:
  - description: Step 2
    request:
      method: GET
      path: /other
    expect: "ok"
`

	if err := os.WriteFile(filepath.Join(dir, "a.yaml"), []byte(content1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.yaml"), []byte(content2), 0o644); err != nil {
		t.Fatal(err)
	}

	diags, err := CheckScenarioDir(dir)
	if err != nil {
		t.Fatalf("CheckScenarioDir: %v", err)
	}

	if !HasErrors(diags) {
		t.Error("expected errors for duplicate IDs")
	}

	found := false
	for _, d := range diags {
		if strings.Contains(d.Message, "duplicate scenario id") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected duplicate ID diagnostic; got: %v", diags)
	}
}

func TestCheckScenarioDirNotFound(t *testing.T) {
	_, err := CheckScenarioDir("/nonexistent/path")
	if err == nil {
		t.Fatal("expected error for nonexistent directory")
	}
}

func TestCheckScenarioFileNotFound(t *testing.T) {
	_, err := CheckScenario("nonexistent.yaml")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}
