package neng

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Execute runs the plan for the transitive dependency closure of roots.
//
// If roots is empty, all tasks are reachable.
//
// Errors:
//   - If one or more tasks fail (non-cancellation), Execute returns either the
//     first failure or all failures joined (see WithCollectAllErrors).
//   - If the context is canceled and no task failures occurred, Execute returns
//     ctx.Err().
func (p *Plan) Execute(
	ctx context.Context,
	roots []string,
	opts ...ExecOption,
) (RunSummary, error) {
	if ctx == nil {
		return RunSummary{}, errors.New("execution: execute: nil context")
	}

	cfg := defaultExecConfig()
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.maxWorkers <= 0 {
		cfg.maxWorkers = defaultExecConfig().maxWorkers
	}

	reachable, reachableCount, err := p.computeReachable(roots)
	if err != nil {
		return RunSummary{}, err
	}
	if reachableCount == 0 {
		return RunSummary{}, errors.New("execution: execute: no reachable tasks")
	}

	n := len(p.tasks)

	remainingDeps := make([]int, n)
	status := make([]taskStatus, n)
	results := make([]Result, n)

	// forcedSkipReason encodes "do not run this task if dequeued":
	// 0 means not forced, otherwise store SkipReason+1.
	// This is read by workers and written by the scheduler.
	forcedSkipReason := make([]atomic.Uint32, n)

	// upstreamFailed marks tasks that are downstream (transitively) of a failed task.
	// Owned by the scheduler goroutine.
	upstreamFailed := make([]bool, n)

	for i := range n {
		if !reachable[i] {
			status[i] = statusUnreachable
			continue
		}
		remainingDeps[i] = len(p.deps[i])
		status[i] = statusPending
		results[i] = Result{
			Name:   p.tasks[i].spec.Name,
			Status: ResultUnknown,
		}
	}

	workerCount := min(cfg.maxWorkers, reachableCount)
	if workerCount <= 0 {
		workerCount = 1
	}

	// readyCh is intentionally buffered to the maximum number of reachable tasks.
	// This prevents the scheduler from blocking on enqueue while workers attempt to
	// report events/completions.
	readyCh := make(chan int, reachableCount)

	// Each dequeued task produces at most 2 messages: started then done.
	// Buffering to 2*reachableCount prevents worker deadlocks if an attached observer
	// blocks and slows the scheduler.
	workCh := make(chan workerMsg, reachableCount*2)

	var stopNewWork atomic.Bool // set true on fail-fast (not on ctx cancellation)

	var wg sync.WaitGroup
	for range workerCount {
		wg.Go(func() {
			p.worker(ctx, &stopNewWork, &forcedSkipReason, readyCh, workCh)
		})
	}

	emit := func(ev Event) {
		if cfg.observer != nil {
			cfg.observer.HandleEvent(ev)
		}
	}

	finished := 0
	firstFailure := taskFailure{name: "", err: nil}

	finalize := func(idx int, r Result) {
		// Only finalize once.
		if status[idx] == statusFinished {
			return
		}
		status[idx] = statusFinished
		results[idx] = r
		finished++

		emit(Event{
			Type:   EventTaskFinished,
			Time:   r.CompletedAt,
			Task:   deepCopySpec(p.tasks[idx].spec),
			Result: ptrCopyResult(r),
		})
	}

	// Mark downstream tasks as upstreamFailed and either finalize them (if still pending)
	// or force-skip them (if already queued but not yet started).
	markAndApplyUpstreamFailed := func(failedIdx int) {
		queue := append([]int(nil), p.dependents[failedIdx]...)
		seen := make([]bool, n)

		for len(queue) > 0 {
			u := queue[0]
			queue = queue[1:]

			if seen[u] {
				continue
			}
			seen[u] = true

			if !reachable[u] {
				continue
			}

			upstreamFailed[u] = true

			switch status[u] {
			case statusPending:
				finalize(u, Result{
					Name:        p.tasks[u].spec.Name,
					Status:      ResultSkipped,
					SkipReason:  SkipUpstreamFailed,
					Err:         nil,
					StartedAt:   results[u].StartedAt,
					CompletedAt: time.Now(),
				})
			case statusQueued:
				// Prevent start; worker will report a skipped completion.
				forcedSkipReason[u].Store(uint32(SkipUpstreamFailed) + 1)
			}

			for _, v := range p.dependents[u] {
				if !seen[v] {
					queue = append(queue, v)
				}
			}
		}
	}

	// Apply fail-fast: prevent starting any queued-but-not-started tasks and finalize
	// remaining pending tasks deterministically.
	applyFailFast := func() {
		// 1) Force-skip anything queued (but not started).
		for i := range n {
			if !reachable[i] {
				continue
			}
			if status[i] == statusQueued {
				// UpstreamFailed has priority if already set (should be rare for queued tasks).
				if upstreamFailed[i] {
					forcedSkipReason[i].Store(uint32(SkipUpstreamFailed) + 1)
				} else {
					forcedSkipReason[i].Store(uint32(SkipFailFast) + 1)
				}
			}
		}

		// 2) Finalize any still-pending tasks as skipped (UpstreamFailed wins).
		for i := range n {
			if !reachable[i] {
				continue
			}
			if status[i] != statusPending {
				continue
			}
			reason := SkipFailFast
			if upstreamFailed[i] {
				reason = SkipUpstreamFailed
			}
			finalize(i, Result{
				Name:        p.tasks[i].spec.Name,
				Status:      ResultSkipped,
				SkipReason:  reason,
				Err:         nil,
				StartedAt:   results[i].StartedAt,
				CompletedAt: time.Now(),
			})
		}
	}

	// Apply context cancellation: finalize any tasks that have not been queued or started.
	ctxCanceledApplied := false
	applyContextCanceled := func() {
		if ctxCanceledApplied {
			return
		}
		if ctx.Err() == nil {
			return
		}
		ctxCanceledApplied = true

		// Finalize pending tasks as canceled. Queued tasks are left for workers to
		// report as canceled when dequeued (so the scheduler still counts them via workCh).
		for i := range n {
			if !reachable[i] {
				continue
			}
			if status[i] != statusPending {
				continue
			}
			finalize(i, Result{
				Name:        p.tasks[i].spec.Name,
				Status:      ResultCanceled,
				SkipReason:  SkipContextCanceled,
				Err:         ctx.Err(),
				StartedAt:   results[i].StartedAt,
				CompletedAt: time.Now(),
			})
		}
	}

	enqueue := func(idx int) {
		if status[idx] != statusPending {
			return
		}
		status[idx] = statusQueued
		readyCh <- idx
	}

	finalizeReadyOrEnqueue := func(idx int) {
		// idx is reachable and currently pending with remainingDeps==0.
		if status[idx] != statusPending {
			return
		}

		// If context is canceled, do not enqueue.
		if ctx.Err() != nil {
			finalize(idx, Result{
				Name:        p.tasks[idx].spec.Name,
				Status:      ResultCanceled,
				SkipReason:  SkipContextCanceled,
				Err:         ctx.Err(),
				StartedAt:   results[idx].StartedAt,
				CompletedAt: time.Now(),
			})
			return
		}

		// If upstream failed, this task cannot run.
		if upstreamFailed[idx] {
			finalize(idx, Result{
				Name:        p.tasks[idx].spec.Name,
				Status:      ResultSkipped,
				SkipReason:  SkipUpstreamFailed,
				Err:         nil,
				StartedAt:   results[idx].StartedAt,
				CompletedAt: time.Now(),
			})
			return
		}

		// If fail-fast engaged, do not start new work.
		if stopNewWork.Load() {
			finalize(idx, Result{
				Name:        p.tasks[idx].spec.Name,
				Status:      ResultSkipped,
				SkipReason:  SkipFailFast,
				Err:         nil,
				StartedAt:   results[idx].StartedAt,
				CompletedAt: time.Now(),
			})
			return
		}

		enqueue(idx)
	}

	// Seed runnable tasks (deps == 0).
	for i := range n {
		if !reachable[i] {
			continue
		}
		if remainingDeps[i] == 0 {
			finalizeReadyOrEnqueue(i)
		}
	}

	// Scheduler loop: consume worker messages until all reachable tasks are finalized.
	for finished < reachableCount {
		// Apply cancellation promptly (finalizes pending tasks).
		if ctx.Err() != nil {
			applyContextCanceled()
			// If cancellation finalized all remaining tasks, exit immediately to avoid
			// blocking on workCh when no workers will send (e.g., pre-canceled context).
			if finished >= reachableCount {
				break
			}
		}

		msg := <-workCh
		if !reachable[msg.idx] {
			continue
		}

		// Ignore messages for tasks already finalized by policy while still pending.
		// (We never finalize queued/running tasks out-of-band, so these should be rare.)
		if status[msg.idx] == statusFinished {
			continue
		}

		switch msg.kind {
		case msgStarted:
			// If already running (duplicate started), ignore.
			if status[msg.idx] == statusRunning {
				continue
			}
			// If this task was forced to skip/cancel before start, workers won't send started.
			status[msg.idx] = statusRunning
			results[msg.idx].StartedAt = msg.startedAt

			emit(Event{
				Type:   EventTaskStarted,
				Time:   msg.startedAt,
				Task:   deepCopySpec(p.tasks[msg.idx].spec),
				Result: nil,
			})

		case msgDone:
			// Determine terminal outcome.
			r := Result{
				Name:        p.tasks[msg.idx].spec.Name,
				StartedAt:   results[msg.idx].StartedAt,
				CompletedAt: msg.completedAt,
			}

			switch {
			case msg.skipped:
				r.Status = ResultSkipped
				r.SkipReason = msg.skipReason
				r.Err = nil

			case msg.err != nil && isContextCancelErr(msg.err):
				r.Status = ResultCanceled
				r.SkipReason = SkipContextCanceled
				r.Err = msg.err

			case msg.err != nil:
				r.Status = ResultFailed
				r.SkipReason = SkipUnknown
				r.Err = msg.err

			default:
				r.Status = ResultSucceeded
				r.SkipReason = SkipUnknown
				r.Err = nil
			}

			finalize(msg.idx, r)

			// Failure handling: mark downstream as upstreamFailed and optionally engage fail-fast.
			if r.Status == ResultFailed {
				if firstFailure.err == nil {
					firstFailure = taskFailure{name: r.Name, err: r.Err}
				}
				markAndApplyUpstreamFailed(msg.idx)

				if cfg.failFast {
					stopNewWork.Store(true)
					applyFailFast()
				}
			}

			// Decrement dependents for any task completion (success/fail/skip/cancel).
			// Only pending tasks are eligible to become runnable.
			for _, depIdx := range p.dependents[msg.idx] {
				if !reachable[depIdx] {
					continue
				}
				if status[depIdx] != statusPending {
					continue
				}
				remainingDeps[depIdx]--
				if remainingDeps[depIdx] == 0 {
					finalizeReadyOrEnqueue(depIdx)
				}
			}
		}
	}

	// Stop workers and close channels.
	close(readyCh)
	wg.Wait()
	close(workCh)

	// Build summary (reachable tasks only).
	summary := RunSummary{
		Results: make(map[string]Result, reachableCount),
		Failed:  false,
	}
	for i := range n {
		if !reachable[i] {
			continue
		}
		r := results[i]
		// Defensive: should not happen, but ensure terminal.
		if r.Status == ResultUnknown {
			now := time.Now()
			if ctx.Err() != nil {
				r.Status = ResultCanceled
				r.SkipReason = SkipContextCanceled
				r.Err = ctx.Err()
				r.CompletedAt = now
			} else {
				r.Status = ResultSkipped
				r.SkipReason = SkipUnknown
				r.CompletedAt = now
			}
		}
		if r.Status == ResultFailed {
			summary.Failed = true
		}
		summary.Results[p.tasks[i].spec.Name] = r
	}

	// Return error with policy: failures take precedence over ctx cancellation.
	if errs := collectErrors(cfg.collectAllErrors, summary.Results, firstFailure); errs != nil {
		return summary, errs
	}

	if ctx.Err() != nil {
		return summary, ctx.Err()
	}
	return summary, nil
}

