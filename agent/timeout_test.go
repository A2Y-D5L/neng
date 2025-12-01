package agent_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a2y-d5l/neng"
	"github.com/a2y-d5l/neng/agent"
)

func TestWithTimeout_CompletesBeforeTimeout(t *testing.T) {
	target := neng.Task{
		Name: "fast",
		Run: func(_ context.Context) error {
			time.Sleep(10 * time.Millisecond)
			return nil
		},
	}

	wrapped := agent.WithTimeout(target, 1*time.Second)
	err := wrapped.Run(context.Background())

	if err != nil {
		t.Errorf("expected success, got %v", err)
	}
}

func TestWithTimeout_ExceedsTimeout(t *testing.T) {
	target := neng.Task{
		Name: "slow",
		Run: func(ctx context.Context) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(10 * time.Second):
				return nil
			}
		},
	}

	wrapped := agent.WithTimeout(target, 50*time.Millisecond)
	err := wrapped.Run(context.Background())

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
}

func TestWithTimeout_ZeroIsNoOp(t *testing.T) {
	target := neng.Task{
		Name: "test",
		Run: func(ctx context.Context) error {
			if _, ok := ctx.Deadline(); ok {
				return errors.New("unexpected deadline")
			}
			return nil
		},
	}

	wrapped := agent.WithTimeout(target, 0)
	err := wrapped.Run(context.Background())

	if err != nil {
		t.Errorf("expected no-op, got %v", err)
	}
}

func TestWithTimeout_NegativeIsNoOp(t *testing.T) {
	target := neng.Task{
		Name: "test",
		Run: func(ctx context.Context) error {
			if _, ok := ctx.Deadline(); ok {
				return errors.New("unexpected deadline")
			}
			return nil
		},
	}

	wrapped := agent.WithTimeout(target, -1*time.Second)
	err := wrapped.Run(context.Background())

	if err != nil {
		t.Errorf("expected no-op for negative, got %v", err)
	}
}

func TestWithTimeout_RespectsParentContext(t *testing.T) {
	target := neng.Task{
		Name: "test",
		Run: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}

	wrapped := agent.WithTimeout(target, 10*time.Second) // Long timeout

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := wrapped.Run(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected parent cancellation, got %v", err)
	}
}

func TestWithTimeout_PreservesTargetMetadata(t *testing.T) {
	target := neng.Task{
		Name: "original",
		Desc: "Original description",
		Deps: []string{"dep1", "dep2"},
		Run:  func(_ context.Context) error { return nil },
	}

	wrapped := agent.WithTimeout(target, 1*time.Second)

	if wrapped.Name != "original" {
		t.Errorf("expected Name 'original', got %q", wrapped.Name)
	}
	if wrapped.Desc != "Original description" {
		t.Errorf("expected Desc 'Original description', got %q", wrapped.Desc)
	}
	if len(wrapped.Deps) != 2 || wrapped.Deps[0] != "dep1" || wrapped.Deps[1] != "dep2" {
		t.Errorf("expected Deps [dep1, dep2], got %v", wrapped.Deps)
	}
}

func TestWithTimeout_PropagatesError(t *testing.T) {
	expectedErr := errors.New("target error")
	target := neng.Task{
		Name: "test",
		Run: func(_ context.Context) error {
			return expectedErr
		},
	}

	wrapped := agent.WithTimeout(target, 1*time.Second)
	err := wrapped.Run(context.Background())

	if !errors.Is(err, expectedErr) {
		t.Errorf("expected original error, got %v", err)
	}
}

// Composition Tests

func TestComposition_RetryWithTimeout(t *testing.T) {
	var attempts int32
	target := neng.Task{
		Name: "flaky",
		Run: func(ctx context.Context) error {
			n := atomic.AddInt32(&attempts, 1)
			if n < 3 {
				// First two attempts time out
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(1 * time.Second):
					return nil
				}
			}
			// Third attempt succeeds quickly
			return nil
		},
	}

	// Each attempt has 50ms timeout, retry up to 3 times
	wrapped := agent.WithRetry(
		agent.WithTimeout(target, 50*time.Millisecond),
		agent.RetryPolicy{
			MaxRetries:  3,
			Backoff:     agent.ConstantBackoff{Delay: 10 * time.Millisecond},
			ShouldRetry: agent.RetryOnTimeout,
		},
	)

	err := wrapped.Run(context.Background())

	if err != nil {
		t.Errorf("expected success after retries, got %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestComposition_TimeoutWithRetry_PerAttemptTimeout(t *testing.T) {
	// Test that each retry attempt gets its own fresh timeout
	var attempts int32
	var attemptTimes []time.Duration

	target := neng.Task{
		Name: "test",
		Run: func(ctx context.Context) error {
			start := time.Now()
			n := atomic.AddInt32(&attempts, 1)

			// Each attempt blocks until timeout
			<-ctx.Done()
			attemptTimes = append(attemptTimes, time.Since(start))

			if n < 3 {
				return ctx.Err()
			}
			// On third attempt, we still time out but check elapsed time
			return ctx.Err()
		},
	}

	timeout := 30 * time.Millisecond
	wrapped := agent.WithRetry(
		agent.WithTimeout(target, timeout),
		agent.RetryPolicy{
			MaxRetries:  2, // Total 3 attempts
			Backoff:     agent.ConstantBackoff{Delay: 5 * time.Millisecond},
			ShouldRetry: agent.RetryOnTimeout,
		},
	)

	err := wrapped.Run(context.Background())

	// Should fail after all retries
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}

	// Verify each attempt had its own timeout (roughly 30ms each)
	for i, elapsed := range attemptTimes {
		// Allow some tolerance
		if elapsed < 20*time.Millisecond || elapsed > 100*time.Millisecond {
			t.Errorf("attempt %d: expected ~30ms timeout, got %v", i, elapsed)
		}
	}
}

func TestComposition_DoubleWrap(t *testing.T) {
	// Test wrapping WithTimeout twice (outer should take precedence if shorter)
	target := neng.Task{
		Name: "test",
		Run: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}

	// Inner timeout: 100ms, Outer timeout: 50ms
	// Outer (shorter) should win
	wrapped := agent.WithTimeout(
		agent.WithTimeout(target, 100*time.Millisecond),
		50*time.Millisecond,
	)

	start := time.Now()
	err := wrapped.Run(context.Background())
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}

	// Should timeout in ~50ms (outer timeout)
	if elapsed > 80*time.Millisecond {
		t.Errorf("expected ~50ms timeout, got %v", elapsed)
	}
}
