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

func TestTierInference(t *testing.T) {
	makeSteps := func(n int) []Step {
		steps := make([]Step, n)
		for i := range steps {
			steps[i] = Step{Request: &Request{Method: "GET", Path: "/items"}}
		}
		return steps
	}
	makeStepsWithCaptures := func(n, withCap int) []Step {
		steps := makeSteps(n)
		for i := range withCap {
			steps[i].Capture = []Capture{{Name: "x", JSONPath: "$.id"}}
		}
		return steps
	}
	makeStepOfType := func(stepType string) Step {
		switch stepType {
		case "browser":
			return Step{Browser: &BrowserRequest{Action: "navigate"}}
		case "grpc":
			return Step{GRPC: &GRPCRequest{Service: "svc", Method: "M"}}
		case "ws":
			return Step{WS: &WSRequest{URL: "/ws"}}
		case "exec":
			return Step{Exec: &ExecRequest{Command: "echo"}}
		case "tui":
			return Step{TUI: &TUIRequest{Command: "myapp"}}
		default:
			return Step{Request: &Request{Method: "GET", Path: "/"}}
		}
	}

	// Explicit tier round-trips through YAML without inference.
	t.Run("explicit tier round-trips", func(t *testing.T) {
		input := "id: t\ntier: 2\nsteps:\n  - request:\n      method: GET\n      path: /items\n    expect: ok\n"
		s, err := Load(strings.NewReader(input))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if s.Tier != 2 {
			t.Errorf("Tier = %d, want 2", s.Tier)
		}
	})

	tests := []struct {
		name     string
		scenario Scenario
		wantTier int
	}{
		{
			name:     "2 steps no captures -> tier 1",
			scenario: Scenario{ID: "t", Steps: makeSteps(2)},
			wantTier: 1,
		},
		{
			name:     "4 steps no captures -> tier 2",
			scenario: Scenario{ID: "t", Steps: makeSteps(4)},
			wantTier: 2,
		},
		{
			name:     "3 steps 1 capture -> tier 2",
			scenario: Scenario{ID: "t", Steps: makeStepsWithCaptures(3, 1)},
			wantTier: 2,
		},
		{
			name:     "8 steps no captures -> tier 3",
			scenario: Scenario{ID: "t", Steps: makeSteps(8)},
			wantTier: 3,
		},
		{
			name:     "3 steps 3 captures -> tier 3",
			scenario: Scenario{ID: "t", Steps: makeStepsWithCaptures(3, 3)},
			wantTier: 3,
		},
		// Weighted complexity cases.
		{
			// 2x browser: score = 2*(1+1) = 4 > 3 → tier 2
			name:     "2 browser steps no captures",
			scenario: Scenario{ID: "t", Steps: []Step{makeStepOfType("browser"), makeStepOfType("browser")}},
			wantTier: 2,
		},
		{
			// 1x grpc+stream: score = 1+1+1 = 3 ≤ 3 → tier 1
			name: "1 grpc streaming step no captures",
			scenario: Scenario{ID: "t", Steps: []Step{
				{GRPC: &GRPCRequest{Service: "svc", Method: "M", Stream: &GRPCStream{}}},
			}},
			wantTier: 1,
		},
		{
			// 2x grpc+stream, 1 capture: score = 2*(1+1+1) = 6 ≤ 6, captures=1 ≥ 1 → tier 2
			name: "2 grpc streaming steps 1 capture",
			scenario: Scenario{ID: "t", Steps: []Step{
				{GRPC: &GRPCRequest{Service: "svc", Method: "M", Stream: &GRPCStream{}}, Capture: []Capture{{Name: "x", JSONPath: "$.id"}}},
				{GRPC: &GRPCRequest{Service: "svc", Method: "M", Stream: &GRPCStream{}}},
			}},
			wantTier: 2,
		},
		{
			// 2x http with retry: score = 2*(1+1) = 4 > 3 → tier 2
			name: "2 steps with retry no captures",
			scenario: Scenario{ID: "t", Steps: []Step{
				{Request: &Request{Method: "GET", Path: "/"}, Retry: &Retry{Attempts: 3}},
				{Request: &Request{Method: "GET", Path: "/"}, Retry: &Retry{Attempts: 3}},
			}},
			wantTier: 2,
		},
		{
			// 1 http + 1 browser + 1 exec: base=3, browser extra=1, mixed bonus=2 → score=6 ≤ 6 → tier 2 (captures=0 but score>3)
			name: "3 mixed types no captures",
			scenario: Scenario{ID: "t", Steps: []Step{
				makeStepOfType("request"),
				makeStepOfType("browser"),
				makeStepOfType("exec"),
			}},
			wantTier: 2,
		},
		{
			// 1x ws with receive: score = 1+1+1 = 3 ≤ 3 → tier 1
			name: "1 ws with receive no captures",
			scenario: Scenario{ID: "t", Steps: []Step{
				{WS: &WSRequest{URL: "/ws", Receive: &WSReceive{Count: 1}}},
			}},
			wantTier: 1,
		},
		{
			// 2x tui: score = 2*(1+1) = 4 > 3 → tier 2 (same as browser/grpc/ws)
			name:     "2 tui steps no captures",
			scenario: Scenario{ID: "t", Steps: []Step{makeStepOfType("tui"), makeStepOfType("tui")}},
			wantTier: 2,
		},
		{
			// 4x http: score = 4 > 3 → tier 2
			name:     "4 http steps no captures",
			scenario: Scenario{ID: "t", Steps: makeSteps(4)},
			wantTier: 2,
		},
		{
			// 2x http: score = 2 ≤ 3 → tier 1
			name:     "2 http steps no captures",
			scenario: Scenario{ID: "t", Steps: makeSteps(2)},
			wantTier: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferTier(tt.scenario)
			if got != tt.wantTier {
				t.Errorf("inferTier() = %d, want %d", got, tt.wantTier)
			}
		})
	}

	// sampleYAML: 2 judged steps, setup-only capture -> tier 1
	t.Run("sampleYAML infers tier 1", func(t *testing.T) {
		s, err := Load(strings.NewReader(sampleYAML))
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if s.Tier != 1 {
			t.Errorf("Tier = %d, want 1", s.Tier)
		}
	})
}

