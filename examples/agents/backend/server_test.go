package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a2y-d5l/neng/examples/agents/internal"
)

// --- Mock LLM Client ---

type mockLLMClient struct {
	responses []string
	index     int
	mu        sync.Mutex
}

func newMockLLMClient(responses ...string) *mockLLMClient {
	return &mockLLMClient{responses: responses}
}

func (m *mockLLMClient) Complete(
	_ context.Context,
	_ []internal.Message,
) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.index >= len(m.responses) {
		return `{"reasoning": "done", "action": "respond"}`, nil
	}
	resp := m.responses[m.index]
	m.index++
	return resp, nil
}

func (m *mockLLMClient) CompleteStream(
	ctx context.Context,
	messages []internal.Message,
	onToken func(token string),
) error {
	resp, err := m.Complete(ctx, messages)
	if err != nil {
		return err
	}
	for word := range strings.SplitSeq(resp, " ") {
		onToken(word + " ")
	}
	return nil
}

// --- Server Tests ---

func TestNewServer(t *testing.T) {
	t.Parallel()

	srv := NewServer()

	if srv.logger == nil {
		t.Error("expected logger to be set")
	}

	if srv.tools == nil {
		t.Error("expected tools to be set")
	}

	if srv.sink == nil {
		t.Error("expected sink to be set")
	}

	if srv.history == nil {
		t.Error("expected history to be set")
	}
}

func TestServer_HandleStatus_NotRunning(t *testing.T) {
	t.Parallel()

	srv := NewServer()
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var resp StatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Running {
		t.Error("expected running to be false")
	}
}

func TestServer_HandleGetTools(t *testing.T) {
	t.Parallel()

	srv := NewServer()
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/tools", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var resp struct {
		Tools []ToolInfo `json:"tools"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Should have builtin tools
	if len(resp.Tools) == 0 {
		t.Error("expected at least one tool")
	}

	// Check that tools have required fields
	for _, tool := range resp.Tools {
		if tool.Name == "" {
			t.Error("expected tool to have a name")
		}
		if tool.Description == "" {
			t.Error("expected tool to have a description")
		}
	}
}

func TestServer_HandleRun_NoLLM(t *testing.T) {
	t.Parallel()

	srv := NewServer()
	handler := srv.Handler()

	body := bytes.NewBufferString(`{"query": "test query"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/run", body)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status %d, got %d", http.StatusServiceUnavailable, rec.Code)
	}
}

func TestServer_HandleRun_EmptyQuery(t *testing.T) {
	t.Parallel()

	mockLLM := newMockLLMClient()
	srv := NewServer(WithLLMClient(mockLLM))
	handler := srv.Handler()

	body := bytes.NewBufferString(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/api/run", body)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestServer_HandleRun_Success(t *testing.T) {
	t.Parallel()

	mockLLM := newMockLLMClient(
		`{"reasoning": "I'll respond", "action": "respond"}`,
	)
	srv := NewServer(WithLLMClient(mockLLM))
	handler := srv.Handler()

	body := bytes.NewBufferString(`{"query": "hello", "maxIterations": 2}`)
	req := httptest.NewRequest(http.MethodPost, "/api/run", body)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Errorf("expected status %d, got %d", http.StatusAccepted, rec.Code)
	}

	var resp RunResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Status != "started" {
		t.Errorf("expected status 'started', got %q", resp.Status)
	}

	if resp.RunID == "" {
		t.Error("expected runId to be set")
	}

	// Wait a bit for the agent to complete
	time.Sleep(100 * time.Millisecond)
}

func TestServer_HandleRun_AlreadyRunning(t *testing.T) {
	t.Parallel()

	// Create a mock that takes a while
	mockLLM := newMockLLMClient()
	srv := NewServer(WithLLMClient(mockLLM))

	// Manually set running state
	srv.runMutex.Lock()
	srv.isRunning = true
	srv.runMutex.Unlock()

	handler := srv.Handler()

	body := bytes.NewBufferString(`{"query": "test"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/run", body)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("expected status %d, got %d", http.StatusConflict, rec.Code)
	}
}

func TestServer_HandleCancel_NotRunning(t *testing.T) {
	t.Parallel()

	srv := NewServer()
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/cancel", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Errorf("expected status %d, got %d", http.StatusConflict, rec.Code)
	}
}

func TestServer_HandleGetHistory(t *testing.T) {
	t.Parallel()

	srv := NewServer()
	handler := srv.Handler()

	// Create a history entry
	srv.history.StartRun("test query", 10, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/history", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var resp struct {
		Runs []*RunEntry `json:"runs"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.Runs) != 1 {
		t.Errorf("expected 1 entry, got %d", len(resp.Runs))
	}
}

