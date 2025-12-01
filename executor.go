package neng

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"sync"
	"time"
)

// ExecutorOption configures an Executor at construction time.
type ExecutorOption func(*Executor)

// WithMaxWorkers sets the maximum number of concurrent workers.
//
// A value <= 0 will be normalized to runtime.NumCPU().
func WithMaxWorkers(n int) ExecutorOption {
	return func(e *Executor) {
		e.maxWorkers = n
	}
}

// WithFailFast stops scheduling new work after the first failure.
//
// Already-running tasks are allowed to complete; all remaining pending
// tasks that depend (directly or transitively) on a failed task are
// marked as skipped.
func WithFailFast() ExecutorOption {
	return func(e *Executor) {
		e.failFast = true
	}
}

// WithEventSink attaches a custom sink to receive all events.
//
// This allows callers to plug in TUIs, JSON loggers, or test recorders.
func WithEventSink(sink EventHandler) ExecutorOption {
	return func(e *Executor) {
		e.sink = sink
	}
}

// WithCollectAllErrors configures the executor to return all task errors
// as a combined error using errors.Join, rather than just the first error.
//
// This is useful when multiple independent branches can fail, and you want
// to see all failures rather than just the first one encountered.
//
// The errors are joined in deterministic order (sorted by task name)
// to ensure reproducible error messages for testing.
//
// Example:
//
//	exec := NewExecutor(plan, WithCollectAllErrors())
//	_, err := exec.Run(ctx)
//	// err contains all failures: "task \"A\": ...\ntask \"B\": ..."
func WithCollectAllErrors() ExecutorOption {
	return func(e *Executor) {
		e.collectAllErrors = true
	}
}

// RunSummary is the top-level result of an Executor run.
type RunSummary struct {
	// Results contains a Result for every *reachable* task.
	Results map[string]Result

	// Failed is true if at least one reachable task failed.
	Failed bool
}

// Executor is a per-run execution engine for a compiled Plan.
//
// It is NOT safe to copy. Use a new Executor per Run.
type Executor struct {
	plan *Plan

	maxWorkers int
	// logf       Logger
	failFast bool

	// collectAllErrors, when true, causes Run to return all task errors
	// joined via errors.Join instead of just the first error.
	collectAllErrors bool

	// event and result streams for observers
	events  chan Event
	results chan Result
	sink    EventHandler

	// internal per-run state
	mu       sync.Mutex // protects firstErr only
	firstErr error
}

// NewExecutor creates a new Executor for the given Plan.
//
// It is cheap compared to building the Plan.
func NewExecutor(plan *Plan, opts ...ExecutorOption) *Executor {
	e := &Executor{
		plan:       plan,
		maxWorkers: runtime.NumCPU(),

		// small default buffers; callers should consume promptly, but
		// emit() is non-blocking to avoid deadlocks.
		events:  make(chan Event, 128),
		results: make(chan Result, 128),
	}
	for _, opt := range opts {
		opt(e)
	}
	if e.maxWorkers <= 0 {
		e.maxWorkers = runtime.NumCPU()
	}
	return e
}

// Events returns a read-only channel of task lifecycle events.
//
// The channel is closed when Run completes.
func (e *Executor) Events() <-chan Event {
	return e.events
}

// Results returns a read-only channel of Result snapshots.
//
// It is a convenience view over Completed/Skipped events. The channel is
// closed when Run completes.
func (e *Executor) Results() <-chan Result {
	return e.results
}

