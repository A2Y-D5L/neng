package stream

import (
	"fmt"
	"sync"
	"sync/atomic"

	neng "github.com/a2y-d5l/neng/v2"
)

// Drops reports the number of items dropped by Observer due to overflow.
type Drops struct {
	// Events is the number of dropped neng.Event values.
	Events uint64

	// Results is the number of dropped neng.Result values.
	Results uint64
}

// Observer implements neng.Observer and forwards events/results into channels.
//
// It is safe under concurrent HandleEvent calls.
type Observer struct {
	events         chan neng.Event
	results        chan neng.Result
	inbox          chan neng.Event
	done           chan struct{}
	cfg            config
	wg             sync.WaitGroup
	droppedEvents  atomic.Uint64
	droppedResults atomic.Uint64
	closeOnce      sync.Once
}

// newInbox creates a channel with a buffer sized to prevent accidental blocking
// for drop policies under short bursts, without being unbounded.
func newInbox(evtBufSize, resBufSize int) chan neng.Event {
	const headroom = 256
	size := max(defaultInboxSize, max(evtBufSize, resBufSize)*2+headroom)
	return make(chan neng.Event, size)
}

// NewObserver constructs a Observer with optional configuration.
//
// Defaults:
//   - EventBuffer: 1024
//   - ResultBuffer: 1024
//   - OverflowPolicy: DropNewest
func NewObserver(opts ...Option) *Observer {
	c := config{
		eventBuf:  defaultEventBufSize,
		resultBuf: defaultResultBufSize,
		policy:    DropNewest,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&c)
		}
	}
	if c.eventBuf < 0 {
		c.eventBuf = 0
	}
	if c.resultBuf < 0 {
		c.resultBuf = 0
	}

	obs := &Observer{
		cfg:     c,
		events:  make(chan neng.Event, c.eventBuf),
		results: make(chan neng.Result, c.resultBuf),
		inbox:   newInbox(c.eventBuf, c.resultBuf),
		done:    make(chan struct{}),
	}

	obs.wg.Go(func() {
		obs.run()
	})

	return obs
}

// HandleEvent forwards events and (when present) results.
//
// HandleEvent obeys the configured overflow policy.
func (o *Observer) HandleEvent(e neng.Event) {
	if o == nil {
		return
	}

	// Fast-path: if closed, drop.
	select {
	case <-o.done:
		return
	default:
	}

	switch o.cfg.policy {
	case Block:
		// Block until enqueued (or closed).
		select {
		case o.inbox <- e:
		case <-o.done:
			return
		}

	case DropOldest, DropNewest:
		select {
		case o.inbox <- e:
		default:
			// If we can't even enqueue (inbox full), treat as overflow drop(s).
			// Count as an event drop; also count a result drop if present.
			o.droppedEvents.Add(1)
			if e.Result != nil {
				o.droppedResults.Add(1)
			}
		}

	default:
		panic(fmt.Errorf("unknown overflow policy: %v", o.cfg.policy))
	}
}

// Events returns a read-only channel of neng.Event values.
func (o *Observer) Events() <-chan neng.Event {
	if o == nil {
		ch := make(chan neng.Event)
		close(ch)
		return ch
	}

	return o.events
}

// Results returns a read-only channel of neng.Result values.
func (o *Observer) Results() <-chan neng.Result {
	if o == nil {
		ch := make(chan neng.Result)
		close(ch)
		return ch
	}

	return o.results
}

// Drops returns current drop counters.
func (o *Observer) Drops() Drops {
	if o == nil {
		return Drops{}
	}

	return Drops{
		Events:  o.droppedEvents.Load(),
		Results: o.droppedResults.Load(),
	}
}

// Close closes the observer and its channels.
//
// Close is safe to call multiple times.
func (o *Observer) Close() {
	if o == nil {
		return
	}

	o.closeOnce.Do(func() {
		close(o.done)
	})

	// Wait for the sender goroutine to stop before closing outbound channels.
	o.wg.Wait()
	close(o.events)
	close(o.results)
}

func (o *Observer) run() {
	for {
		select {
		case <-o.done:
			return

		case msg := <-o.inbox:
			// Note: msg is a copy of a neng.Event; forwarding is safe.
			o.sendEvent(msg)
			if msg.Result != nil {
				o.sendResult(*msg.Result)
			}
		}
	}
}

func (o *Observer) sendEvent(e neng.Event) {
	switch o.cfg.policy {
	case Block:
		select {
		case o.events <- e:
		case <-o.done:
		}

	case DropOldest:
		// Try send; if full, drop one buffered item then retry.
		select {
		case o.events <- e:
			return
		case <-o.done:
			return
		default:
		}
		select {
		case <-o.events:
		default:
		}
		select {
		case o.events <- e:
		default:
			o.droppedEvents.Add(1)
		}

	case DropNewest:
		select {
		case o.events <- e:
		default:
			o.droppedEvents.Add(1)
		}

	default:
		panic(fmt.Errorf("unknown overflow policy: %v", o.cfg.policy))
	}
}

func (o *Observer) sendResult(r neng.Result) {
	switch o.cfg.policy {
	case Block:
		select {
		case o.results <- r:
		case <-o.done:
		}

	case DropOldest:
		select {
		case o.results <- r:
			return
		case <-o.done:
			return
		default:
		}
		select {
		case <-o.results:
		default:
		}
		select {
		case o.results <- r:
		default:
			o.droppedResults.Add(1)
		}

	case DropNewest:
		select {
		case o.results <- r:
		default:
			o.droppedResults.Add(1)
		}

	default:
		panic(fmt.Errorf("unknown overflow policy: %v", o.cfg.policy))
	}
}
