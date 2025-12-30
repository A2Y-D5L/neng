package neng

import (
	"runtime"
)

// ExecOption configures a single Execute run.
type ExecOption func(*execConfig)

type execConfig struct {
	observer         Observer
	maxWorkers       int
	failFast         bool
	collectAllErrors bool
}

// WithMaxWorkers sets the maximum number of concurrent workers.
//
// Values <= 0 are normalized to runtime.NumCPU().
func WithMaxWorkers(n int) ExecOption {
	return func(c *execConfig) {
		c.maxWorkers = n
	}
}

// WithFailFast stops starting new tasks after the first non-cancellation failure.
//
// Tasks already running are allowed to complete.
func WithFailFast() ExecOption {
	return func(c *execConfig) {
		c.failFast = true
	}
}

// WithCollectAllErrors causes Execute to return all failures joined via errors.Join.
//
// When enabled, failures are wrapped with their task name and joined in
// deterministic order (sorted by task name).
func WithCollectAllErrors() ExecOption {
	return func(c *execConfig) {
		c.collectAllErrors = true
	}
}

// WithObserver attaches an observer to receive lifecycle events.
//
// Observers must be concurrency-safe; HandleEvent may be called concurrently.
// For production automation, observers should avoid blocking.
func WithObserver(o Observer) ExecOption {
	return func(c *execConfig) {
		if o == nil {
			return
		}
		if c.observer == nil {
			c.observer = o
			return
		}
		c.observer = MultiObserver(c.observer, o)
	}
}

func defaultExecConfig() execConfig {
	return execConfig{
		maxWorkers: runtime.NumCPU(),
	}
}
