// Package backend provides the HTTP server for the agent web UI.
//
// The server exposes REST endpoints for agent control and SSE streams
// for real-time event observation.
package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/a2y-d5l/neng/examples/agents/internal"
)

// Server configuration constants.
const (
	// sseClientBufferSize is the channel buffer size for SSE clients.
	sseClientBufferSize = 100

	// maxDroppedMessages is the threshold after which a slow SSE client is evicted.
	maxDroppedMessages = 10

	// serverReadHeaderTimeout prevents Slowloris attacks.
	serverReadHeaderTimeout = 10 * time.Second

	// serverAddr is the default server address.
	serverAddr = ":8080"
)

// Server encapsulates all server state.
type Server struct {
	logger        *slog.Logger
	sink          *SSEEventSink
	tools         *internal.ToolRegistry
	llm           internal.LLMClient
	staticFS      fs.FS
	runMutex      sync.Mutex
	isRunning     bool
	currentCtx    context.Context
	currentCancel context.CancelFunc
	history       *RunHistory
}

// ServerOption configures a Server.
type ServerOption func(*Server)

// WithLogger sets a custom logger for the server.
func WithLogger(logger *slog.Logger) ServerOption {
	return func(s *Server) {
		s.logger = logger
	}
}

// WithLLMClient sets a custom LLM client for the server.
func WithLLMClient(llm internal.LLMClient) ServerOption {
	return func(s *Server) {
		s.llm = llm
	}
}

// WithStaticFS sets the filesystem for serving static files.
func WithStaticFS(staticFS fs.FS) ServerOption {
	return func(s *Server) {
		s.staticFS = staticFS
	}
}

// NewServer creates a new Server instance with default configuration.
func NewServer(opts ...ServerOption) *Server {
	memory := internal.NewMemory()
	s := &Server{
		logger:  slog.Default(),
		tools:   internal.BuiltinTools(memory),
		history: NewRunHistory(),
	}

	for _, opt := range opts {
		opt(s)
	}

	s.sink = NewSSEEventSink(s.logger)

	return s
}

// Handler returns an http.Handler with all routes configured.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// API endpoints
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("POST /api/run", s.handleRun)
	mux.HandleFunc("POST /api/cancel", s.handleCancel)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/tools", s.handleGetTools)
	mux.HandleFunc("GET /api/history", s.handleGetHistory)
	mux.HandleFunc("GET /api/history/{id}", s.handleGetHistoryEntry)

	// Static file serving
	if s.staticFS != nil {
		// Serve static assets under /static/
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(s.staticFS))))

		// Serve index.html at root
		mux.HandleFunc("/", s.handleIndex)
	}

	// Wrap with middleware
	return s.withMiddleware(mux)
}

// ListenAndServe starts the server on the default address.
func (s *Server) ListenAndServe() error {
	return s.ListenAndServeAddr(serverAddr)
}

// ListenAndServeAddr starts the server on the given address.
func (s *Server) ListenAndServeAddr(addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: serverReadHeaderTimeout,
	}

	s.logger.Info("Starting agent server", "addr", addr)
	return srv.ListenAndServe()
}

// withMiddleware wraps the handler with logging, CORS, and recovery.
func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CORS headers for development
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// Request logging
		start := time.Now()
		s.logger.Debug("Request started", "method", r.Method, "path", r.URL.Path)

		// Recovery from panics
		defer func() {
			if err := recover(); err != nil {
				s.logger.Error("Panic recovered", "error", err)
				http.Error(w, "Internal server error", http.StatusInternalServerError)
			}
		}()

		next.ServeHTTP(w, r)

		s.logger.Debug("Request completed",
			"method", r.Method,
			"path", r.URL.Path,
			"duration", time.Since(start),
		)
	})
}

// --- Static File Handlers ---

