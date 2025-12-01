package agent

import (
	"context"

	"github.com/a2y-d5l/neng"
)

// WithCondition wraps a target to only execute if the condition returns true.
// If the condition returns false, the target succeeds immediately (returns nil).
//
// This is useful for implementing optional targets:
//   - "Run fallback search only if primary search returned no results"
//   - "Run validation only in production mode"
//   - "Skip expensive processing if quick check indicates it's unnecessary"
//
// The condition closure typically captures a *Results to check previous outputs:
//
//	target := agent.WithCondition(
//	    neng.Target{Name: "fallback_search", ...},
//	    func() bool {
//	        hits, ok := agent.Load[[]SearchHit](results, "primary_search")
//	        return !ok || len(hits) == 0
//	    },
//	)
//
// Semantic note: When the condition returns false, the target is marked as
// "completed successfully" (not "skipped"). This is intentional—from the
// executor's perspective, the target ran and returned nil. If you need to
// track whether the actual work ran, set a flag in the Results container.
func WithCondition(target neng.Target, condition func() bool) neng.Target {
	return neng.Target{
		Name: target.Name,
		Desc: target.Desc,
		Deps: target.Deps,
		Run: func(ctx context.Context) error {
			if !condition() {
				return nil // Skip silently as success
			}
			return target.Run(ctx)
		},
	}
}

// ConditionFromResults creates a condition that checks a Results container.
// Reduces boilerplate for common patterns.
//
// The check function receives the value from the Results container and returns
// whether the target should run.
//
// Example usage:
//
//	target := WithCondition(
//	    fallbackTarget,
//	    ConditionFromResults(results, "primary_search", func(hits []SearchHit) bool {
//	        return len(hits) == 0 // Run fallback if no hits
//	    }),
//	)
func ConditionFromResults[T any](
	results *Results,
	key string,
	check func(T) bool,
) func() bool {
	return func() bool {
		v, ok := Load[T](results, key)
		if !ok {
			return false // Key missing or wrong type
		}
		return check(v)
	}
}
