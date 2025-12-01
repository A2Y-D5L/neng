package agent_test

import (
	"context"
	"errors"
	"testing"

	"github.com/a2y-d5l/neng"
	"github.com/a2y-d5l/neng/agent"
)

func TestSubPlanExecutor_BasicExecution(t *testing.T) {
	results := agent.NewResults()

	subPlan, err := neng.BuildPlan(
		neng.Target{
			Name: "step1",
			Run: func(_ context.Context) error {
				agent.Store(results, "step1", "done")
				return nil
			},
		},
		neng.Target{
			Name: "step2",
			Deps: []string{"step1"},
			Run: func(_ context.Context) error {
				agent.Store(results, "step2", "done")
				return nil
			},
		},
	)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	subExec := &agent.SubPlanExecutor{Results: results} // FailFast is default
	_, runErr := subExec.Run(context.Background(), subPlan)

	if runErr != nil {
		t.Fatalf("expected success, got %v", runErr)
	}

	if v := agent.MustLoad[string](results, "step1"); v != "done" {
		t.Errorf("step1 not executed")
	}
	if v := agent.MustLoad[string](results, "step2"); v != "done" {
		t.Errorf("step2 not executed")
	}
}

func TestSubPlanExecutor_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	subPlan, err := neng.BuildPlan(
		neng.Target{
			Name: "step1",
			Run: func(_ context.Context) error {
				return nil // Should not reach here
			},
		},
	)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	subExec := &agent.SubPlanExecutor{} // FailFast is default
	_, runErr := subExec.Run(ctx, subPlan)

	if !errors.Is(runErr, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", runErr)
	}
}

func TestSubPlanExecutor_SharedResults(t *testing.T) {
	results := agent.NewResults()
	agent.Store(results, "input", "parent_value")

	subPlan, err := neng.BuildPlan(
		neng.Target{
			Name: "child",
			Run: func(_ context.Context) error {
				// Read from parent
				input := agent.MustLoad[string](results, "input")
				// Write for parent
				agent.Store(results, "output", input+"_processed")
				return nil
			},
		},
	)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	subExec := &agent.SubPlanExecutor{Results: results} // FailFast is default
	_, _ = subExec.Run(context.Background(), subPlan)

	output := agent.MustLoad[string](results, "output")
	if output != "parent_value_processed" {
		t.Errorf("expected 'parent_value_processed', got %q", output)
	}
}

func TestSubPlanExecutor_FailurePropagates(t *testing.T) {
	subPlan, err := neng.BuildPlan(
		neng.Target{
			Name: "failing",
			Run: func(_ context.Context) error {
				return errors.New("sub-plan failure")
			},
		},
	)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	subExec := &agent.SubPlanExecutor{} // FailFast is default
	_, runErr := subExec.Run(context.Background(), subPlan)

	if runErr == nil {
		t.Error("expected error from failing sub-plan")
	}
}

func TestSubPlanExecutor_WithRoots(t *testing.T) {
	results := agent.NewResults()

	// Build a plan with two branches
	subPlan, err := neng.BuildPlan(
		neng.Target{
			Name: "branch_a",
			Run: func(_ context.Context) error {
				agent.Store(results, "a", true)
				return nil
			},
		},
		neng.Target{
			Name: "branch_b",
			Run: func(_ context.Context) error {
				agent.Store(results, "b", true)
				return nil
			},
		},
	)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	subExec := &agent.SubPlanExecutor{Results: results}
	_, runErr := subExec.Run(context.Background(), subPlan, "branch_a")

	if runErr != nil {
		t.Fatalf("expected success, got %v", runErr)
	}

	// Only branch_a should have run
	if !results.Has("a") {
		t.Error("expected 'a' to be set")
	}
	if results.Has("b") {
		t.Error("expected 'b' to not be set (wasn't a root)")
	}
}

func TestSubPlanExecutor_DisableFailFast(t *testing.T) {
	results := agent.NewResults()

	// Two independent targets, one fails
	subPlan, err := neng.BuildPlan(
		neng.Target{
			Name: "fail",
			Run: func(_ context.Context) error {
				return errors.New("intentional failure")
			},
		},
		neng.Target{
			Name: "succeed",
			Run: func(_ context.Context) error {
				agent.Store(results, "success", true)
				return nil
			},
		},
	)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	subExec := &agent.SubPlanExecutor{
		Results:         results,
		DisableFailFast: true, // Allow all to complete
	}
	summary, _ := subExec.Run(context.Background(), subPlan)

	// Both should have completed (one with error)
	if summary.Results["fail"].Err == nil {
		t.Error("expected 'fail' to have error")
	}
	if !results.Has("success") {
		t.Error("expected 'success' to have run despite failure")
	}
}