// ResultsByName returns reachable results sorted by task name.
func (p *Plan) ResultsByName(sum RunSummary) []TaskResult {
	names := make([]string, 0, len(sum.Results))
	for name := range sum.Results {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]TaskResult, 0, len(names))
	for _, name := range names {
		spec, _ := p.Spec(name)
		out = append(out, TaskResult{Spec: spec, Result: sum.Results[name]})
	}
	return out
}

// ResultsTopo returns reachable results in deterministic topological order.
func (p *Plan) ResultsTopo(sum RunSummary) []TaskResult {
	out := make([]TaskResult, 0, len(sum.Results))
	for _, idx := range p.topoOrder {
		name := p.tasks[idx].spec.Name
		r, ok := sum.Results[name]
		if !ok {
			continue
		}
		out = append(out, TaskResult{
			Spec:   deepCopySpec(p.tasks[idx].spec),
			Result: r,
		})
	}
	return out
}

type taskStatus uint8

const (
	statusUnreachable taskStatus = iota
	statusPending
	statusQueued
	statusRunning
	statusFinished
)

type msgKind uint8

const (
	msgStarted msgKind = iota
	msgDone
)

type workerMsg struct {
	startedAt   time.Time
	completedAt time.Time
	err         error
	idx         int
	kind        msgKind
	skipped     bool
	skipReason  SkipReason
}

