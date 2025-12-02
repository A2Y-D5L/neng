package internal

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a2y-d5l/neng"
	"github.com/a2y-d5l/neng/agent"
)

// --- Multi-Search Tool Tests ---

func TestMultiSearchTool_GetTool(t *testing.T) {
	t.Parallel()

	sources := []SearchSource{
		{Name: "web", Search: mockSearch("web")},
		{Name: "db", Search: mockSearch("db")},
	}

	tool := NewMultiSearchTool(sources...)
	toolDef := tool.GetTool()

	if toolDef.Name != "multi_search" {
		t.Errorf("expected name 'multi_search', got %q", toolDef.Name)
	}

	if !tool.IsComplex() {
		t.Error("expected IsComplex() to return true")
	}

	// Verify parameters.
	queryParam, ok := toolDef.Parameters["query"]
	if !ok {
		t.Fatal("expected 'query' parameter")
	}
	if !queryParam.Required {
		t.Error("expected 'query' to be required")
	}
}

func TestMultiSearchTool_Execute(t *testing.T) {
	t.Parallel()

	sources := []SearchSource{
		{Name: "web", Search: mockSearch("web")},
		{Name: "db", Search: mockSearch("db")},
	}

	tool := NewMultiSearchTool(sources...)
	ctx := context.Background()

	t.Run("successful search", func(t *testing.T) {
		t.Parallel()
		result, err := tool.Execute(ctx, map[string]any{"query": "test query"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		results, ok := result.([]SearchResult)
		if !ok {
			t.Fatalf("expected []SearchResult, got %T", result)
		}

		// Should have results from both sources.
		if len(results) == 0 {
			t.Error("expected results from both sources")
		}
	})

	t.Run("missing query", func(t *testing.T) {
		t.Parallel()
		_, err := tool.Execute(ctx, map[string]any{})
		if err == nil {
			t.Error("expected error for missing query")
		}
	})
}

func TestMultiSearchTool_BuildPlan(t *testing.T) {
	t.Parallel()

	sources := []SearchSource{
		{Name: "web", Search: mockSearch("web")},
		{Name: "db", Search: mockSearch("db")},
	}

	tool := NewMultiSearchTool(sources...)
	results := agent.NewResults()
	ctx := context.Background()

	plan, err := tool.BuildPlan(ctx, map[string]any{"query": "test"}, results)
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	// Should have 3 targets: search:web, search:db, aggregate.
	if plan == nil {
		t.Fatal("expected non-nil plan")
	}

	// Execute the plan.
	exec := neng.NewExecutor(plan)
	_, execErr := exec.Run(ctx)
	if execErr != nil {
		t.Fatalf("plan execution failed: %v", execErr)
	}

	// Check aggregated results.
	aggregated, ok := agent.Load[[]SearchResult](results, "multi_search:results")
	if !ok {
		t.Fatal("expected multi_search:results")
	}

	if len(aggregated) == 0 {
		t.Error("expected aggregated results")
	}
}

func TestMultiSearchTool_ParallelExecution(t *testing.T) {
	t.Parallel()

	var executionOrder sync.Map
	var counter atomic.Int32

	// Create sources that track execution order.
	sources := []SearchSource{
		{
			Name: "slow1",
			Search: func(_ context.Context, _ string) ([]SearchResult, error) {
				order := counter.Add(1)
				executionOrder.Store("slow1", order)
				time.Sleep(10 * time.Millisecond)
				return []SearchResult{{Title: "slow1"}}, nil
			},
		},
		{
			Name: "slow2",
			Search: func(_ context.Context, _ string) ([]SearchResult, error) {
				order := counter.Add(1)
				executionOrder.Store("slow2", order)
				time.Sleep(10 * time.Millisecond)
				return []SearchResult{{Title: "slow2"}}, nil
			},
		},
	}

	tool := NewMultiSearchTool(sources...)
	results := agent.NewResults()
	ctx := context.Background()

	plan, err := tool.BuildPlan(ctx, map[string]any{"query": "test"}, results)
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	start := time.Now()
	exec := neng.NewExecutor(plan)
	_, execErr := exec.Run(ctx)
	elapsed := time.Since(start)

	if execErr != nil {
		t.Fatalf("execution failed: %v", execErr)
	}

	// If executed in parallel, should complete in ~10ms, not ~20ms.
	// Allow some slack for test overhead.
	if elapsed > 50*time.Millisecond {
		t.Errorf(
			"expected parallel execution (<50ms), took %v; sources may have run sequentially",
			elapsed,
		)
	}
}

// --- RAG Pipeline Tool Tests ---

func TestRAGPipelineTool_GetTool(t *testing.T) {
	t.Parallel()

	rag := NewRAGPipelineTool(
		mockRetriever,
		mockReranker,
		mockGenerator,
	)

	toolDef := rag.GetTool()
	if toolDef.Name != "rag_query" {
		t.Errorf("expected name 'rag_query', got %q", toolDef.Name)
	}
}

func TestRAGPipelineTool_Execute(t *testing.T) {
	t.Parallel()

	rag := NewRAGPipelineTool(
		mockRetriever,
		mockReranker,
		mockGenerator,
	)

	ctx := context.Background()
	result, err := rag.Execute(ctx, map[string]any{"query": "test question"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	response, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", result)
	}

	if _, hasResponse := response["response"].(string); !hasResponse {
		t.Error("expected 'response' in result")
	}
	if _, hasDocs := response["documents"].([]Document); !hasDocs {
		t.Error("expected 'documents' in result")
	}
}

func TestRAGPipelineTool_BuildPlan_WithReranker(t *testing.T) {
	t.Parallel()

	var rerankerCalled atomic.Bool
	reranker := func(_ context.Context, _ string, docs []Document) ([]Document, error) {
		rerankerCalled.Store(true)
		return docs, nil
	}

	rag := NewRAGPipelineTool(
		mockRetriever,
		reranker,
		mockGenerator,
	)

	results := agent.NewResults()
	ctx := context.Background()

	plan, err := rag.BuildPlan(ctx, map[string]any{"query": "test"}, results)
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	exec := neng.NewExecutor(plan)
	_, execErr := exec.Run(ctx)
	if execErr != nil {
		t.Fatalf("execution failed: %v", execErr)
	}

	if !rerankerCalled.Load() {
		t.Error("expected reranker to be called")
	}

	// Check final response.
	response, ok := agent.Load[string](results, "rag:response")
	if !ok {
		t.Fatal("expected rag:response")
	}
	if response == "" {
		t.Error("expected non-empty response")
	}
}

func TestRAGPipelineTool_BuildPlan_WithoutReranker(t *testing.T) {
	t.Parallel()

	rag := NewRAGPipelineTool(
		mockRetriever,
		nil, // No reranker
		mockGenerator,
	)

	results := agent.NewResults()
	ctx := context.Background()

	plan, err := rag.BuildPlan(ctx, map[string]any{"query": "test"}, results)
	if err != nil {
		t.Fatalf("BuildPlan failed: %v", err)
	}

	exec := neng.NewExecutor(plan)
	_, execErr := exec.Run(ctx)
	if execErr != nil {
		t.Fatalf("execution failed: %v", execErr)
	}

	// Should still produce a response.
	response, ok := agent.Load[string](results, "rag:response")
	if !ok {
		t.Fatal("expected rag:response")
	}
	if response == "" {
		t.Error("expected non-empty response")
	}
}

// --- ExecuteComplexTool Tests ---

func TestExecuteComplexTool(t *testing.T) {
	t.Parallel()

	sources := []SearchSource{
		{Name: "web", Search: mockSearch("web")},
	}

	tool := NewMultiSearchTool(sources...)
	results := agent.NewResults()
	ctx := context.Background()

	err := ExecuteComplexTool(ctx, tool, map[string]any{"query": "test"}, results, nil)
	if err != nil {
		t.Fatalf("ExecuteComplexTool failed: %v", err)
	}

	// Check that results were stored with prefix.
	aggregated, ok := agent.Load[[]SearchResult](results, "multi_search:results")
	if !ok {
		t.Fatal("expected multi_search:results from sub-plan")
	}
	if len(aggregated) == 0 {
		t.Error("expected results")
	}
}

// testEventHandler implements neng.EventHandler for testing.
type testEventHandler struct {
	mu     sync.Mutex
	events []neng.Event
}

func (h *testEventHandler) HandleEvent(e neng.Event) {
	h.mu.Lock()
	h.events = append(h.events, e)
	h.mu.Unlock()
}

func (h *testEventHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.events)
}

func TestExecuteComplexTool_WithEventHandler(t *testing.T) {
	t.Parallel()

	sources := []SearchSource{
		{Name: "web", Search: mockSearch("web")},
	}

	tool := NewMultiSearchTool(sources...)
	results := agent.NewResults()
	ctx := context.Background()

	handler := &testEventHandler{}

	err := ExecuteComplexTool(ctx, tool, map[string]any{"query": "test"}, results, handler)
	if err != nil {
		t.Fatalf("ExecuteComplexTool failed: %v", err)
	}

	if handler.count() == 0 {
		t.Error("expected events to be captured")
	}
}

func TestExecuteComplexTool_BuildPlanError(t *testing.T) {
	t.Parallel()

	tool := NewMultiSearchTool() // No sources, but query error should happen first
	results := agent.NewResults()
	ctx := context.Background()

	// Missing query should cause BuildPlan to fail.
	err := ExecuteComplexTool(ctx, tool, map[string]any{}, results, nil)
	if err == nil {
		t.Error("expected error for missing query")
	}
}

// --- Streaming Progress Tests ---

func TestStreamingProgress(t *testing.T) {
	t.Parallel()

	var callCount atomic.Int32
	progress := NewStreamingProgress(func(tokens, chars int64, _ time.Duration) {
		callCount.Add(1)
		_ = tokens
		_ = chars
	})

	// Record some tokens.
	progress.RecordToken("Hello")
	progress.RecordToken(" ")
	progress.RecordToken("world")

	stats := progress.Stats()

	if stats.TokenCount != 3 {
		t.Errorf("expected 3 tokens, got %d", stats.TokenCount)
	}

	// "Hello" (5) + " " (1) + "world" (5) = 11.
	if stats.CharCount != 11 {
		t.Errorf("expected 11 chars, got %d", stats.CharCount)
	}

	if stats.TokensPerSec <= 0 {
		t.Error("expected positive tokens per second")
	}

	if callCount.Load() != 3 {
		t.Errorf("expected OnProgress called 3 times, got %d", callCount.Load())
	}
}

func TestStreamingProgress_NoCallback(t *testing.T) {
	t.Parallel()

	progress := NewStreamingProgress(nil)
	progress.RecordToken("test")

	stats := progress.Stats()
	if stats.TokenCount != 1 {
		t.Errorf("expected 1 token, got %d", stats.TokenCount)
	}
}

// --- LLM Retry Policy Tests ---

func TestLLMRetryPolicy(t *testing.T) {
	t.Parallel()

	policy := LLMRetryPolicy(3)

	if policy.MaxRetries != 3 {
		t.Errorf("expected MaxRetries=3, got %d", policy.MaxRetries)
	}

	// Type assert to check the concrete backoff type.
	backoff, ok := policy.Backoff.(agent.ExponentialBackoff)
	if !ok {
		t.Fatalf("expected ExponentialBackoff, got %T", policy.Backoff)
	}

	if backoff.Base != llmRetryBaseDelay {
		t.Errorf("expected Base=%v, got %v", llmRetryBaseDelay, backoff.Base)
	}

	if backoff.Max != llmRetryMaxDelay {
		t.Errorf("expected Max=%v, got %v", llmRetryMaxDelay, backoff.Max)
	}

	// Test ShouldRetry with retryable error.
	if !policy.ShouldRetry(&LLMError{StatusCode: 429, Message: "rate limited", Retryable: true}) {
		t.Error("expected 429 to be retryable")
	}

	// Test OnRetry doesn't panic.
	policy.OnRetry(1, errors.New("test"))
}

func TestWrapWithLLMRetry(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	originalTarget := neng.Task{
		Name: "test",
		Desc: "test task",
		Run: func(_ context.Context) error {
			if attempts.Add(1) < 3 {
				return &LLMError{StatusCode: 429, Message: "rate limited", Retryable: true}
			}
			return nil
		},
	}

	wrappedTarget := WrapWithLLMRetry(originalTarget, 5)

	ctx := context.Background()
	err := wrappedTarget.Run(ctx)
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}

	if attempts.Load() != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts.Load())
	}
}

