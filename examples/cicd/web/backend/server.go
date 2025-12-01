package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/a2y-d5l/neng"
)

// Server configuration constants.
const (
	// sseClientBufferSize is the channel buffer size for SSE clients.
	sseClientBufferSize = 100

	// maxDroppedMessages is the threshold after which a slow SSE client is evicted.
	maxDroppedMessages = 10

	// planInfoDelay is the delay before starting execution to allow clients to receive plan info.
	planInfoDelay = 100 * time.Millisecond

	// serverReadHeaderTimeout prevents Slowloris attacks.
	serverReadHeaderTimeout = 10 * time.Second

	// serverShutdownTimeout is the graceful shutdown timeout.
	serverShutdownTimeout = 10 * time.Second

	// serverAddr is the default server address.
	serverAddr = ":8080"
)

// ClientEvent represents a JSON-serializable event sent to the web client.
type ClientEvent struct {
	Time       time.Time     `json:"time"`
	Result     *ClientResult `json:"result,omitempty"`
	Type       string        `json:"type"`
	TargetName string        `json:"targetName,omitempty"`
	TargetDesc string        `json:"targetDesc,omitempty"`
	TargetDeps []string      `json:"targetDeps,omitempty"`
}

// ClientResult represents a JSON-serializable result.
type ClientResult struct {
	StartedAt   time.Time `json:"startedAt"`
	CompletedAt time.Time `json:"completedAt"`
	Name        string    `json:"name"`
	Error       string    `json:"error,omitempty"`
	DurationMs  int64     `json:"durationMs"`
	Skipped     bool      `json:"skipped"`
}

// ClientSummary represents the final run summary for the client.
type ClientSummary struct {
	Results map[string]*ClientResult `json:"results"`
	Type    string                   `json:"type"`
	Error   string                   `json:"error,omitempty"`
	Failed  bool                     `json:"failed"`
}

// PlanInfo represents plan metadata sent to the client.
type PlanInfo struct {
	Type    string   `json:"type"`
	Targets []Target `json:"targets"`
	Stages  []Stage  `json:"stages"`
}

// Target represents target info for the client.
type Target struct {
	Name string   `json:"name"`
	Desc string   `json:"desc"`
	Deps []string `json:"deps"`
}

// Stage represents a stage for the client.
type Stage struct {
	Targets []string `json:"targets"`
	Index   int      `json:"index"`
}

// TargetDefinition is a pre-defined target that users can select.
type TargetDefinition struct {
	Name        string   `json:"name"`
	Desc        string   `json:"desc"`
	Deps        []string `json:"deps"`
	DurationMin int      `json:"durationMin"` // min duration in ms
	DurationMax int      `json:"durationMax"` // max duration in ms
	CanFail     bool     `json:"canFail"`     // if true, can be set to fail
}

// AvailableTargetsResponse is sent to clients listing available targets.
type AvailableTargetsResponse struct {
	Targets []TargetDefinition `json:"targets"`
}

// RunRequest is the request body for starting a run.
type RunRequest struct {
	Targets     []string `json:"targets"`     // target names to include in the plan
	RootTargets []string `json:"rootTargets"` // targets to execute (and their deps)
	FailTargets []string `json:"failTargets"` // targets that should fail
}

// PreviewRequest is the request body for previewing a plan.
type PreviewRequest struct {
	Targets     []string `json:"targets"`     // target names to include in the plan
	RootTargets []string `json:"rootTargets"` // targets to execute (and their deps)
}

// PreviewResponse shows what would be executed.
type PreviewResponse struct {
	Error   string   `json:"error,omitempty"`
	Targets []Target `json:"targets,omitempty"`
	Stages  []Stage  `json:"stages,omitempty"`
	Valid   bool     `json:"valid"`
}

// SavedPlan represents a saved/pre-compiled plan configuration.
type SavedPlan struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	CreatedAt   string   `json:"createdAt"`
	Targets     []string `json:"targets"`
	IsBuiltin   bool     `json:"isBuiltin"`
}

// SavePlanRequest is the request body for saving a plan.
type SavePlanRequest struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Targets     []string   `json:"targets"`
	Stages      [][]string `json:"stages"`
}

// SavedPlansResponse returns all saved plans.
type SavedPlansResponse struct {
	Plans []SavedPlan `json:"plans"`
}

// RunFromPlanRequest is the request to run from a saved plan.
type RunFromPlanRequest struct {
	PlanID      string   `json:"planId"`
	Mode        string   `json:"mode"`        // 'all', 'target', or 'stage'
	TargetName  string   `json:"targetName"`  // for 'target' mode
	RootTargets []string `json:"rootTargets"` // specific targets to run (optional)
	StageIndex  *int     `json:"stageIndex"`  // specific stage to run (optional)
	FailTargets []string `json:"failTargets"` // targets that should fail
}