// Run executes the plan for the given root tasks.
//
// roots is the set of task names whose transitive dependencies will be
// considered reachable. If roots is empty, the entire plan is executed.
//
// The returned RunSummary only includes results for reachable tasks.
//
// NOTE: An Executor instance is intended for a single Run call.
func (e *Executor) Run(ctx context.Context, roots ...string) (RunSummary, error) {
	if ctx == nil {
		return RunSummary{}, errors.New("executor run: nil context")
	}

	reachable, reachableCount, err := e.computeReachable(roots)
	if err != nil {
		return RunSummary{}, err
	}
	if reachableCount == 0 {
		return RunSummary{}, errors.New("executor run: no reachable tasks")
	}

	n := len(e.plan.tasks)

	remainingDeps := make([]int, n)
	status := make([]taskStatus, n)
	results := make([]Result, n)

	for i := range n {
		if !reachable[i] {
			continue
		}

		// Copy indegree for reachable nodes only.
		remainingDeps[i] = len(e.plan.deps[i])

		status[i] = statusPending
		results[i] = Result{Name: e.plan.tasks[i].Name}
	}

	readyCh := make(chan int)
	// Buffered so workers can complete sending even if Run stops reading.
	doneCh := make(chan taskDone, reachableCount)

	var wg sync.WaitGroup

	workerCount := min(e.maxWorkers, reachableCount)
	if workerCount <= 0 {
		workerCount = 1
	}

	for range workerCount {
		wg.Go(func() {
			e.worker(ctx, readyCh, doneCh)
		})
	}

	// Seed initial ready queue (nodes with remainingDeps == 0).
	for i := range n {
		if !reachable[i] {
			continue
		}
		if remainingDeps[i] == 0 {
			results[i].StartedAt = time.Now()
			status[i] = statusRunning

			e.emit(Event{
				Type:   EventTaskStarted,
				Time:   results[i].StartedAt,
				Task:   e.plan.tasks[i],
				Result: nil,
			})

			// We don't check ctx here; workers will see ctx.Done() and
			// short-circuit if cancellation has already occurred.
			readyCh <- i
		}
	}

	completed := 0
	e.firstErr = nil
	done := false

	for !done && completed < reachableCount {
		select {
		case <-ctx.Done():
			// Mark pending nodes as skipped.
			for i := range n {
				if !reachable[i] {
					continue
				}
				if status[i] == statusPending {
					status[i] = statusSkipped
					results[i].Skipped = true
					results[i].CompletedAt = time.Now()
					completed++

					e.emit(Event{
						Type:   EventTaskSkipped,
						Time:   results[i].CompletedAt,
						Task:   e.plan.tasks[i],
						Result: &results[i],
					})
				}
			}
			done = true

		case td := <-doneCh:
			i := td.index
			t := e.plan.tasks[i]

			if td.err != nil && !errors.Is(td.err, context.Canceled) {
				// Hard failure from task.
				// e.logf("task %s failed: %v", t.Name, td.err)
				status[i] = statusFailed
				results[i].Err = td.err
				results[i].CompletedAt = time.Now()
				completed++

				e.setFirstErr(td.err)

				// Emit completed event with error.
				e.emit(Event{
					Type:   EventTaskCompleted,
					Time:   results[i].CompletedAt,
					Task:   t,
					Result: &results[i],
				})

				// Mark all transitive dependents as skipped via BFS traversal.
				e.skipTransitiveDependents(i, status, results, reachable, &completed)

				if e.failFast {
					// Don't schedule any new work after failure.
					// Already running tasks continue.
					continue
				}

				continue
			}

			if status[i] == statusRunning {
				// Success path.
				status[i] = statusCompleted
				results[i].CompletedAt = time.Now()
				completed++

				e.emit(Event{
					Type:   EventTaskCompleted,
					Time:   results[i].CompletedAt,
					Task:   t,
					Result: &results[i],
				})
			}

			// Decrement remainingDeps for dependents and enqueue newly ready ones.
			for _, depIdx := range e.plan.dependents[i] {
				if !reachable[depIdx] {
					continue
				}
				if status[depIdx] != statusPending {
					continue
				}
				remainingDeps[depIdx]--
				if remainingDeps[depIdx] == 0 {
					results[depIdx].StartedAt = time.Now()
					status[depIdx] = statusRunning

					e.emit(Event{
						Type:   EventTaskStarted,
						Time:   results[depIdx].StartedAt,
						Task:   e.plan.tasks[depIdx],
						Result: nil,
					})

					// Again, we don't gate scheduling on ctx here; workers
					// will check ctx before running the task body.
					readyCh <- depIdx
				}
			}
		}
	}

	// Stop workers.
	close(readyCh)
	wg.Wait()
	close(doneCh)

	// Close observer channels to signal completion.
	close(e.events)
	close(e.results)

	// Build summary.
	summary := RunSummary{
		Results: make(map[string]Result, reachableCount),
		Failed:  e.firstErr != nil,
	}
	for i := range n {
		if !reachable[i] {
			continue
		}
		summary.Results[e.plan.tasks[i].Name] = results[i]
	}

	if err := e.collectErrors(&summary); err != nil {
		return summary, err
	}

	// If context was canceled but no task returned an error,
	// propagate ctx.Err() so callers can distinguish.
	if err := ctx.Err(); err != nil {
		return summary, err
	}

	return summary, nil
}

// computeReachable marks all nodes reachable from roots.
//
// If roots is empty, all nodes are reachable.
func (e *Executor) computeReachable(roots []string) ([]bool, int, error) {
	n := len(e.plan.tasks)
	reachable := make([]bool, n)

	// If no roots specified, everything is reachable.
	if len(roots) == 0 {
		for i := range reachable {
			reachable[i] = true
		}
		return reachable, n, nil
	}

	// Map roots to indices.
	rootIdx := make([]int, 0, len(roots))
	for _, name := range roots {
		idx, ok := e.plan.indexByName[name]
		if !ok {
			return nil, 0, fmt.Errorf("executor run: unknown root task %q", name)
		}
		rootIdx = append(rootIdx, idx)
	}

	// DFS/BFS from each root following dependencies backwards.
	var stack []int
	seen := make([]bool, n)

	for _, r := range rootIdx {
		if seen[r] {
			continue
		}
		stack = append(stack[:0], r)
		for len(stack) > 0 {
			curr := stack[len(stack)-1]
			stack = stack[:len(stack)-1]

			if seen[curr] {
				continue
			}
			seen[curr] = true
			reachable[curr] = true

			for _, dep := range e.plan.deps[curr] {
				if !seen[dep] {
					stack = append(stack, dep)
				}
			}
		}
	}

	count := 0
	for i := range reachable {
		if reachable[i] {
			count++
		}
	}

	return reachable, count, nil
}