// --- Conditional Execution Tests ---

func TestConditionalRunner_Skip(t *testing.T) {
	t.Parallel()

	var runCalled atomic.Bool
	var skipCalled atomic.Bool

	runner := &ConditionalRunner{
		Condition: func(_ *agent.Results) ConditionResult {
			return ConditionSkip
		},
		Run: func(_ context.Context, _ *agent.Results) error {
			runCalled.Store(true)
			return nil
		},
		OnSkip: func() {
			skipCalled.Store(true)
		},
	}

	targetDef := runner.ToTargetDef("test", "test target", nil)
	results := agent.NewResults()

	err := targetDef.Run(context.Background(), results)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if runCalled.Load() {
		t.Error("expected Run not to be called when skipped")
	}

	if !skipCalled.Load() {
		t.Error("expected OnSkip to be called")
	}
}

func TestConditionalRunner_Run(t *testing.T) {
	t.Parallel()

	var runCalled atomic.Bool

	runner := &ConditionalRunner{
		Condition: func(_ *agent.Results) ConditionResult {
			return ConditionRun
		},
		Run: func(_ context.Context, _ *agent.Results) error {
			runCalled.Store(true)
			return nil
		},
	}

	targetDef := runner.ToTargetDef("test", "test target", nil)
	results := agent.NewResults()

	err := targetDef.Run(context.Background(), results)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !runCalled.Load() {
		t.Error("expected Run to be called")
	}
}

