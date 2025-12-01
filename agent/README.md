# Agent Extension for neng

This package provides resilience patterns and utilities for building LLM agent
workflows on top of neng's DAG execution engine.

## Philosophy

This package follows the **wrapper pattern**: functions that wrap `neng.Target`
to add behavior (retry, timeout, conditions) without modifying the core library.

Two principles guide what belongs here vs. in the core:

1. **The Wrapper Test**: Can this feature be implemented as a wrapper around
   `Target.Run`? If yes, it belongs here.
2. **The Generality Test**: Is this feature universally useful (CI/CD, builds,
   data pipelines) or domain-specific (LLM agents)? Domain-specific features
   belong here.

## Quick Start

### Retry with Exponential Backoff

```go
import "github.com/a2y-d5l/neng/agent"

target := agent.WithRetry(unreliableTarget, agent.RetryPolicy{
    MaxRetries: 3,
    Backoff: agent.ExponentialBackoff{
        Base:   100 * time.Millisecond,
        Max:    10 * time.Second,
        Jitter: 0.1,
    },
    ShouldRetry: agent.RetryOnTimeout,
    OnRetry: func(attempt int, err error) {
        log.Printf("Retry %d after error: %v", attempt, err)
    },
})
```

### Per-Attempt Timeout

```go
// Each attempt has 30 seconds to complete
target := agent.WithRetry(
    agent.WithTimeout(baseTarget, 30*time.Second),
    policy,
)
```

### Composition Order

Wrappers are applied from innermost to outermost:

```go
// Order: timeout → retry → condition
target := agent.WithCondition(
    agent.WithRetry(
        agent.WithTimeout(baseTarget, 30*time.Second),
        retryPolicy,
    ),
    condition,
)
```

## Wrappers

### WithRetry

Wraps a target with configurable retry logic:

```go
target := agent.WithRetry(unreliableTarget, agent.RetryPolicy{
    MaxRetries:  3,                    // Retry up to 3 times (4 total attempts)
    Backoff:     backoffStrategy,      // Delay between retries
    ShouldRetry: agent.RetryOnTimeout, // Only retry on timeouts
    OnRetry:     observabilityHook,    // Called before each retry
})
```

The retry policy fields:

| Field | Description |
|-------|-------------|
| `MaxRetries` | Max retry attempts after initial failure (0 = no retries) |
| `Backoff` | Strategy for delay between retries (nil = immediate) |
| `ShouldRetry` | Predicate to check if error is retryable (nil = retry all) |
| `OnRetry` | Callback before each retry for observability |

### WithTimeout

Wraps a target with a per-execution timeout:

```go
target := agent.WithTimeout(slowTarget, 30*time.Second)
```

Behavior:

- `timeout <= 0`: Returns target unchanged (no timeout applied)
- `timeout > 0`: Wraps with `context.WithTimeout`
- Respects parent context's deadline (shorter deadline wins)

## Backoff Strategies

| Strategy | Use Case |
|----------|----------|
| `ConstantBackoff` | Simple fixed delay; testing |
| `ExponentialBackoff` | Network errors; rate limits with jitter |
| `LinearBackoff` | Rate limits where exponential is too aggressive |

### ConstantBackoff

```go
backoff := agent.ConstantBackoff{Delay: 1 * time.Second}
// Retry 1: wait 1s, Retry 2: wait 1s, Retry 3: wait 1s
```

### ExponentialBackoff

```go
backoff := agent.ExponentialBackoff{
    Base:       100 * time.Millisecond, // Initial delay
    Max:        30 * time.Second,       // Cap delay at 30s
    Multiplier: 2.0,                    // Double each attempt (default: 2.0)
    Jitter:     0.1,                    // ±10% random variance
}
// Retry 1: ~100ms, Retry 2: ~200ms, Retry 3: ~400ms, ...
```

**Jitter Rationale**: When multiple targets retry simultaneously (e.g., after a
rate limit), jitter prevents them from hitting the service at the exact same
time (thundering herd problem).

### LinearBackoff

```go
backoff := agent.LinearBackoff{
    Initial:   1 * time.Second,   // First retry delay
    Increment: 1 * time.Second,   // Add 1s each retry
    Max:       10 * time.Second,  // Cap at 10s
}
// Retry 1: 1s, Retry 2: 2s, Retry 3: 3s, ..., Retry 10+: 10s
```

## Retry Predicates