// handleIndex serves the main HTML page.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	// Only serve index.html at the root path
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	// Open index.html from the embedded filesystem
	f, err := s.staticFS.Open("index.html")
	if err != nil {
		http.Error(w, "Index not found", http.StatusNotFound)
		return
	}
	defer f.Close()

	// Read content
	content, err := io.ReadAll(f)
	if err != nil {
		http.Error(w, "Failed to read index", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(content)
}

// --- API Handlers ---

// RunRequest is the request body for starting an agent run.
type RunRequest struct {
	Query         string   `json:"query"`
	MaxIterations int      `json:"maxIterations,omitempty"`
	EnabledTools  []string `json:"enabledTools,omitempty"`
	TimeoutMs     int      `json:"timeoutMs,omitempty"`
	StreamTokens  bool     `json:"streamTokens,omitempty"`
}

// Default run configuration values.
const (
	defaultMaxIterations = 10
	defaultTimeoutMs     = 60000 // 1 minute
)

// RunResponse is returned when an agent run is started.
type RunResponse struct {
	RunID  string `json:"runId"`
	Status string `json:"status"`
}

// handleRun starts a new agent run.
func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	s.runMutex.Lock()
	if s.isRunning {
		s.runMutex.Unlock()
		writeError(w, http.StatusConflict, "Agent is already running")
		return
	}
	s.isRunning = true
	s.runMutex.Unlock()

	var req RunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.setNotRunning()
		writeError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if req.Query == "" {
		s.setNotRunning()
		writeError(w, http.StatusBadRequest, "Query is required")
		return
	}

	// Apply defaults
	if req.MaxIterations <= 0 {
		req.MaxIterations = defaultMaxIterations
	}
	if req.TimeoutMs <= 0 {
		req.TimeoutMs = defaultTimeoutMs
	}

	// Check LLM client
	if s.llm == nil {
		s.setNotRunning()
		writeError(w, http.StatusServiceUnavailable, "No LLM client configured")
		return
	}

	// Create run entry
	entry := s.history.StartRun(req.Query, req.MaxIterations, req.EnabledTools)

	// Create context with timeout - use Background() not r.Context() since the
	// agent runs after the HTTP response is sent
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(req.TimeoutMs)*time.Millisecond)
	s.currentCtx = ctx
	s.currentCancel = cancel

	// Create agent config
	config := internal.AgentConfig{
		Query:         req.Query,
		MaxIterations: req.MaxIterations,
		EnabledTools:  req.EnabledTools,
		StreamTokens:  req.StreamTokens,
	}

	// Create agent with event handler
	agentInst := internal.NewAgent(
		config,
		s.llm,
		s.tools,
		internal.WithEventHandler(func(ev internal.AgentEvent) {
			s.sink.BroadcastAgentEvent(ev)
		}),
	)

	// Run in background
	go func() {
		defer cancel()
		defer s.setNotRunning()

		runErr := agentInst.Run(ctx)

		// Get final response if available
		response, _ := agentInst.GetFinalResponse()

		// Complete history entry
		if runErr != nil {
			s.history.CompleteRun(entry.ID, "", runErr)
		} else {
			s.history.CompleteRun(entry.ID, response, nil)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(RunResponse{
		RunID:  entry.ID,
		Status: "started",
	}); err != nil {
		s.logger.Error("Failed to encode response", "error", err)
	}
}

func (s *Server) setNotRunning() {
	s.runMutex.Lock()
	s.isRunning = false
	s.runMutex.Unlock()
}

// handleCancel cancels a running agent.
func (s *Server) handleCancel(w http.ResponseWriter, _ *http.Request) {
	s.runMutex.Lock()
	if !s.isRunning {
		s.runMutex.Unlock()
		writeError(w, http.StatusConflict, "No agent is running")
		return
	}

	if s.currentCancel != nil {
		s.currentCancel()
	}
	s.runMutex.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "cancelled"}); err != nil {
		s.logger.Error("Failed to encode response", "error", err)
	}
}

// StatusResponse is returned by the status endpoint.
type StatusResponse struct {
	Running bool   `json:"running"`
	RunID   string `json:"runId,omitempty"`
}

// handleStatus returns the current agent status.
func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	s.runMutex.Lock()
	running := s.isRunning
	s.runMutex.Unlock()

	resp := StatusResponse{Running: running}

	// Get current run ID if running
	if running {
		if latest := s.history.GetLatest(); latest != nil && latest.CompletedAt.IsZero() {
			resp.RunID = latest.ID
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.logger.Error("Failed to encode response", "error", err)
	}
}

