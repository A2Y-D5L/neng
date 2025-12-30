package neng

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Plan is an immutable, compiled DAG of tasks.
//
// A Plan is safe for concurrent read-only use.
type Plan struct {
	tasks       []*taskDef
	indexByName map[string]int
	// deps: i -> indices of tasks that i depends on.
	deps [][]int
	// dependents: i -> indices of tasks that depend on i.
	dependents [][]int
	// topoOrder is a deterministic topological ordering of node indices.
	topoOrder []int
	// stages is a deterministic grouping of topo levels.
	stages [][]int
}

type taskDef struct {
	run  TaskFunc
	spec TaskSpec
}

// BuildPlan compiles tasks into an immutable Plan.
//
// It validates:
//   - at least one task is provided
//   - task names are non-empty and unique
//   - all dependency names exist
//   - the graph is acyclic
//
// BuildPlan deep-copies dependency slices, making the resulting Plan stable even
// if the caller mutates input Task values after the call returns.
func BuildPlan(tasks ...Task) (*Plan, error) {
	if len(tasks) == 0 {
		return nil, errors.New("execution: build plan: no tasks provided")
	}

	p := &Plan{
		tasks:       make([]*taskDef, len(tasks)),
		indexByName: make(map[string]int, len(tasks)),
		deps:        make([][]int, len(tasks)),
		dependents:  make([][]int, len(tasks)),
	}

	// Copy tasks, validate basic invariants, build index.
	for i := range tasks {
		in := tasks[i] // copy struct
		if strings.TrimSpace(in.Name) == "" {
			return nil, fmt.Errorf("execution: build plan: task at index %d has empty Name", i)
		}
		if _, exists := p.indexByName[in.Name]; exists {
			return nil, fmt.Errorf("execution: build plan: duplicate task name %q", in.Name)
		}
		if in.Run == nil {
			return nil, fmt.Errorf("execution: build plan: task %q has nil Run", in.Name)
		}

		// Deep-copy deps for immutability.
		depsCopy := append([]string(nil), in.Deps...)

		p.indexByName[in.Name] = i
		p.tasks[i] = &taskDef{
			spec: TaskSpec{
				Name: in.Name,
				Desc: in.Desc,
				Deps: depsCopy,
			},
			run: in.Run,
		}
	}

	// Resolve deps.
	indegree := make([]int, len(tasks))
	for i, t := range p.tasks {
		for _, depName := range t.spec.Deps {
			depIdx, ok := p.indexByName[depName]
			if !ok {
				return nil, fmt.Errorf(
					"execution: build plan: task %q depends on unknown task %q",
					t.spec.Name,
					depName,
				)
			}
			// edge depIdx -> i
			p.deps[i] = append(p.deps[i], depIdx)
			p.dependents[depIdx] = append(p.dependents[depIdx], i)
			indegree[i]++
		}
	}

	// Deterministic topo/stages.
	topo, stages, err := topoSortDeterministic(indegree, p.dependents, p.tasks)
	if err != nil {
		return nil, err
	}
	p.topoOrder = topo
	p.stages = stages

	return p, nil
}

// Spec returns an immutable task snapshot by name.
func (p *Plan) Spec(name string) (TaskSpec, bool) {
	idx, ok := p.indexByName[name]
	if !ok {
		return TaskSpec{}, false
	}
	return deepCopySpec(p.tasks[idx].spec), true
}

// TaskNames returns all task names in deterministic (sorted) order.
func (p *Plan) TaskNames() []string {
	names := make([]string, 0, len(p.tasks))
	for _, t := range p.tasks {
		names = append(names, t.spec.Name)
	}
	sort.Strings(names)
	return names
}

// Topo returns task names in a deterministic topological order.
func (p *Plan) Topo() []string {
	out := make([]string, 0, len(p.topoOrder))
	for _, idx := range p.topoOrder {
		out = append(out, p.tasks[idx].spec.Name)
	}
	return out
}

// Stages returns deterministic stage groupings.
//
// Task names within each stage are sorted.
func (p *Plan) Stages() []Stage {
	out := make([]Stage, 0, len(p.stages))
	for i, layer := range p.stages {
		names := make([]string, 0, len(layer))
		for _, idx := range layer {
			names = append(names, p.tasks[idx].spec.Name)
		}
		sort.Strings(names)
		out = append(out, Stage{Index: i, Tasks: names})
	}
	return out
}

func deepCopySpec(s TaskSpec) TaskSpec {
	return TaskSpec{
		Name: s.Name,
		Desc: s.Desc,
		Deps: append([]string(nil), s.Deps...),
	}
}

func topoSortDeterministic(
	indegree []int,
	adj [][]int,
	tasks []*taskDef,
) ([]int, [][]int, error) {
	n := len(indegree)
	if n == 0 {
		return nil, nil, errors.New("execution: build plan: empty graph")
	}

	queue := make([]int, 0, n)
	for i, d := range indegree {
		if d == 0 {
			queue = append(queue, i)
		}
	}
	if len(queue) == 0 {
		return nil, nil, errors.New("execution: build plan: graph has a cycle (no starting node)")
	}

	sortByName(queue, tasks)

	var topo []int
	var stages [][]int

	visited := 0
	for len(queue) > 0 {
		stage := append([]int(nil), queue...)
		stages = append(stages, stage)

		next := make([]int, 0)
		for _, u := range stage {
			topo = append(topo, u)
			visited++

			for _, v := range adj[u] {
				indegree[v]--
				if indegree[v] == 0 {
					next = append(next, v)
				}
			}
		}
		sortByName(next, tasks)
		queue = next
	}

	if visited != n {
		return nil, nil, errors.New("execution: build plan: graph has at least one cycle")
	}

	return topo, stages, nil
}

func sortByName(indices []int, tasks []*taskDef) {
	sort.Slice(indices, func(i, j int) bool {
		return tasks[indices[i]].spec.Name < tasks[indices[j]].spec.Name
	})
}

func (p *Plan) computeReachable(roots []string) ([]bool, int, error) {
	n := len(p.tasks)
	reachable := make([]bool, n)

	if len(roots) == 0 {
		for i := range reachable {
			reachable[i] = true
		}
		return reachable, n, nil
	}

	rootIdx := make([]int, 0, len(roots))
	for _, name := range roots {
		idx, ok := p.indexByName[name]
		if !ok {
			return nil, 0, fmt.Errorf("execution: execute: unknown root task %q", name)
		}
		rootIdx = append(rootIdx, idx)
	}

	seen := make([]bool, n)
	stack := make([]int, 0, n)

	for _, r := range rootIdx {
		if seen[r] {
			continue
		}
		stack = append(stack[:0], r)
		for len(stack) > 0 {
			u := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if seen[u] {
				continue
			}
			seen[u] = true
			reachable[u] = true
			for _, dep := range p.deps[u] {
				if !seen[dep] {
					stack = append(stack, dep)
				}
			}
		}
	}

	count := 0
	for _, ok := range reachable {
		if ok {
			count++
		}
	}
	return reachable, count, nil
}
