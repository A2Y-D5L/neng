package internal

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestToolRegistry(t *testing.T) {
	t.Parallel()

	t.Run("register and get", func(t *testing.T) {
		t.Parallel()
		registry := NewToolRegistry()

		tool := Tool{
			Name:        "test",
			Description: "A test tool",
		}
		registry.Register(tool)

		got, ok := registry.Get("test")
		if !ok {
			t.Fatal("expected to find tool")
		}
		if got.Name != "test" {
			t.Errorf("got name %q, want %q", got.Name, "test")
		}
	})

	t.Run("get missing", func(t *testing.T) {
		t.Parallel()
		registry := NewToolRegistry()

		_, ok := registry.Get("missing")
		if ok {
			t.Fatal("expected not to find tool")
		}
	})

	t.Run("list", func(t *testing.T) {
		t.Parallel()
		registry := NewToolRegistry()

		registry.Register(Tool{Name: "a"})
		registry.Register(Tool{Name: "b"})

		names := registry.List()
		if len(names) != 2 {
			t.Errorf("got %d names, want 2", len(names))
		}
	})

	t.Run("clone", func(t *testing.T) {
		t.Parallel()
		registry := NewToolRegistry()
		registry.Register(Tool{Name: "original"})

		clone := registry.Clone()
		clone.Register(Tool{Name: "new"})

		if len(registry.List()) != 1 {
			t.Error("original registry should not be modified")
		}
		if len(clone.List()) != 2 {
			t.Error("clone should have both tools")
		}
	})

	t.Run("concurrent access", func(t *testing.T) {
		t.Parallel()
		registry := NewToolRegistry()

		var wg sync.WaitGroup
		for i := range 100 {
			wg.Go(func() {
				registry.Register(Tool{Name: itoa(i)})
				registry.Get(itoa(i))
				registry.List()
			})
		}
		wg.Wait()
	})
}

