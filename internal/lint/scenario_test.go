package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foundatron/octopusgarden/internal/scenario"
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
			wantMsg:    "exactly one of request, exec, browser, grpc, ws, or tui is required",
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
			name: "capture missing jsonpath and source",
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
			wantMsg:    "capture requires at least one of jsonpath or source",
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
			wantMsg:    "multiple step types",
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
		{
			name: "exec with stdin env timeout",
			yaml: `id: test
steps:
  - description: Full exec
    exec:
      command: myapp
      stdin: "input data"
      env:
        FOO: bar
        BAZ: qux
      timeout: 10s
    expect: "outputs data"
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "exec invalid timeout",
			yaml: `id: test
steps:
  - description: Bad timeout
    exec:
      command: echo hello
      timeout: notaduration
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "not a valid duration",
		},
		{
			name: "exec env not a mapping",
			yaml: `id: test
steps:
  - description: Bad env
    exec:
      command: echo hello
      env: "not a mapping"
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "exec env must be a mapping",
		},
		{
			name: "exec capture with valid source",
			yaml: `id: test
steps:
  - description: Capture stdout
    exec:
      command: echo hello
    expect: "ok"
    capture:
      - name: output
        source: stdout
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "exec capture with source and jsonpath",
			yaml: `id: test
steps:
  - description: Capture stdout json field
    exec:
      command: echo '{"id":"1"}'
    expect: "ok"
    capture:
      - name: item_id
        source: stdout
        jsonpath: $.id
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "exec capture with invalid source",
			yaml: `id: test
steps:
  - description: Bad source
    exec:
      command: echo hello
    expect: "ok"
    capture:
      - name: output
        source: invalid
`,
			wantErrors: 1,
			wantMsg:    "invalid source",
		},
		{
			name: "request capture with source error",
			yaml: `id: test
steps:
  - description: Source on request
    request:
      method: GET
      path: /items
    expect: "ok"
    capture:
      - name: output
        source: stdout
`,
			wantErrors: 1,
			wantMsg:    "source is not supported on request steps",
		},
		{
			name: "exec stdin var ref",
			yaml: `id: test
setup:
  - description: Get data
    request:
      method: GET
      path: /data
    capture:
      - name: data
        jsonpath: $.value
steps:
  - description: Pipe data
    exec:
      command: cat
      stdin: "{data}"
    expect: "ok"
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "exec env var ref uncaptured",
			yaml: `id: test
steps:
  - description: Env ref
    exec:
      command: echo $FOO
      env:
        FOO: "{missing}"
    expect: "ok"
`,
			wantErrors: 0,
			wantWarns:  1,
			wantMsg:    "never captured",
		},
		{
			name: "valid browser navigate step",
			yaml: `id: test
steps:
  - description: Open homepage
    browser:
      action: navigate
      url: /
    expect: "Page loads"
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "valid browser click step",
			yaml: `id: test
steps:
  - description: Click button
    browser:
      action: click
      selector: "#submit-btn"
    expect: "Button clicked"
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "valid browser fill step",
			yaml: `id: test
steps:
  - description: Fill form
    browser:
      action: fill
      selector: "#name-input"
      value: "Test User"
    expect: "Input filled"
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "valid browser assert step",
			yaml: `id: test
steps:
  - description: Check heading
    browser:
      action: assert
      selector: h1
      text: Welcome
    expect: "Heading present"
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "browser missing action",
			yaml: `id: test
steps:
  - description: Bad step
    browser: {}
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "browser missing required field: action",
		},
		{
			name: "browser invalid action",
			yaml: `id: test
steps:
  - description: Bad action
    browser:
      action: hover
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "invalid browser action",
		},
		{
			name: "browser navigate missing url",
			yaml: `id: test
steps:
  - description: Navigate nowhere
    browser:
      action: navigate
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "navigate action requires url",
		},
		{
			name: "browser click missing selector",
			yaml: `id: test
steps:
  - description: Click nothing
    browser:
      action: click
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "click action requires selector",
		},
		{
			name: "browser fill missing selector",
			yaml: `id: test
steps:
  - description: Fill nothing
    browser:
      action: fill
      value: test
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "fill action requires selector",
		},
		{
			name: "browser fill missing value",
			yaml: `id: test
steps:
  - description: Fill no value
    browser:
      action: fill
      selector: "#input"
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "fill action requires value",
		},
		{
			name: "browser assert missing selector",
			yaml: `id: test
steps:
  - description: Assert nothing
    browser:
      action: assert
      text: Hello
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "assert action requires selector",
		},
		{
			name: "browser assert no assertion fields warning",
			yaml: `id: test
steps:
  - description: Assert nothing specific
    browser:
      action: assert
      selector: h1
    expect: "ok"
`,
			wantErrors: 0,
			wantWarns:  1,
			wantMsg:    "no assertion fields",
		},
		{
			name: "browser invalid timeout",
			yaml: `id: test
steps:
  - description: Bad timeout
    browser:
      action: navigate
      url: /
      timeout: notaduration
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "browser timeout: invalid duration",
		},
		{
			name: "browser with variable substitution",
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
  - description: View item
    browser:
      action: navigate
      url: "/items/{item_id}"
    expect: "Shows item"
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "browser capture with valid source",
			yaml: `id: test
steps:
  - description: Get text
    browser:
      action: navigate
      url: /
    expect: "ok"
    capture:
      - name: page_text
        source: text
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "browser capture with invalid source",
			yaml: `id: test
steps:
  - description: Bad source
    browser:
      action: navigate
      url: /
    expect: "ok"
    capture:
      - name: output
        source: stdout
`,
			wantErrors: 1,
			wantMsg:    "invalid source",
		},
		{
			name: "browser and request on same step",
			yaml: `id: test
steps:
  - description: Ambiguous
    browser:
      action: navigate
      url: /
    request:
      method: GET
      path: /
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "multiple step types",
		},
		{
			name: "valid retry block",
			yaml: `id: test
steps:
  - description: Poll for result
    request:
      method: GET
      path: /items/1
    retry:
      attempts: 5
      interval: 2s
      timeout: 30s
    expect: "Status 200"
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "retry invalid interval",
			yaml: `id: test
steps:
  - description: Poll
    request:
      method: GET
      path: /items/1
    retry:
      attempts: 3
      interval: notaduration
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "retry interval",
		},
		{
			name: "retry invalid timeout",
			yaml: `id: test
steps:
  - description: Poll
    request:
      method: GET
      path: /items/1
    retry:
      attempts: 3
      timeout: notaduration
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "retry timeout",
		},
		{
			name: "retry negative attempts warning",
			yaml: `id: test
steps:
  - description: Poll
    request:
      method: GET
      path: /items/1
    retry:
      attempts: -1
    expect: "ok"
`,
			wantErrors: 0,
			wantWarns:  1,
			wantMsg:    "retry attempts should be at least 1",
		},
		{
			name: "retry not a mapping",
			yaml: `id: test
steps:
  - description: Poll
    request:
      method: GET
      path: /items/1
    retry: 5
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "retry must be a mapping",
		},
		{
			name: "retry empty block uses defaults",
			yaml: `id: test
steps:
  - description: Poll
    request:
      method: GET
      path: /items/1
    retry: {}
    expect: "ok"
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "browser and exec on same step",
			yaml: `id: test
steps:
  - description: Ambiguous
    browser:
      action: navigate
      url: /
    exec:
      command: echo hello
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "multiple step types",
		},
		{
			name: "valid exec with files",
			yaml: `id: test
steps:
  - description: Run with config
    exec:
      command: cat /tmp/config.yaml
      files:
        /tmp/config.yaml: "key: value"
    expect: "outputs config"
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "exec files relative path",
			yaml: `id: test
steps:
  - description: Bad file path
    exec:
      command: echo hello
      files:
        relative/path.txt: "content"
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "must be absolute",
		},
		{
			name: "exec files var ref in content",
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
  - description: Write config with token
    exec:
      command: cat /tmp/config
      files:
        /tmp/config: "auth: {token}"
    expect: "ok"
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "exec files var ref in path key",
			yaml: `id: test
setup:
  - description: Get dir
    request:
      method: GET
      path: /setup
    capture:
      - name: base_dir
        jsonpath: $.dir
steps:
  - description: Write to dynamic path
    exec:
      command: echo done
      files:
        /{base_dir}/config.yaml: "key: value"
    expect: "ok"
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "exec files uncaptured var ref in content",
			yaml: `id: test
steps:
  - description: Write file with missing var
    exec:
      command: echo done
      files:
        /tmp/config.yaml: "auth: {missing_token}"
    expect: "ok"
`,
			wantErrors: 0,
			wantWarns:  1,
			wantMsg:    "never captured",
		},
		{
			name: "exec files not a mapping",
			yaml: `id: test
steps:
  - description: Bad files field
    exec:
      command: echo hello
      files: "not a mapping"
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "exec files must be a mapping",
		},
		{
			name: "tier 0 is invalid",
			yaml: `id: test
tier: 0
steps:
  - description: A step
    request:
      method: GET
      path: /items
    expect: "ok"
`,
			wantErrors: 0,
			wantWarns:  1,
			wantMsg:    "tier must be between 1 and 3",
		},
		{
			name: "tier 4 is invalid",
			yaml: `id: test
tier: 4
steps:
  - description: A step
    request:
      method: GET
      path: /items
    expect: "ok"
`,
			wantErrors: 0,
			wantWarns:  1,
			wantMsg:    "tier must be between 1 and 3",
		},
		{
			name: "tier 2 is valid",
			yaml: `id: test
tier: 2
steps:
  - description: A step
    request:
      method: GET
      path: /items
    expect: "ok"
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "valid delay on step",
			yaml: `id: test
steps:
  - description: A step
    delay: "2s"
    request:
      method: GET
      path: /items
    expect: "ok"
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "invalid delay on step",
			yaml: `id: test
steps:
  - description: A step
    delay: "notaduration"
    request:
      method: GET
      path: /items
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    `delay "notaduration" is not a valid duration`,
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
	diags, err := CheckScenarioDir("../../examples/hello-api/scenarios")
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

func TestLintTUISteps(t *testing.T) {
	tests := []struct {
		name       string
		yaml       string
		wantErrors int
		wantWarns  int
		wantMsg    string
	}{
		{
			name: "valid tui launch step",
			yaml: `id: test
steps:
  - description: Launch app
    tui:
      command: myapp
    expect: "App launches"
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "valid tui interaction step no command",
			yaml: `id: test
steps:
  - description: Send key
    tui:
      send_key: "Enter"
    expect: "ok"
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "valid tui step all action fields",
			yaml: `id: test
steps:
  - description: Interact
    tui:
      send_text: "hello"
      wait_for: "prompt"
      assert_screen: "ready"
      assert_absent: "error"
      timeout: 10s
    expect: "ok"
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "tui step empty no command no actions",
			yaml: `id: test
steps:
  - description: Empty tui
    tui: {}
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "tui step requires command",
		},
		{
			name: "tui step empty command",
			yaml: `id: test
steps:
  - description: Bad command
    tui:
      command: ""
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "tui command must not be empty",
		},
		{
			name: "tui step invalid timeout",
			yaml: `id: test
steps:
  - description: Bad timeout
    tui:
      command: myapp
      timeout: notaduration
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "tui timeout: invalid duration",
		},
		{
			name: "tui and request on same step",
			yaml: `id: test
steps:
  - description: Ambiguous
    tui:
      command: myapp
    request:
      method: GET
      path: /items
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "multiple step types",
		},
		{
			name: "tui step with captured variable reference",
			yaml: `id: test
setup:
  - description: Get name
    request:
      method: GET
      path: /config
    capture:
      - name: app_name
        jsonpath: $.name
steps:
  - description: Launch with var
    tui:
      command: "{app_name}"
    expect: "ok"
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "tui step with uncaptured variable",
			yaml: `id: test
steps:
  - description: Launch missing var
    tui:
      command: "{missing_var}"
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
				t.Errorf("errors: got %d, want %d; diags: %v", errs, tt.wantErrors, diags)
			}
			if tt.wantWarns > 0 && warns != tt.wantWarns {
				t.Errorf("warnings: got %d, want %d", warns, tt.wantWarns)
			}
			if tt.wantMsg != "" {
				found := false
				for _, d := range diags {
					if strings.Contains(d.Message, tt.wantMsg) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected diagnostic containing %q, got: %v", tt.wantMsg, diags)
				}
			}
		})
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

