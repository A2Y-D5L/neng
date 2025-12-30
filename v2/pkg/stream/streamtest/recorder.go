package streamtest

import (
	"sync"

	"github.com/a2y-d5l/neng/v2"
)

// EventRecorder records events for tests and diagnostics.
//
// EventRecorder is safe under concurrent HandleEvent calls.
type EventRecorder struct {
	events []neng.Event
	mu     sync.Mutex
}

// NewEventRecorder constructs a StreamRecorder.
func NewEventRecorder() *EventRecorder {
	return &EventRecorder{}
}

// HandleEvent appends the event to the recorder.
func (r *EventRecorder) HandleEvent(e neng.Event) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
}

// Events returns a snapshot copy of recorded events.
func (r *EventRecorder) Events() []neng.Event {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]neng.Event, len(r.events))
	copy(cp, r.events)
	return cp
}

// Results returns a snapshot copy of all Result payloads from finished events.
//
// This is a convenience helper; results are extracted in recorded event order.
func (r *EventRecorder) Results() []neng.Result {
	if r == nil {
		return nil
	}
	evs := r.Events()
	out := make([]neng.Result, 0, len(evs))
	for _, e := range evs {
		if e.Result != nil {
			out = append(out, *e.Result)
		}
	}
	return out
}

// Reset clears the recorder.
func (r *EventRecorder) Reset() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.events = nil
	r.mu.Unlock()
}
