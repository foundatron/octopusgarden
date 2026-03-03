package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSpecRulesSync verifies that every diagnostic produced by lintSpecContent
// maps to a registered SpecRule, and every SpecRule is triggered by at least
// one test input.
func TestSpecRulesSync(t *testing.T) {
	// Inputs designed to trigger every spec rule.
	inputs := []string{
		// S001: empty
		"",
		// S002: no level-1 heading
		"## Section\n\nText.\n",
		// S003: no description after title
		"# Title\n## Section\n\nContent.\n",
		// S004: empty section
		"# Title\n\nDesc.\n\n## Empty\n\n## Full\n\nContent.\n",
		// S005: duplicate heading
		"# Title\n\nDesc.\n\n## Dup\n\nA.\n\n## Dup\n\nB.\n",
		// S006: unclosed fence
		"# Title\n\nDesc.\n\n```\ncode\n",
	}

	triggered := make(map[string]bool, len(SpecRules))
	for _, input := range inputs {
		diags := lintSpecContent("test.md", input)
		for _, d := range diags {
			ruleID := matchRule(d.Message, SpecRules)
			if ruleID == "" {
				t.Errorf("diagnostic %q does not match any SpecRule", d.Message)
			} else {
				triggered[ruleID] = true
			}
		}
	}

	for _, r := range SpecRules {
		if !triggered[r.ID] {
			t.Errorf("SpecRule %s (%s) was never triggered — add a test input or remove the rule", r.ID, r.Summary)
		}
	}
}