func (p *Plan) worker(
	ctx context.Context,
	stopNewWork *atomic.Bool,
	forcedSkipReason *[]atomic.Uint32,
	readyCh <-chan int,
	workCh chan<- workerMsg,
) {
	for idx := range readyCh {
		// If the run context is canceled, do not start; report canceled completion.
		select {
		case <-ctx.Done():
			workCh <- workerMsg{
				kind:        msgDone,
				idx:         idx,
				err:         ctx.Err(),
				completedAt: time.Now(),
				skipped:     false,
			}
			continue
		default:
		}

		// Fail-fast / upstream-failure forced-skip for queued tasks.
		if v := (*forcedSkipReason)[idx].Load(); v != 0 {
			reason := SkipReason(v - 1)
			workCh <- workerMsg{
				kind:        msgDone,
				idx:         idx,
				err:         nil,
				completedAt: time.Now(),
				skipped:     true,
				skipReason:  reason,
			}
			continue
		}

		// Fail-fast: do not start new work after stopNewWork is set.
		// This is a backstop; the scheduler also avoids enqueuing when engaged.
		if stopNewWork.Load() {
			workCh <- workerMsg{
				kind:        msgDone,
				idx:         idx,
				err:         nil,
				completedAt: time.Now(),
				skipped:     true,
				skipReason:  SkipFailFast,
			}
			continue
		}

		start := time.Now()
		workCh <- workerMsg{kind: msgStarted, idx: idx, startedAt: start}

		err := p.tasks[idx].run(ctx)

		workCh <- workerMsg{
			kind:        msgDone,
			idx:         idx,
			err:         err,
			completedAt: time.Now(),
			skipped:     false,
		}
	}
}

func isContextCancelErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

type taskFailure struct {
	err  error
	name string
}

func collectErrors(collectAll bool, results map[string]Result, first taskFailure) error {
	if !collectAll {
		if first.err == nil {
			return nil
		}
		return fmt.Errorf("task %q: %w", first.name, first.err)
	}

	var failed []string
	for name, r := range results {
		if r.Status == ResultFailed && r.Err != nil {
			failed = append(failed, name)
		}
	}
	if len(failed) == 0 {
		return nil
	}
	sort.Strings(failed)

	errs := make([]error, 0, len(failed))
	for _, name := range failed {
		r := results[name]
		errs = append(errs, fmt.Errorf("task %q: %w", name, r.Err))
	}
	return errors.Join(errs...)
}

func ptrCopyResult(r Result) *Result {
	rr := r
	return &rr
}