func TestConditionalRunner_Fail(t *testing.T) {
	t.Parallel()

	runner := &ConditionalRunner{
		Condition: func(_ *agent.Results) ConditionResult {
			return ConditionFail
		},
		Run: func(_ context.Context, _ *agent.Results) error {
			return nil
		},
	}

	targetDef := runner.ToTargetDef("test", "test target", nil)
	results := agent.NewResults()

	err := targetDef.Run(context.Background(), results)
	if err == nil {
		t.Error("expected error for ConditionFail")
	}
}

func TestConditionalRunner_PlanFactoryCondition(t *testing.T) {
	t.Parallel()

	runner := &ConditionalRunner{
		Condition: func(_ *agent.Results) ConditionResult {
			return ConditionRun
		},
		Run: func(_ context.Context, _ *agent.Results) error {
			return nil
		},
	}

	targetDef := runner.ToTargetDef("test", "test target", nil)

	if targetDef.Condition == nil {
		t.Fatal("expected Condition function to be set")
	}

	results := agent.NewResults()
	if !targetDef.Condition(results) {
		t.Error("expected Condition to return true for ConditionRun")
	}
}

// --- IsComplexToolInstance Tests ---

func TestIsComplexToolInstance(t *testing.T) {
	t.Parallel()

	tool := NewMultiSearchTool()

	ct, ok := IsComplexToolInstance(tool)
	if !ok {
		t.Fatal("expected MultiSearchTool to be a ComplexTool")
	}

	if ct.GetTool().Name != "multi_search" {
		t.Errorf("expected multi_search, got %q", ct.GetTool().Name)
	}
}

