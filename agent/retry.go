// Package agent provides resilience patterns and utilities for building
// LLM agent workflows on top of neng.
//
// This package implements the "wrapper pattern"—functions that wrap neng.Target
// to add behavior (retry, timeout, conditions) without modifying the core library.
//
// Example:
//
//	target := agent.WithRetry(
//	    agent.WithTimeout(baseTarget, 30*time.Second),
//	    agent.RetryPolicy{
//	        MaxRetries: 3,
//	        Backoff:    agent.ExponentialBackoff{Base: 100*time.Millisecond, Max: 10*time.Second},
//	    },
//	)
package agent

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"time"

	"github.com/a2y-d5l/neng"
)

// RetryPolicy configures retry behavior for a target.
type RetryPolicy struct {
	// MaxRetries is the maximum number of retry attempts after the initial failure.
	// A value of 0 means no retries (1 total attempt).
	// A value of 3 means up to 3 retries (4 total attempts).
	MaxRetries int

	// Backoff determines the delay between retry attempts.
	// If nil, defaults to no delay (immediate retry).
	Backoff BackoffStrategy

	// ShouldRetry determines if a given error should trigger a retry.
	// If nil, all non-nil errors are retried.
	//
	// Common implementations:
	//   - RetryOnAny: retry on any error
	//   - RetryOnTimeout: retry only on context.DeadlineExceeded
	//   - RetryOnTemporary: retry on errors with Temporary() bool method
	ShouldRetry func(error) bool

	// OnRetry is called before each retry attempt, providing observability.
	// The attempt parameter is 0-indexed (0 = first retry after initial failure).
	// Use this for logging, metrics, or custom event emission.
	//
	// Note: This runs synchronously; expensive operations will delay the retry.
	OnRetry func(attempt int, err error)
}

// BackoffStrategy determines delays between retry attempts.
type BackoffStrategy interface {
	// Next returns the delay before retry attempt `attempt` (0-indexed).
	// Returns (0, false) to signal that no more retries should occur.
	//
	// Example: For MaxRetries=3, Next is called with attempt=0, 1, 2.
	Next(attempt int) (delay time.Duration, shouldContinue bool)
}

// ConstantBackoff provides a fixed delay between retry attempts.
//
// Example:
//
//	backoff := ConstantBackoff{Delay: 1 * time.Second}
//	// Retry 1: wait 1s, Retry 2: wait 1s, Retry 3: wait 1s
type ConstantBackoff struct {
	Delay time.Duration
}

// Next returns the constant delay for any attempt.
func (c ConstantBackoff) Next(_ int) (time.Duration, bool) {
	return c.Delay, true
}

// ExponentialBackoff provides exponentially increasing delays with optional jitter.
//
// The delay for attempt n is: min(Base * Multiplier^n * jitterFactor, Max)
//
// Example:
//
//	backoff := ExponentialBackoff{
//	    Base:       100 * time.Millisecond,
//	    Max:        30 * time.Second,
//	    Multiplier: 2.0,  // Default if zero
//	    Jitter:     0.1,  // ±10% random variance
//	}
//	// Retry 1: ~100ms, Retry 2: ~200ms, Retry 3: ~400ms, ...
type ExponentialBackoff struct {
	// Base is the initial delay (e.g., 100ms).
	Base time.Duration

	// Max is the maximum delay cap. Delays will not exceed this value.
	Max time.Duration

	// Multiplier is the growth factor per attempt. Defaults to 2.0 if zero.
	Multiplier float64

	// Jitter adds random variance to prevent thundering herd.
	// A value of 0.1 means ±10% variance. Range: [0, 1].
	// 0 = deterministic delays (no jitter).
	Jitter float64
}

// Next returns the exponentially increasing delay for the given attempt.
func (e ExponentialBackoff) Next(attempt int) (time.Duration, bool) {
	multiplier := e.Multiplier
	if multiplier == 0 {
		multiplier = 2.0
	}

	// Calculate base delay: Base * Multiplier^attempt
	delay := float64(e.Base) * math.Pow(multiplier, float64(attempt))

	// Apply jitter: delay * (1 + jitter * random[-1, 1])
	if e.Jitter > 0 {
		//nolint:gosec // Jitter doesn't require cryptographic randomness
		jitterFactor := 1.0 + e.Jitter*(2*rand.Float64()-1)
		delay *= jitterFactor
	}

	// Cap at Max
	if e.Max > 0 && time.Duration(delay) > e.Max {
		delay = float64(e.Max)
	}

	return time.Duration(delay), true
}

