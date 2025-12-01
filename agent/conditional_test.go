package agent_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/a2y-d5l/neng"
	"github.com/a2y-d5l/neng/agent"
)

func TestWithCondition_True(t *testing.T) {
	var ran bool
	target := neng.Task{
		Name: "test",
		Run: func(_ context.Context) error {
			ran = true
			return nil
		},
	}

	wrapped := agent.WithCondition(target, func() bool { return true })
	err := wrapped.Run(context.Background())

	if err != nil {
		t.Errorf("expected success, got %v", err)
	}
	if !ran {
		t.Error("expected target to run when condition is true")
	}
}

func TestWithCondition_False(t *testing.T) {
	var ran bool
	target := neng.Task{
		Name: "test",
		Run: func(_ context.Context) error {
			ran = true
			return nil
		},
	}

	wrapped := agent.WithCondition(target, func() bool { return false })
	err := wrapped.Run(context.Background())

	if err != nil {
		t.Errorf("expected success (skipped), got %v", err)
	}
	if ran {
		t.Error("expected target NOT to run when condition is false")
	}
}

func TestWithCondition_WithResults(t *testing.T) {
	results := agent.NewResults()
	agent.Store(results, "search_hits", []string{}) // Empty results

	var fallbackRan bool
	fallback := neng.Task{
		Name: "fallback",
		Run: func(_ context.Context) error {
			fallbackRan = true
			return nil
		},
	}

	wrapped := agent.WithCondition(fallback, func() bool {
		hits, ok := agent.Load[[]string](results, "search_hits")
		return !ok || len(hits) == 0
	})

	_ = wrapped.Run(context.Background())

	if !fallbackRan {
		t.Error("fallback should run when search_hits is empty")
	}

	// Now with non-empty results
	agent.Store(results, "search_hits", []string{"result1"})
	fallbackRan = false

	_ = wrapped.Run(context.Background())

	if fallbackRan {
		t.Error("fallback should NOT run when search_hits has results")
	}
}

func TestWithCondition_Composition(t *testing.T) {
	var attempts int32
	target := neng.Task{
		Name: "test",
		Run: func(_ context.Context) error {
			atomic.AddInt32(&attempts, 1)
			return errors.New("fail")
		},
	}

	var conditionChecks int32
	condition := func() bool {
		atomic.AddInt32(&conditionChecks, 1)
		return true
	}

	// Retry wrapping conditional
	wrapped := agent.WithRetry(
		agent.WithCondition(target, condition),
		agent.RetryPolicy{MaxRetries: 2},
	)

	_ = wrapped.Run(context.Background())

	// Condition checked on each retry
	if conditionChecks != 3 { // 1 initial + 2 retries
		t.Errorf("expected 3 condition checks, got %d", conditionChecks)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestWithCondition_PreservesTargetFields(t *testing.T) {
	target := neng.Task{
		Name: "original",
		Desc: "original description",
		Deps: []string{"dep1", "dep2"},
		Run:  func(_ context.Context) error { return nil },
	}

	wrapped := agent.WithCondition(target, func() bool { return true })

	if wrapped.Name != target.Name {
		t.Errorf("Name not preserved: %s vs %s", wrapped.Name, target.Name)
	}
	if wrapped.Desc != target.Desc {
		t.Errorf("Desc not preserved: %s vs %s", wrapped.Desc, target.Desc)
	}
	if len(wrapped.Deps) != len(target.Deps) {
		t.Errorf("Deps not preserved: %v vs %v", wrapped.Deps, target.Deps)
	}
}

func TestWithCondition_PropagatesError(t *testing.T) {
	expectedErr := errors.New("target error")
	target := neng.Task{
		Name: "test",
		Run: func(_ context.Context) error {
			return expectedErr
		},
	}

	wrapped := agent.WithCondition(target, func() bool { return true })
	err := wrapped.Run(context.Background())

	if !errors.Is(err, expectedErr) {
		t.Errorf("expected original error, got %v", err)
	}
}

func TestWithCondition_ContextPropagated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	var receivedCanceled bool
	target := neng.Task{
		Name: "test",
		Run: func(ctx context.Context) error {
			receivedCanceled = ctx.Err() != nil
			return ctx.Err()
		},
	}

	wrapped := agent.WithCondition(target, func() bool { return true })
	err := wrapped.Run(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if !receivedCanceled {
		t.Error("expected target to receive canceled context")
	}
}

func TestConditionFromResults_Found(t *testing.T) {
	results := agent.NewResults()
	agent.Store(results, "count", 10)

	condition := agent.ConditionFromResults(results, "count", func(v int) bool {
		return v > 5
	})

	if !condition() {
		t.Error("expected condition to return true for count > 5")
	}
}

func TestConditionFromResults_NotFound(t *testing.T) {
	results := agent.NewResults()

	condition := agent.ConditionFromResults(results, "missing", func(v int) bool {
		return v > 5
	})

	if condition() {
		t.Error("expected condition to return false for missing key")
	}
}

func TestConditionFromResults_WrongType(t *testing.T) {
	results := agent.NewResults()
	agent.Store(results, "key", "not an int")

	condition := agent.ConditionFromResults(results, "key", func(v int) bool {
		return v > 5
	})

	if condition() {
		t.Error("expected condition to return false for wrong type")
	}
}

func TestConditionFromResults_CheckReturnsFalse(t *testing.T) {
	results := agent.NewResults()
	agent.Store(results, "count", 3)

	condition := agent.ConditionFromResults(results, "count", func(v int) bool {
		return v > 5
	})

	if condition() {
		t.Error("expected condition to return false when check fails")
	}
}

func TestWithCondition_WithConditionFromResults(t *testing.T) {
	results := agent.NewResults()
	agent.Store(results, "primary_results", []string{}) // Empty

	var fallbackRan bool
	fallback := neng.Task{
		Name: "fallback",
		Run: func(_ context.Context) error {
			fallbackRan = true
			return nil
		},
	}

	// Use ConditionFromResults to run fallback when primary has no results
	wrapped := agent.WithCondition(
		fallback,
		agent.ConditionFromResults(results, "primary_results", func(hits []string) bool {
			return len(hits) == 0
		}),
	)

	_ = wrapped.Run(context.Background())

	if !fallbackRan {
		t.Error("fallback should run when primary_results is empty")
	}
}
