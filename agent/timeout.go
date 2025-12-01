package agent

import (
	"context"
	"time"

	"github.com/a2y-d5l/neng"
)

// WithTimeout wraps a target with a per-execution timeout.
//
// If the target's Run function does not complete within the specified duration,
// the context is cancelled and context.DeadlineExceeded is returned.
//
// Behavior:
//   - timeout <= 0: returns target unchanged (no timeout applied)
//   - timeout > 0: wraps with context.WithTimeout
//
// The timeout respects the parent context's deadline. If the parent context
// has a shorter deadline, that deadline takes precedence.
//
// Common composition patterns:
//
//	// Per-attempt timeout with retry (each attempt gets fresh timeout)
//	target = WithRetry(WithTimeout(target, 5*time.Second), policy)
//
//	// Total timeout across all retries (use parent context)
//	ctx, cancel := context.WithTimeout(parentCtx, 30*time.Second)
//	defer cancel()
//	exec.Run(ctx, roots...)
func WithTimeout(target neng.Task, timeout time.Duration) neng.Task {
	if timeout <= 0 {
		return target
	}

	return neng.Task{
		Name: target.Name,
		Desc: target.Desc,
		Deps: target.Deps,
		Run: func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			return target.Run(ctx)
		},
	}
}