func TestLoad_TUIStep(t *testing.T) {
	const input = `
id: tui-test
description: "Test TUI step loading"
steps:
  - description: "Launch app"
    tui:
      command: myapp
      send_key: "Enter"
      send_text: "hello"
      wait_for: "prompt"
      assert_screen: "ready"
      assert_absent: "error"
      timeout: 10s
    expect: "App launches and responds"
`
	s, err := Load(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s.Steps) != 1 {
		t.Fatalf("len(Steps) = %d, want 1", len(s.Steps))
	}
	step := s.Steps[0]
	if step.StepType() != "tui" {
		t.Errorf("StepType() = %q, want %q", step.StepType(), "tui")
	}
	tui := step.TUI
	if tui == nil {
		t.Fatal("TUI is nil")
	}
	if tui.Command != "myapp" {
		t.Errorf("Command = %q, want %q", tui.Command, "myapp")
	}
	if tui.SendKey != "Enter" {
		t.Errorf("SendKey = %q, want %q", tui.SendKey, "Enter")
	}
	if tui.SendText != "hello" {
		t.Errorf("SendText = %q, want %q", tui.SendText, "hello")
	}
	if tui.WaitFor != "prompt" {
		t.Errorf("WaitFor = %q, want %q", tui.WaitFor, "prompt")
	}
	if tui.AssertScreen != "ready" {
		t.Errorf("AssertScreen = %q, want %q", tui.AssertScreen, "ready")
	}
	if tui.AssertAbsent != "error" {
		t.Errorf("AssertAbsent = %q, want %q", tui.AssertAbsent, "error")
	}
	if tui.Timeout != "10s" {
		t.Errorf("Timeout = %q, want %q", tui.Timeout, "10s")
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