// Server encapsulates all server state.
type Server struct {
	logger            *slog.Logger
	sink              *sseEventSink
	availableTargets  []TargetDefinition
	savedPlans        map[string]*SavedPlan
	savedPlansMu      sync.RWMutex
	planCounter       int
	runMutex          sync.Mutex
	isRunning         bool
	currentCtx        context.Context
	currentCancel     context.CancelFunc
	defaultFullTarget []string
}

// defaultAvailableTargets returns the pre-configured target definitions for the demo pipeline.
//
//nolint:mnd,funlen // Demo data: DurationMin/DurationMax are intentional simulation values in milliseconds
func defaultAvailableTargets() []TargetDefinition {
	return []TargetDefinition{
		{
			Name:        "checkout",
			Desc:        "Checkout source code from repository",
			Deps:        nil,
			DurationMin: 200,
			DurationMax: 500,
			CanFail:     false,
		},
		{
			Name:        "install-deps",
			Desc:        "Install project dependencies",
			Deps:        []string{"checkout"},
			DurationMin: 400,
			DurationMax: 800,
			CanFail:     false,
		},
		{
			Name:        "lint",
			Desc:        "Run static analysis and linters",
			Deps:        []string{"install-deps"},
			DurationMin: 300,
			DurationMax: 500,
			CanFail:     true,
		},
		{
			Name:        "format-check",
			Desc:        "Check code formatting",
			Deps:        []string{"install-deps"},
			DurationMin: 200,
			DurationMax: 350,
			CanFail:     true,
		},
		{
			Name:        "unit-tests",
			Desc:        "Run unit test suite",
			Deps:        []string{"install-deps"},
			DurationMin: 600,
			DurationMax: 1000,
			CanFail:     true,
		},
		{
			Name:        "integration-tests",
			Desc:        "Run integration test suite",
			Deps:        []string{"unit-tests"},
			DurationMin: 800,
			DurationMax: 1300,
			CanFail:     true,
		},
		{
			Name:        "build",
			Desc:        "Compile and build artifacts",
			Deps:        []string{"lint", "format-check"},
			DurationMin: 500,
			DurationMax: 800,
			CanFail:     true,
		},
		{
			Name:        "security-scan",
			Desc:        "Run security vulnerability scan",
			Deps:        []string{"build"},
			DurationMin: 400,
			DurationMax: 700,
			CanFail:     true,
		},
		{
			Name:        "package",
			Desc:        "Package build artifacts",
			Deps:        []string{"build", "integration-tests"},
			DurationMin: 300,
			DurationMax: 500,
			CanFail:     false,
		},
		{
			Name:        "docker-build",
			Desc:        "Build Docker container image",
			Deps:        []string{"package"},
			DurationMin: 700,
			DurationMax: 1100,
			CanFail:     true,
		},
		{
			Name:        "push-registry",
			Desc:        "Push image to container registry",
			Deps:        []string{"docker-build", "security-scan"},
			DurationMin: 400,
			DurationMax: 700,
			CanFail:     true,
		},
		{
			Name:        "deploy-staging",
			Desc:        "Deploy to staging environment",
			Deps:        []string{"push-registry"},
			DurationMin: 500,
			DurationMax: 800,
			CanFail:     true,
		},
		{
			Name:        "smoke-tests",
			Desc:        "Run smoke tests on staging",
			Deps:        []string{"deploy-staging"},
			DurationMin: 300,
			DurationMax: 500,
			CanFail:     true,
		},
		{
			Name:        "deploy-prod",
			Desc:        "Deploy to production environment",
			Deps:        []string{"smoke-tests"},
			DurationMin: 600,
			DurationMax: 900,
			CanFail:     true,
		},
		{
			Name:        "notify",
			Desc:        "Send deployment notifications",
			Deps:        []string{"deploy-prod"},
			DurationMin: 100,
			DurationMax: 200,
			CanFail:     false,
		},
	}
}

// defaultFullTargetList returns the full list of targets for the default pipeline.
func defaultFullTargetList() []string {
	return []string{
		"checkout", "install-deps", "lint", "format-check",
		"unit-tests", "integration-tests", "build", "security-scan",
		"package", "docker-build", "push-registry", "deploy-staging",
		"smoke-tests", "deploy-prod", "notify",
	}
}

