package neng_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/a2y-d5l/neng/v2"
	"github.com/a2y-d5l/neng/v2/pkg/stream/streamtest"
)

func TestExecute_EventOrderPerTask_StartedBeforeFinished(t *testing.T) {
	p, err := neng.BuildPlan(
		neng.Task{Name: "A", Run: func(context.Context) error { return nil }},
		neng.Task{
			Name: "B",
			Deps: []string{"A"},
			Run:  func(context.Context) error { return nil },
		},
		neng.Task{
			Name: "C",
			Deps: []string{"A"},
			Run:  func(context.Context) error { return nil },
		},
	)
	if err != nil {
		t.Fatalf("BuildPlan error: %v", err)
	}

	rec := streamtest.NewEventRecorder()
	_, runErr := p.Execute(context.Background(), nil, neng.WithObserver(rec))
	if runErr != nil {
		t.Fatalf("Execute error: %v", runErr)
	}

	evs := rec.Events()

	startIdx := map[string]int{}
	finishIdx := map[string]int{}

	for i, ev := range evs {
		switch ev.Type {
		case neng.EventTaskStarted:
			startIdx[ev.Task.Name] = i
		case neng.EventTaskFinished:
			finishIdx[ev.Task.Name] = i
		}
	}

	// For each finished task, if we saw a started event, it must be before finished.
	for name, fi := range finishIdx {
		si, ok := startIdx[name]
		if !ok {
			continue // skipped/canceled tasks may not have a started event
		}
		if si > fi {
			t.Fatalf("task %q: started index %d > finished index %d", name, si, fi)
		}
	}
}

func TestExecute_CancellationFinalizesAndReturns(t *testing.T) {
	// Many independent tasks that block until ctx is canceled.
	const tasks = 64

	in := make([]neng.Task, 0, tasks)
	for i := range tasks {
		name := string(rune('A' + i))
		in = append(in, neng.Task{
			Name: name,
			Run: func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			},
		})
	}

	p, err := neng.BuildPlan(in...)
	if err != nil {
		t.Fatalf("BuildPlan error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(25 * time.Millisecond)
		cancel()
	}()

	sum, runErr := p.Execute(ctx, nil, neng.WithMaxWorkers(8))
	if runErr == nil {
		t.Fatalf("expected cancellation error, got nil")
	}
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", runErr)
	}
	if len(sum.Results) != tasks {
		t.Fatalf("expected %d results, got %d", tasks, len(sum.Results))
	}
	for name, r := range sum.Results {
		if r.Status != neng.ResultCanceled {
			t.Fatalf("task %q: expected canceled, got %v", name, r.Status)
		}
	}
}

func TestExecute_FailFastSkipsQueuedButNotStarted(t *testing.T) {
	// With a single worker and tasks enqueued in order A,B,C, A will fail first
	// and B/C will be dequeued and skipped without starting.
	p, err := neng.BuildPlan(
		neng.Task{
			Name: "A",
			Run:  func(context.Context) error { return errors.New("boom") },
		},
		neng.Task{
			Name: "B",
			Run:  func(context.Context) error { return nil },
		},
		neng.Task{
			Name: "C",
			Run:  func(context.Context) error { return nil },
		},
	)
	if err != nil {
		t.Fatalf("BuildPlan error: %v", err)
	}

	rec := streamtest.NewEventRecorder()
	sum, runErr := p.Execute(
		context.Background(),
		nil,
		neng.WithMaxWorkers(1),
		neng.WithFailFast(),
		neng.WithObserver(rec),
	)
	if runErr == nil {
		t.Fatalf("expected error")
	}

	if sum.Results["A"].Status != neng.ResultFailed {
		t.Fatalf("A status = %v", sum.Results["A"].Status)
	}
	if sum.Results["B"].Status != neng.ResultSkipped ||
		sum.Results["B"].SkipReason != neng.SkipFailFast {
		t.Fatalf("B result = %#v", sum.Results["B"])
	}
	if sum.Results["C"].Status != neng.ResultSkipped ||
		sum.Results["C"].SkipReason != neng.SkipFailFast {
		t.Fatalf("C result = %#v", sum.Results["C"])
	}

	// B and C should not have started.
	if !sum.Results["B"].StartedAt.IsZero() || !sum.Results["C"].StartedAt.IsZero() {
		t.Fatalf(
			"expected B and C StartedAt to be zero; got B=%v C=%v",
			sum.Results["B"].StartedAt,
			sum.Results["C"].StartedAt,
		)
	}

	// Recorder should not contain started events for B/C.
	evs := rec.Events()
	started := map[string]bool{}
	for _, ev := range evs {
		if ev.Type == neng.EventTaskStarted {
			started[ev.Task.Name] = true
		}
	}
	if started["B"] || started["C"] {
		t.Fatalf("expected no started events for B/C; got started=%v", started)
	}
}

func TestExecute_PreCanceledContextDoesNotDeadlock(t *testing.T) {
	// Regression test: when the context is already canceled before Execute runs,
	// the scheduler must not block on workCh waiting for worker messages that
	// will never arrive (because nothing was enqueued).
	p, err := neng.BuildPlan(
		neng.Task{
			Name: "A",
			Run:  func(context.Context) error { return nil },
		},
		neng.Task{
			Name: "B",
			Deps: []string{"A"},
			Run:  func(context.Context) error { return nil },
		},
	)
	if err != nil {
		t.Fatalf("BuildPlan error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel before Execute

	done := make(chan struct{})
	var sum neng.RunSummary
	var runErr error

	go func() {
		sum, runErr = p.Execute(ctx, nil)
		close(done)
	}()

	select {
	case <-done:
		// Success: Execute returned
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock: Execute did not return within timeout")
	}

	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", runErr)
	}

	// All tasks should be finalized as canceled.
	for name, r := range sum.Results {
		if r.Status != neng.ResultCanceled {
			t.Fatalf("task %q: expected canceled, got %v", name, r.Status)
		}
	}
}
