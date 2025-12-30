// Package neng defines an engine for executing a DAG of named tasks.
//
// # Immutability
//
// A Plan is immutable after construction and safe for concurrent read-only use.
//
// The Plan never exposes pointers to internal task definitions. Methods that
// expose task information return neng.TaskSpec snapshots.
//
// # Execution semantics
//
// Execute runs the transitive dependency closure of the given roots. If roots is
// empty, all tasks in the plan are reachable.
//
// # Observers
//
// The engine emits events by calling neng.Observer.HandleEvent(neng.Event).
// The engine may call observers concurrently and does not guarantee global
// ordering between tasks. The event package provides adapters that offer
// explicit buffering/overflow policies.
//
// # Fail-fast
//
// When WithFailFast is enabled, the engine stops starting new work after the
// first non-cancellation failure. Tasks already running are allowed to complete.
// Tasks that did not start are finalized as skipped with SkipFailFast (unless
// they are downstream of a failed task, in which case they are skipped with
// SkipUpstreamFailed).
//
// # Observer guarantees
//
// The execution engine emits events by calling Observer.HandleEvent(Event).
// The contract is deliberately minimal to preserve the freedom to evolve engine
// internals:
//
//   - HandleEvent MAY be called concurrently.
//   - For a given task, EventTaskStarted (if emitted) happens-before
//     EventTaskFinished.
//   - No global total order is guaranteed between tasks.
//   - If an Observer blocks, it may slow execution; observers intended for
//     production automation should be non-blocking.
//
// The event package provides first-party adapters (e.g., StreamObserver) that
// implement overflow/backpressure policies explicitly.
package neng