func TestSubPlanExecutor_CustomWorkers(t *testing.T) {
	results := agent.NewResults()

	subPlan, err := neng.BuildPlan(
		neng.Target{
			Name: "a",
			Run: func(_ context.Context) error {
				agent.Store(results, "a", true)
				return nil
			},
		},
		neng.Target{
			Name: "b",
			Run: func(_ context.Context) error {
				agent.Store(results, "b", true)
				return nil
			},
		},
	)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	subExec := &agent.SubPlanExecutor{
		Results: results,
		Workers: 1, // Single worker
	}
	_, runErr := subExec.Run(context.Background(), subPlan)

	if runErr != nil {
		t.Fatalf("expected success, got %v", runErr)
	}

	if !results.Has("a") || !results.Has("b") {
		t.Error("expected both targets to complete")
	}
}

// Test event handler
type testEventHandler struct {
	events []neng.Event
}

func (h *testEventHandler) HandleEvent(ev neng.Event) {
	h.events = append(h.events, ev)
}

func TestSubPlanExecutor_RunWithEvents(t *testing.T) {
	results := agent.NewResults()
	handler := &testEventHandler{}

	subPlan, err := neng.BuildPlan(
		neng.Target{
			Name: "step1",
			Run: func(_ context.Context) error {
				return nil
			},
		},
	)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	subExec := &agent.SubPlanExecutor{Results: results}
	_, runErr := subExec.RunWithEvents(context.Background(), subPlan, handler)

	if runErr != nil {
		t.Fatalf("expected success, got %v", runErr)
	}

	// Should have received events
	if len(handler.events) == 0 {
		t.Error("expected to receive events")
	}
}

func TestSubPlanExecutor_RunWithEventsPrefix(t *testing.T) {
	results := agent.NewResults()
	handler := &testEventHandler{}

	subPlan, err := neng.BuildPlan(
		neng.Target{
			Name: "step1",
			Run: func(_ context.Context) error {
				return nil
			},
		},
	)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	subExec := &agent.SubPlanExecutor{
		Results: results,
		Prefix:  "subplan",
	}
	_, runErr := subExec.RunWithEvents(context.Background(), subPlan, handler)

	if runErr != nil {
		t.Fatalf("expected success, got %v", runErr)
	}

	// Events should have prefixed target names
	found := false
	for _, ev := range handler.events {
		if ev.Target != nil && ev.Target.Name == "subplan:step1" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected event with prefixed target name 'subplan:step1'")
	}
}

func TestSubPlanExecutor_NestedExecution(t *testing.T) {
	// Test that sub-plans can nest: parent -> child -> grandchild
	results := agent.NewResults()

	// Grandchild plan
	grandchildPlan, err := neng.BuildPlan(
		neng.Target{
			Name: "grandchild",
			Run: func(_ context.Context) error {
				agent.Store(results, "grandchild", "executed")
				return nil
			},
		},
	)
	if err != nil {
		t.Fatalf("BuildPlan grandchild: %v", err)
	}

	// Child plan that executes grandchild
	childPlan, err := neng.BuildPlan(
		neng.Target{
			Name: "child",
			Run: func(ctx context.Context) error {
				agent.Store(results, "child", "executed")
				subExec := &agent.SubPlanExecutor{Results: results}
				_, err := subExec.Run(ctx, grandchildPlan)
				return err
			},
		},
	)
	if err != nil {
		t.Fatalf("BuildPlan child: %v", err)
	}

	// Parent executes child
	subExec := &agent.SubPlanExecutor{Results: results}
	_, runErr := subExec.Run(context.Background(), childPlan)

	if runErr != nil {
		t.Fatalf("expected success, got %v", runErr)
	}

	if agent.MustLoad[string](results, "child") != "executed" {
		t.Error("child not executed")
	}
	if agent.MustLoad[string](results, "grandchild") != "executed" {
		t.Error("grandchild not executed")
	}
}
