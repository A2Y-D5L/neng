package stream

import (
	"context"
	"sync"

	"github.com/a2y-d5l/neng/v2"
)

// Handle is an asynchronous run handle that exposes channels for events/results.
type Handle struct {
	err     error
	obs     *Observer
	done    chan struct{}
	summary neng.RunSummary
	mu      sync.Mutex
}

// Start runs plan.Execute in a goroutine and returns a Handle.
//
// execOpts are passed to Plan.Execute; an observer is automatically attached.
// obsOpts configure the underlying Observer.
func Start(
	ctx context.Context,
	p *neng.Plan,
	roots []string,
	execOpts []neng.ExecOption,
	obsOpts ...Option,
) *Handle {
	obs := NewObserver(obsOpts...)
	x := &Handle{
		obs:  obs,
		done: make(chan struct{}),
	}

	opts := make([]neng.ExecOption, 0, len(execOpts)+1)
	opts = append(opts, execOpts...)
	opts = append(opts, neng.WithObserver(obs))

	go func() {
		defer close(x.done)
		defer obs.Close()

		sum, err := p.Execute(ctx, roots, opts...)

		x.mu.Lock()
		x.summary = sum
		x.err = err
		x.mu.Unlock()
	}()

	return x
}

// Events returns a read-only channel of events for this neng.
func (x *Handle) Events() <-chan neng.Event {
	if x == nil {
		ch := make(chan neng.Event)
		close(ch)
		return ch
	}
	return x.obs.Events()
}

// Results returns a read-only channel of results for this neng.
func (x *Handle) Results() <-chan neng.Result {
	if x == nil {
		ch := make(chan neng.Result)
		close(ch)
		return ch
	}
	return x.obs.Results()
}

// Done returns a channel that closes when the execution completes.
func (x *Handle) Done() <-chan struct{} {
	if x == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return x.done
}

// Wait blocks until the execution completes and returns the summary and error.
func (x *Handle) Wait() (neng.RunSummary, error) {
	if x == nil {
		return neng.RunSummary{}, context.Canceled
	}
	<-x.done
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.summary, x.err
}

// Drops returns current drop counters for the underlying Observer.
func (x *Handle) Drops() Drops {
	if x == nil {
		return Drops{}
	}
	return x.obs.Drops()
}
