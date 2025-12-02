package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/a2y-d5l/neng"
)

// ReactLoop executes iterative think→act→observe cycles until a termination
// condition is met or maximum iterations are exceeded.
//
// This implements the ReAct (Reasoning + Acting) pattern common in LLM agents:
// 1. Think: Analyze the current state and decide on an action
// 2. Act: Execute the chosen action
// 3. Observe: Process the action's results
// 4. Repeat until done
//
// Each iteration executes a fresh plan built by buildIterationPlan.
// The isDone function checks results after each iteration to determine
// if the loop should terminate.
//
// Example:
//
//	err := agent.ReactLoop(
//	    ctx,
//	    results,
//	    10, // max iterations
//	    func(iteration int, r *Results) *neng.Plan {
//	        plan, _ := neng.BuildPlan(
//	            neng.Target{Name: "think", Run: thinkFn(r, iteration)},
//	            neng.Target{Name: "act", Deps: []string{"think"}, Run: actFn(r)},
//	            neng.Target{Name: "observe", Deps: []string{"act"}, Run: observeFn(r)},
//	        )
//	        return plan
//	    },
//	    func(r *Results) bool {
//	        obs, _ := Load[Observation](r, "observation")
//	        return obs.IsFinal
//	    },
//	)
func ReactLoop(
	ctx context.Context,
	results *Results,
	maxIterations int,
	buildIterationPlan func(iteration int, results *Results) *neng.Plan,
	isDone func(results *Results) bool,
) error {
	for i := range maxIterations {
		// Check context before each iteration
		if err := ctx.Err(); err != nil {
			return err
		}

		// Build and execute iteration plan
		plan := buildIterationPlan(i, results)
		if plan == nil {
			return fmt.Errorf("iteration %d: buildIterationPlan returned nil", i)
		}

		exec := neng.NewExecutor(plan, neng.WithFailFast())
		go func() {
			for range exec.Events() {
			}
		}()
		go func() {
			for range exec.Results() {
			}
		}()

		if _, err := exec.Run(ctx); err != nil {
			return fmt.Errorf("iteration %d failed: %w", i, err)
		}

		// Check termination condition
		if isDone(results) {
			return nil
		}
	}

	return errors.New("max iterations exceeded without reaching termination condition")
}

// Phase represents a single execution phase in a multi-phase workflow.
type Phase struct {
	// Name identifies the phase for logging/debugging.
	Name string

	// Plan is the neng plan to execute for this phase.
	// The plan should be built before calling RunPhases.
	Plan *neng.Plan

	// Roots specifies which targets to execute. If empty, all targets run.
	Roots []string
}

// RunPhases executes phases in sequence, stopping on first failure.
//
// This pattern is useful when phases have sequential dependencies but
// each phase internally has parallelizable work.
//
// Example:
//
//	err := agent.RunPhases(ctx,
//	    Phase{Name: "retrieve", Plan: retrievePlan},
//	    Phase{Name: "process", Plan: processPlan},
//	    Phase{Name: "generate", Plan: generatePlan},
//	)
func RunPhases(ctx context.Context, phases ...Phase) error {
	for i, phase := range phases {
		if err := ctx.Err(); err != nil {
			return err
		}

		if phase.Plan == nil {
			return fmt.Errorf("phase %d (%s): nil plan", i, phase.Name)
		}

		exec := neng.NewExecutor(phase.Plan, neng.WithFailFast())
		go func() {
			for range exec.Events() {
			}
		}()
		go func() {
			for range exec.Results() {
			}
		}()

		if _, err := exec.Run(ctx, phase.Roots...); err != nil {
			return fmt.Errorf("phase %q failed: %w", phase.Name, err)
		}
	}
	return nil
}

// RunPhasesWithResults is like RunPhases but uses a shared Results container.
// This is a convenience wrapper that doesn't add functionality—phases share
// results via closure captures in their targets.
func RunPhasesWithResults(ctx context.Context, _ *Results, phases ...Phase) error {
	// Results sharing happens via closures in targets, not here
	return RunPhases(ctx, phases...)
}

// TargetDef defines a target template with an optional condition.
// Used with PlanFactory to build plans dynamically based on runtime state.
type TargetDef struct {
	// Name is the target name.
	Name string

	// Desc is an optional description.
	Desc string

	// Deps lists dependency target names.
	Deps []string

	// Run is the work function. It receives the shared Results for data access.
	Run func(ctx context.Context, results *Results) error

	// Condition, if non-nil, determines whether this target is included
	// in the built plan. If it returns false, the target is omitted.
	Condition func(results *Results) bool
}

// PlanFactory creates plans from target definitions, filtering by conditions.
//
// This enables building plans dynamically based on runtime state:
//   - Targets whose conditions return false are excluded
//   - Dependencies are filtered to only include present targets
//
// Example:
//
//	factory := agent.NewPlanFactory(results,
//	    TargetDef{Name: "search", Run: searchFn},
//	    TargetDef{
//	        Name: "fallback",
//	        Deps: []string{"search"},
//	        Condition: func(r *Results) bool {
//	            hits, _ := Load[[]Hit](r, "search_hits")
//	            return len(hits) == 0
//	        },
//	        Run: fallbackFn,
//	    },
//	)
//
//	plan, err := factory.Build("search", "fallback")
type PlanFactory struct {
	defs    map[string]TargetDef
	results *Results
}

// NewPlanFactory creates a factory with the given target definitions.
func NewPlanFactory(results *Results, defs ...TargetDef) *PlanFactory {
	defMap := make(map[string]TargetDef, len(defs))
	for _, d := range defs {
		defMap[d.Name] = d
	}
	return &PlanFactory{
		defs:    defMap,
		results: results,
	}
}

// Build creates a plan from the specified target names.
// Targets whose conditions return false are excluded.
// Dependencies are filtered to only include targets that passed conditions.
func (f *PlanFactory) Build(names ...string) (*neng.Plan, error) {
	// First pass: determine which targets pass their conditions
	included := make(map[string]bool)
	for _, name := range names {
		def, ok := f.defs[name]
		if !ok {
			return nil, fmt.Errorf("unknown target: %s", name)
		}
		if def.Condition == nil || def.Condition(f.results) {
			included[name] = true
		}
	}

	if len(included) == 0 {
		return nil, errors.New("no targets passed their conditions")
	}

	// Second pass: build targets with filtered dependencies
	targets := make([]neng.Task, 0, len(included))
	for name := range included {
		def := f.defs[name]

		// Filter deps to only included targets
		filteredDeps := make([]string, 0, len(def.Deps))
		for _, dep := range def.Deps {
			if included[dep] {
				filteredDeps = append(filteredDeps, dep)
			}
		}

		// Capture for closure
		results := f.results
		runFn := def.Run

		targets = append(targets, neng.Task{
			Name: def.Name,
			Desc: def.Desc,
			Deps: filteredDeps,
			Run: func(ctx context.Context) error {
				return runFn(ctx, results)
			},
		})
	}

	return neng.BuildPlan(targets...)
}

// RegisterTarget adds a target definition to the factory.
func (f *PlanFactory) RegisterTarget(def TargetDef) {
	f.defs[def.Name] = def
}
