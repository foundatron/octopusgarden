package attractor

import (
	"errors"
	"testing"

	"github.com/foundatron/octopusgarden/internal/gene"
)

func TestTopoSort(t *testing.T) {
	tests := []struct {
		name       string
		components []gene.Component
		wantOrder  []string // expected name order (dependency-first)
		wantErr    error
	}{
		{
			name: "linear_chain",
			components: []gene.Component{
				{Name: "A", DependsOn: []string{"B"}},
				{Name: "B", DependsOn: []string{"C"}},
				{Name: "C"},
			},
			wantOrder: []string{"C", "B", "A"},
		},
		{
			name: "diamond",
			components: []gene.Component{
				{Name: "A", DependsOn: []string{"B", "C"}},
				{Name: "B", DependsOn: []string{"D"}},
				{Name: "C", DependsOn: []string{"D"}},
				{Name: "D"},
			},
			// D must come first; B and C before A; D before B and C.
			wantOrder: nil, // check constraints instead
		},
		{
			name: "no_dependencies",
			components: []gene.Component{
				{Name: "X"},
				{Name: "Y"},
				{Name: "Z"},
			},
			wantOrder: nil, // just check count
		},
		{
			name: "single_component",
			components: []gene.Component{
				{Name: "solo"},
			},
			wantOrder: []string{"solo"},
		},
		{
			name: "cycle_detection",
			components: []gene.Component{
				{Name: "A", DependsOn: []string{"B"}},
				{Name: "B", DependsOn: []string{"A"}},
			},
			wantErr: errTopoSortCycle,
		},
		{
			name: "unknown_dependency",
			components: []gene.Component{
				{Name: "A", DependsOn: []string{"missing"}},
			},
			wantErr: errUnknownDependency,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sorted, err := topoSort(tt.components)
			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !errors.Is(err, tt.wantErr) && err.Error() != tt.wantErr.Error() {
					t.Fatalf("expected error %q, got %q", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(sorted) != len(tt.components) {
				t.Fatalf("expected %d components, got %d", len(tt.components), len(sorted))
			}

			if tt.wantOrder != nil {
				for i, want := range tt.wantOrder {
					if sorted[i].Name != want {
						t.Errorf("position %d: want %q, got %q", i, want, sorted[i].Name)
					}
				}
				return
			}

			// For non-deterministic orders, verify topological constraint:
			// every component appears after all its dependencies.
			pos := make(map[string]int, len(sorted))
			for i, c := range sorted {
				pos[c.Name] = i
			}
			for _, c := range sorted {
				for _, dep := range c.DependsOn {
					if pos[dep] >= pos[c.Name] {
						t.Errorf("component %q (pos %d) appears before dependency %q (pos %d)", c.Name, pos[c.Name], dep, pos[dep])
					}
				}
			}
		})
	}
}
