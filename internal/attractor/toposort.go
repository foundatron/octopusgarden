package attractor

import (
	"errors"
	"fmt"

	"github.com/foundatron/octopusgarden/internal/gene"
)

var errTopoSortCycle = errors.New("attractor: dependency cycle detected")

// topoSort returns components in dependency-first order (leaves first) using Kahn's algorithm.
// Returns an error if the dependency graph contains a cycle or references an unknown component.
func topoSort(components []gene.Component) ([]gene.Component, error) {
	byName, inDegree, err := buildTopoGraph(components)
	if err != nil {
		return nil, err
	}
	return kahnSort(components, byName, inDegree)
}

// buildTopoGraph validates dependencies and computes in-degrees.
func buildTopoGraph(components []gene.Component) (map[string]gene.Component, map[string]int, error) {
	byName := make(map[string]gene.Component, len(components))
	inDegree := make(map[string]int, len(components))
	for _, c := range components {
		byName[c.Name] = c
		inDegree[c.Name] = 0
	}
	for _, c := range components {
		for _, dep := range c.DependsOn {
			if _, ok := byName[dep]; !ok {
				return nil, nil, fmt.Errorf("%w: %q depends on %q", errUnknownDependency, c.Name, dep)
			}
			inDegree[c.Name]++
		}
	}
	return byName, inDegree, nil
}

// kahnSort performs Kahn's BFS topological sort using precomputed in-degrees.
func kahnSort(components []gene.Component, byName map[string]gene.Component, inDegree map[string]int) ([]gene.Component, error) {
	// Build reverse adjacency: dep -> list of dependents.
	dependents := make(map[string][]string, len(components))
	for _, c := range components {
		for _, dep := range c.DependsOn {
			dependents[dep] = append(dependents[dep], c.Name)
		}
	}

	queue := make([]string, 0, len(components))
	for _, c := range components {
		if inDegree[c.Name] == 0 {
			queue = append(queue, c.Name)
		}
	}

	sorted := make([]gene.Component, 0, len(components))
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		sorted = append(sorted, byName[name])
		for _, dependent := range dependents[name] {
			inDegree[dependent]--
			if inDegree[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}

	if len(sorted) != len(components) {
		return nil, errTopoSortCycle
	}
	return sorted, nil
}
