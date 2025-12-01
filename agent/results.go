package agent

import (
	"fmt"
	"sync"
)

// Results provides thread-safe storage for passing data between targets.
//
// Unlike closures alone, Results provides:
//   - Type-safe access via generic Store/Load functions
//   - Explicit key management (keys are visible in code)
//   - Thread-safe concurrent access
//
// Usage pattern:
//
//	results := agent.NewResults()
//
//	plan, _ := neng.BuildPlan(
//	    neng.Target{
//	        Name: "search",
//	        Run: func(ctx context.Context) error {
//	            hits := doSearch("query")
//	            agent.Store(results, "search", hits)
//	            return nil
//	        },
//	    },
//	    neng.Target{
//	        Name: "summarize",
//	        Deps: []string{"search"},
//	        Run: func(ctx context.Context) error {
//	            hits := agent.MustLoad[[]SearchHit](results, "search")
//	            // ... use hits
//	            return nil
//	        },
//	    },
//	)
type Results struct {
	mu   sync.RWMutex
	data map[string]any
}

// NewResults creates an empty Results container.
func NewResults() *Results {
	return &Results{
		data: make(map[string]any),
	}
}

// Store saves a value under the given key.
// The type T is inferred from the value at the call site.
//
// Thread-safe: can be called concurrently from multiple targets.
func Store[T any](r *Results, key string, value T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[key] = value
}

// Load retrieves a value by key with type assertion.
// Returns (value, true) if found and type matches.
// Returns (zero, false) if key is missing or type doesn't match.
//
// Thread-safe: can be called concurrently from multiple targets.
//
// Example:
//
//	if hits, ok := agent.Load[[]SearchHit](results, "search"); ok {
//	    // use hits
//	}
func Load[T any](r *Results, key string) (T, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	v, ok := r.data[key]
	if !ok {
		var zero T
		return zero, false
	}

	typed, ok := v.(T)
	if !ok {
		var zero T
		return zero, false
	}

	return typed, true
}

// MustLoad retrieves a value by key with type assertion, or panics.
// Use this when the DAG structure guarantees the key exists
// (e.g., the current target depends on the target that stored the value).
//
// Panics if:
//   - Key does not exist
//   - Value type doesn't match T
//
// Example:
//
//	// Safe because "search" is in Deps
//	hits := agent.MustLoad[[]SearchHit](results, "search")
func MustLoad[T any](r *Results, key string) T {
	v, ok := Load[T](r, key)
	if !ok {
		panic(fmt.Sprintf("results: key %q not found or wrong type (expected %T)", key, *new(T)))
	}
	return v
}

// Has returns true if the key exists in the container.
// Does not check the value's type.
func (r *Results) Has(key string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.data[key]
	return ok
}

// Keys returns all keys currently stored.
// The order is not guaranteed.
func (r *Results) Keys() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	keys := make([]string, 0, len(r.data))
	for k := range r.data {
		keys = append(keys, k)
	}
	return keys
}

// Clear removes all stored values.
// Useful for resetting state between test runs or plan re-executions.
func (r *Results) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data = make(map[string]any)
}

// Len returns the number of stored values.
func (r *Results) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.data)
}

// Delete removes a value by key.
// No-op if key doesn't exist.
func (r *Results) Delete(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.data, key)
}
