package cli_functions

import (
	"fmt"
	"strings"

	"github.com/vhPedroGitHub/tya/pkg/configyml"
)

// ValidateDependencyGraph validates the dependency graph defined by the flows'
// depends_on fields. It checks:
//
//  1. Every name listed in depends_on refers to an existing flow.
//  2. The graph is acyclic — no circular dependencies exist.
//
// Returns a descriptive error on the first violation found.
func ValidateDependencyGraph(flows []configyml.Flow) error {
	// Build a set of top-level flow names.
	names := make(map[string]struct{}, len(flows))
	for _, f := range flows {
		names[f.Name] = struct{}{}
	}

	// Build adjacency list and check that every referenced name exists.
	adj := make(map[string][]string, len(flows))
	for _, f := range flows {
		adj[f.Name] = f.DependsOn
		for _, dep := range f.DependsOn {
			if _, ok := names[dep]; !ok {
				return fmt.Errorf(
					"flow %q references unknown dependency %q in depends_on",
					f.Name, dep,
				)
			}
		}
	}

	// DFS cycle detection.
	// colour: 0 = unvisited, 1 = in current path (grey), 2 = fully explored (black)
	colour := make(map[string]int, len(flows))
	path := make([]string, 0, len(flows))

	var dfs func(node string) error
	dfs = func(node string) error {
		colour[node] = 1
		path = append(path, node)

		for _, dep := range adj[node] {
			switch colour[dep] {
			case 1:
				// dep is already on the current DFS path — cycle found.
				// Find where the cycle starts in the path slice.
				cycleStart := 0
				for i, n := range path {
					if n == dep {
						cycleStart = i
						break
					}
				}
				cycle := append(path[cycleStart:], dep)
				return fmt.Errorf("cycle detected: %s", strings.Join(cycle, " → "))
			case 0:
				if err := dfs(dep); err != nil {
					return err
				}
			}
			// colour 2 = already fully explored, safe to skip
		}

		path = path[:len(path)-1]
		colour[node] = 2
		return nil
	}

	for _, f := range flows {
		if colour[f.Name] == 0 {
			if err := dfs(f.Name); err != nil {
				return err
			}
		}
	}

	return nil
}

// TopologicalOrder returns flows sorted so that every flow appears after all
// of its dependencies. The input flows must have already passed
// ValidateDependencyGraph. Panics if an unexpected cycle is encountered
// (should never happen after validation).
func TopologicalOrder(flows []configyml.Flow) []configyml.Flow {
	// Kahn's algorithm.
	inDegree := make(map[string]int, len(flows))
	adj := make(map[string][]string, len(flows)) // dependency → dependents

	for _, f := range flows {
		if _, ok := inDegree[f.Name]; !ok {
			inDegree[f.Name] = 0
		}
		for _, dep := range f.DependsOn {
			adj[dep] = append(adj[dep], f.Name)
			inDegree[f.Name]++
		}
	}

	// Seed the queue with all zero-in-degree flows (in original order for stability).
	queue := make([]string, 0, len(flows))
	for _, f := range flows {
		if inDegree[f.Name] == 0 {
			queue = append(queue, f.Name)
		}
	}

	byName := make(map[string]configyml.Flow, len(flows))
	for _, f := range flows {
		byName[f.Name] = f
	}

	result := make([]configyml.Flow, 0, len(flows))
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		result = append(result, byName[node])
		for _, dependent := range adj[node] {
			inDegree[dependent]--
			if inDegree[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}

	if len(result) != len(flows) {
		// Should never happen after ValidateDependencyGraph passes.
		panic("topological sort produced fewer nodes than expected — undetected cycle")
	}

	return result
}