type taskDone struct {
	index int
	err   error
}

// worker executes tasks sent on readyCh and reports completion on doneCh.
func (e *Executor) worker(
	ctx context.Context,
	readyCh <-chan int,
	doneCh chan<- taskDone,
) {
	for idx := range readyCh {
		t := e.plan.tasks[idx]

		select {
		case <-ctx.Done():
			// Don't run new tasks after context cancellation.
			doneCh <- taskDone{index: idx, err: ctx.Err()}
			continue
		default:
		}

		err := t.Run(ctx)
		doneCh <- taskDone{index: idx, err: err}
	}
}

func (e *Executor) setFirstErr(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.firstErr == nil {
		e.firstErr = err
	}
}

// emit sends an event to the internal channels and optional external sink.
//
// Event is passed by value; Result pointer (if present) is treated as a
// snapshot at the time of emission.
func (e *Executor) emit(ev Event) {
	// Non-blocking send to event stream; drop if buffer is full.
	if e.events != nil {
		select {
		case e.events <- ev:
		default:
		}
	}

	// Convenience result stream: only for events with a Result snapshot.
	if ev.Result != nil && e.results != nil {
		select {
		case e.results <- *ev.Result:
		default:
		}
	}

	// Optional pluggable sink (synchronous).
	if e.sink != nil {
		e.sink.HandleEvent(ev)
	}
}

// skipTransitiveDependents marks all transitive dependents of a failed task as skipped.
//
// When a task fails, its dependents cannot run (their dependency is unsatisfied).
// This function performs a BFS traversal from the failed task through plan.dependents,
// marking each reachable pending task as skipped and emitting EventTaskSkipped.
//
// This ensures RunSummary provides a complete view of execution—no tasks remain
// in an ambiguous pending state.
//
// Parameters:
//   - failedIdx: Index of the failed task in plan.tasks
//   - status: Slice tracking task status (statusPending, statusRunning, etc.)
//   - results: Slice of Result structs to update with skip information
//   - reachable: Slice indicating which tasks are reachable from roots
//   - completed: Pointer to completed counter for termination detection
func (e *Executor) skipTransitiveDependents(
	failedIdx int,
	status []taskStatus,
	results []Result,
	reachable []bool,
	completed *int,
) {
	visited := make(map[int]bool)
	queue := make([]int, 0, len(e.plan.dependents[failedIdx]))

	// Seed queue with direct dependents of the failed task
	for _, idx := range e.plan.dependents[failedIdx] {
		if reachable[idx] && status[idx] == statusPending && !visited[idx] {
			queue = append(queue, idx)
			visited[idx] = true
		}
	}

	for len(queue) > 0 {
		idx := queue[0]
		queue = queue[1:]

		// Defensive: skip if already processed (should not happen due to visited check)
		if status[idx] != statusPending {
			continue
		}

		// Mark as skipped
		status[idx] = statusSkipped
		results[idx].Skipped = true
		results[idx].CompletedAt = time.Now()
		(*completed)++

		// Emit skip event
		e.emit(Event{
			Type:   EventTaskSkipped,
			Time:   results[idx].CompletedAt,
			Task:   e.plan.tasks[idx],
			Result: &results[idx],
		})

		// Queue dependents of this now-skipped task (transitive propagation)
		for _, depIdx := range e.plan.dependents[idx] {
			if reachable[depIdx] && status[depIdx] == statusPending && !visited[depIdx] {
				queue = append(queue, depIdx)
				visited[depIdx] = true
			}
		}
	}
}

// collectErrors returns either the first error or all errors joined,
// depending on the collectAllErrors option.
func (e *Executor) collectErrors(summary *RunSummary) error {
	if !e.collectAllErrors {
		return e.firstErr
	}

	// Collect all failed task names
	var failedNames []string
	for name, res := range summary.Results {
		if res.Err != nil {
			failedNames = append(failedNames, name)
		}
	}

	if len(failedNames) == 0 {
		return nil
	}

	// Sort for deterministic ordering
	sort.Strings(failedNames)

	// Build joined error
	errs := make([]error, 0, len(failedNames))
	for _, name := range failedNames {
		res := summary.Results[name]
		errs = append(errs, fmt.Errorf("task %q: %w", name, res.Err))
	}

	return errors.Join(errs...)
}