// NewServer creates a new Server instance with default configuration.
func NewServer() *Server {
	logger := slog.Default()
	s := &Server{
		logger:            logger,
		sink:              newSSEEventSink(logger),
		savedPlans:        make(map[string]*SavedPlan),
		availableTargets:  defaultAvailableTargets(),
		defaultFullTarget: defaultFullTargetList(),
	}
	s.initBuiltinPlans()
	return s
}

// initBuiltinPlans creates some pre-defined demo plans.
func (s *Server) initBuiltinPlans() {
	builtinPlans := []SavedPlan{
		{
			ID:          "builtin-full",
			Name:        "Full CI/CD Pipeline",
			Description: "Complete pipeline from checkout to production deployment",
			Targets: []string{
				"checkout",
				"install-deps",
				"lint",
				"format-check",
				"unit-tests",
				"integration-tests",
				"build",
				"security-scan",
				"package",
				"docker-build",
				"push-registry",
				"deploy-staging",
				"smoke-tests",
				"deploy-prod",
				"notify",
			},
			CreatedAt: time.Now().Format(time.RFC3339),
			IsBuiltin: true,
		},
		{
			ID:          "builtin-test",
			Name:        "Test Suite Only",
			Description: "Run linting and all tests without deployment",
			Targets: []string{
				"checkout",
				"install-deps",
				"lint",
				"format-check",
				"unit-tests",
				"integration-tests",
			},
			CreatedAt: time.Now().Format(time.RFC3339),
			IsBuiltin: true,
		},
		{
			ID:          "builtin-build",
			Name:        "Build & Package",
			Description: "Build artifacts and create Docker image",
			Targets: []string{
				"checkout",
				"install-deps",
				"lint",
				"format-check",
				"build",
				"package",
				"docker-build",
			},
			CreatedAt: time.Now().Format(time.RFC3339),
			IsBuiltin: true,
		},
		{
			ID:          "builtin-deploy-staging",
			Name:        "Deploy to Staging",
			Description: "Full pipeline up to staging deployment with smoke tests",
			Targets: []string{
				"checkout",
				"install-deps",
				"lint",
				"format-check",
				"unit-tests",
				"integration-tests",
				"build",
				"security-scan",
				"package",
				"docker-build",
				"push-registry",
				"deploy-staging",
				"smoke-tests",
			},
			CreatedAt: time.Now().Format(time.RFC3339),
			IsBuiltin: true,
		},
		{
			ID:          "builtin-quick",
			Name:        "Quick Check",
			Description: "Fast lint and format check only",
			Targets:     []string{"checkout", "install-deps", "lint", "format-check"},
			CreatedAt:   time.Now().Format(time.RFC3339),
			IsBuiltin:   true,
		},
	}

	s.savedPlansMu.Lock()
	for i := range builtinPlans {
		s.savedPlans[builtinPlans[i].ID] = &builtinPlans[i]
	}
	s.savedPlansMu.Unlock()
}

// sseClientState tracks per-client SSE connection state.
type sseClientState struct {
	ch           chan string
	droppedCount atomic.Int32
}

// sseEventSink implements engine.EventHandler and broadcasts events to SSE clients.
type sseEventSink struct {
	logger  *slog.Logger
	clients map[chan string]*sseClientState
	mu      sync.RWMutex
}

func newSSEEventSink(logger *slog.Logger) *sseEventSink {
	return &sseEventSink{
		logger:  logger,
		clients: make(map[chan string]*sseClientState),
	}
}

func (s *sseEventSink) addClient(ch chan string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clients[ch] = &sseClientState{ch: ch}
}

func (s *sseEventSink) removeClient(ch chan string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Only close the channel if it's still in the map (not already evicted)
	if _, exists := s.clients[ch]; exists {
		delete(s.clients, ch)
		close(ch)
	}
}

