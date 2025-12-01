package agent

import (
	"context"
	"fmt"

	"github.com/a2y-d5l/neng"
)

// SubPlanExecutor provides utilities for executing nested plans within targets.
//
// Sub-plans enable dynamic branching: a target can decide at runtime which
// plan to execute based on previous results. The sub-plan inherits the parent
// context for cancellation and shares the Results container via closures.
//
// Example:
//
//	results := agent.NewResults()
//
//	parentTarget := neng.Target{
//	    Name: "branch",
//	    Deps: []string{"analyze"},
//	    Run: func(ctx context.Context) error {
//	        analysis := agent.MustLoad[AnalysisResult](results, "analyze")
//
//	        var subPlan *neng.Plan
//	        if analysis.NeedsSearch {
//	            subPlan = buildSearchPlan(results)
//	        } else {
//	            subPlan = buildDirectAnswerPlan(results)
//	        }
//
//	        subExec := &agent.SubPlanExecutor{Results: results}
//	        _, err := subExec.Run(ctx, subPlan)
//	        return err
//	    },
//	}
type SubPlanExecutor struct {
	// Results is the shared container for passing data between targets.
	// Sub-plan targets can read/write to this same container.
	Results *Results

	// Prefix is an optional namespace prefix for result keys from sub-plans.
	// Useful when the same target names might exist in parent and sub-plan.
	// If set, sub-plan results are stored as "prefix:key".
	// If empty, results are stored with their original keys.
	Prefix string

	// DisableFailFast, when true, allows all branches to complete even if one fails.
	// By default (false), the sub-plan stops on first failure (failFast behavior).
	DisableFailFast bool

	// Workers sets the concurrency level for the sub-plan executor.
	// If zero, uses the neng default.
	Workers int
}

// Run executes a sub-plan, inheriting the parent context for cancellation.
//
// The sub-plan's targets should use closures to capture the same Results
// container, enabling data flow between parent and sub-plan targets.
//
// If roots is empty, the entire plan is executed.
//
// Returns the RunSummary from the sub-plan execution.
func (s *SubPlanExecutor) Run(
	ctx context.Context,
	plan *neng.Plan,
	roots ...string,
) (*neng.RunSummary, error) {
	// Build executor options
	var opts []neng.ExecutorOption

	// Default to failFast (stop on first failure) unless explicitly disabled
	if !s.DisableFailFast {
		opts = append(opts, neng.WithFailFast())
	}

	if s.Workers > 0 {
		opts = append(opts, neng.WithMaxWorkers(s.Workers))
	}

	exec := neng.NewExecutor(plan, opts...)

	// Drain event and result channels to prevent blocking
	go func() {
		for range exec.Events() {
		}
	}()
	go func() {
		for range exec.Results() {
		}
	}()

	summary, err := exec.Run(ctx, roots...)

	// Note: Results are shared via closure captures in targets,
	// not explicitly merged here. This is intentional—it's simpler
	// and avoids key collision issues.

	return &summary, err
}

// RunWithEvents executes a sub-plan and forwards events to a parent handler.
//
// Events from the sub-plan are forwarded to the provided handler with an
// optional prefix prepended to target names for disambiguation.
//
// This is useful when you want to observe sub-plan progress in the parent's
// event stream.
func (s *SubPlanExecutor) RunWithEvents(
	ctx context.Context,
	plan *neng.Plan,
	handler neng.EventHandler,
	roots ...string,
) (*neng.RunSummary, error) {
	var opts []neng.ExecutorOption

	// Default to failFast (stop on first failure) unless explicitly disabled
	if !s.DisableFailFast {
		opts = append(opts, neng.WithFailFast())
	}

	if s.Prefix != "" {
		// Wrap handler to prefix target names
		opts = append(opts, neng.WithEventSink(&prefixedHandler{
			inner:  handler,
			prefix: s.Prefix,
		}))
	} else {
		opts = append(opts, neng.WithEventSink(handler))
	}

	if s.Workers > 0 {
		opts = append(opts, neng.WithMaxWorkers(s.Workers))
	}

	exec := neng.NewExecutor(plan, opts...)

	// Still need to drain Results channel
	go func() {
		for range exec.Results() {
		}
	}()

	summary, err := exec.Run(ctx, roots...)
	return &summary, err
}

type prefixedHandler struct {
	inner  neng.EventHandler
	prefix string
}

func (p *prefixedHandler) HandleEvent(ev neng.Event) {
	if ev.Task != nil {
		// Create a copy with prefixed name
		prefixedTarget := *ev.Task
		prefixedTarget.Name = fmt.Sprintf("%s:%s", p.prefix, ev.Task.Name)
		ev.Task = &prefixedTarget
	}
	p.inner.HandleEvent(ev)
}