// TestCaptureSourceMapContents verifies that CaptureSourceMap returns the
// expected entries from each executor's ValidCaptureSources method.
func TestCaptureSourceMapContents(t *testing.T) {
	m := scenario.CaptureSourceMap()

	// Exec sources.
	if exec, ok := m["exec"]; !ok {
		t.Fatal("CaptureSourceMap missing exec entry")
	} else {
		for _, s := range []string{"stdout", "stderr", "exitcode"} {
			if !exec[s] {
				t.Errorf("exec missing source %q", s)
			}
		}
	}

	// Browser sources.
	if browser, ok := m["browser"]; !ok {
		t.Fatal("CaptureSourceMap missing browser entry")
	} else {
		for _, s := range []string{"text", "html", "count", "location"} {
			if !browser[s] {
				t.Errorf("browser missing source %q", s)
			}
		}
	}

	// gRPC sources.
	if grpc, ok := m["grpc"]; !ok {
		t.Fatal("CaptureSourceMap missing grpc entry")
	} else {
		for _, s := range []string{"status", "headers"} {
			if !grpc[s] {
				t.Errorf("grpc missing source %q", s)
			}
		}
	}

	// HTTP (request) returns nil sources, so no entry.
	if _, ok := m["request"]; ok {
		t.Error("CaptureSourceMap should not have a 'request' entry; HTTP steps do not support source captures")
	}
}