func (s *sseEventSink) broadcast(data string) {
	// Snapshot clients under read lock to avoid holding lock during sends
	s.mu.RLock()
	snapshot := make([]*sseClientState, 0, len(s.clients))
	for _, state := range s.clients {
		snapshot = append(snapshot, state)
	}
	s.mu.RUnlock()

	// Track clients to evict (those exceeding drop threshold)
	var toEvict []chan string

	for _, state := range snapshot {
		select {
		case state.ch <- data:
			// Message sent successfully; reset drop counter
			state.droppedCount.Store(0)
		default:
			// Buffer full; increment drop counter atomically
			newCount := state.droppedCount.Add(1)
			if newCount >= maxDroppedMessages {
				s.logger.Warn(
					"SSE client exceeded drop threshold, evicting",
					"threshold",
					maxDroppedMessages,
				)
				toEvict = append(toEvict, state.ch)
			}
		}
	}

	// Evict slow clients outside the broadcast loop
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

func (s *sseEventSink) HandleEvent(ev neng.Event) {
	clientEv := ClientEvent{
		Time: ev.Time,
	}

	if ev.Task != nil {
		clientEv.TargetName = ev.Task.Name
		clientEv.TargetDesc = ev.Task.Desc
		clientEv.TargetDeps = ev.Task.Deps
	}

	switch ev.Type {
	case neng.EventTaskStarted:
		clientEv.Type = "started"
	case neng.EventTaskCompleted:
		clientEv.Type = "completed"
	case neng.EventTaskSkipped:
		clientEv.Type = "skipped"
	default:
		clientEv.Type = "unknown"
	}

	if ev.Result != nil {
		clientEv.Result = &ClientResult{
			Name:        ev.Result.Name,
			StartedAt:   ev.Result.StartedAt,
			CompletedAt: ev.Result.CompletedAt,
			Skipped:     ev.Result.Skipped,
			DurationMs:  ev.Result.CompletedAt.Sub(ev.Result.StartedAt).Milliseconds(),
		}
		if ev.Result.Err != nil {
			clientEv.Result.Error = ev.Result.Err.Error()
		}
	}

	data, err := json.Marshal(clientEv)
	if err != nil {
		s.logger.Error("Error marshaling event", "error", err)
		return
	}
	s.broadcast(string(data))
}

func (s *sseEventSink) sendPlanInfo(plan *neng.Plan) {
	stages := plan.Stages()
	planInfo := PlanInfo{
		Type:    "plan",
		Targets: make([]Target, 0),
		Stages:  make([]Stage, len(stages)),
	}

	for _, name := range plan.TaskNames() {
		t, _ := plan.Task(name)
		planInfo.Targets = append(planInfo.Targets, Target{
			Name: t.Name,
			Desc: t.Desc,
			Deps: t.Deps,
		})
	}

	for i, stage := range stages {
		planInfo.Stages[i] = Stage{
			Index:   stage.Index,
			Targets: stage.Tasks,
		}
	}

	data, err := json.Marshal(planInfo)
	if err != nil {
		s.logger.Error("Error marshaling plan info", "error", err)
		return
	}
	s.broadcast(string(data))
}

func (s *sseEventSink) sendSummary(summary neng.RunSummary, runErr error) {
	clientSummary := ClientSummary{
		Type:    "summary",
		Failed:  summary.Failed,
		Results: make(map[string]*ClientResult),
	}

	if runErr != nil {
		clientSummary.Error = runErr.Error()
	}

	for name, res := range summary.Results {
		cr := &ClientResult{
			Name:        res.Name,
			StartedAt:   res.StartedAt,
			CompletedAt: res.CompletedAt,
			Skipped:     res.Skipped,
			DurationMs:  res.CompletedAt.Sub(res.StartedAt).Milliseconds(),
		}
		if res.Err != nil {
			cr.Error = res.Err.Error()
		}
		clientSummary.Results[name] = cr
	}

	data, err := json.Marshal(clientSummary)
	if err != nil {
		s.logger.Error("Error marshaling summary", "error", err)
		return
	}
	s.broadcast(string(data))
}

// enableCORS wraps a handler with CORS headers for cross-origin requests.
func enableCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next(w, r)
	}
}

// writeJSONError writes a JSON error response with the specified status code.
func (s *Server) writeJSONError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(map[string]any{
		"status":  "error",
		"message": message,
	}); err != nil {
		s.logger.Error("Error encoding JSON error response", "error", err)
	}
}

// writeJSONSuccess writes a JSON success response.
func (s *Server) writeJSONSuccess(w http.ResponseWriter, data map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	if data == nil {
		data = make(map[string]any)
	}
	data["status"] = "success"
	if err := json.NewEncoder(w).Encode(data); err != nil {
		s.logger.Error("Error encoding JSON success response", "error", err)
	}
}

// writeJSON writes any JSON payload with the specified status code.
func (s *Server) writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		s.logger.Error("Error encoding JSON response", "error", err)
	}
}

// targetDefByName returns a target definition by name.
func (s *Server) targetDefByName(name string) (TargetDefinition, bool) {
	for _, t := range s.availableTargets {
		if t.Name == name {
			return t, true
		}
	}
	return TargetDefinition{}, false
}