| Predicate | Retries On |
|-----------|------------|
| `RetryOnAny` | Any non-nil error |
| `RetryOnTimeout` | `context.DeadlineExceeded` |
| `RetryOnTemporary` | Errors with `Temporary() bool` method |
| `RetryOn(errs...)` | Specific error types (uses `errors.Is`) |
| `RetryNever` | Never (useful for observability-only mode) |

### Custom Predicates

```go
// Retry on rate limit errors
policy.ShouldRetry = func(err error) bool {
    var rateLimitErr *RateLimitError
    return errors.As(err, &rateLimitErr)
}
```

### Combining Predicates

```go
// Retry on timeout OR specific errors
policy.ShouldRetry = func(err error) bool {
    return agent.RetryOnTimeout(err) || 
           errors.Is(err, ErrConnectionReset)
}
```

## Common Patterns

### Pattern 1: Per-Attempt Timeout with Retry

Each retry attempt gets a fresh timeout:

```go
target := agent.WithRetry(
    agent.WithTimeout(baseTarget, 30*time.Second),
    agent.RetryPolicy{
        MaxRetries:  3,
        Backoff:     agent.ExponentialBackoff{Base: 1*time.Second, Max: 10*time.Second},
        ShouldRetry: agent.RetryOnTimeout, // Retry on timeout
    },
)
```

### Pattern 2: Total Timeout via Parent Context

All retries must complete within a total time budget:

```go
ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
defer cancel()

exec := neng.NewExecutor(plan)
exec.Run(ctx, roots...)
```

### Pattern 3: Both Per-Attempt and Total Timeout

Combine patterns 1 and 2:

```go
target := agent.WithRetry(
    agent.WithTimeout(baseTarget, 30*time.Second),  // Per-attempt: 30s
    policy,
)

ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)  // Total: 2m
defer cancel()
exec.Run(ctx, roots...)
```

### Pattern 4: Observability Without Retry

Use `RetryNever` to get callbacks without actual retries:

```go
target := agent.WithRetry(baseTarget, agent.RetryPolicy{
    MaxRetries:  3,
    ShouldRetry: agent.RetryNever, // Never actually retry
    OnRetry: func(attempt int, err error) {
        metrics.Increment("target.failures")
    },
})
```

## FAQ

**Q: Why isn't retry in the core library?**

A: Retry semantics are domain-specific. What's "retryable" varies by use case:
rate limits for LLM APIs, flaky tests for CI/CD, connection resets for network
ops. The wrapper pattern lets you define your own predicates.

**Q: How do I set a total timeout across all retries?**

A: Use the parent context:

```go
ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
defer cancel()
exec.Run(ctx, roots...)
```

**Q: Can I compose multiple wrappers?**

A: Yes! Wrappers compose naturally from inner to outer:

```go
target := agent.WithRetry(          // Outer: retry
    agent.WithTimeout(              // Inner: timeout per attempt
        baseTarget,
        30*time.Second,
    ),
    policy,
)
```

**Q: What happens if backoff returns `shouldContinue=false`?**

A: The retry loop terminates immediately with the last error.

**Q: Does OnRetry block the retry?**

A: Yes, `OnRetry` runs synchronously. Keep it fast or use goroutines for
expensive operations.

## Advanced Patterns

### Sub-Plan Execution

Execute nested DAGs dynamically within a target:

```go
results := agent.NewResults()

parentTarget := neng.Target{
    Name: "branch",
    Deps: []string{"analyze"},
    Run: func(ctx context.Context) error {
        analysis := agent.MustLoad[AnalysisResult](results, "analyze")
        
        var subPlan *neng.Plan
        if analysis.NeedsSearch {
            subPlan = buildSearchPlan(results)
        } else {
            subPlan = buildDirectAnswerPlan(results)
        }
        
        subExec := &agent.SubPlanExecutor{Results: results}
        _, err := subExec.Run(ctx, subPlan)
        return err
    },
}
```

### Streaming Targets

Create targets that emit streaming output (e.g., LLM tokens):

```go
target, output := agent.StreamingTarget(
    "generate",
    "Stream LLM response",
    nil,
    func(ctx context.Context, emit func([]byte)) error {
        for token := range llm.Stream(ctx, prompt) {
            emit([]byte(token))
        }
        return nil
    },
)

// Start consumer BEFORE exec.Run()
go func() {
    for chunk := range output {
        fmt.Print(string(chunk))
    }
}()
```

**Important**: The consumer goroutine must be started BEFORE `exec.Run()`.

### ReAct Loop Pattern

