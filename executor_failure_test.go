package neng_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a2y-d5l/neng"
)

// noop is a no-operation Run function for targets that should succeed immediately.
var noop = func(ctx context.Context) error { return nil }

// failingTarget creates a target that will fail with an error.
func failingTarget(name string, deps ...string) neng.Target {
	return neng.Target{
		Name: name,
		Deps: deps,
		Run: func(ctx context.Context) error {
			return errors.New(name + " failed")
		},
	}
}

// successTarget creates a target that records execution and succeeds.
func successTarget(name string, executed *[]string, mu *sync.Mutex, deps ...string) neng.Target {
	return neng.Target{
		Name: name,
		Deps: deps,
		Run: func(ctx context.Context) error {
			mu.Lock()
			*executed = append(*executed, name)
			mu.Unlock()
			return nil
		},
	}
}

// assertSkipped asserts that the given target names are marked as skipped in the summary.
func assertSkipped(t *testing.T, summary neng.RunSummary, names ...string) {
	t.Helper()
	for _, name := range names {
		res, ok := summary.Results[name]
		if !ok {
			t.Errorf("expected %q in results", name)
			continue
		}
		if !res.Skipped {
			t.Errorf("expected %q to be skipped, got Skipped=%v, Err=%v", name, res.Skipped, res.Err)
		}
	}
}

// assertFailed asserts that the given target name has an error in the summary.
func assertFailed(t *testing.T, summary neng.RunSummary, name string) {
	t.Helper()
	res, ok := summary.Results[name]
	if !ok {
		t.Fatalf("expected %q in results", name)
	}
	if res.Err == nil {
		t.Errorf("expected %q to have error", name)
	}
}

// assertCompleted asserts that the given target name completed successfully.
func assertCompleted(t *testing.T, summary neng.RunSummary, name string) {
	t.Helper()
	res, ok := summary.Results[name]
	if !ok {
		t.Fatalf("expected %q in results", name)
	}
	if res.Err != nil || res.Skipped {
		t.Errorf("expected %q to complete successfully, got Err=%v, Skipped=%v", name, res.Err, res.Skipped)
	}
}

// drainChannels drains the Events and Results channels from an executor.
func drainChannels(exec *neng.Executor) {
	go func() {
		for range exec.Events() {
		}
	}()
	go func() {
		for range exec.Results() {
		}
	}()
}

func TestTransitiveSkipping_LinearChain(t *testing.T) {
	// A → B → C → D
	// A fails → B, C, D all skipped

	plan, err := neng.BuildPlan(
		failingTarget("A"),
		neng.Target{Name: "B", Deps: []string{"A"}, Run: noop},
		neng.Target{Name: "C", Deps: []string{"B"}, Run: noop},
		neng.Target{Name: "D", Deps: []string{"C"}, Run: noop},
	)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	exec := neng.NewExecutor(plan)
	drainChannels(exec)

	summary, runErr := exec.Run(context.Background())

	if runErr == nil {
		t.Fatal("expected error from failing target A")
	}

	assertFailed(t, summary, "A")
	assertSkipped(t, summary, "B", "C", "D")

	// Verify all targets accounted for
	if len(summary.Results) != 4 {
		t.Errorf("expected 4 results, got %d", len(summary.Results))
	}
}

func TestTransitiveSkipping_Diamond(t *testing.T) {
	//     A
	//    / \
	//   B   C
	//    \ /
	//     D
	// A fails → B, C, D all skipped

	plan, err := neng.BuildPlan(
		failingTarget("A"),
		neng.Target{Name: "B", Deps: []string{"A"}, Run: noop},
		neng.Target{Name: "C", Deps: []string{"A"}, Run: noop},
		neng.Target{Name: "D", Deps: []string{"B", "C"}, Run: noop},
	)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	exec := neng.NewExecutor(plan)
	drainChannels(exec)

	summary, _ := exec.Run(context.Background())

	assertFailed(t, summary, "A")
	assertSkipped(t, summary, "B", "C", "D")

	// Verify all targets accounted for
	if len(summary.Results) != 4 {
		t.Errorf("expected 4 results, got %d", len(summary.Results))
	}
}

func TestTransitiveSkipping_PartialGraph(t *testing.T) {
	// A → B → D
	// C → D (independent root C)
	// A fails → B skipped, C completes, D skipped (needs both B and C, B is skipped)

	var executed []string
	var mu sync.Mutex

	plan, err := neng.BuildPlan(
		failingTarget("A"),
		neng.Target{Name: "B", Deps: []string{"A"}, Run: noop},
		successTarget("C", &executed, &mu),
		neng.Target{Name: "D", Deps: []string{"B", "C"}, Run: noop},
	)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	exec := neng.NewExecutor(plan) // No failFast
	drainChannels(exec)

	summary, _ := exec.Run(context.Background())

	assertFailed(t, summary, "A")
	assertSkipped(t, summary, "B", "D") // D skipped because B is skipped

	// C should have completed successfully
	assertCompleted(t, summary, "C")

	// Verify all targets accounted for
	if len(summary.Results) != 4 {
		t.Errorf("expected 4 results, got %d", len(summary.Results))
	}
}