// buildPlanFromSelection creates a Plan from selected target names.
func (s *Server) buildPlanFromSelection(
	targetNames []string,
	failTargets map[string]bool,
) (*neng.Plan, error) {
	targets := make([]neng.Task, 0, len(targetNames))

	// Build selectedSet once before the loop for O(n) instead of O(n²)
	selectedSet := make(map[string]bool, len(targetNames))
	for _, n := range targetNames {
		selectedSet[n] = true
	}

	for _, name := range targetNames {
		def, ok := s.targetDefByName(name)
		if !ok {
			return nil, fmt.Errorf("unknown target: %s", name)
		}

		// Filter deps to only include targets in the selection
		filteredDeps := make([]string, 0)
		for _, dep := range def.Deps {
			if selectedSet[dep] {
				filteredDeps = append(filteredDeps, dep)
			}
		}

		shouldFail := failTargets[name]
		durationMin := def.DurationMin
		durationMax := def.DurationMax

		targets = append(targets, neng.Task{
			Name: def.Name,
			Desc: def.Desc,
			Deps: filteredDeps,
			Run: func(ctx context.Context) error {
				// Calculate duration safely: if max <= min, use min as constant duration
				var duration time.Duration
				if durationMax > durationMin {
					//nolint:gosec // G404: Weak random is intentional for demo simulation durations
					duration = time.Duration(
						durationMin+rand.IntN(durationMax-durationMin),
					) * time.Millisecond
				} else {
					duration = time.Duration(durationMin) * time.Millisecond
				}
				// Use context-aware waiting to respect cancellation
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(duration):
				}
				if shouldFail {
					return errors.New("target failed (simulated)")
				}
				return nil
			},
		})
	}

	return neng.BuildPlan(targets...)
}

// drainExecutorChannels drains the Events and Results channels from an executor
// to prevent blocking. This is required when using an EventSink that handles
// events directly (like sseEventSink).
func drainExecutorChannels(exec *neng.Executor) {
	go func() {
		//nolint:revive // intentionally empty - draining channel
		for range exec.Events() {
		}
	}()
	go func() {
		//nolint:revive // intentionally empty - draining channel
		for range exec.Results() {
		}
	}()
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeJSONError(w, http.StatusInternalServerError, "SSE not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	clientCh := make(chan string, sseClientBufferSize)
	s.sink.addClient(clientCh)
	defer s.sink.removeClient(clientCh)

	// Send initial connection message
	fmt.Fprintf(w, "data: {\"type\":\"connected\"}\n\n")
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case msg, open := <-clientCh:
			if !open {
				// Channel was closed (client evicted for being slow)
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		}
	}
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	s.runMutex.Lock()
	if s.isRunning {
		s.runMutex.Unlock()
		s.writeJSONError(w, http.StatusConflict, "A run is already in progress")
		return
	}

	// Parse optional request body; if empty or missing targets, use default full pipeline
	var req RunRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			// Ignore io.EOF for truly empty bodies (ContentLength == -1 with no data)
			if !errors.Is(err, io.EOF) {
				s.runMutex.Unlock()
				s.writeJSONError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
				return
			}
		}
	}

	// Default to full pipeline if no targets specified
	if len(req.Targets) == 0 {
		req.Targets = s.defaultFullTarget
	}

	// Build fail targets map
	failTargets := make(map[string]bool)
	for _, name := range req.FailTargets {
		failTargets[name] = true
	}

	plan, err := s.buildPlanFromSelection(req.Targets, failTargets)
	if err != nil {
		s.runMutex.Unlock()
		s.writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.isRunning = true
	s.currentCtx, s.currentCancel = context.WithCancel(context.Background())
	s.runMutex.Unlock()

	// Determine root targets
	rootTargets := req.RootTargets
	if len(rootTargets) == 0 {
		// If no roots specified, find leaf nodes (targets with no dependents)
		rootTargets = s.findLeafTargets(req.Targets)
	}

	go func() {
		defer func() {
			s.runMutex.Lock()
			s.isRunning = false
			s.currentCtx = nil
			s.currentCancel = nil
			s.runMutex.Unlock()
		}()

		exec := neng.NewExecutor(plan, neng.WithEventSink(s.sink))
		drainExecutorChannels(exec)

		// Send plan info first
		s.sink.sendPlanInfo(plan)

		// Small delay so client receives plan info before events
		time.Sleep(planInfoDelay)

		summary, runErr := exec.Run(s.currentCtx, rootTargets...)
		s.sink.sendSummary(summary, runErr)
	}()

	s.writeJSONSuccess(w, map[string]any{
		"message": "Run started",
		"roots":   rootTargets,
	})
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	s.runMutex.Lock()
	if !s.isRunning || s.currentCancel == nil {
		s.runMutex.Unlock()
		s.writeJSONError(w, http.StatusBadRequest, "No run in progress")
		return
	}
	s.currentCancel()
	s.runMutex.Unlock()

	s.writeJSONSuccess(w, map[string]any{
		"message": "Run cancellation requested",
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	s.runMutex.Lock()
	running := s.isRunning
	s.runMutex.Unlock()

	s.writeJSON(w, http.StatusOK, map[string]any{
		"running": running,
	})
}

// handleTargets returns the list of available targets for plan composition.
func (s *Server) handleTargets(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, AvailableTargetsResponse{
		Targets: s.availableTargets,
	})
}

// handlePreview previews what a plan would look like without executing it.
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req PreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeJSON(w, http.StatusBadRequest, PreviewResponse{
			Valid: false,
			Error: "Invalid request body: " + err.Error(),
		})
		return
	}

	if len(req.Targets) == 0 {
		s.writeJSON(w, http.StatusBadRequest, PreviewResponse{
			Valid: false,
			Error: "No targets selected",
		})
		return
	}

	plan, err := s.buildPlanFromSelection(req.Targets, nil)
	if err != nil {
		s.writeJSON(w, http.StatusBadRequest, PreviewResponse{
			Valid: false,
			Error: err.Error(),
		})
		return
	}

	// Build response
	stages := plan.Stages()
	resp := PreviewResponse{
		Valid:   true,
		Targets: make([]Target, 0),
		Stages:  make([]Stage, len(stages)),
	}

	for _, name := range plan.TaskNames() {
		t, _ := plan.Task(name)
		resp.Targets = append(resp.Targets, Target{
			Name: t.Name,
			Desc: t.Desc,
			Deps: t.Deps,
		})
	}

	for i, stage := range stages {
		resp.Stages[i] = Stage{
			Index:   stage.Index,
			Targets: stage.Tasks,
		}
	}

	s.writeJSON(w, http.StatusOK, resp)
}