// ToolInfo represents a tool for the API response.
type ToolInfo struct {
	Name        string                       `json:"name"`
	Description string                       `json:"description"`
	Parameters  map[string]internal.ParamDef `json:"parameters"`
}

// handleGetTools returns the list of available tools.
func (s *Server) handleGetTools(w http.ResponseWriter, _ *http.Request) {
	toolList := s.tools.All()
	tools := make([]ToolInfo, 0, len(toolList))

	for _, tool := range toolList {
		tools = append(tools, ToolInfo{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.Parameters,
		})
	}

	response := struct {
		Tools []ToolInfo `json:"tools"`
	}{Tools: tools}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		s.logger.Error("Failed to encode response", "error", err)
	}
}

// handleGetHistory returns the run history.
func (s *Server) handleGetHistory(w http.ResponseWriter, _ *http.Request) {
	entries := s.history.List()

	response := struct {
		Runs []*RunEntry `json:"runs"`
	}{Runs: entries}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		s.logger.Error("Failed to encode response", "error", err)
	}
}

// handleGetHistoryEntry returns a specific history entry.
func (s *Server) handleGetHistoryEntry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	entry := s.history.Get(id)

	if entry == nil {
		writeError(w, http.StatusNotFound, "Run not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(entry); err != nil {
		s.logger.Error("Failed to encode response", "error", err)
	}
}

// --- SSE Events ---

// handleEvents sets up an SSE stream for real-time events.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Create client channel
	ch := make(chan string, sseClientBufferSize)
	s.sink.AddClient(ch)
	defer s.sink.RemoveClient(ch)

	// Flush headers
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	// Send connected event (formatted as SSE)
	connectedEvent := fmt.Sprintf("event: connected\ndata: {\"type\":\"connected\",\"time\":\"%s\"}\n\n",
		time.Now().Format(time.RFC3339))
	if _, err := w.Write([]byte(connectedEvent)); err != nil {
		return
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	// Stream events until client disconnects
	for {
		select {
		case <-r.Context().Done():
			return
		case data, dataOk := <-ch:
			if !dataOk {
				return
			}
			// Data is already formatted as complete SSE message
			if _, err := w.Write([]byte(data)); err != nil {
				return
			}
			if f, canFlush := w.(http.Flusher); canFlush {
				f.Flush()
			}
		}
	}
}

// writeError sends a JSON error response.
func writeError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// --- SSE Event Sink ---

// sseClientState tracks per-client SSE connection state.
type sseClientState struct {
	ch           chan string
	droppedCount atomic.Int32
}

// SSEEventSink broadcasts events to connected SSE clients.
type SSEEventSink struct {
	logger  *slog.Logger
	clients map[chan string]*sseClientState
	mu      sync.RWMutex
}

// NewSSEEventSink creates a new SSE event sink.
func NewSSEEventSink(logger *slog.Logger) *SSEEventSink {
	return &SSEEventSink{
		logger:  logger,
		clients: make(map[chan string]*sseClientState),
	}
}

// AddClient registers a new SSE client.
func (s *SSEEventSink) AddClient(ch chan string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clients[ch] = &sseClientState{ch: ch}
}

// RemoveClient unregisters an SSE client.
func (s *SSEEventSink) RemoveClient(ch chan string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.clients[ch]; exists {
		delete(s.clients, ch)
		close(ch)
	}
}

// broadcast sends data to all connected clients.
func (s *SSEEventSink) broadcast(data string) {
	// Snapshot clients under read lock
	s.mu.RLock()
	snapshot := make([]*sseClientState, 0, len(s.clients))
	for _, state := range s.clients {
		snapshot = append(snapshot, state)
	}
	s.mu.RUnlock()

	// Track clients to evict
	var toEvict []chan string

	for _, state := range snapshot {
		select {
		case state.ch <- data:
			state.droppedCount.Store(0)
		default:
			newCount := state.droppedCount.Add(1)
			if newCount >= maxDroppedMessages {
				s.logger.Warn("SSE client exceeded drop threshold, evicting",
					"threshold", maxDroppedMessages)
				toEvict = append(toEvict, state.ch)
			}
		}
	}

	// Evict slow clients
	if len(toEvict) > 0 {
		s.mu.Lock()
		for _, ch := range toEvict {
			if _, exists := s.clients[ch]; exists {
				delete(s.clients, ch)
				close(ch)
			}
		}
		s.mu.Unlock()
	}
}

