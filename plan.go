package neng

import (
	"errors"
	"fmt"
	"sort"
)

// Plan is an immutable, compiled DAG of Targets.
//
// It can be reused across many runs with different contexts/root sets.
type Plan struct {
	// targets is the source-of-truth list, index -> Target.
	targets []*Target

	// indexByName lets us go from name -> index in O(1).
	indexByName map[string]int

	// deps is adjacency list of dependencies: i -> dep indices for i.
	deps [][]int

	// dependents is reverse adjacency: i -> dependent indices.
	dependents [][]int

	// topoOrder is a topological sort of all targets.
	topoOrder []int

	// stages groups node indices into "levels" that can run in parallel.
	stages [][]int
}

// BuildPlan compiles a slice of Targets into an immutable Plan.
//
// It validates:
//   - unique names
//   - all dependencies exist
//   - the graph is acyclic
//
// The returned Plan is safe for concurrent read-only use and may be reused
// across many executions.
func BuildPlan(targets ...Target) (*Plan, error) {
	if len(targets) == 0 {
		return nil, errors.New("build plan: no targets provided")
	}

	p := &Plan{
		targets:     make([]*Target, len(targets)),
		indexByName: make(map[string]int, len(targets)),
		deps:        make([][]int, len(targets)),
		dependents:  make([][]int, len(targets)),
	}

	// Copy targets into heap-allocated slice and build name index.
	for i := range targets {
		t := targets[i] // copy
		if t.Name == "" {
			return nil, fmt.Errorf("build plan: target at index %d has empty Name", i)
		}
		if _, exists := p.indexByName[t.Name]; exists {
			return nil, fmt.Errorf("build plan: duplicate target name %q", t.Name)
		}
		if t.Run == nil {
			return nil, fmt.Errorf("build plan: target %q has nil Run", t.Name)
		}
		p.indexByName[t.Name] = i
		p.targets[i] = &t
	}

	// Resolve dependencies by index.
	indegree := make([]int, len(targets))
	for i, t := range p.targets {
		if len(t.Deps) == 0 {
			continue
		}
		for _, depName := range t.Deps {
			depIndex, ok := p.indexByName[depName]
			if !ok {
				return nil, fmt.Errorf("build plan: target %q depends on unknown target %q", t.Name, depName)
			}
			// i depends on depIndex => edge depIndex -> i
			p.deps[i] = append(p.deps[i], depIndex)
			p.dependents[depIndex] = append(p.dependents[depIndex], i)
			indegree[i]++
		}
	}

	// Compute topoOrder and stages using Kahn's algorithm.
	// Note: topoSort operates on adjacency of "dependents" (edges u -> v for v that depend on u).
	topoOrder, stages, err := topoSort(indegree, p.dependents)
	if err != nil {
		return nil, err
	}
	p.topoOrder = topoOrder
	p.stages = stages

	return p, nil
}

// TargetNames returns all target names in deterministic order.
func (p *Plan) TargetNames() []string {
	names := make([]string, len(p.targets))
	for i, t := range p.targets {
		names[i] = t.Name
	}
	sort.Strings(names)
	return names
}

// Stages returns logical stages (levels) of the DAG.
//
// Each inner slice contains target names that may be run in parallel.
func (p *Plan) Stages() []Stage {
	stages := make([]Stage, len(p.stages))
	for i, layer := range p.stages {
		names := make([]string, len(layer))
		for j, idx := range layer {
			names[j] = p.targets[idx].Name
		}
		stages[i] = Stage{
			Index:   i,
			Targets: names,
		}
	}
	return stages
}

// Target returns the immutable Target definition by name.
func (p *Plan) Target(name string) (*Target, bool) {
	idx, ok := p.indexByName[name]
	if !ok {
		return nil, false
	}
	return p.targets[idx], true
}

// internal helpers

// topoSort performs a topological sort and groups nodes into levels.
//
// indegree is consumed and mutated. adj is adjacency of outgoing edges:
// u -> v for each v in adj[u].
func topoSort(indegree []int, adj [][]int) (topo []int, stages [][]int, err error) {
	n := len(indegree)
	queue := make([]int, 0, n)

	// Start with all nodes that have no incoming edges.
	for i, d := range indegree {
		if d == 0 {
			queue = append(queue, i)
		}
	}

	if len(queue) == 0 {
		return nil, nil, errors.New("build plan: graph has a cycle (no starting node)")
	}

	visited := 0

	for len(queue) > 0 {
		// Nodes in the current queue form one "stage".
		stage := make([]int, len(queue))
		copy(stage, queue)
		stages = append(stages, stage)

		nextQueue := make([]int, 0)

		for _, u := range stage {
			topo = append(topo, u)
			visited++

			// Remove edges u -> v.
			for _, v := range adj[u] {
				indegree[v]--
				if indegree[v] == 0 {
					nextQueue = append(nextQueue, v)
				}
			}
		}

		queue = nextQueue
	}

	if visited != n {
		return nil, nil, errors.New("build plan: graph has at least one cycle")
	}

	return topo, stages, nil
}