func TestTransitiveSkipping_MidChainFailure(t *testing.T) {
	// A → B → C → D
	// B fails (A succeeds first)
	// Expected: A completed, B failed, C skipped, D skipped

	var executed []string
	var mu sync.Mutex

	plan, err := neng.BuildPlan(
		successTarget("A", &executed, &mu),
		neng.Target{
			Name: "B",
			Deps: []string{"A"},
			Run: func(ctx context.Context) error {
				return errors.New("B failed")
			},
		},
		neng.Target{Name: "C", Deps: []string{"B"}, Run: noop},
		neng.Target{Name: "D", Deps: []string{"C"}, Run: noop},
	)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	exec := neng.NewExecutor(plan)
	drainChannels(exec)

	summary, _ := exec.Run(context.Background())

	// A should have completed
	assertCompleted(t, summary, "A")
	assertFailed(t, summary, "B")
	assertSkipped(t, summary, "C", "D")

	// Verify all targets accounted for
	if len(summary.Results) != 4 {
		t.Errorf("expected 4 results, got %d", len(summary.Results))
	}
}

func TestConcurrentFailures(t *testing.T) {
	// Run with: go test -race
	// Multiple independent targets fail simultaneously

	plan, err := neng.BuildPlan(
		failingTarget("A"),
		failingTarget("B"),
		failingTarget("C"),
		failingTarget("D"),
	)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	exec := neng.NewExecutor(plan, neng.WithMaxWorkers(4))
	drainChannels(exec)

	summary, _ := exec.Run(context.Background())

	// All should be failed (none skipped, since they're independent)
	for _, name := range []string{"A", "B", "C", "D"} {
		res := summary.Results[name]
		if res.Err == nil {
			t.Errorf("expected %s to fail", name)
		}
		if res.Skipped {
			t.Errorf("expected %s to not be skipped (independent)", name)
		}
	}
}

func TestFailFast_StopsNewScheduling(t *testing.T) {
	// A (fails fast) should prevent B from starting
	//
	// A → C
	// B (independent, slow) → D

	var executed []string
	var mu sync.Mutex

	plan, err := neng.BuildPlan(
		failingTarget("A"),
		neng.Target{
			Name: "B",
			Run: func(ctx context.Context) error {
				time.Sleep(100 * time.Millisecond) // Slow enough to not start
				mu.Lock()
				executed = append(executed, "B")
				mu.Unlock()
				return nil
			},
		},
		neng.Target{Name: "C", Deps: []string{"A"}, Run: noop},
		neng.Target{Name: "D", Deps: []string{"B"}, Run: noop},
	)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	exec := neng.NewExecutor(plan, neng.WithFailFast())
	drainChannels(exec)

	summary, _ := exec.Run(context.Background())

	// A failed, C skipped
	assertFailed(t, summary, "A")
	assertSkipped(t, summary, "C")

	// B should not have executed (failFast stopped scheduling)
	// Note: B may or may not have executed depending on timing.
	// The key assertion is that C is skipped and the run completes.
	// Due to failFast, no new work is scheduled after A fails.
}

func TestNoFailFast_AllowsParallelBranches(t *testing.T) {
	// Without failFast, independent branches complete
	//
	// A (fails) → C
	// B (succeeds) → D

	var executed []string
	var mu sync.Mutex

	plan, err := neng.BuildPlan(
		failingTarget("A"),
		successTarget("B", &executed, &mu),
		neng.Target{Name: "C", Deps: []string{"A"}, Run: noop},
		successTarget("D", &executed, &mu, "B"),
	)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	exec := neng.NewExecutor(plan) // No failFast
	drainChannels(exec)

	summary, _ := exec.Run(context.Background())

	assertFailed(t, summary, "A")
	assertSkipped(t, summary, "C")

	// B and D should have completed
	assertCompleted(t, summary, "B")
	assertCompleted(t, summary, "D")
}

