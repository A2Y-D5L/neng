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

func TestWithRetry_SuccessNoRetry(t *testing.T) {
	var attempts int32
	target := neng.Target{
		Name: "test",
		Run: func(_ context.Context) error {
			atomic.AddInt32(&attempts, 1)
			return nil
		},
	}

	wrapped := agent.WithRetry(target, agent.RetryPolicy{MaxRetries: 3})
	err := wrapped.Run(context.Background())

	if err != nil {
		t.Errorf("expected success, got %v", err)
	}
	if attempts != 1 {
		t.Errorf("expected 1 attempt, got %d", attempts)
	}
}

func TestWithRetry_SuccessAfterTransientFailure(t *testing.T) {
	var attempts int32
	target := neng.Target{
		Name: "test",
		Run: func(_ context.Context) error {
			n := atomic.AddInt32(&attempts, 1)
			if n < 3 {
				return errors.New("transient")
			}
			return nil
		},
	}

	wrapped := agent.WithRetry(target, agent.RetryPolicy{
		MaxRetries: 5,
		Backoff:    agent.ConstantBackoff{Delay: time.Millisecond},
	})
	err := wrapped.Run(context.Background())

	if err != nil {
		t.Errorf("expected success after retries, got %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestWithRetry_MaxRetriesExhausted(t *testing.T) {
	var attempts int32
	errFail := errors.New("always fails")
	target := neng.Target{
		Name: "test",
		Run: func(_ context.Context) error {
			atomic.AddInt32(&attempts, 1)
			return errFail
		},
	}

	wrapped := agent.WithRetry(target, agent.RetryPolicy{
		MaxRetries: 3,
		Backoff:    agent.ConstantBackoff{Delay: time.Millisecond},
	})
	err := wrapped.Run(context.Background())

	if !errors.Is(err, errFail) {
		t.Errorf("expected original error, got %v", err)
	}
	if attempts != 4 { // 1 initial + 3 retries
		t.Errorf("expected 4 attempts (1+3), got %d", attempts)
	}
}

func TestWithRetry_ZeroMaxRetries(t *testing.T) {
	var attempts int32
	target := neng.Target{
		Name: "test",
		Run: func(_ context.Context) error {
			atomic.AddInt32(&attempts, 1)
			return errors.New("fail")
		},
	}

	wrapped := agent.WithRetry(target, agent.RetryPolicy{MaxRetries: 0})
	_ = wrapped.Run(context.Background())

	if attempts != 1 {
		t.Errorf("expected exactly 1 attempt with MaxRetries=0, got %d", attempts)
	}
}

func TestWithRetry_ShouldRetryPredicate(t *testing.T) {
	errRetryable := errors.New("retryable")
	errFatal := errors.New("fatal")

	var attempts int32
	target := neng.Target{
		Name: "test",
		Run: func(_ context.Context) error {
			n := atomic.AddInt32(&attempts, 1)
			if n == 1 {
				return errRetryable
			}
			return errFatal
		},
	}

	wrapped := agent.WithRetry(target, agent.RetryPolicy{
		MaxRetries: 5,
		Backoff:    agent.ConstantBackoff{Delay: time.Millisecond},
		ShouldRetry: func(err error) bool {
			return errors.Is(err, errRetryable)
		},
	})
	err := wrapped.Run(context.Background())

	// Should stop on fatal error (non-retryable)
	if !errors.Is(err, errFatal) {
		t.Errorf("expected fatal error, got %v", err)
	}
	if attempts != 2 {
		t.Errorf("expected 2 attempts (stop on non-retryable), got %d", attempts)
	}
}

func TestWithRetry_OnRetryCallback(t *testing.T) {
	var callbacks []int
	target := neng.Target{
		Name: "test",
		Run: func(_ context.Context) error {
			return errors.New("fail")
		},
	}

	wrapped := agent.WithRetry(target, agent.RetryPolicy{
		MaxRetries: 3,
		Backoff:    agent.ConstantBackoff{Delay: time.Millisecond},
		OnRetry: func(attempt int, _ error) {
			callbacks = append(callbacks, attempt)
		},
	})
	_ = wrapped.Run(context.Background())

	// OnRetry called for attempts 0, 1, 2 (before each retry)
	expected := []int{0, 1, 2}
	if len(callbacks) != len(expected) {
		t.Errorf("expected %d callbacks, got %d", len(expected), len(callbacks))
	}
	for i, v := range callbacks {
		if v != expected[i] {
			t.Errorf("callback %d: expected attempt %d, got %d", i, expected[i], v)
		}
	}
}

func TestWithRetry_PreservesTargetMetadata(t *testing.T) {
	target := neng.Target{
		Name: "original",
		Desc: "Original description",
		Deps: []string{"dep1", "dep2"},
		Run:  func(_ context.Context) error { return nil },
	}

	wrapped := agent.WithRetry(target, agent.RetryPolicy{MaxRetries: 3})

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

// Backoff Strategy Tests

func TestConstantBackoff(t *testing.T) {
	backoff := agent.ConstantBackoff{Delay: 100 * time.Millisecond}

	for attempt := 0; attempt < 5; attempt++ {
		delay, cont := backoff.Next(attempt)
		if !cont {
			t.Errorf("expected shouldContinue=true")
		}
		if delay != 100*time.Millisecond {
			t.Errorf("attempt %d: expected 100ms, got %v", attempt, delay)
		}
	}
}

func TestExponentialBackoff_Growth(t *testing.T) {
	backoff := agent.ExponentialBackoff{
		Base:       100 * time.Millisecond,
		Max:        10 * time.Second,
		Multiplier: 2.0,
		Jitter:     0, // Deterministic
	}

	expected := []time.Duration{
		100 * time.Millisecond,  // 100 * 2^0
		200 * time.Millisecond,  // 100 * 2^1
		400 * time.Millisecond,  // 100 * 2^2
		800 * time.Millisecond,  // 100 * 2^3
		1600 * time.Millisecond, // 100 * 2^4
	}

	for attempt, want := range expected {
		delay, _ := backoff.Next(attempt)
		if delay != want {
			t.Errorf("attempt %d: expected %v, got %v", attempt, want, delay)
		}
	}
}

func TestExponentialBackoff_MaxCap(t *testing.T) {
	backoff := agent.ExponentialBackoff{
		Base:       1 * time.Second,
		Max:        5 * time.Second,
		Multiplier: 2.0,
		Jitter:     0,
	}

	// After a few attempts, delay should be capped at Max
	delay, _ := backoff.Next(10) // 1s * 2^10 = 1024s, but capped at 5s
	if delay != 5*time.Second {
		t.Errorf("expected delay capped at 5s, got %v", delay)
	}
}

func TestExponentialBackoff_Jitter(t *testing.T) {
	backoff := agent.ExponentialBackoff{
		Base:       1 * time.Second,
		Max:        10 * time.Second,
		Multiplier: 2.0,
		Jitter:     0.5, // ±50%
	}

	// Run multiple times and verify variance
	var delays []time.Duration
	for range 100 {
		delay, _ := backoff.Next(0)
		delays = append(delays, delay)
	}

	// Check that we have variance (not all same)
	allSame := true
	for _, d := range delays {
		if d != delays[0] {
			allSame = false
			break
		}
	}
	if allSame {
		t.Error("expected jitter to produce varying delays")
	}

	// Check bounds: 1s ± 50% = [0.5s, 1.5s]
	for _, d := range delays {
		if d < 500*time.Millisecond || d > 1500*time.Millisecond {
			t.Errorf("delay %v outside expected jitter range [0.5s, 1.5s]", d)
		}
	}
}

func TestExponentialBackoff_DefaultMultiplier(t *testing.T) {
	backoff := agent.ExponentialBackoff{
		Base:       100 * time.Millisecond,
		Multiplier: 0, // Should default to 2.0
		Jitter:     0,
	}

	delay, _ := backoff.Next(1)
	expected := 200 * time.Millisecond // 100ms * 2^1
	if delay != expected {
		t.Errorf("expected default multiplier 2.0 to give %v, got %v", expected, delay)
	}
}

func TestLinearBackoff(t *testing.T) {
	backoff := agent.LinearBackoff{
		Initial:   100 * time.Millisecond,
		Increment: 100 * time.Millisecond,
		Max:       500 * time.Millisecond,
	}

	expected := []time.Duration{
		100 * time.Millisecond, // 100 + 0*100
		200 * time.Millisecond, // 100 + 1*100
		300 * time.Millisecond, // 100 + 2*100
		400 * time.Millisecond, // 100 + 3*100
		500 * time.Millisecond, // 100 + 4*100 (at max)
		500 * time.Millisecond, // capped
	}

	for attempt, want := range expected {
		delay, _ := backoff.Next(attempt)
		if delay != want {
			t.Errorf("attempt %d: expected %v, got %v", attempt, want, delay)
		}
	}
}

func TestLinearBackoff_NoMax(t *testing.T) {
	backoff := agent.LinearBackoff{
		Initial:   100 * time.Millisecond,
		Increment: 100 * time.Millisecond,
		Max:       0, // No cap
	}

	delay, _ := backoff.Next(100)
	expected := 100*time.Millisecond + 100*100*time.Millisecond // 10.1 seconds
	if delay != expected {
		t.Errorf("expected %v without cap, got %v", expected, delay)
	}
}

// Context Cancellation Tests

func TestWithRetry_ContextCancelledBeforeFirstAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	var attempts int32
	target := neng.Target{
		Name: "test",
		Run: func(_ context.Context) error {
			atomic.AddInt32(&attempts, 1)
			return nil
		},
	}

	wrapped := agent.WithRetry(target, agent.RetryPolicy{MaxRetries: 3})
	err := wrapped.Run(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if attempts != 0 {
		t.Errorf("expected 0 attempts (cancelled before start), got %d", attempts)
	}
}

func TestWithRetry_ContextCancelledDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var attempts int32
	target := neng.Target{
		Name: "test",
		Run: func(_ context.Context) error {
			n := atomic.AddInt32(&attempts, 1)
			if n == 1 {
				// Cancel during first backoff wait
				go func() {
					time.Sleep(50 * time.Millisecond)
					cancel()
				}()
			}
			return errors.New("fail")
		},
	}

	wrapped := agent.WithRetry(target, agent.RetryPolicy{
		MaxRetries: 5,
		Backoff:    agent.ConstantBackoff{Delay: 1 * time.Second}, // Long backoff
	})
	err := wrapped.Run(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled during backoff, got %v", err)
	}
	if attempts != 1 {
		t.Errorf("expected 1 attempt (cancelled during backoff), got %d", attempts)
	}
}

// Retry Predicate Tests

func TestRetryOnAny(t *testing.T) {
	if !agent.RetryOnAny(errors.New("any error")) {
		t.Error("RetryOnAny should return true for any error")
	}
	if agent.RetryOnAny(nil) {
		t.Error("RetryOnAny should return false for nil")
	}
}

func TestRetryOnTimeout(t *testing.T) {
	if !agent.RetryOnTimeout(context.DeadlineExceeded) {
		t.Error("RetryOnTimeout should return true for DeadlineExceeded")
	}
	if agent.RetryOnTimeout(context.Canceled) {
		t.Error("RetryOnTimeout should return false for Canceled")
	}
	if agent.RetryOnTimeout(errors.New("other")) {
		t.Error("RetryOnTimeout should return false for other errors")
	}
}

func TestRetryOnTemporary(t *testing.T) {
	// Create a temporary error
	tempErr := &temporaryError{temp: true}
	if !agent.RetryOnTemporary(tempErr) {
		t.Error("RetryOnTemporary should return true for temporary error")
	}

	nonTempErr := &temporaryError{temp: false}
	if agent.RetryOnTemporary(nonTempErr) {
		t.Error("RetryOnTemporary should return false for non-temporary error")
	}

	regularErr := errors.New("regular")
	if agent.RetryOnTemporary(regularErr) {
		t.Error("RetryOnTemporary should return false for error without Temporary()")
	}
}

type temporaryError struct {
	temp bool
}

func (e *temporaryError) Error() string   { return "temporary error" }
func (e *temporaryError) Temporary() bool { return e.temp }

func TestRetryNever(t *testing.T) {
	if agent.RetryNever(errors.New("any")) {
		t.Error("RetryNever should always return false")
	}
	if agent.RetryNever(nil) {
		t.Error("RetryNever should return false even for nil")
	}
}

func TestRetryOn(t *testing.T) {
	errA := errors.New("error A")
	errB := errors.New("error B")
	errC := errors.New("error C")

	predicate := agent.RetryOn(errA, errB)

	if !predicate(errA) {
		t.Error("RetryOn should return true for errA")
	}
	if !predicate(errB) {
		t.Error("RetryOn should return true for errB")
	}
	if predicate(errC) {
		t.Error("RetryOn should return false for errC")
	}
}

func TestRetryOn_WrappedErrors(t *testing.T) {
	errBase := errors.New("base error")
	wrappedErr := errors.Join(errors.New("wrapper"), errBase)

	predicate := agent.RetryOn(errBase)

	if !predicate(wrappedErr) {
		t.Error("RetryOn should match wrapped errors using errors.Is")
	}
}

// Integration test: No backoff (immediate retry)
func TestWithRetry_NoBackoff(t *testing.T) {
	var attempts int32
	target := neng.Target{
		Name: "test",
		Run: func(_ context.Context) error {
			atomic.AddInt32(&attempts, 1)
			return errors.New("fail")
		},
	}

	start := time.Now()
	wrapped := agent.WithRetry(target, agent.RetryPolicy{
		MaxRetries: 3,
		Backoff:    nil, // No backoff
	})
	_ = wrapped.Run(context.Background())
	elapsed := time.Since(start)

	if attempts != 4 {
		t.Errorf("expected 4 attempts, got %d", attempts)
	}
	// Should be very fast without backoff
	if elapsed > 100*time.Millisecond {
		t.Errorf("expected fast execution without backoff, took %v", elapsed)
	}
}
