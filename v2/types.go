package neng

import (
	"context"
	"time"
)

// TaskFunc is the executable work for a task.
type TaskFunc func(context.Context) error

// Task is the user-provided task definition used to build a Plan.
//
// Task values are consumed by BuildPlan and should be treated as inputs.
// BuildPlan deep-copies dependency slices.
type Task struct {
	Run  TaskFunc
	Name string
	Desc string
	Deps []string
}

// TaskSpec is an immutable snapshot of a task definition suitable for exposing
// to callers and embedding in events.
//
// TaskSpec must never contain executable code (e.g., function pointers). It is
// safe to retain and reuse across runs.
type TaskSpec struct {
	// Name is the unique task name within a plan.
	Name string

	// Desc is optional human-readable documentation.
	Desc string

	// Deps is the list of dependency task names. The slice is treated as an
	// immutable snapshot (callers should not mutate it).
	Deps []string
}

// TaskResult pairs a task spec with its result.
type TaskResult struct {
	Result Result
	Spec   TaskSpec
}

// RunSummary is the top-level result of an Execute run.
type RunSummary struct {
	// Results contains a Result for every reachable task.
	Results map[string]Result

	// Failed is true if at least one reachable task failed.
	Failed bool
}

// Stage represents a DAG "level" that can contain tasks that can safely run in
// parallel.
type Stage struct {
	Tasks []string
	Index int
}

// ResultStatus describes the terminal outcome of a task within a run.
type ResultStatus uint8

const (
	// ResultUnknown indicates the result has not been finalized (should not
	// appear in RunSummary for reachable tasks after Execute returns).
	ResultUnknown ResultStatus = iota

	// ResultSucceeded indicates the task ran and returned nil error.
	ResultSucceeded

	// ResultFailed indicates the task ran and returned a non-cancellation error.
	ResultFailed

	// ResultSkipped indicates the task did not run due to policy or upstream
	// failure, and the run was not globally canceled.
	ResultSkipped

	// ResultCanceled indicates the task did not run or did not complete because
	// the run context was canceled.
	ResultCanceled
)

// SkipReason refines why a task ended in ResultSkipped or ResultCanceled.
type SkipReason uint8

const (
	// SkipUnknown indicates no specific skip reason is available.
	SkipUnknown SkipReason = iota

	// SkipUpstreamFailed indicates a dependency failed (directly or transitively).
	SkipUpstreamFailed

	// SkipFailFast indicates execution policy prevented starting new work after
	// a failure.
	SkipFailFast

	// SkipContextCanceled indicates the run context was canceled before the task
	// started or could complete.
	SkipContextCanceled
)

// Result is a terminal snapshot of a single task's execution within a run.
type Result struct {
	StartedAt   time.Time
	CompletedAt time.Time
	Err         error
	Name        string
	Status      ResultStatus
	SkipReason  SkipReason
}

// EventType describes the type of lifecycle event.
type EventType uint8

const (
	// EventTaskStarted indicates a task began executing.
	EventTaskStarted EventType = iota

	// EventTaskFinished indicates a task finalized with a terminal Result.
	EventTaskFinished
)

// Event is a fire-and-forget notification about task lifecycle.
//
// The payload is intentionally snapshot-based (TaskSpec and Result) so observers
// cannot mutate engine internals.
type Event struct {
	Time   time.Time
	Result *Result
	Task   TaskSpec
	Type   EventType
}

// Observer receives task lifecycle events emitted by the execution engine.
//
// See package-level documentation in doc.go for concurrency and ordering
// guarantees.
type Observer interface {
	HandleEvent(Event)
}

// ObserverFunc adapts a function to the Observer interface.
type ObserverFunc func(Event)

// HandleEvent calls f(e).
func (f ObserverFunc) HandleEvent(e Event) {
	f(e)
}

// MultiObserver fans out events to multiple observers.
//
// MultiObserver does not impose additional ordering guarantees across observers.
// Implementations may change over time; callers must not depend on a specific
// fanout order.
//
// Nil observers are ignored.
func MultiObserver(obs ...Observer) Observer {
	// Copy to avoid surprises if caller mutates the input slice.
	cp := make([]Observer, 0, len(obs))
	for _, o := range obs {
		if o != nil {
			cp = append(cp, o)
		}
	}
	if len(cp) == 0 {
		return ObserverFunc(func(Event) {})
	}
	if len(cp) == 1 {
		return cp[0]
	}

	return ObserverFunc(func(e Event) {
		for _, o := range cp {
			o.HandleEvent(e)
		}
	})
}