func TestIsComplexToolInstance_NotComplex(t *testing.T) {
	t.Parallel()

	simpleTool := Tool{
		Name: "simple",
		Execute: func(_ context.Context, _ map[string]any) (any, error) {
			return "result", nil
		},
	}

	_, ok := IsComplexToolInstance(simpleTool)
	if ok {
		t.Error("expected simple Tool not to be a ComplexTool")
	}
}

// --- Helper Functions ---

func mockSearch(source string) func(context.Context, string) ([]SearchResult, error) {
	return func(_ context.Context, query string) ([]SearchResult, error) {
		return []SearchResult{
			{Title: source + " result 1 for " + query, URL: "https://" + source + ".com/1"},
			{Title: source + " result 2 for " + query, URL: "https://" + source + ".com/2"},
		}, nil
	}
}

func mockRetriever(_ context.Context, _ string, topK int) ([]Document, error) {
	docs := make([]Document, 0, topK)
	for i := range topK {
		docs = append(docs, Document{
			ID:      "doc" + string(rune('A'+i)),
			Content: "Document content " + string(rune('A'+i)),
			Score:   float64(topK-i) / float64(topK),
		})
	}
	return docs, nil
}

func mockReranker(_ context.Context, _ string, docs []Document) ([]Document, error) {
	// Simulate reranking by reversing the order.
	reranked := make([]Document, len(docs))
	for i, doc := range docs {
		reranked[len(docs)-1-i] = doc
	}
	return reranked, nil
}

func mockGenerator(_ context.Context, query string, docs []Document) (string, error) {
	return "Generated response for: " + query + " using " + string(rune('0'+len(docs))) + " documents", nil
}
