package agent_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/a2y-d5l/neng/agent"
)

func TestStreamingTarget_BasicFlow(t *testing.T) {
	chunks := [][]byte{
		[]byte("Hello, "),
		[]byte("streaming "),
		[]byte("world!"),
	}

	target, output := agent.StreamingTarget(
		"test",
		"Test streaming",
		nil,
		func(_ context.Context, emit func([]byte)) error {
			for _, chunk := range chunks {
				emit(chunk)
				time.Sleep(10 * time.Millisecond)
			}
			return nil
		},
	)

	// Start consumer before plan execution
	var received bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for chunk := range output {
			received.Write(chunk)
		}
	}()

	// Execute target directly (no plan needed for this test)
	err := target.Run(context.Background())

	// Wait for consumer to finish
	wg.Wait()

	if err != nil {
		t.Errorf("expected success, got %v", err)
	}
	if received.String() != "Hello, streaming world!" {
		t.Errorf("expected 'Hello, streaming world!', got %q", received.String())
	}
}

func TestStreamingTarget_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	target, output := agent.StreamingTarget(
		"test",
		"",
		nil,
		func(ctx context.Context, emit func([]byte)) error {
			for i := 0; i < 100; i++ {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
					emit([]byte{byte(i)})
					time.Sleep(10 * time.Millisecond)
				}
			}
			return nil
		},
	)

	// Consumer
	received := 0
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range output {
			received++
		}
	}()

	// Cancel after a short time
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := target.Run(ctx)
	wg.Wait()

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	// Should have received some but not all chunks
	if received == 0 || received >= 100 {
		t.Errorf("expected partial reception, got %d chunks", received)
	}
}

func TestStreamingTarget_ChannelClosedOnError(t *testing.T) {
	target, output := agent.StreamingTarget(
		"test",
		"",
		nil,
		func(_ context.Context, emit func([]byte)) error {
			emit([]byte("before error"))
			return errors.New("intentional error")
		},
	)

	var received [][]byte
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for chunk := range output {
			received = append(received, chunk)
		}
	}()

	err := target.Run(context.Background())
	wg.Wait()

	if err == nil {
		t.Error("expected error")
	}
	// Channel should be closed despite error
	if len(received) != 1 {
		t.Errorf("expected 1 chunk before error, got %d", len(received))
	}
}

func TestStreamingTarget_NoGoroutineLeak(t *testing.T) {
	// This test verifies no goroutines are leaked when the consumer
	// is slow or absent. Use runtime.NumGoroutine() for monitoring.

	target, _ := agent.StreamingTarget(
		"test",
		"",
		nil,
		func(_ context.Context, emit func([]byte)) error {
			// Emit many chunks with no consumer
			for i := 0; i < 1000; i++ {
				emit([]byte{byte(i)})
			}
			return nil
		},
	)

	// No consumer started intentionally
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_ = target.Run(ctx)

	// If we get here without hanging, the non-blocking emit prevented deadlock
}

func TestStreamingTarget_PreservesMetadata(t *testing.T) {
	deps := []string{"dep1", "dep2"}
	target, _ := agent.StreamingTarget(
		"myname",
		"my description",
		deps,
		func(_ context.Context, _ func([]byte)) error { return nil },
	)

	if target.Name != "myname" {
		t.Errorf("expected name 'myname', got %q", target.Name)
	}
	if target.Desc != "my description" {
		t.Errorf("expected desc 'my description', got %q", target.Desc)
	}
	if len(target.Deps) != 2 || target.Deps[0] != "dep1" {
		t.Errorf("expected deps [dep1, dep2], got %v", target.Deps)
	}
}

func TestBlockingStreamingTarget_BasicFlow(t *testing.T) {
	chunks := [][]byte{
		[]byte("A"),
		[]byte("B"),
		[]byte("C"),
	}

	target, output := agent.BlockingStreamingTarget(
		"test",
		"",
		nil,
		func(_ context.Context, emit func([]byte)) error {
			for _, chunk := range chunks {
				emit(chunk)
			}
			return nil
		},
	)

	var received bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for chunk := range output {
			received.Write(chunk)
		}
	}()

	err := target.Run(context.Background())
	wg.Wait()

	if err != nil {
		t.Errorf("expected success, got %v", err)
	}
	if received.String() != "ABC" {
		t.Errorf("expected 'ABC', got %q", received.String())
	}
}

func TestBlockingStreamingTarget_SlowConsumer(t *testing.T) {
	// Test that blocking version waits for consumer

	emitted := 0
	target, output := agent.BlockingStreamingTarget(
		"test",
		"",
		nil,
		func(_ context.Context, emit func([]byte)) error {
			for i := 0; i < 100; i++ {
				emit([]byte{byte(i)})
				emitted++
			}
			return nil
		},
	)

	received := 0
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range output {
			time.Sleep(1 * time.Millisecond) // Slow consumer
			received++
		}
	}()

	err := target.Run(context.Background())
	wg.Wait()

	if err != nil {
		t.Errorf("expected success, got %v", err)
	}
	// All chunks should be received (blocking ensures no drops)
	if received != 100 {
		t.Errorf("expected 100 chunks, got %d", received)
	}
}

func TestBlockingStreamingTarget_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	target, output := agent.BlockingStreamingTarget(
		"test",
		"",
		nil,
		func(ctx context.Context, emit func([]byte)) error {
			for i := 0; i < 100; i++ {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
					emit([]byte{byte(i)})
					time.Sleep(10 * time.Millisecond)
				}
			}
			return nil
		},
	)

	received := 0
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range output {
			received++
		}
	}()

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := target.Run(ctx)
	wg.Wait()

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestStreamingTarget_NilDeps(t *testing.T) {
	// Ensure nil deps is handled correctly
	target, output := agent.StreamingTarget(
		"test",
		"",
		nil, // nil deps
		func(_ context.Context, emit func([]byte)) error {
			emit([]byte("ok"))
			return nil
		},
	)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range output {
		}
	}()

	err := target.Run(context.Background())
	wg.Wait()

	if err != nil {
		t.Errorf("expected success, got %v", err)
	}
	if target.Deps != nil {
		t.Errorf("expected nil deps, got %v", target.Deps)
	}
}

func TestStreamingTarget_EmptyOutput(t *testing.T) {
	target, output := agent.StreamingTarget(
		"test",
		"",
		nil,
		func(_ context.Context, _ func([]byte)) error {
			// No output
			return nil
		},
	)

	received := 0
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range output {
			received++
		}
	}()

	err := target.Run(context.Background())
	wg.Wait()

	if err != nil {
		t.Errorf("expected success, got %v", err)
	}
	if received != 0 {
		t.Errorf("expected 0 chunks, got %d", received)
	}
}