// LinearBackoff provides linearly increasing delays.
//
// The delay for attempt n is: min(Initial + n*Increment, Max)
//
// Useful for rate-limit scenarios where exponential growth is too aggressive.
//
// Example:
//
//	backoff := LinearBackoff{
//	    Initial:   1 * time.Second,
//	    Increment: 1 * time.Second,
//	    Max:       10 * time.Second,
//	}
//	// Retry 1: 1s, Retry 2: 2s, Retry 3: 3s, ..., Retry 10+: 10s
type LinearBackoff struct {
	// Initial is the delay before the first retry.
	Initial time.Duration

	// Increment is added to the delay for each subsequent retry.
	Increment time.Duration

	// Max is the maximum delay cap.
	Max time.Duration
}

// Next returns the linearly increasing delay for the given attempt.
func (l LinearBackoff) Next(attempt int) (time.Duration, bool) {
	delay := l.Initial + time.Duration(attempt)*l.Increment

	if l.Max > 0 && delay > l.Max {
		delay = l.Max
	}

	return delay, true
}

// WithRetry wraps a target with retry logic.
//
// The wrapper:
//   - Executes the target up to MaxRetries+1 times (initial + retries)
//   - Checks context cancellation before each attempt and during backoff
//   - Invokes OnRetry callback before each retry (if provided)
//   - Respects ShouldRetry predicate (if nil, retries on any error)
//   - Returns the last error after all attempts are exhausted
//
// Example:
//
//	target := WithRetry(unreliableTarget, RetryPolicy{
//	    MaxRetries:  3,
//	    Backoff:     ExponentialBackoff{Base: 100*time.Millisecond, Max: 10*time.Second},
//	    ShouldRetry: RetryOnTimeout,
//	    OnRetry:     func(attempt int, err error) { log.Printf("retry %d: %v", attempt, err) },
//	})
func WithRetry(target neng.Target, policy RetryPolicy) neng.Target {
	return neng.Target{
		Name: target.Name,
		Desc: target.Desc,
		Deps: target.Deps,
		Run:  retryRunner(target, policy),
	}
}

// retryRunner creates the Run function for a retry-wrapped target.
func retryRunner(target neng.Target, policy RetryPolicy) func(context.Context) error {
	return func(ctx context.Context) error {
		var lastErr error

		for attempt := 0; attempt <= policy.MaxRetries; attempt++ {
			if err := ctx.Err(); err != nil {
				return err
			}

			lastErr = target.Run(ctx)
			if lastErr == nil {
				return nil
			}

			if !shouldRetryError(policy, lastErr) {
				return lastErr
			}

			if attempt == policy.MaxRetries {
				break
			}

			if err := waitForRetry(ctx, policy, attempt, lastErr); err != nil {
				return err
			}
		}

		return lastErr
	}
}

// shouldRetryError checks if the error should trigger a retry based on policy.
func shouldRetryError(policy RetryPolicy, err error) bool {
	if policy.ShouldRetry == nil {
		return true
	}
	return policy.ShouldRetry(err)
}

// waitForRetry handles the backoff delay and OnRetry callback between attempts.
func waitForRetry(ctx context.Context, policy RetryPolicy, attempt int, lastErr error) error {
	if policy.OnRetry != nil {
		policy.OnRetry(attempt, lastErr)
	}

	delay := getBackoffDelay(policy, attempt)
	if delay <= 0 {
		return nil
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}

// getBackoffDelay returns the delay for the given attempt, or 0 if no backoff or should stop.
func getBackoffDelay(policy RetryPolicy, attempt int) time.Duration {
	if policy.Backoff == nil {
		return 0
	}
	delay, shouldContinue := policy.Backoff.Next(attempt)
	if !shouldContinue {
		return 0
	}
	return delay
}

// RetryOnAny returns true for any non-nil error.
// Use this as a catch-all retry predicate.
func RetryOnAny(err error) bool {
	return err != nil
}

// RetryOnTimeout returns true for context.DeadlineExceeded errors.
// Useful when combined with WithTimeout for per-attempt timeouts.
func RetryOnTimeout(err error) bool {
	return errors.Is(err, context.DeadlineExceeded)
}

// RetryOnTemporary returns true for errors that implement the Temporary() bool method
// and return true. Many network errors implement this interface.
func RetryOnTemporary(err error) bool {
	type temporary interface {
		Temporary() bool
	}
	var t temporary
	if errors.As(err, &t) {
		return t.Temporary()
	}
	return false
}

// RetryNever always returns false. Use to explicitly disable retries
// while still using the retry infrastructure for observability.
func RetryNever(_ error) bool {
	return false
}

// RetryOn creates a predicate that retries on specific error types.
// Uses errors.Is for matching.
//
// Example:
//
//	policy.ShouldRetry = RetryOn(context.DeadlineExceeded, io.ErrUnexpectedEOF)
func RetryOn(targets ...error) func(error) bool {
	return func(err error) bool {
		for _, target := range targets {
			if errors.Is(err, target) {
				return true
			}
		}
		return false
	}
}
