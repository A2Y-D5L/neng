package stream

// OverflowPolicy controls how an Observer behaves when buffers are full.
type OverflowPolicy uint8

const (
	// DropNewest drops the newest item when the channel buffer is full.
	//
	// This policy never blocks the execution engine and is typically the best
	// default for production automation.
	DropNewest OverflowPolicy = iota

	// DropOldest drops one buffered item to make room for the newest item.
	//
	// This policy never blocks and is useful for TUIs that prefer "latest state".
	DropOldest

	// Block blocks in HandleEvent until the consumer receives.
	//
	// This policy may slow execution and is best suited for tests/debugging when
	// fidelity is more important than throughput.
	Block
)

const (
	defaultEventBufSize  = 1024
	defaultResultBufSize = 1024
	defaultInboxSize     = 64
)

// Option configures an Observer.
type Option func(*config)

type config struct {
	eventBuf  int
	resultBuf int
	policy    OverflowPolicy
}

// WithEventBuffer sets the event channel buffer size.
func WithEventBuffer(n int) Option {
	return func(c *config) {
		c.eventBuf = n
	}
}

// WithResultBuffer sets the result channel buffer size.
func WithResultBuffer(n int) Option {
	return func(c *config) {
		c.resultBuf = n
	}
}

// WithOverflowPolicy sets the overflow policy.
func WithOverflowPolicy(p OverflowPolicy) Option {
	return func(c *config) {
		c.policy = p
	}
}