// handleRunCustom runs a custom plan with selected targets.
func (s *Server) handleRunCustom(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	s.runMutex.Lock()
	if s.isRunning {
		s.runMutex.Unlock()
		s.writeJSONError(w, http.StatusConflict, "A run is already in progress")
		return
	}

	var req RunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.runMutex.Unlock()
		s.writeJSONError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if len(req.Targets) == 0 {
		s.runMutex.Unlock()
		s.writeJSONError(w, http.StatusBadRequest, "No targets selected")
		return
	}

	// Build fail targets map
	failTargets := make(map[string]bool)
	for _, name := range req.FailTargets {
		failTargets[name] = true
	}

	plan, err := s.buildPlanFromSelection(req.Targets, failTargets)
	if err != nil {
		s.runMutex.Unlock()
		s.writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.isRunning = true
	s.currentCtx, s.currentCancel = context.WithCancel(context.Background())
	s.runMutex.Unlock()

	// Determine root targets
	rootTargets := req.RootTargets
	if len(rootTargets) == 0 {
		// If no roots specified, find leaf nodes (targets with no dependents)
		rootTargets = s.findLeafTargets(req.Targets)
	}

	go func() {
		defer func() {
			s.runMutex.Lock()
			s.isRunning = false
			s.currentCtx = nil
			s.currentCancel = nil
			s.runMutex.Unlock()
		}()

		exec := neng.NewExecutor(plan, neng.WithEventSink(s.sink))
		drainExecutorChannels(exec)

		// Send plan info first
		s.sink.sendPlanInfo(plan)

		// Small delay so client receives plan info before events
		time.Sleep(planInfoDelay)

		summary, runErr := exec.Run(s.currentCtx, rootTargets...)
		s.sink.sendSummary(summary, runErr)
	}()

	s.writeJSONSuccess(w, map[string]any{
		"message": "Custom run started",
		"roots":   rootTargets,
	})
}

// findLeafTargets finds targets that are not dependencies of any other target.
func (s *Server) findLeafTargets(targetNames []string) []string {
	// Build set of all dependencies
	depSet := make(map[string]bool)
	for _, name := range targetNames {
		def, ok := s.targetDefByName(name)
		if !ok {
			continue
		}
		for _, dep := range def.Deps {
			depSet[dep] = true
		}
	}

	// Targets not in depSet are leaves
	leaves := make([]string, 0)
	for _, name := range targetNames {
		if !depSet[name] {
			leaves = append(leaves, name)
		}
	}

	// If somehow all are deps, return all
	if len(leaves) == 0 {
		return targetNames
	}

	return leaves
}

// handleSavedPlans returns all saved plans.
func (s *Server) handleSavedPlans(w http.ResponseWriter, _ *http.Request) {
	s.savedPlansMu.RLock()
	plans := make([]SavedPlan, 0, len(s.savedPlans))
	for _, p := range s.savedPlans {
		plans = append(plans, *p)
	}
	s.savedPlansMu.RUnlock()

	s.writeJSON(w, http.StatusOK, SavedPlansResponse{Plans: plans})
}

// handleSavePlan saves a new plan.
func (s *Server) handleSavePlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req SavePlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeJSONError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if req.Name == "" {
		s.writeJSONError(w, http.StatusBadRequest, "Plan name is required")
		return
	}

	if len(req.Targets) == 0 {
		s.writeJSONError(w, http.StatusBadRequest, "At least one target is required")
		return
	}

	s.savedPlansMu.Lock()
	s.planCounter++
	id := fmt.Sprintf("user-%d", s.planCounter)
	plan := &SavedPlan{
		ID:          id,
		Name:        req.Name,
		Description: req.Description,
		Targets:     req.Targets,
		CreatedAt:   time.Now().Format(time.RFC3339),
		IsBuiltin:   false,
	}
	s.savedPlans[id] = plan
	s.savedPlansMu.Unlock()

	s.writeJSONSuccess(w, map[string]any{
		"plan": plan,
	})
}

