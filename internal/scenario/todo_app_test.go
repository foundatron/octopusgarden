package scenario

import (
	"testing"
)

func TestLoadTodoAppScenarios(t *testing.T) {
	scenarios, err := LoadDir("../../scenarios/examples/todo-app")
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}

	if got := len(scenarios); got != 13 {
		t.Fatalf("expected 13 scenarios, got %d", got)
	}

	ids := make(map[string]bool)
	for _, s := range scenarios {
		if s.ID == "" {
			t.Error("scenario has empty ID")
		}
		if ids[s.ID] {
			t.Errorf("duplicate scenario ID: %s", s.ID)
		}
		ids[s.ID] = true

		if s.Description == "" {
			t.Errorf("scenario %s has empty description", s.ID)
		}
		if s.SatisfactionCriteria == "" {
			t.Errorf("scenario %s has empty satisfaction_criteria", s.ID)
		}
		if len(s.Steps) == 0 {
			t.Errorf("scenario %s has no steps", s.ID)
		}
		for i, step := range s.Steps {
			if step.Request == nil {
				t.Errorf("scenario %s step %d has nil request", s.ID, i)
				continue
			}
			if step.Request.Method == "" {
				t.Errorf("scenario %s step %d has empty method", s.ID, i)
			}
			if step.Request.Path == "" {
				t.Errorf("scenario %s step %d has empty path", s.ID, i)
			}
			if step.Expect == "" {
				t.Errorf("scenario %s step %d has empty expect", s.ID, i)
			}
		}
	}

	expectedIDs := []string{
		"register", "crud", "list", "filter-completed", "pagination",
		"ownership", "auth-required", "auth-invalid", "validation",
		"not-found", "register-validation", "register-duplicate", "mark-completed",
	}
	for _, id := range expectedIDs {
		if !ids[id] {
			t.Errorf("missing expected scenario ID: %s", id)
		}
	}
}