Execute iterative think→act→observe cycles:

```go
err := agent.ReactLoop(
    ctx,
    results,
    10, // max iterations
    func(iteration int, r *agent.Results) *neng.Plan {
        plan, _ := neng.BuildPlan(
            neng.Target{Name: "think", Run: thinkFn(r, iteration)},
            neng.Target{Name: "act", Deps: []string{"think"}, Run: actFn(r)},
            neng.Target{Name: "observe", Deps: []string{"act"}, Run: observeFn(r)},
        )
        return plan
    },
    func(r *agent.Results) bool {
        obs, _ := agent.Load[Observation](r, "observation")
        return obs.IsFinal
    },
)
```

### Multi-Phase Execution

Execute phases in sequence:

```go
err := agent.RunPhases(ctx,
    agent.Phase{Name: "retrieve", Plan: retrievePlan},
    agent.Phase{Name: "process", Plan: processPlan},
    agent.Phase{Name: "generate", Plan: generatePlan},
)
```

### Dynamic Plan Building

Use `PlanFactory` to build plans based on runtime state:

```go
factory := agent.NewPlanFactory(results,
    agent.TargetDef{Name: "search", Run: searchFn},
    agent.TargetDef{
        Name: "fallback",
        Deps: []string{"search"},
        Condition: func(r *agent.Results) bool {
            hits, _ := agent.Load[[]Hit](r, "search_hits")
            return len(hits) == 0 // Include only if no hits
        },
        Run: fallbackFn,
    },
)

plan, err := factory.Build("search", "fallback")
```

## Circuit Breaker Integration

Circuit breakers prevent cascading failures by temporarily blocking calls to
failing services. neng does NOT implement circuit breakers internally because:

1. They require shared state across invocations (conflicts with stateless execution)
2. They're a service-level concern, not a target-level concern
3. Excellent external libraries exist

### Integration Pattern

Use an external library like [sony/gobreaker](https://github.com/sony/gobreaker):

```go
import "github.com/sony/gobreaker"

// Create circuit breaker (shared across targets calling the same service)
llmBreaker := gobreaker.NewCircuitBreaker(gobreaker.Settings{
    Name:        "llm-api",
    MaxRequests: 3,                // Requests allowed in half-open
    Interval:    10 * time.Second, // Reset interval
    Timeout:     30 * time.Second, // Time in open state before half-open
    ReadyToTrip: func(counts gobreaker.Counts) bool {
        // Open circuit after 5 consecutive failures
        return counts.ConsecutiveFailures > 5
    },
})

// Use in target
target := neng.Target{
    Name: "call_llm",
    Run: func(ctx context.Context) error {
        _, err := llmBreaker.Execute(func() (any, error) {
            return nil, callLLM(ctx, prompt)
        })
        return err
    },
}
```

### Sharing Breakers Across Targets

Multiple targets calling the same service should share a breaker:

```go
// Shared breaker for all LLM calls
llmBreaker := gobreaker.NewCircuitBreaker(settings)

targets := []neng.Target{
    {Name: "summarize", Run: wrapWithBreaker(llmBreaker, summarizeFn)},
    {Name: "translate", Run: wrapWithBreaker(llmBreaker, translateFn)},
    {Name: "classify", Run: wrapWithBreaker(llmBreaker, classifyFn)},
}

func wrapWithBreaker(cb *gobreaker.CircuitBreaker, fn func(ctx context.Context) error) func(ctx context.Context) error {
    return func(ctx context.Context) error {
        _, err := cb.Execute(func() (any, error) {
            return nil, fn(ctx)
        })
        return err
    }
}
```

### Combining with Retry

Circuit breakers and retries complement each other:

- **Retry**: Handle transient failures within a single request
- **Circuit breaker**: Prevent overwhelming a service that's failing persistently

```go
target := agent.WithRetry(
    neng.Target{
        Name: "api_call",
        Run: func(ctx context.Context) error {
            _, err := breaker.Execute(func() (any, error) {
                return nil, callAPI(ctx)
            })
            return err
        },
    },
    agent.RetryPolicy{
        MaxRetries:  3,
        Backoff:     agent.ExponentialBackoff{Base: 100*time.Millisecond},
        ShouldRetry: func(err error) bool {
            // Don't retry if circuit is open
            return !errors.Is(err, gobreaker.ErrOpenState)
        },
    },
)
```

**Note**: Circuit breaker state persists across plan executions. A breaker
opened in one execution affects subsequent executions.