// BroadcastAgentEvent sends an agent event to all connected clients.
func (s *SSEEventSink) BroadcastAgentEvent(ev internal.AgentEvent) {
	data, err := json.Marshal(ev)
	if err != nil {
		s.logger.Error("Failed to marshal agent event", "error", err)
		return
	}
	// Format as SSE with event type and data
	sseMessage := fmt.Sprintf("event: %s\ndata: %s\n\n", ev.Type, string(data))
	s.broadcast(sseMessage)
}

// BroadcastError sends an error event to all connected clients.
func (s *SSEEventSink) BroadcastError(err error) {
	ev := internal.AgentEvent{
		Type:  internal.EventError,
		Time:  time.Now(),
		Error: err.Error(),
	}
	s.BroadcastAgentEvent(ev)
}

// --- Run History ---

// RunEntry represents a completed or in-progress agent run.
type RunEntry struct {
	ID            string    `json:"id"`
	Query         string    `json:"query"`
	MaxIterations int       `json:"maxIterations"`
	EnabledTools  []string  `json:"enabledTools,omitempty"`
	StartedAt     time.Time `json:"startedAt"`
	CompletedAt   time.Time `json:"completedAt,omitzero"`
	Response      string    `json:"response,omitempty"`
	Error         string    `json:"error,omitempty"`
}

// RunHistory tracks agent run history.
type RunHistory struct {
	entries []*RunEntry
	byID    map[string]*RunEntry
	counter int
	mu      sync.RWMutex
}

// Maximum number of history entries to keep.
const maxHistoryEntries = 50

// NewRunHistory creates a new run history.
func NewRunHistory() *RunHistory {
	return &RunHistory{
		entries: make([]*RunEntry, 0),
		byID:    make(map[string]*RunEntry),
	}
}

// StartRun creates a new run entry.
func (h *RunHistory) StartRun(query string, maxIterations int, enabledTools []string) *RunEntry {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.counter++
	entry := &RunEntry{
		ID:            generateRunID(h.counter),
		Query:         query,
		MaxIterations: maxIterations,
		EnabledTools:  enabledTools,
		StartedAt:     time.Now(),
	}

	h.entries = append(h.entries, entry)
	h.byID[entry.ID] = entry

	// Trim old entries
	if len(h.entries) > maxHistoryEntries {
		old := h.entries[0]
		delete(h.byID, old.ID)
		h.entries = h.entries[1:]
	}

	return entry
}

// CompleteRun marks a run as completed.
func (h *RunHistory) CompleteRun(id, response string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	entry, ok := h.byID[id]
	if !ok {
		return
	}

	entry.CompletedAt = time.Now()
	entry.Response = response
	if err != nil && !errors.Is(err, context.Canceled) {
		entry.Error = err.Error()
	}
}

// Get returns a run entry by ID.
func (h *RunHistory) Get(id string) *RunEntry {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.byID[id]
}

// GetLatest returns the most recent run entry.
func (h *RunHistory) GetLatest() *RunEntry {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if len(h.entries) == 0 {
		return nil
	}
	return h.entries[len(h.entries)-1]
}

// List returns all run entries, newest first.
func (h *RunHistory) List() []*RunEntry {
	h.mu.RLock()
	defer h.mu.RUnlock()

	result := make([]*RunEntry, len(h.entries))
	for i, entry := range h.entries {
		result[len(h.entries)-1-i] = entry
	}
	return result
}

// generateRunID creates a unique run ID.
func generateRunID(counter int) string {
	return "run-" + time.Now().Format("20060102-150405") + "-" + itoa(counter)
}

// itoa converts an int to a string without importing strconv.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	if i < 0 {
		return "-" + itoa(-i)
	}
	s := ""
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return s
}
