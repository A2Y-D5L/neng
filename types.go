package neng

import (
	"context"
	"strconv"
	"strings"
	"time"
)

type Stage struct {
	Index   int
	Targets []string
}

func (s Stage) String() string {
	return "Stage " + strconv.Itoa(s.Index) + ": [" + strings.Join(s.Targets, ", ") + "]"
}

// Target describes a single logical CI step.
type Target struct {
	// Name must be unique within the plan.
	Name string `json:"name,omitempty"`
	// Desc is human-readable documentation.
	Desc string `json:"desc,omitempty"`
	// Deps are the names of other targets that must complete first.
	Deps []string `json:"deps,omitempty"`
	// Run is the work for this target.
	Run func(ctx context.Context) error `json:"-"`
}

// Result captures the outcome of a single target execution.
type Result struct {
	// Name is the logical target name.
	Name string `json:"name,omitempty"`

	// Err is the error returned from the Run function, if any.
	Err error `json:"err,omitempty"`

	// StartedAt is when the target began running.
	StartedAt time.Time `json:"started_at"`

	// CompletedAt is when the target finished running.
	CompletedAt time.Time `json:"completed_at"`

	// Skipped indicates the target did not run because of upstream
	// failure or cancellation.
	Skipped bool `json:"skipped,omitempty"`
}

type targetStatus uint8

const (
	statusPending targetStatus = iota
	statusRunning
	statusCompleted
	statusFailed
	statusSkipped
)

// EventType describes the type of lifecycle event for a target.
type EventType int

const (
	EventTargetStarted EventType = iota
	EventTargetCompleted
	EventTargetSkipped
)

// Event is a fire-and-forget notification about target lifecycle.
type Event struct {
	Type   EventType `json:"type,omitempty"`
	Time   time.Time `json:"time"`
	Target *Target   `json:"target,omitempty"`
	Result *Result   `json:"result,omitempty"` // nil unless Completed/Skipped
}

// EventHandler can be implemented by callers who want a pluggable way to
// observe events (e.g. TUIs, JSON loggers, tests).
type EventHandler interface {
	HandleEvent(e Event)
}
