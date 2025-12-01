package agent_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/a2y-d5l/neng/agent"
)

func TestResults_StoreLoad(t *testing.T) {
	r := agent.NewResults()

	agent.Store(r, "string", "hello")
	agent.Store(r, "int", 42)
	agent.Store(r, "slice", []string{"a", "b"})

	s, ok := agent.Load[string](r, "string")
	if !ok || s != "hello" {
		t.Errorf("string: expected 'hello', got %v (ok=%v)", s, ok)
	}

	i, ok := agent.Load[int](r, "int")
	if !ok || i != 42 {
		t.Errorf("int: expected 42, got %v (ok=%v)", i, ok)
	}

	sl, ok := agent.Load[[]string](r, "slice")
	if !ok || len(sl) != 2 {
		t.Errorf("slice: expected [a,b], got %v (ok=%v)", sl, ok)
	}
}

func TestResults_LoadMissing(t *testing.T) {
	r := agent.NewResults()

	v, ok := agent.Load[string](r, "nonexistent")
	if ok {
		t.Error("expected ok=false for missing key")
	}
	if v != "" {
		t.Errorf("expected zero value, got %q", v)
	}
}

func TestResults_LoadWrongType(t *testing.T) {
	r := agent.NewResults()
	agent.Store(r, "key", "string value")

	v, ok := agent.Load[int](r, "key")
	if ok {
		t.Error("expected ok=false for wrong type")
	}
	if v != 0 {
		t.Errorf("expected zero int, got %d", v)
	}
}

func TestResults_MustLoad_Success(t *testing.T) {
	r := agent.NewResults()
	agent.Store(r, "key", 42)

	v := agent.MustLoad[int](r, "key")
	if v != 42 {
		t.Errorf("expected 42, got %d", v)
	}
}

func TestResults_MustLoad_PanicMissing(t *testing.T) {
	r := agent.NewResults()

	defer func() {
		if recover() == nil {
			t.Error("expected panic for missing key")
		}
	}()

	_ = agent.MustLoad[int](r, "nonexistent")
}

func TestResults_MustLoad_PanicWrongType(t *testing.T) {
	r := agent.NewResults()
	agent.Store(r, "key", "string")

	defer func() {
		if recover() == nil {
			t.Error("expected panic for wrong type")
		}
	}()

	_ = agent.MustLoad[int](r, "key")
}

func TestResults_ConcurrentAccess(t *testing.T) {
	// Run with: go test -race
	r := agent.NewResults()
	var wg sync.WaitGroup

	// Many writers
	for i := range 100 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := fmt.Sprintf("key%d", n%10)
			agent.Store(r, key, n)
		}(i)
	}

	// Many readers
	for i := range 100 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := fmt.Sprintf("key%d", n%10)
			_, _ = agent.Load[int](r, key)
		}(i)
	}

	wg.Wait()
}

func TestResults_UtilityMethods(t *testing.T) {
	r := agent.NewResults()

	// Initially empty
	if r.Len() != 0 {
		t.Errorf("expected Len=0, got %d", r.Len())
	}
	if r.Has("key") {
		t.Error("expected Has=false for empty")
	}

	// Add some values
	agent.Store(r, "a", 1)
	agent.Store(r, "b", 2)
	agent.Store(r, "c", 3)

	if r.Len() != 3 {
		t.Errorf("expected Len=3, got %d", r.Len())
	}
	if !r.Has("b") {
		t.Error("expected Has=true for existing key")
	}

	keys := r.Keys()
	if len(keys) != 3 {
		t.Errorf("expected 3 keys, got %d", len(keys))
	}

	// Delete
	r.Delete("b")
	if r.Has("b") {
		t.Error("expected Has=false after Delete")
	}
	if r.Len() != 2 {
		t.Errorf("expected Len=2 after Delete, got %d", r.Len())
	}

	// Clear
	r.Clear()
	if r.Len() != 0 {
		t.Errorf("expected Len=0 after Clear, got %d", r.Len())
	}
}

func TestResults_OverwriteValue(t *testing.T) {
	r := agent.NewResults()

	agent.Store(r, "key", 1)
	agent.Store(r, "key", 2)

	v, ok := agent.Load[int](r, "key")
	if !ok || v != 2 {
		t.Errorf("expected 2 after overwrite, got %v (ok=%v)", v, ok)
	}
}

func TestResults_OverwriteWithDifferentType(t *testing.T) {
	r := agent.NewResults()

	agent.Store(r, "key", 42)
	agent.Store(r, "key", "now a string")

	// Should be able to load as string now
	v, ok := agent.Load[string](r, "key")
	if !ok || v != "now a string" {
		t.Errorf("expected 'now a string', got %v (ok=%v)", v, ok)
	}

	// Old type should fail
	_, ok = agent.Load[int](r, "key")
	if ok {
		t.Error("expected ok=false for old type after overwrite")
	}
}

func TestResults_NilValue(t *testing.T) {
	r := agent.NewResults()

	// Store a nil slice
	var nilSlice []string
	agent.Store(r, "nil_slice", nilSlice)

	v, ok := agent.Load[[]string](r, "nil_slice")
	if !ok {
		t.Error("expected ok=true for nil slice")
	}
	if v != nil {
		t.Errorf("expected nil slice, got %v", v)
	}
}

func TestResults_ComplexTypes(t *testing.T) {
	r := agent.NewResults()

	type CustomStruct struct {
		Name  string
		Value int
	}

	agent.Store(r, "struct", CustomStruct{Name: "test", Value: 42})

	v, ok := agent.Load[CustomStruct](r, "struct")
	if !ok {
		t.Error("expected ok=true for struct")
	}
	if v.Name != "test" || v.Value != 42 {
		t.Errorf("expected {test, 42}, got %+v", v)
	}
}

func TestResults_PointerTypes(t *testing.T) {
	r := agent.NewResults()

	value := 42
	agent.Store(r, "ptr", &value)

	v, ok := agent.Load[*int](r, "ptr")
	if !ok {
		t.Error("expected ok=true for pointer")
	}
	if v == nil || *v != 42 {
		t.Errorf("expected pointer to 42, got %v", v)
	}
}

func TestResults_MapTypes(t *testing.T) {
	r := agent.NewResults()

	m := map[string]int{"a": 1, "b": 2}
	agent.Store(r, "map", m)

	v, ok := agent.Load[map[string]int](r, "map")
	if !ok {
		t.Error("expected ok=true for map")
	}
	if len(v) != 2 || v["a"] != 1 || v["b"] != 2 {
		t.Errorf("expected {a:1, b:2}, got %v", v)
	}
}
