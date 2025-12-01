package agent_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/a2y-d5l/neng"
	"github.com/a2y-d5l/neng/agent"
)

func TestReactLoop_TerminatesOnCondition(t *testing.T) {
	results := agent.NewResults()
	var iterations int32

	err := agent.ReactLoop(
		context.Background(),
		results,
		10,
		func(_ int, r *agent.Results) *neng.Plan {
			plan, _ := neng.BuildPlan(
				neng.Target{
					Name: "iterate",
					Run: func(_ context.Context) error {
						n := atomic.AddInt32(&iterations, 1)
						agent.Store(r, "iteration", int(n))
						return nil
					},
				},
			)
			return plan
		},
		func(r *agent.Results) bool {
			n, _ := agent.Load[int](r, "iteration")
			return n >= 3 // Stop after 3 iterations
		},
	)

	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if iterations != 3 {
		t.Errorf("expected 3 iterations, got %d", iterations)
	}
}

func TestReactLoop_MaxIterationsExceeded(t *testing.T) {
	results := agent.NewResults()

	err := agent.ReactLoop(
		context.Background(),
		results,
		3, // Max 3 iterations
		func(_ int, _ *agent.Results) *neng.Plan {
			plan, _ := neng.BuildPlan(
				neng.Target{
					Name: "iterate",
					Run:  func(_ context.Context) error { return nil },
				},
			)
			return plan
		},
		func(_ *agent.Results) bool {
			return false // Never terminate
		},
	)

	if err == nil {
		t.Error("expected max iterations error")
	}
}

func TestReactLoop_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results := agent.NewResults()

	err := agent.ReactLoop(
		ctx,
		results,
		10,
		func(_ int, _ *agent.Results) *neng.Plan {
			plan, _ := neng.BuildPlan(
				neng.Target{Name: "iterate", Run: func(_ context.Context) error { return nil }},
			)
			return plan
		},
		func(_ *agent.Results) bool { return false },
	)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestReactLoop_IterationFailure(t *testing.T) {
	results := agent.NewResults()
	iterErr := errors.New("iteration failed")

	err := agent.ReactLoop(
		context.Background(),
		results,
		10,
		func(_ int, _ *agent.Results) *neng.Plan {
			plan, _ := neng.BuildPlan(
				neng.Target{
					Name: "failing",
					Run:  func(_ context.Context) error { return iterErr },
				},
			)
			return plan
		},
		func(_ *agent.Results) bool { return false },
	)

	if err == nil {
		t.Error("expected error from failing iteration")
	}
	if !errors.Is(err, iterErr) {
		t.Errorf("expected iterErr, got %v", err)
	}
}

func TestReactLoop_NilPlan(t *testing.T) {
	results := agent.NewResults()

	err := agent.ReactLoop(
		context.Background(),
		results,
		10,
		func(_ int, _ *agent.Results) *neng.Plan {
			return nil // Return nil plan
		},
		func(_ *agent.Results) bool { return false },
	)

	if err == nil {
		t.Error("expected error for nil plan")
	}
}

func TestReactLoop_ReceivesIterationNumber(t *testing.T) {
	results := agent.NewResults()
	var receivedIterations []int

	err := agent.ReactLoop(
		context.Background(),
		results,
		5,
		func(iteration int, r *agent.Results) *neng.Plan {
			receivedIterations = append(receivedIterations, iteration)
			plan, _ := neng.BuildPlan(
				neng.Target{
					Name: "iterate",
					Run: func(_ context.Context) error {
						agent.Store(r, "done", len(receivedIterations) >= 3)
						return nil
					},
				},
			)
			return plan
		},
		func(r *agent.Results) bool {
			done, _ := agent.Load[bool](r, "done")
			return done
		},
	)

	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	expected := []int{0, 1, 2}
	if len(receivedIterations) != len(expected) {
		t.Errorf("expected iterations %v, got %v", expected, receivedIterations)
	}
	for i, v := range receivedIterations {
		if v != expected[i] {
			t.Errorf("iteration %d: expected %d, got %d", i, expected[i], v)
		}
	}
}

