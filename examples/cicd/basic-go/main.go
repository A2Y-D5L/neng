package main

import (
	"context"
	"fmt"
	"time"

	engine "github.com/a2y-d5l/neng"
)

// loggingSink is a simple EventSink that prints a human-readable log of
// target lifecycle events. In a real system, this could be a TUI renderer,
// JSON logger, or test recorder.
type loggingSink struct{}

func (loggingSink) HandleEvent(ev engine.Event) {
	name := "<nil>"
	if ev.Task != nil {
		name = ev.Task.Name
	}

	switch ev.Type {
	case engine.EventTaskStarted:
		fmt.Printf("[event] %s STARTED at %s\n", name, ev.Time.Format(time.RFC3339Nano))
	case engine.EventTaskCompleted:
		status := "ok"
		if ev.Result != nil && ev.Result.Err != nil {
			status = "failed"
		}
		fmt.Printf("[event] %s COMPLETED (%s) at %s\n", name, status, ev.Time.Format(time.RFC3339Nano))
	case engine.EventTaskSkipped:
		fmt.Printf("[event] %s SKIPPED at %s\n", name, ev.Time.Format(time.RFC3339Nano))
	default:
		fmt.Printf("[event] %s UNKNOWN event at %s\n", name, ev.Time.Format(time.RFC3339Nano))
	}
}

func createPlan() *engine.Plan {
	plan, err := engine.BuildPlan(
		engine.Task{
			Name: "lint",
			Desc: "Run linters",
			Run: func(_ context.Context) error {
				time.Sleep(300 * time.Millisecond)
				return nil
			}},
		engine.Task{
			Name: "test",
			Desc: "Run unit tests",
			Run: func(_ context.Context) error {
				time.Sleep(500 * time.Millisecond)
				return nil
			}},
		engine.Task{
			Name: "build",
			Desc: "Build binaries",
			Deps: []string{"lint", "test"},
			Run: func(_ context.Context) error {
				time.Sleep(400 * time.Millisecond)
				return nil
			}},
		engine.Task{
			Name: "package",
			Desc: "Package artifacts",
			Deps: []string{"build"},
			Run: func(_ context.Context) error {
				time.Sleep(200 * time.Millisecond)
				return nil
			}},
		engine.Task{
			Name: "all",
			Desc: "Top-level aggregate target",
			Deps: []string{"package"},
			Run: func(_ context.Context) error {
				time.Sleep(100 * time.Millisecond)
				return nil
			},
		})
	if err != nil {
		panic(err)
	}
	return plan
}

func printSummary(summary engine.RunSummary) {
	fmt.Println("Result:")
	for name, res := range summary.Results {
		status := "ok"
		if res.Skipped {
			status = "skipped"
		} else if res.Err != nil {
			status = "failed"
		}
		fmt.Printf("  %-8s status=%-7s start=%s end=%s err=%v\n",
			name, status,
			res.StartedAt.Format(time.RFC3339Nano),
			res.CompletedAt.Format(time.RFC3339Nano),
			res.Err,
		)
	}
}

func drainChannel[T any](ch <-chan T) {
	//nolint:revive // drain the channel
	for range ch {
	}
}

func runTarget(ctx context.Context, plan *engine.Plan, target string, opts ...engine.ExecutorOption) {
	exec := engine.NewExecutor(plan, opts...)
	go drainChannel(exec.Events())
	go drainChannel(exec.Results())

	fmt.Println("\n--- Running target: " + target + " ---")
	summary, err := exec.Run(ctx, target)
	if err != nil {
		fmt.Printf("Run completed with error: %v\n", err)
	}

	printSummary(summary)
}

func main() {
	plan := createPlan()
	fmt.Println("Plan targets:", plan.TaskNames())
	fmt.Println("Plan stages:")
	for _, s := range plan.Stages() {
		fmt.Println("  ", s)
	}
	runTarget(context.Background(), plan, "all", engine.WithEventSink(loggingSink{}))
	runTarget(context.Background(), plan, "build", engine.WithEventSink(loggingSink{}))
}