func TestServer_HandleGetHistoryEntry_NotFound(t *testing.T) {
	t.Parallel()

	srv := NewServer()
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/history/nonexistent", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

// --- SSE Event Sink Tests ---

func TestSSEEventSink_AddRemoveClient(t *testing.T) {
	t.Parallel()

	sink := NewSSEEventSink(nil)

	ch := make(chan string, 10)
	sink.AddClient(ch)

	if len(sink.clients) != 1 {
		t.Errorf("expected 1 client, got %d", len(sink.clients))
	}

	sink.RemoveClient(ch)

	if len(sink.clients) != 0 {
		t.Errorf("expected 0 clients, got %d", len(sink.clients))
	}
}

func TestSSEEventSink_Broadcast(t *testing.T) {
	t.Parallel()

	sink := NewSSEEventSink(nil)

	ch := make(chan string, 10)
	sink.AddClient(ch)

	sink.broadcast("test message")

	select {
	case msg := <-ch:
		if msg != "test message" {
			t.Errorf("expected 'test message', got %q", msg)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("expected to receive message")
	}
}

func TestSSEEventSink_BroadcastAgentEvent(t *testing.T) {
	t.Parallel()

	sink := NewSSEEventSink(nil)

	ch := make(chan string, 10)
	sink.AddClient(ch)

	ev := internal.AgentEvent{
		Type: internal.EventStart,
		Time: time.Now(),
	}
	sink.BroadcastAgentEvent(ev)

	select {
	case msg := <-ch:
		// Message is now in SSE format: "event: start\ndata: {...}\n\n"
		// Parse out the data portion
		lines := strings.Split(msg, "\n")
		var dataLine string
		for _, line := range lines {
			if after, ok := strings.CutPrefix(line, "data: "); ok {
				dataLine = after
				break
			}
		}
		if dataLine == "" {
			t.Fatal("expected data line in SSE message")
		}

		var decoded internal.AgentEvent
		if err := json.Unmarshal([]byte(dataLine), &decoded); err != nil {
			t.Fatalf("failed to decode event: %v", err)
		}
		if decoded.Type != internal.EventStart {
			t.Errorf("expected type %q, got %q", internal.EventStart, decoded.Type)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("expected to receive message")
	}
}

// --- Run History Tests ---

func TestRunHistory_StartRun(t *testing.T) {
	t.Parallel()

	history := NewRunHistory()

	entry := history.StartRun("test query", 10, []string{"search"})

	if entry.ID == "" {
		t.Error("expected ID to be set")
	}
	if entry.Query != "test query" {
		t.Errorf("expected query 'test query', got %q", entry.Query)
	}
	if entry.MaxIterations != 10 {
		t.Errorf("expected maxIterations 10, got %d", entry.MaxIterations)
	}
	if len(entry.EnabledTools) != 1 || entry.EnabledTools[0] != "search" {
		t.Errorf("expected enabledTools [search], got %v", entry.EnabledTools)
	}
}

func TestRunHistory_CompleteRun(t *testing.T) {
	t.Parallel()

	history := NewRunHistory()

	entry := history.StartRun("test", 10, nil)
	history.CompleteRun(entry.ID, "response text", nil)

	completed := history.Get(entry.ID)
	if completed.Response != "response text" {
		t.Errorf("expected response 'response text', got %q", completed.Response)
	}
	if completed.CompletedAt.IsZero() {
		t.Error("expected completedAt to be set")
	}
}

func TestRunHistory_List(t *testing.T) {
	t.Parallel()

	history := NewRunHistory()

	history.StartRun("first", 10, nil)
	history.StartRun("second", 10, nil)
	history.StartRun("third", 10, nil)

	entries := history.List()

	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}

	// List should be newest first
	if entries[0].Query != "third" {
		t.Errorf("expected first entry to be 'third', got %q", entries[0].Query)
	}
}

func TestRunHistory_MaxEntries(t *testing.T) {
	t.Parallel()

	history := NewRunHistory()

	// Add more than max entries
	for i := range maxHistoryEntries + 10 {
		history.StartRun("query "+itoa(i), 10, nil)
	}

	if len(history.entries) != maxHistoryEntries {
		t.Errorf("expected %d entries, got %d", maxHistoryEntries, len(history.entries))
	}
}

// --- CORS Tests ---

func TestServer_CORS(t *testing.T) {
	t.Parallel()

	srv := NewServer()
	handler := srv.Handler()

	req := httptest.NewRequest(http.MethodOptions, "/api/status", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected status %d, got %d", http.StatusNoContent, rec.Code)
	}

	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("expected CORS header to be set")
	}
}