func TestRunPhases_SequentialExecution(t *testing.T) {
	var order []string

	phase1, _ := neng.BuildPlan(
		neng.Target{
			Name: "p1",
			Run: func(_ context.Context) error {
				order = append(order, "p1")
				return nil
			},
		},
	)
	phase2, _ := neng.BuildPlan(
		neng.Target{
			Name: "p2",
			Run: func(_ context.Context) error {
				order = append(order, "p2")
				return nil
			},
		},
	)

	err := agent.RunPhases(context.Background(),
		agent.Phase{Name: "phase1", Plan: phase1},
		agent.Phase{Name: "phase2", Plan: phase2},
	)

	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(order) != 2 || order[0] != "p1" || order[1] != "p2" {
		t.Errorf("expected [p1, p2], got %v", order)
	}
}

func TestRunPhases_StopsOnFailure(t *testing.T) {
	var phase2Ran bool

	phase1, _ := neng.BuildPlan(
		neng.Target{
			Name: "failing",
			Run: func(_ context.Context) error {
				return errors.New("phase1 failed")
			},
		},
	)
	phase2, _ := neng.BuildPlan(
		neng.Target{
			Name: "p2",
			Run: func(_ context.Context) error {
				phase2Ran = true
				return nil
			},
		},
	)

	err := agent.RunPhases(context.Background(),
		agent.Phase{Name: "phase1", Plan: phase1},
		agent.Phase{Name: "phase2", Plan: phase2},
	)

	if err == nil {
		t.Error("expected error")
	}
	if phase2Ran {
		t.Error("phase2 should not have run")
	}
}

func TestRunPhases_NilPlan(t *testing.T) {
	err := agent.RunPhases(context.Background(),
		agent.Phase{Name: "nil_phase", Plan: nil},
	)

	if err == nil {
		t.Error("expected error for nil plan")
	}
}

func TestRunPhases_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	phase, _ := neng.BuildPlan(
		neng.Target{
			Name: "p",
			Run:  func(_ context.Context) error { return nil },
		},
	)

	err := agent.RunPhases(ctx,
		agent.Phase{Name: "phase", Plan: phase},
	)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestRunPhases_WithRoots(t *testing.T) {
	var ran []string

	phase, _ := neng.BuildPlan(
		neng.Target{
			Name: "a",
			Run: func(_ context.Context) error {
				ran = append(ran, "a")
				return nil
			},
		},
		neng.Target{
			Name: "b",
			Run: func(_ context.Context) error {
				ran = append(ran, "b")
				return nil
			},
		},
	)

	err := agent.RunPhases(context.Background(),
		agent.Phase{Name: "phase", Plan: phase, Roots: []string{"a"}},
	)

	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	// Only "a" should have run
	if len(ran) != 1 || ran[0] != "a" {
		t.Errorf("expected [a], got %v", ran)
	}
}

func TestRunPhasesWithResults(t *testing.T) {
	results := agent.NewResults()

	phase, _ := neng.BuildPlan(
		neng.Target{
			Name: "store",
			Run: func(_ context.Context) error {
				agent.Store(results, "key", "value")
				return nil
			},
		},
	)

	err := agent.RunPhasesWithResults(context.Background(), results,
		agent.Phase{Name: "phase", Plan: phase},
	)

	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	v, ok := agent.Load[string](results, "key")
	if !ok || v != "value" {
		t.Error("expected results to be stored via closure capture")
	}
}

func TestPlanFactory_BuildsValidPlan(t *testing.T) {
	results := agent.NewResults()

	factory := agent.NewPlanFactory(results,
		agent.TargetDef{
			Name: "a",
			Run: func(_ context.Context, r *agent.Results) error {
				agent.Store(r, "a", true)
				return nil
			},
		},
		agent.TargetDef{
			Name: "b",
			Deps: []string{"a"},
			Run: func(_ context.Context, r *agent.Results) error {
				agent.Store(r, "b", true)
				return nil
			},
		},
	)

	plan, err := factory.Build("a", "b")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
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

	_, runErr := exec.Run(context.Background())
	if runErr != nil {
		t.Fatalf("Run failed: %v", runErr)
	}

	if _, ok := agent.Load[bool](results, "a"); !ok {
		t.Error("target 'a' did not run")
	}
	if _, ok := agent.Load[bool](results, "b"); !ok {
		t.Error("target 'b' did not run")
	}
}