func TestLintGRPCSteps(t *testing.T) {
	tests := []struct {
		name       string
		yaml       string
		wantErrors int
		wantWarns  int
		wantMsg    string
	}{
		{
			name: "valid grpc unary",
			yaml: `id: grpc-test
steps:
  - description: Register sensor
    grpc:
      service: telemetry.TelemetryService
      method: RegisterSensor
      body: '{"name": "sensor-1"}'
    expect: "Status OK"
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "grpc missing service",
			yaml: `id: grpc-test
steps:
  - description: Test
    grpc:
      method: RegisterSensor
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "grpc missing required field: service",
		},
		{
			name: "grpc missing method",
			yaml: `id: grpc-test
steps:
  - description: Test
    grpc:
      service: telemetry.Service
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "grpc missing required field: method",
		},
		{
			name: "grpc invalid timeout",
			yaml: `id: grpc-test
steps:
  - description: Test
    grpc:
      service: telemetry.Service
      method: Register
      timeout: notaduration
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "grpc timeout",
		},
		{
			name: "grpc with stream messages",
			yaml: `id: grpc-test
steps:
  - description: Stream upload
    grpc:
      service: telemetry.Service
      method: StreamReadings
      stream:
        messages:
          - '{"value": 1}'
          - '{"value": 2}'
    expect: "ok"
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "grpc stream collect by id (no service)",
			yaml: `id: grpc-test
steps:
  - description: Collect background
    grpc:
      stream:
        id: watch
        receive:
          timeout: 5s
          count: 1
    expect: "got message"
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "grpc capture source status",
			yaml: `id: grpc-test
steps:
  - description: Check status
    grpc:
      service: telemetry.Service
      method: Get
    expect: "ok"
    capture:
      - name: grpc_status
        source: status
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "grpc invalid receive timeout",
			yaml: `id: grpc-test
steps:
  - description: Watch
    grpc:
      service: telemetry.Service
      method: Watch
      stream:
        receive:
          timeout: badvalue
          count: 1
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "grpc receive timeout",
		},
		{
			name: "valid ws step with url",
			yaml: `id: test
steps:
  - description: Connect and receive
    ws:
      url: /ws/bids
      receive:
        timeout: 5s
        count: 1
    expect: "Received a bid"
`,
			wantErrors: 0,
			wantWarns:  0,
		},
		{
			name: "ws step missing url warns",
			yaml: `id: test
steps:
  - description: Receive only
    ws:
      receive:
        timeout: 1s
        count: 1
    expect: "ok"
`,
			wantErrors: 0,
			wantWarns:  1,
			wantMsg:    "ws step missing url",
		},
		{
			name: "ws receive timeout invalid",
			yaml: `id: test
steps:
  - description: Connect
    ws:
      url: /ws
      receive:
        timeout: notaduration
        count: 1
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "ws receive timeout: invalid duration",
		},
		{
			name: "ws receive count zero errors",
			yaml: `id: test
steps:
  - description: Connect
    ws:
      url: /ws
      receive:
        count: 0
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "ws receive count must be a positive integer",
		},
		{
			name: "ws step with multiple types errors",
			yaml: `id: test
steps:
  - description: Both ws and request
    ws:
      url: /ws
    request:
      method: GET
      path: /items
    expect: "ok"
`,
			wantErrors: 1,
			wantMsg:    "step has multiple step types",
		},
		{
			name: "ws send with undefined var ref warns",
			yaml: `id: test
steps:
  - description: Connect
    ws:
      url: /ws
      send: "{missing_var}"
    expect: "ok"
`,
			wantErrors: 0,
			wantWarns:  1,
			wantMsg:    "missing_var",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diags := lintScenarioContent("test.yaml", []byte(tt.yaml))
			errs, warns := CountByLevel(diags)
			if errs != tt.wantErrors {
				t.Errorf("errors: got %d, want %d; diags: %v", errs, tt.wantErrors, diags)
			}
			if tt.wantWarns > 0 && warns != tt.wantWarns {
				t.Errorf("warnings: got %d, want %d", warns, tt.wantWarns)
			}
			if tt.wantMsg != "" {
				found := false
				for _, d := range diags {
					if strings.Contains(d.Message, tt.wantMsg) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected diagnostic containing %q, got: %v", tt.wantMsg, diags)
				}
			}
		})
	}
}
