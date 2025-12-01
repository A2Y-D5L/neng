package agent

import (
	"context"
	"sync"

	"github.com/a2y-d5l/neng"
)

// StreamingWork is the function signature for work that produces streaming output.
// The emit function sends chunks to consumers. It is safe to call concurrently.
type StreamingWork func(ctx context.Context, emit func([]byte)) error

// StreamingTarget creates a target with a channel for streaming output.
//
// Returns the neng.Target (for inclusion in a plan) and a receive-only channel
// that produces chunks as they are emitted.
//
// IMPORTANT: The consumer goroutine MUST be started BEFORE calling exec.Run().
// If no consumer is reading, the emit function may block or drop chunks.
//
// The output channel is closed when the target's Run function returns
// (successfully or with an error).
//
// Example:
//
//	target, output := agent.StreamingTarget(
//	    "generate",
//	    "Stream LLM response",
//	    nil, // no deps
//	    func(ctx context.Context, emit func([]byte)) error {
//	        for token := range llm.Stream(ctx, prompt) {
//	            emit([]byte(token))
//	        }
//	        return nil
//	    },
//	)
//
//	// Start consumer BEFORE Run
//	go func() {
//	    for chunk := range output {
//	        fmt.Print(string(chunk))
//	    }
//	}()
//
//	exec.Run(ctx, "generate")
func StreamingTarget(
	name, desc string,
	deps []string,
	work StreamingWork,
) (neng.Target, <-chan []byte) {
	chunks := make(chan []byte, 64) // Buffered to reduce blocking
	var closeOnce sync.Once

	target := neng.Target{
		Name: name,
		Desc: desc,
		Deps: deps,
		Run: func(ctx context.Context) error {
			// Ensure channel is closed exactly once when Run returns
			defer closeOnce.Do(func() { close(chunks) })

			emit := func(chunk []byte) {
				// Non-blocking send with context check
				select {
				case <-ctx.Done():
					// Context cancelled, drop chunk
				case chunks <- chunk:
					// Sent successfully
				default:
					// Buffer full, drop chunk (log warning in production)
				}
			}

			return work(ctx, emit)
		},
	}

	return target, chunks
}

// BlockingStreamingTarget is like StreamingTarget but blocks on emit if the
// buffer is full, rather than dropping chunks.
//
// Use this when chunk loss is unacceptable and the consumer can keep up.
// Be aware that a slow consumer will slow down the producer.
func BlockingStreamingTarget(
	name, desc string,
	deps []string,
	work StreamingWork,
) (neng.Target, <-chan []byte) {
	chunks := make(chan []byte, 64)
	var closeOnce sync.Once

	target := neng.Target{
		Name: name,
		Desc: desc,
		Deps: deps,
		Run: func(ctx context.Context) error {
			defer closeOnce.Do(func() { close(chunks) })

			emit := func(chunk []byte) {
				select {
				case <-ctx.Done():
					// Context cancelled, drop
				case chunks <- chunk:
					// Sent (may block if buffer full)
				}
			}

			return work(ctx, emit)
		},
	}

	return target, chunks
}