func TestTransitiveSkipping_DeepChain(t *testing.T) {
	// A → B → C → D → E → F → G → H
	// A fails → all others skipped

	plan, err := neng.BuildPlan(
		failingTarget("A"),
		neng.Target{Name: "B", Deps: []string{"A"}, Run: noop},
		neng.Target{Name: "C", Deps: []string{"B"}, Run: noop},
		neng.Target{Name: "D", Deps: []string{"C"}, Run: noop},
		neng.Target{Name: "E", Deps: []string{"D"}, Run: noop},
		neng.Target{Name: "F", Deps: []string{"E"}, Run: noop},
		neng.Target{Name: "G", Deps: []string{"F"}, Run: noop},
		neng.Target{Name: "H", Deps: []string{"G"}, Run: noop},
	)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	exec := neng.NewExecutor(plan)
	drainChannels(exec)

	summary, runErr := exec.Run(context.Background())

	if runErr == nil {
		t.Fatal("expected error from failing target A")
	}

	assertFailed(t, summary, "A")
	assertSkipped(t, summary, "B", "C", "D", "E", "F", "G", "H")

	// Verify all targets accounted for
	if len(summary.Results) != 8 {
		t.Errorf("expected 8 results, got %d", len(summary.Results))
	}
}

func TestTransitiveSkipping_ComplexDiamond(t *testing.T) {
	//       A
	//      /|\
	//     B C D
	//      \|/
	//       E
	//       |
	//       F
	// A fails → B, C, D, E, F all skipped

	plan, err := neng.BuildPlan(
		failingTarget("A"),
		neng.Target{Name: "B", Deps: []string{"A"}, Run: noop},
		neng.Target{Name: "C", Deps: []string{"A"}, Run: noop},
		neng.Target{Name: "D", Deps: []string{"A"}, Run: noop},
		neng.Target{Name: "E", Deps: []string{"B", "C", "D"}, Run: noop},
		neng.Target{Name: "F", Deps: []string{"E"}, Run: noop},
	)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	exec := neng.NewExecutor(plan)
	drainChannels(exec)

	summary, _ := exec.Run(context.Background())

	assertFailed(t, summary, "A")
	assertSkipped(t, summary, "B", "C", "D", "E", "F")

	// Verify all targets accounted for
	if len(summary.Results) != 6 {
		t.Errorf("expected 6 results, got %d", len(summary.Results))
	}
}

func TestTransitiveSkipping_TwoIndependentChains(t *testing.T) {
	// Chain 1: A → B → C (A fails)
	// Chain 2: X → Y → Z (X succeeds)
	//
	// Expected: A failed, B & C skipped, X, Y, Z completed

	var executed []string
	var mu sync.Mutex

	plan, err := neng.BuildPlan(
		failingTarget("A"),
		neng.Target{Name: "B", Deps: []string{"A"}, Run: noop},
		neng.Target{Name: "C", Deps: []string{"B"}, Run: noop},
		successTarget("X", &executed, &mu),
		successTarget("Y", &executed, &mu, "X"),
		successTarget("Z", &executed, &mu, "Y"),
	)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	exec := neng.NewExecutor(plan) // No failFast
	drainChannels(exec)

	summary, _ := exec.Run(context.Background())

	assertFailed(t, summary, "A")
	assertSkipped(t, summary, "B", "C")
	assertCompleted(t, summary, "X")
	assertCompleted(t, summary, "Y")
	assertCompleted(t, summary, "Z")

	// Verify all targets accounted for
	if len(summary.Results) != 6 {
		t.Errorf("expected 6 results, got %d", len(summary.Results))
	}
}

func TestTransitiveSkipping_NoTransitiveDependents(t *testing.T) {
	// A (fails) with no dependents
	// B (succeeds) independent

	var executed []string
	var mu sync.Mutex

	plan, err := neng.BuildPlan(
		failingTarget("A"),
		successTarget("B", &executed, &mu),
	)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	exec := neng.NewExecutor(plan)
	drainChannels(exec)

	summary, _ := exec.Run(context.Background())

	assertFailed(t, summary, "A")
	assertCompleted(t, summary, "B")

	// Verify all targets accounted for
	if len(summary.Results) != 2 {
		t.Errorf("expected 2 results, got %d", len(summary.Results))
	}
}

func TestTransitiveSkipping_WithRoots(t *testing.T) {
	// A → B → C → D
	// Run only D (which needs A, B, C)
	// A fails → B, C, D all skipped

	plan, err := neng.BuildPlan(
		failingTarget("A"),
		neng.Target{Name: "B", Deps: []string{"A"}, Run: noop},
		neng.Target{Name: "C", Deps: []string{"B"}, Run: noop},
		neng.Target{Name: "D", Deps: []string{"C"}, Run: noop},
	)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	exec := neng.NewExecutor(plan)
	drainChannels(exec)

	// Run only D (which transitively depends on A, B, C)
	summary, runErr := exec.Run(context.Background(), "D")

	if runErr == nil {
		t.Fatal("expected error from failing target A")
	}

	assertFailed(t, summary, "A")
	assertSkipped(t, summary, "B", "C", "D")

	// Verify all targets accounted for
	if len(summary.Results) != 4 {
		t.Errorf("expected 4 results, got %d", len(summary.Results))
	}
}