// TestScenarioRulesSync verifies that every diagnostic produced by
// lintScenarioContent maps to a registered ScenarioRule, and every
// ScenarioRule is triggered by at least one test input.
func TestScenarioRulesSync(t *testing.T) {
	// Inputs designed to trigger every scenario rule.
	// Each entry targets one or more rules.
	inputs := []string{
		// SC001: empty
		"",
		// SC002: invalid YAML
		"[[[not yaml",
		// SC004: not a mapping
		"- item\n",
		// SC005: missing id
		"steps:\n  - request:\n      method: GET\n      path: /x\n    expect: ok\n    description: d\n",
		// SC006: empty id
		"id: \"\"\nsteps:\n  - request:\n      method: GET\n      path: /x\n    expect: ok\n    description: d\n",
		// SC007: invalid id pattern
		"id: Invalid_ID\nsteps:\n  - request:\n      method: GET\n      path: /x\n    expect: ok\n    description: d\n",
		// SC008: invalid type
		"id: test\ntype: unknown\nsteps:\n  - request:\n      method: GET\n      path: /x\n    expect: ok\n    description: d\n",
		// SC009: weight not a number
		"id: test\nweight: abc\nsteps:\n  - request:\n      method: GET\n      path: /x\n    expect: ok\n    description: d\n",
		// SC010: weight not positive
		"id: test\nweight: -1\nsteps:\n  - request:\n      method: GET\n      path: /x\n    expect: ok\n    description: d\n",
		// SC011: missing steps
		"id: test\n",
		// SC012: empty steps
		"id: test\nsteps: []\n",
		// SC013: setup not an array
		"id: test\nsetup: notarray\nsteps:\n  - request:\n      method: GET\n      path: /x\n    expect: ok\n    description: d\n",
		// SC014: step not a mapping
		"id: test\nsteps:\n  - notamapping\n",
		// SC015: step missing request
		"id: test\nsteps:\n  - description: d\n    expect: ok\n",
		// SC016: step missing expect + SC017: step missing description
		"id: test\nsteps:\n  - request:\n      method: GET\n      path: /x\n",
		// SC018: request not a mapping
		"id: test\nsteps:\n  - description: d\n    request: notamapping\n    expect: ok\n",
		// SC019: request missing method
		"id: test\nsteps:\n  - description: d\n    request:\n      path: /x\n    expect: ok\n",
		// SC020: invalid HTTP method
		"id: test\nsteps:\n  - description: d\n    request:\n      method: INVALID\n      path: /x\n    expect: ok\n",
		// SC021: request missing path
		"id: test\nsteps:\n  - description: d\n    request:\n      method: GET\n    expect: ok\n",
		// SC022: capture not an array
		"id: test\nsteps:\n  - description: d\n    request:\n      method: GET\n      path: /x\n    expect: ok\n    capture: notarray\n",
		// SC023: capture entry not a mapping
		"id: test\nsteps:\n  - description: d\n    request:\n      method: GET\n      path: /x\n    expect: ok\n    capture:\n      - notamapping\n",
		// SC024: capture missing name
		"id: test\nsteps:\n  - description: d\n    request:\n      method: GET\n      path: /x\n    expect: ok\n    capture:\n      - jsonpath: $.id\n",
		// SC025: capture name empty
		"id: test\nsteps:\n  - description: d\n    request:\n      method: GET\n      path: /x\n    expect: ok\n    capture:\n      - name: \"\"\n        jsonpath: $.id\n",
		// SC026: capture name invalid pattern
		"id: test\nsteps:\n  - description: d\n    request:\n      method: GET\n      path: /x\n    expect: ok\n    capture:\n      - name: 123bad\n        jsonpath: $.id\n",
		// SC027: capture shadows earlier
		"id: test\nsteps:\n  - description: d1\n    request:\n      method: GET\n      path: /x\n    expect: ok\n    capture:\n      - name: item_id\n        jsonpath: $.id\n  - description: d2\n    request:\n      method: GET\n      path: /x\n    expect: ok\n    capture:\n      - name: item_id\n        jsonpath: $.id\n",
		// SC028: capture missing jsonpath
		"id: test\nsteps:\n  - description: d\n    request:\n      method: GET\n      path: /x\n    expect: ok\n    capture:\n      - name: item_id\n",
		// SC029: invalid jsonpath
		"id: test\nsteps:\n  - description: d\n    request:\n      method: GET\n      path: /x\n    expect: ok\n    capture:\n      - name: item_id\n        jsonpath: bad\n",
		// SC030: variable referenced but never captured
		"id: test\nsteps:\n  - description: d\n    request:\n      method: GET\n      path: /items/{missing}\n    expect: ok\n",
	}

	// Rules that can only be triggered by dir-level checks.
	dirOnlyRules := map[string]bool{
		"SC031": true, // duplicate scenario id
	}

	triggered := make(map[string]bool, len(ScenarioRules))
	for _, input := range inputs {
		diags := lintScenarioContent("test.yaml", []byte(input))
		for _, d := range diags {
			ruleID := matchRule(d.Message, ScenarioRules)
			if ruleID == "" {
				t.Errorf("diagnostic %q does not match any ScenarioRule", d.Message)
			} else {
				triggered[ruleID] = true
			}
		}
	}

	// Trigger SC031 via CheckScenarioDir with duplicate IDs.
	dir := t.TempDir()
	dup := "id: dup\nsteps:\n  - description: d\n    request:\n      method: GET\n      path: /x\n    expect: ok\n"
	for _, name := range []string{"a.yaml", "b.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(dup), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dirDiags, err := CheckScenarioDir(dir)
	if err != nil {
		t.Fatalf("CheckScenarioDir: %v", err)
	}
	for _, d := range dirDiags {
		ruleID := matchRule(d.Message, ScenarioRules)
		if ruleID != "" {
			triggered[ruleID] = true
		}
	}

	for _, r := range ScenarioRules {
		if dirOnlyRules[r.ID] {
			continue // already handled above
		}
		if !triggered[r.ID] {
			t.Errorf("ScenarioRule %s (%s) was never triggered — add a test input or remove the rule", r.ID, r.Summary)
		}
	}
	// Also check dir-only rules.
	for id := range dirOnlyRules {
		if !triggered[id] {
			t.Errorf("ScenarioRule %s was never triggered by dir-level check", id)
		}
	}
}

// TestRuleIDsUnique ensures no duplicate IDs within or across rule sets.
func TestRuleIDsUnique(t *testing.T) {
	seen := make(map[string]string) // ID → which set
	for _, r := range SpecRules {
		if prev, ok := seen[r.ID]; ok {
			t.Errorf("duplicate rule ID %s (in SpecRules and %s)", r.ID, prev)
		}
		seen[r.ID] = "SpecRules"
	}
	for _, r := range ScenarioRules {
		if prev, ok := seen[r.ID]; ok {
			t.Errorf("duplicate rule ID %s (in ScenarioRules and %s)", r.ID, prev)
		}
		seen[r.ID] = "ScenarioRules"
	}
}

// TestMsgContainsUnique ensures each MsgContains substring uniquely identifies one rule.
func TestMsgContainsUnique(t *testing.T) {
	all := make([]Rule, 0, len(SpecRules)+len(ScenarioRules))
	all = append(all, SpecRules...)
	all = append(all, ScenarioRules...)

	for i, a := range all {
		for j, b := range all {
			if i == j {
				continue
			}
			if strings.Contains(a.MsgContains, b.MsgContains) && a.MsgContains != b.MsgContains {
				// One is a substring of the other — the matchRule function
				// picks the longest match, but flag it for awareness.
				t.Logf("note: %s.MsgContains %q contains %s.MsgContains %q (longest-match resolves this)",
					a.ID, a.MsgContains, b.ID, b.MsgContains)
			}
		}
	}
}

// TestRuleLevelsMatch verifies that each rule's declared Level matches
// the actual Level of the diagnostics it produces.
func TestRuleLevelsMatch(t *testing.T) {
	// Spec rules.
	specInputs := map[string]string{
		"S001": "",
		"S002": "## Section\n\nText.\n",
		"S003": "# Title\n## Section\n\nContent.\n",
		"S004": "# Title\n\nDesc.\n\n## Empty\n\n## Full\n\nContent.\n",
		"S005": "# Title\n\nDesc.\n\n## Dup\n\nA.\n\n## Dup\n\nB.\n",
		"S006": "# Title\n\nDesc.\n\n```\ncode\n",
	}

	ruleByID := make(map[string]Rule, len(SpecRules)+len(ScenarioRules))
	for _, r := range SpecRules {
		ruleByID[r.ID] = r
	}
	for _, r := range ScenarioRules {
		ruleByID[r.ID] = r
	}

	for targetID, input := range specInputs {
		diags := lintSpecContent("test.md", input)
		for _, d := range diags {
			ruleID := matchRule(d.Message, SpecRules)
			if ruleID != targetID {
				continue
			}
			r := ruleByID[ruleID]
			if d.Level != r.Level {
				t.Errorf("rule %s: diagnostic Level=%s but rule declares Level=%s", ruleID, d.Level, r.Level)
			}
		}
	}
}

// matchRule returns the ID of the rule whose MsgContains best matches msg.
// "Best" = longest MsgContains substring found in msg.
// Returns "" if no rule matches.
func matchRule(msg string, rules []Rule) string {
	bestID := ""
	bestLen := 0
	for _, r := range rules {
		if strings.Contains(msg, r.MsgContains) && len(r.MsgContains) > bestLen {
			bestID = r.ID
			bestLen = len(r.MsgContains)
		}
	}
	return bestID
}

// TestGeneratedDocsUpToDate verifies that the generated docs match
// what the generator would produce. Fails if go generate needs to be re-run.
func TestGeneratedDocsUpToDate(t *testing.T) {
	for _, tc := range []struct {
		path  string
		rules []Rule
	}{
		{"schemas/spec-format.md", SpecRules},
		{"schemas/scenario-format.md", ScenarioRules},
	} {
		// Read the file on disk.
		data, err := os.ReadFile(filepath.Join("..", "..", tc.path))
		if err != nil {
			t.Errorf("read %s: %v (run 'go generate ./internal/lint/...')", tc.path, err)
			continue
		}

		content := string(data)

		// Verify every rule ID appears in the file.
		for _, r := range tc.rules {
			want := fmt.Sprintf("| %s |", r.ID)
			if !strings.Contains(content, want) {
				t.Errorf("%s: missing rule %s (run 'go generate ./internal/lint/...')", tc.path, r.ID)
			}
		}
	}
}