//nolint:gocognit // test function with many subtests
func TestBuiltinTools(t *testing.T) {
	t.Parallel()
	memory := NewMemory()
	registry := BuiltinTools(memory)

	t.Run("search tool", func(t *testing.T) {
		t.Parallel()
		tool, ok := registry.Get("search")
		if !ok {
			t.Fatal("search tool not found")
		}

		result, err := tool.Execute(t.Context(), map[string]any{"query": "test query"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		results, ok := result.([]SearchResult)
		if !ok {
			t.Fatalf("expected []SearchResult, got %T", result)
		}
		if len(results) == 0 {
			t.Error("expected search results")
		}
	})

	t.Run("search tool missing query", func(t *testing.T) {
		t.Parallel()
		tool, _ := registry.Get("search")

		_, err := tool.Execute(t.Context(), map[string]any{})
		if err == nil {
			t.Error("expected error for missing query")
		}
	})

	t.Run("calculate tool", func(t *testing.T) {
		t.Parallel()
		tool, ok := registry.Get("calculate")
		if !ok {
			t.Fatal("calculate tool not found")
		}

		result, err := tool.Execute(t.Context(), map[string]any{"expression": "2 + 3 * 4"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		resultMap, ok := result.(map[string]any)
		if !ok {
			t.Fatalf("expected map, got %T", result)
		}

		if resultMap["result"] != 14.0 {
			t.Errorf("got %v, want 14", resultMap["result"])
		}
	})

	t.Run("calculate tool complex expression", func(t *testing.T) {
		t.Parallel()
		tool, _ := registry.Get("calculate")

		testCases := []struct {
			expr     string
			expected float64
		}{
			{"1 + 1", 2},
			{"10 - 5", 5},
			{"3 * 4", 12},
			{"15 / 3", 5},
			{"2 ^ 3", 8},
			{"(1 + 2) * 3", 9},
			{"-5 + 10", 5},
			{"2.5 * 2", 5},
		}

		for _, tc := range testCases {
			result, err := tool.Execute(t.Context(), map[string]any{"expression": tc.expr})
			if err != nil {
				t.Errorf("expression %q: unexpected error: %v", tc.expr, err)
				continue
			}

			resultMap := result.(map[string]any)
			got := resultMap["result"].(float64)
			if got != tc.expected {
				t.Errorf("expression %q: got %v, want %v", tc.expr, got, tc.expected)
			}
		}
	})

	t.Run("browse tool", func(t *testing.T) {
		t.Parallel()
		tool, ok := registry.Get("browse")
		if !ok {
			t.Fatal("browse tool not found")
		}

		result, err := tool.Execute(t.Context(), map[string]any{"url": "https://example.com"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		resultMap, ok := result.(map[string]any)
		if !ok {
			t.Fatalf("expected map, got %T", result)
		}

		if resultMap["status"] != 200 {
			t.Errorf("got status %v, want 200", resultMap["status"])
		}
	})

	t.Run("browse tool invalid url", func(t *testing.T) {
		t.Parallel()
		tool, _ := registry.Get("browse")

		_, err := tool.Execute(t.Context(), map[string]any{"url": "not-a-url"})
		if err == nil {
			t.Error("expected error for invalid URL")
		}
	})

	t.Run("memory tools", func(t *testing.T) {
		t.Parallel()
		storeTool, _ := registry.Get("memory_store")
		loadTool, _ := registry.Get("memory_load")

		// Store a value.
		_, err := storeTool.Execute(t.Context(), map[string]any{
			"key":   "testKey",
			"value": "testValue",
		})
		if err != nil {
			t.Fatalf("store error: %v", err)
		}

		// Load the value.
		result, err := loadTool.Execute(t.Context(), map[string]any{"key": "testKey"})
		if err != nil {
			t.Fatalf("load error: %v", err)
		}

		resultMap := result.(map[string]any)
		if !resultMap["found"].(bool) {
			t.Error("expected to find stored value")
		}
		if resultMap["value"] != "testValue" {
			t.Errorf("got %v, want testValue", resultMap["value"])
		}
	})

	t.Run("memory load missing key", func(t *testing.T) {
		t.Parallel()
		loadTool, _ := registry.Get("memory_load")

		result, err := loadTool.Execute(t.Context(), map[string]any{"key": "missing"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		resultMap := result.(map[string]any)
		if resultMap["found"].(bool) {
			t.Error("expected not to find missing key")
		}
	})

	t.Run("tool context cancellation", func(t *testing.T) {
		t.Parallel()
		tool, _ := registry.Get("search")

		cancelCtx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately.

		_, err := tool.Execute(cancelCtx, map[string]any{"query": "test"})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	})
}

//nolint:gocognit // test function with many subtests
func TestMockLLMClient(t *testing.T) {
	t.Parallel()

	t.Run("complete basic", func(t *testing.T) {
		t.Parallel()
		client := &MockLLMClient{
			ResponseText: "test response",
		}

		messages := []Message{{Role: "user", Content: "hello"}}
		resp, err := client.Complete(t.Context(), messages)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp != "test response" {
			t.Errorf("got %q, want %q", resp, "test response")
		}
	})

	t.Run("complete with think responses", func(t *testing.T) {
		t.Parallel()
		client := &MockLLMClient{
			ThinkResponses: []ThinkOutput{
				{Reasoning: "step 1", Action: "search", ActionInput: map[string]any{"query": "test"}},
				{Reasoning: "step 2", Action: "respond"},
			},
		}

		messages := []Message{{Role: "user", Content: "think about this"}}

		// First call should return first think response.
		resp1, _ := client.Complete(t.Context(), messages)
		_, err := CompleteJSON[ThinkOutput](t.Context(), client, messages)
		if err != nil {
			t.Logf("raw response: %s", resp1)
		}

		client.Reset()
		think1, err := CompleteJSON[ThinkOutput](t.Context(), client, messages)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if think1.Action != "search" {
			t.Errorf("got action %q, want %q", think1.Action, "search")
		}
	})

	t.Run("stream tokens", func(t *testing.T) {
		t.Parallel()
		client := &MockLLMClient{
			ResponseText: "hello world",
			TokenDelay:   1 * time.Millisecond,
		}

		var tokens []string
		messages := []Message{{Role: "user", Content: "test"}}

		err := client.CompleteStream(t.Context(), messages, func(token string) {
			tokens = append(tokens, token)
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(tokens) == 0 {
			t.Error("expected tokens to be streamed")
		}
	})

	t.Run("stream with context cancellation", func(t *testing.T) {
		t.Parallel()
		client := &MockLLMClient{
			ResponseText: "hello world this is a long response",
			TokenDelay:   100 * time.Millisecond,
		}

		timeoutCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		messages := []Message{{Role: "user", Content: "test"}}
		err := client.CompleteStream(timeoutCtx, messages, func(_ string) {})

		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("expected DeadlineExceeded, got %v", err)
		}
	})

	t.Run("error propagation", func(t *testing.T) {
		t.Parallel()
		expectedErr := errors.New("test error")
		client := &MockLLMClient{Error: expectedErr}

		messages := []Message{{Role: "user", Content: "test"}}

		_, err := client.Complete(t.Context(), messages)
		if !errors.Is(err, expectedErr) {
			t.Errorf("expected %v, got %v", expectedErr, err)
		}

		err = client.CompleteStream(t.Context(), messages, func(_ string) {})
		if !errors.Is(err, expectedErr) {
			t.Errorf("expected %v, got %v", expectedErr, err)
		}
	})

	t.Run("call history", func(t *testing.T) {
		t.Parallel()
		client := &MockLLMClient{}

		messages := []Message{{Role: "user", Content: "test"}}
		_, _ = client.Complete(t.Context(), messages)
		_ = client.CompleteStream(t.Context(), messages, func(_ string) {})

		history := client.CallHistory()
		if len(history) != 2 {
			t.Errorf("expected 2 calls, got %d", len(history))
		}
		if history[0].Method != "Complete" {
			t.Errorf("expected Complete, got %s", history[0].Method)
		}
		if history[1].Method != "CompleteStream" {
			t.Errorf("expected CompleteStream, got %s", history[1].Method)
		}
	})
}

func TestCompleteJSON(t *testing.T) {
	t.Parallel()

	t.Run("parse raw json", func(t *testing.T) {
		t.Parallel()
		client := &MockLLMClient{
			ResponseText: `{"reasoning": "test", "action": "search"}`,
		}

		result, err := CompleteJSON[ThinkOutput](t.Context(), client, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Reasoning != "test" {
			t.Errorf("got reasoning %q, want %q", result.Reasoning, "test")
		}
	})

	t.Run("parse json in code block", func(t *testing.T) {
		t.Parallel()
		client := &MockLLMClient{
			ResponseText: "Here's the response:\n```json\n{\"reasoning\": \"test\", \"action\": \"respond\"}\n```",
		}

		result, err := CompleteJSON[ThinkOutput](t.Context(), client, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Action != "respond" {
			t.Errorf("got action %q, want %q", result.Action, "respond")
		}
	})

	t.Run("parse json with surrounding text", func(t *testing.T) {
		t.Parallel()
		client := &MockLLMClient{
			ResponseText: "Let me think... {\"reasoning\": \"hmm\", \"action\": \"search\"} ...that's my answer.",
		}

		result, err := CompleteJSON[ThinkOutput](t.Context(), client, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Reasoning != "hmm" {
			t.Errorf("got reasoning %q, want %q", result.Reasoning, "hmm")
		}
	})
}

func TestBuildSystemPrompt(t *testing.T) {
	t.Parallel()

	tools := []Tool{
		{
			Name:        "search",
			Description: "Search the web",
			Parameters: map[string]ParamDef{
				"query": {Type: "string", Required: true},
			},
		},
	}

	prompt := BuildSystemPrompt("", tools)

	if prompt == "" {
		t.Error("expected non-empty prompt")
	}
	if !containsString(prompt, "search") {
		t.Error("prompt should contain tool name")
	}
	if !containsString(prompt, "query") {
		t.Error("prompt should contain parameter name")
	}
}

//nolint:gocognit // test function with many subtests
func TestAgent(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("basic run", func(t *testing.T) {
		t.Parallel()

		memory := NewMemory()
		tools := BuiltinTools(memory)

		client := &MockLLMClient{
			ThinkResponses: []ThinkOutput{
				{Reasoning: "I need to search", Action: "search", ActionInput: map[string]any{"query": "test"}},
				{Reasoning: "Got results, ready to respond", Action: "respond"},
			},
			ResponseText: "Here is my answer based on the search.",
		}

		config := AgentConfig{
			Query:         "What is the meaning of life?",
			MaxIterations: 5,
		}

		var events []AgentEvent
		agent := NewAgent(config, client, tools, WithEventHandler(func(e AgentEvent) {
			events = append(events, e)
		}))

		err := agent.Run(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Check final response.
		response, ok := agent.GetFinalResponse()
		if !ok {
			t.Error("expected final response")
		}
		if response == "" {
			t.Error("expected non-empty response")
		}

		// Check state.
		state := agent.GetState()
		if state.Phase != PhaseDone {
			t.Errorf("got phase %v, want %v", state.Phase, PhaseDone)
		}

		// Check events were emitted.
		if len(events) == 0 {
			t.Error("expected events to be emitted")
		}

		// Verify we got start and complete events.
		hasStart := false
		hasComplete := false
		for _, e := range events {
			if e.Type == EventStart {
				hasStart = true
			}
			if e.Type == EventComplete {
				hasComplete = true
			}
		}
		if !hasStart {
			t.Error("expected start event")
		}
		if !hasComplete {
			t.Error("expected complete event")
		}
	})

	t.Run("with streaming", func(t *testing.T) {
		t.Parallel()

		memory := NewMemory()
		tools := BuiltinTools(memory)

		client := &MockLLMClient{
			ThinkResponses: []ThinkOutput{
				{Reasoning: "Ready to respond", Action: "respond"},
			},
			ResponseText: "Streaming response here",
			TokenDelay:   1 * time.Millisecond,
		}

		config := AgentConfig{
			Query:         "Test query",
			MaxIterations: 3,
			StreamTokens:  true,
		}

		var tokenEvents int
		agent := NewAgent(config, client, tools, WithEventHandler(func(e AgentEvent) {
			if e.Type == EventToken {
				tokenEvents++
			}
		}))

		err := agent.Run(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if tokenEvents == 0 {
			t.Error("expected token events from streaming")
		}
	})

	t.Run("max iterations exceeded", func(t *testing.T) {
		t.Parallel()

		memory := NewMemory()
		tools := BuiltinTools(memory)

		// Client never returns "respond" action - provide enough for MaxIterations.
		client := &MockLLMClient{
			ThinkResponses: []ThinkOutput{
				{Reasoning: "Keep searching 1", Action: "search", ActionInput: map[string]any{"query": "more"}},
				{Reasoning: "Keep searching 2", Action: "search", ActionInput: map[string]any{"query": "again"}},
				{Reasoning: "Keep searching 3", Action: "search", ActionInput: map[string]any{"query": "still"}},
			},
		}

		config := AgentConfig{
			Query:         "Infinite search",
			MaxIterations: 2,
		}

		agent := NewAgent(config, client, tools)

		err := agent.Run(ctx)
		if err == nil {
			t.Error("expected error for max iterations")
		}
		if !containsString(err.Error(), "max iterations") {
			t.Errorf("error should mention max iterations: %v", err)
		}
	})

	t.Run("with retry config", func(t *testing.T) {
		t.Parallel()

		memory := NewMemory()
		tools := BuiltinTools(memory)

		client := &MockLLMClient{
			ThinkResponses: []ThinkOutput{
				{Reasoning: "Done", Action: "respond"},
			},
			ResponseText: "Answer",
		}

		config := AgentConfig{
			Query:         "Test",
			MaxIterations: 3,
			RetryConfig:   DefaultRetryConfig(),
		}

		agent := NewAgent(config, client, tools)

		err := agent.Run(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		t.Parallel()

		memory := NewMemory()
		tools := BuiltinTools(memory)

		client := &MockLLMClient{
			CompleteDelay: 100 * time.Millisecond,
		}

		config := AgentConfig{
			Query:         "Test",
			MaxIterations: 10,
		}

		agent := NewAgent(config, client, tools)

		ctxWithTimeout, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		err := agent.Run(ctxWithTimeout)
		if err == nil {
			t.Error("expected error from context cancellation")
		}
	})
}

func TestIterationKey(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		phase     string
		iteration int
		expected  string
	}{
		{"think", 0, "agent:think:0"},
		{"action", 5, "agent:action:5"},
		{"observe", 10, "agent:observe:10"},
	}

	for _, tc := range testCases {
		got := IterationKey(tc.phase, tc.iteration)
		if got != tc.expected {
			t.Errorf("IterationKey(%q, %d) = %q, want %q", tc.phase, tc.iteration, got, tc.expected)
		}
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStringHelper(s, substr))
}

func containsStringHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
