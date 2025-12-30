// Package stream provides first-party observation utilities.
//
// The execution engine emits neng.Event values to a neng.Observer. This package
// supplies adapters that make observation ergonomic in Go without baking channel
// semantics into the execution engine.
//
// # Observer
//
// Observer implements neng.Observer and forwards events/results to channels.
// It supports explicit buffering and overflow policies.
//
// Overflow policies:
//   - DropNewest: never blocks the engine; drops newest items when buffers fill.
//   - DropOldest: never blocks the engine; removes one buffered item to keep the newest.
//   - Block: blocks in HandleEvent until the consumer receives; best for tests/debug.
//
// Drop counts are exposed via Drops.
//
// # Start
//
// Start runs execution.Plan.Execute in a goroutine and exposes channels via a
// Handle. It is a convenience for channel-based consumption.
//
// The engine's observer guarantees still apply (HandleEvent may be called
// concurrently), and Observer is safe under concurrency.
package stream
