package lint

import (
	"slices"
	"testing"
)

func TestExtractVarRefs(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "no refs",
			input: "/items/123",
			want:  nil,
		},
		{
			name:  "single ref",
			input: "/items/{item_id}",
			want:  []string{"item_id"},
		},
		{
			name:  "multiple refs",
			input: "/items/{item_id}/sub/{sub_id}",
			want:  []string{"item_id", "sub_id"},
		},
		{
			name:  "ref in json body",
			input: `{"parent_id": "{parent_id}"}`,
			want:  []string{"parent_id"},
		},
		{
			name:  "not a var ref - numeric",
			input: "/items/{123}",
			want:  nil,
		},
		{
			name:  "underscore start",
			input: "{_private}",
			want:  []string{"_private"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractVarRefs(tt.input)
			if !slices.Equal(got, tt.want) {
				t.Errorf("extractVarRefs(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestValidateJSONPathSyntax(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"valid simple", "$.id", false},
		{"valid nested", "$.data.items", false},
		{"valid deep", "$.a.b.c.d", false},
		{"missing dollar-dot", "id", true},
		{"dollar only", "$", true},
		{"dollar-dot only", "$.", true},
		{"empty segment", "$.a..b", true},
		{"trailing dot", "$.a.", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateJSONPathSyntax(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateJSONPathSyntax(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
		})
	}
}

func TestCaptureSet(t *testing.T) {
	cs := newCaptureSet()
	if cs.has("foo") {
		t.Error("expected empty capture set to not have 'foo'")
	}

	cs.add("foo", "test.yaml", 10)
	if !cs.has("foo") {
		t.Error("expected capture set to have 'foo' after add")
	}

	info, ok := cs.info("foo")
	if !ok {
		t.Fatal("expected info to be found")
	}
	if info.file != "test.yaml" || info.line != 10 {
		t.Errorf("got info %+v, want {file: test.yaml, line: 10}", info)
	}
}

func TestCheckVarRefs(t *testing.T) {
	cs := newCaptureSet()
	cs.add("item_id", "test.yaml", 5)

	// Reference to captured var — no diagnostic.
	diags := checkVarRefs([]string{"item_id"}, cs, "test.yaml", 10)
	if len(diags) != 0 {
		t.Errorf("expected no diagnostics for captured var, got %v", diags)
	}

	// Reference to uncaptured var — warning.
	diags = checkVarRefs([]string{"missing_var"}, cs, "test.yaml", 10)
	if len(diags) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(diags))
	}
	if diags[0].Level != Warning {
		t.Errorf("expected Warning level, got %v", diags[0].Level)
	}

	// Duplicate refs should not produce duplicate diagnostics.
	diags = checkVarRefs([]string{"missing_var", "missing_var"}, cs, "test.yaml", 10)
	if len(diags) != 1 {
		t.Errorf("expected 1 diagnostic for duplicate refs, got %d", len(diags))
	}
}