func TestPlanFactory_ConditionFiltering(t *testing.T) {
	results := agent.NewResults()
	agent.Store(results, "skip_b", true)

	factory := agent.NewPlanFactory(results,
		agent.TargetDef{
			Name: "a",
			Run:  func(_ context.Context, _ *agent.Results) error { return nil },
		},
		agent.TargetDef{
			Name: "b",
			Condition: func(r *agent.Results) bool {
				skip, _ := agent.Load[bool](r, "skip_b")
				return !skip // Don't include if skip_b is true
			},
			Run: func(_ context.Context, _ *agent.Results) error { return nil },
		},
	)

	plan, err := factory.Build("a", "b")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Plan should only have target "a" since "b" was filtered
	names := plan.TargetNames()
	if len(names) != 1 {
		t.Errorf("expected 1 target, got %d", len(names))
	}
	if names[0] != "a" {
		t.Errorf("expected target 'a', got %q", names[0])
	}
}

func TestPlanFactory_UnknownTarget(t *testing.T) {
	results := agent.NewResults()
	factory := agent.NewPlanFactory(results)

	_, err := factory.Build("nonexistent")
	if err == nil {
		t.Error("expected error for unknown target")
	}
}

func TestPlanFactory_AllConditionsFail(t *testing.T) {
	results := agent.NewResults()

	factory := agent.NewPlanFactory(results,
		agent.TargetDef{
			Name:      "a",
			Condition: func(_ *agent.Results) bool { return false },
			Run:       func(_ context.Context, _ *agent.Results) error { return nil },
		},
	)

	_, err := factory.Build("a")
	if err == nil {
		t.Error("expected error when all conditions fail")
	}
}

func TestPlanFactory_RegisterTarget(t *testing.T) {
	results := agent.NewResults()
	factory := agent.NewPlanFactory(results)

	// Register after creation
	factory.RegisterTarget(agent.TargetDef{
		Name: "late",
		Run: func(_ context.Context, r *agent.Results) error {
			agent.Store(r, "late", true)
			return nil
		},
	})

	plan, err := factory.Build("late")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
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

	_, _ = exec.Run(context.Background())

	if _, ok := agent.Load[bool](results, "late"); !ok {
		t.Error("late target did not run")
	}
}

func TestPlanFactory_DependencyFiltering(t *testing.T) {
	results := agent.NewResults()
	agent.Store(results, "skip_a", true)

	factory := agent.NewPlanFactory(results,
		agent.TargetDef{
			Name: "a",
			Condition: func(r *agent.Results) bool {
				skip, _ := agent.Load[bool](r, "skip_a")
				return !skip
			},
			Run: func(_ context.Context, _ *agent.Results) error { return nil },
		},
		agent.TargetDef{
			Name: "b",
			Deps: []string{"a"}, // Depends on 'a' which is filtered
			Run:  func(_ context.Context, _ *agent.Results) error { return nil },
		},
	)

	plan, err := factory.Build("a", "b")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// Plan should have only "b" with no deps (since "a" was filtered)
	names := plan.TargetNames()
	if len(names) != 1 || names[0] != "b" {
		t.Errorf("expected only 'b', got %v", names)
	}
}

func TestPlanFactory_PreservesDesc(t *testing.T) {
	results := agent.NewResults()

	factory := agent.NewPlanFactory(results,
		agent.TargetDef{
			Name: "a",
			Desc: "my description",
			Run:  func(_ context.Context, _ *agent.Results) error { return nil },
		},
	)

	plan, err := factory.Build("a")
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	target, ok := plan.Target("a")
	if !ok {
		t.Fatal("target 'a' not found")
	}
	if target.Desc != "my description" {
		t.Errorf("expected desc 'my description', got %q", target.Desc)
	}
}