// handleDeletePlan deletes a saved plan.
func (s *Server) handleDeletePlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		s.writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	planID := r.URL.Query().Get("id")
	if planID == "" {
		s.writeJSONError(w, http.StatusBadRequest, "Plan ID is required")
		return
	}

	s.savedPlansMu.Lock()
	plan, exists := s.savedPlans[planID]
	if !exists {
		s.savedPlansMu.Unlock()
		s.writeJSONError(w, http.StatusNotFound, "Plan not found")
		return
	}

	if plan.IsBuiltin {
		s.savedPlansMu.Unlock()
		s.writeJSONError(w, http.StatusForbidden, "Cannot delete built-in plans")
		return
	}

	delete(s.savedPlans, planID)
	s.savedPlansMu.Unlock()

	s.writeJSONSuccess(w, map[string]any{
		"message": "Plan deleted successfully",
	})
}

// handleGetPlanDetails returns details of a saved plan including its stages.
func (s *Server) handleGetPlanDetails(w http.ResponseWriter, r *http.Request) {
	planID := r.URL.Query().Get("id")
	if planID == "" {
		s.writeJSONError(w, http.StatusBadRequest, "Plan ID is required")
		return
	}

	s.savedPlansMu.RLock()
	plan, exists := s.savedPlans[planID]
	s.savedPlansMu.RUnlock()

	if !exists {
		s.writeJSONError(w, http.StatusNotFound, "Plan not found")
		return
	}

	// Build the plan to get stages
	builtPlan, err := s.buildPlanFromSelection(plan.Targets, nil)
	if err != nil {
		s.writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Get stages and targets info
	stages := builtPlan.Stages()
	stagesInfo := make([]Stage, len(stages))
	for i, stage := range stages {
		stagesInfo[i] = Stage{
			Index:   stage.Index,
			Targets: stage.Tasks,
		}
	}

	targetsInfo := make([]Target, 0)
	for _, name := range builtPlan.TaskNames() {
		t, _ := builtPlan.Task(name)
		targetsInfo = append(targetsInfo, Target{
			Name: t.Name,
			Desc: t.Desc,
			Deps: t.Deps,
		})
	}

	s.writeJSONSuccess(w, map[string]any{
		"plan":    plan,
		"targets": targetsInfo,
		"stages":  stagesInfo,
	})
}

// determineRootTargetsFromMode determines which targets to run based on the request mode.
// Returns the root targets and any validation error.
func (s *Server) determineRootTargetsFromMode(
	req *RunFromPlanRequest,
	plan *SavedPlan,
	builtPlan *neng.Plan,
) ([]string, error) {
	switch req.Mode {
	case "target":
		if req.TargetName == "" {
			return nil, errors.New("mode 'target' requires a non-empty 'targetName'")
		}
		if _, ok := builtPlan.Task(req.TargetName); !ok {
			return nil, fmt.Errorf("target '%s' not found in plan", req.TargetName)
		}
		return []string{req.TargetName}, nil

	case "stage":
		if req.StageIndex == nil {
			return nil, errors.New("mode 'stage' requires a non-null 'stageIndex'")
		}
		stages := builtPlan.Stages()
		if *req.StageIndex < 0 || *req.StageIndex >= len(stages) {
			return nil, fmt.Errorf(
				"stage index %d is out of range (valid: 0-%d)",
				*req.StageIndex,
				len(stages)-1,
			)
		}
		return stages[*req.StageIndex].Tasks, nil

	case "all", "":
		rootTargets := req.RootTargets
		if len(rootTargets) == 0 {
			rootTargets = s.findLeafTargets(plan.Targets)
		}
		return rootTargets, nil

	default:
		return nil, fmt.Errorf("invalid mode '%s': must be 'all', 'target', or 'stage'", req.Mode)
	}
}

// handleRunFromPlan runs a saved plan with optional target/stage selection.
func (s *Server) handleRunFromPlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	s.runMutex.Lock()
	if s.isRunning {
		s.runMutex.Unlock()
		s.writeJSONError(w, http.StatusConflict, "A run is already in progress")
		return
	}

	var req RunFromPlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.runMutex.Unlock()
		s.writeJSONError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	s.savedPlansMu.RLock()
	plan, exists := s.savedPlans[req.PlanID]
	s.savedPlansMu.RUnlock()

	if !exists {
		s.runMutex.Unlock()
		s.writeJSONError(w, http.StatusNotFound, "Plan not found")
		return
	}

	// Build fail targets map
	failTargets := make(map[string]bool)
	for _, name := range req.FailTargets {
		failTargets[name] = true
	}

	builtPlan, err := s.buildPlanFromSelection(plan.Targets, failTargets)
	if err != nil {
		s.runMutex.Unlock()
		s.writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Determine root targets based on mode
	rootTargets, err := s.determineRootTargetsFromMode(&req, plan, builtPlan)
	if err != nil {
		s.runMutex.Unlock()
		s.writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.isRunning = true
	s.currentCtx, s.currentCancel = context.WithCancel(context.Background())
	s.runMutex.Unlock()

	go func() {
		defer func() {
			s.runMutex.Lock()
			s.isRunning = false
			s.currentCtx = nil
			s.currentCancel = nil
			s.runMutex.Unlock()
		}()

		exec := neng.NewExecutor(builtPlan, neng.WithEventSink(s.sink))
		drainExecutorChannels(exec)

		// Send plan info first
		s.sink.sendPlanInfo(builtPlan)

		// Small delay so client receives plan info before events
		time.Sleep(planInfoDelay)

		summary, runErr := exec.Run(s.currentCtx, rootTargets...)
		s.sink.sendSummary(summary, runErr)
	}()

	s.writeJSONSuccess(w, map[string]any{
		"message":  "Running from saved plan",
		"planName": plan.Name,
		"roots":    rootTargets,
	})
}

func Run(ctx context.Context, indexHTML []byte, static fs.FS) error {
	srv := NewServer()

	mux := http.NewServeMux()

	// Serve static files from the frontend sub-filesystem
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))

	// Serve index.html at root
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := w.Write(indexHTML); err != nil {
			http.Error(w, "Failed to write response", http.StatusInternalServerError)
		}
	})

	// API endpoints
	mux.HandleFunc("/api/events", srv.handleSSE) // SSE already has CORS headers
	mux.HandleFunc("/api/run", enableCORS(srv.handleRun))
	mux.HandleFunc("/api/run/custom", enableCORS(srv.handleRunCustom))
	mux.HandleFunc("/api/run/plan", enableCORS(srv.handleRunFromPlan))
	mux.HandleFunc("/api/cancel", enableCORS(srv.handleCancel))
	mux.HandleFunc("/api/status", enableCORS(srv.handleStatus))
	mux.HandleFunc("/api/targets", enableCORS(srv.handleTargets))
	mux.HandleFunc("/api/preview", enableCORS(srv.handlePreview))
	mux.HandleFunc("/api/plans", enableCORS(srv.handleSavedPlans))
	mux.HandleFunc("/api/plans/save", enableCORS(srv.handleSavePlan))
	mux.HandleFunc("/api/plans/delete", enableCORS(srv.handleDeletePlan))
	mux.HandleFunc("/api/plans/details", enableCORS(srv.handleGetPlanDetails))

	httpServer := &http.Server{
		Addr:              serverAddr,
		Handler:           mux,
		ReadHeaderTimeout: serverReadHeaderTimeout,
	}

	// Start server in a goroutine
	errCh := make(chan error, 1)
	go func() {
		fmt.Printf("🚀 CI Dashboard server starting on http://localhost%s\n", serverAddr)
		fmt.Println("   Open your browser to view the CI pipeline visualization")
		fmt.Println("   Press Ctrl+C to stop the server")
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	// Wait for context cancellation or server error
	select {
	case <-ctx.Done():
		// Graceful shutdown with a timeout
		shutdownCtx, cancel := context.WithTimeout(context.Background(), serverShutdownTimeout)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("server shutdown failed: %w", err)
		}
		return ctx.Err()
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("server failed: %w", err)
		}
		return nil
	}
}