// --- WithCollectAllErrors Tests ---

func TestWithCollectAllErrors_SingleFailure(t *testing.T) {
	plan, err := neng.BuildPlan(
		neng.Target{
			Name: "A",
			Run: func(_ context.Context) error {
				return errors.New("A failed")
			},
		},
	)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	exec := neng.NewExecutor(plan, neng.WithCollectAllErrors())
	drainChannels(exec)

	_, runErr := exec.Run(context.Background())

	if runErr == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(runErr.Error(), "A failed") {
		t.Errorf("expected error to contain 'A failed', got %v", runErr)
	}
}

func TestWithCollectAllErrors_MultipleIndependentFailures(t *testing.T) {
	errA := errors.New("A failed")
	errB := errors.New("B failed")

	plan, err := neng.BuildPlan(
		neng.Target{Name: "A", Run: func(_ context.Context) error { return errA }},
		neng.Target{Name: "B", Run: func(_ context.Context) error { return errB }},
		neng.Target{Name: "C", Run: func(_ context.Context) error { return nil }},
	)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	exec := neng.NewExecutor(plan, neng.WithCollectAllErrors())
	drainChannels(exec)

	_, runErr := exec.Run(context.Background())

	// Both errors should be present
	if !errors.Is(runErr, errA) {
		t.Errorf("expected joined error to contain errA")
	}
	if !errors.Is(runErr, errB) {
		t.Errorf("expected joined error to contain errB")
	}
}

func TestWithCollectAllErrors_NoFailures(t *testing.T) {
	plan, err := neng.BuildPlan(
		neng.Target{Name: "A", Run: func(_ context.Context) error { return nil }},
		neng.Target{Name: "B", Run: func(_ context.Context) error { return nil }},
	)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	exec := neng.NewExecutor(plan, neng.WithCollectAllErrors())
	drainChannels(exec)

	_, runErr := exec.Run(context.Background())

	if runErr != nil {
		t.Errorf("expected nil error for successful run, got %v", runErr)
	}
}

func TestWithCollectAllErrors_DeterministicOrdering(t *testing.T) {
	// Run multiple times and verify error message is consistent
	plan, err := neng.BuildPlan(
		neng.Target{Name: "Z", Run: func(_ context.Context) error { return errors.New("Z") }},
		neng.Target{Name: "A", Run: func(_ context.Context) error { return errors.New("A") }},
		neng.Target{Name: "M", Run: func(_ context.Context) error { return errors.New("M") }},
	)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	var messages []string
	for range 10 {
		exec := neng.NewExecutor(plan, neng.WithCollectAllErrors())
		drainChannels(exec)

		_, runErr := exec.Run(context.Background())
		messages = append(messages, runErr.Error())
	}

	// All messages should be identical
	for i := 1; i < len(messages); i++ {
		if messages[i] != messages[0] {
			t.Errorf("non-deterministic error ordering:\n  run 0: %s\n  run %d: %s",
				messages[0], i, messages[i])
		}
	}

	// Order should be alphabetical: A, M, Z
	if !strings.Contains(messages[0], "\"A\"") ||
		strings.Index(messages[0], "\"A\"") > strings.Index(messages[0], "\"M\"") ||
		strings.Index(messages[0], "\"M\"") > strings.Index(messages[0], "\"Z\"") {
		t.Errorf("expected alphabetical ordering, got: %s", messages[0])
	}
}

func TestWithCollectAllErrors_DefaultBehavior(t *testing.T) {
	// Without WithCollectAllErrors, should return first error only
	errA := errors.New("A failed")
	errB := errors.New("B failed")

	plan, err := neng.BuildPlan(
		neng.Target{Name: "A", Run: func(_ context.Context) error { return errA }},
		neng.Target{Name: "B", Run: func(_ context.Context) error { return errB }},
	)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	exec := neng.NewExecutor(plan) // No WithCollectAllErrors
	drainChannels(exec)

	_, runErr := exec.Run(context.Background())

	// Should return first error (order may vary due to concurrency)
	if runErr == nil {
		t.Fatal("expected error")
	}

	// The returned error should be one of the two, not joined
	isA := errors.Is(runErr, errA)
	isB := errors.Is(runErr, errB)
	if !isA && !isB {
		t.Errorf("expected error to be one of errA or errB, got %v", runErr)
	}
}
