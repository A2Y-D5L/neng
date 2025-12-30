package neng_test

import (
	"context"
	"errors"
	"testing"

	"github.com/a2y-d5l/neng/v2"
)

func TestBuildPlan_ValidatesAndStagesDeterministic(t *testing.T) {
	p, err := neng.BuildPlan(
		neng.Task{
			Name: "C",
			Deps: []string{"A", "B"},
			Run:  func(context.Context) error { return nil },
		},
		neng.Task{Name: "B", Run: func(context.Context) error { return nil }},
		neng.Task{Name: "A", Run: func(context.Context) error { return nil }},
	)
	if err != nil {
		t.Fatalf("BuildPlan returned error: %v", err)
	}

	stages := p.Stages()
	if len(stages) != 2 {
		t.Fatalf("expected 2 stages, got %d", len(stages))
	}
	if got := stages[0].Tasks; len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Fatalf("unexpected stage0 tasks: %#v", got)
	}
	if got := stages[1].Tasks; len(got) != 1 || got[0] != "C" {
		t.Fatalf("unexpected stage1 tasks: %#v", got)
	}

	topo := p.Topo()
	if len(topo) != 3 {
		t.Fatalf("expected topo length 3, got %d", len(topo))
	}

	// Spec returns immutable snapshot.
	spec, ok := p.Spec("C")
	if !ok || spec.Name != "C" {
		t.Fatalf("expected Spec(C)")
	}
	if len(spec.Deps) != 2 {
		t.Fatalf("expected 2 deps")
	}
}

func TestExecute_SkipsUpstreamFailure(t *testing.T) {
	p, err := neng.BuildPlan(
		neng.Task{Name: "A", Run: func(context.Context) error { return errors.New("boom") }},
		neng.Task{Name: "B", Deps: []string{"A"}, Run: func(context.Context) error { return nil }},
		neng.Task{Name: "C", Deps: []string{"B"}, Run: func(context.Context) error { return nil }},
	)
	if err != nil {
		t.Fatalf("BuildPlan error: %v", err)
	}

	sum, runErr := p.Execute(context.Background(), nil)
	if runErr == nil {
		t.Fatalf("expected error")
	}

	if sum.Results["A"].Status != neng.ResultFailed {
		t.Fatalf("A status = %v", sum.Results["A"].Status)
	}
	if sum.Results["B"].Status != neng.ResultSkipped ||
		sum.Results["B"].SkipReason != neng.SkipUpstreamFailed {
		t.Fatalf("B result = %#v", sum.Results["B"])
	}
	if sum.Results["C"].Status != neng.ResultSkipped ||
		sum.Results["C"].SkipReason != neng.SkipUpstreamFailed {
		t.Fatalf("C result = %#v", sum.Results["C"])
	}
}
