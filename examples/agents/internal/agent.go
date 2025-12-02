package internal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/a2y-d5l/neng"
	"github.com/a2y-d5l/neng/agent"
)

// Agent orchestrates the ReAct loop using neng primitives.
//
// The agent implements a think→act→observe cycle until a termination
// condition is met or maximum iterations are exceeded.
type Agent struct {
	config  AgentConfig
	llm     LLMClient
	tools   *ToolRegistry
	results *agent.Results
	memory  *Memory

	// onEvent receives agent-specific events for observability.
	// This is separate from neng events since ReactLoop drains those internally.
	onEvent func(AgentEvent)

	// mu protects state during concurrent access.
	mu        sync.RWMutex
	state     *AgentState
	startedAt time.Time
}

// AgentEvent represents observable events during agent execution.
// These are agent-specific and separate from neng's EventHandler system.
type AgentEvent struct {
	Type      AgentEventType `json:"type"`
	Time      time.Time      `json:"time"`
	Iteration int            `json:"iteration,omitempty"`
	Phase     AgentPhase     `json:"phase,omitempty"`
	Data      any            `json:"data,omitempty"`
	Error     string         `json:"error,omitempty"`
}

// AgentEventType identifies the kind of agent event.
type AgentEventType string

const (
	EventStart     AgentEventType = "start"
	EventIteration AgentEventType = "iteration"
	EventThink     AgentEventType = "think"
	EventAction    AgentEventType = "action"
	EventObserve   AgentEventType = "observe"
	EventGenerate  AgentEventType = "generate"
	EventToken     AgentEventType = "token"
	EventComplete  AgentEventType = "complete"
	EventError     AgentEventType = "error"
)

// actionRespond is the sentinel action name indicating the agent should respond.
const actionRespond = "respond"

// AgentOption configures an Agent.
type AgentOption func(*Agent)

// WithEventHandler sets a callback for agent events.
func WithEventHandler(handler func(AgentEvent)) AgentOption {
	return func(a *Agent) {
		a.onEvent = handler
	}
}

// WithMemory sets a shared memory for the agent.
func WithMemory(memory *Memory) AgentOption {
	return func(a *Agent) {
		a.memory = memory
	}
}

// NewAgent creates an agent with the given configuration.
func NewAgent(config AgentConfig, llm LLMClient, tools *ToolRegistry, opts ...AgentOption) *Agent {
	a := &Agent{
		config:  config,
		llm:     llm,
		tools:   tools,
		results: agent.NewResults(),
		state:   &AgentState{Phase: PhaseInit},
		memory:  NewMemory(),
	}

	for _, opt := range opts {
		opt(a)
	}

	return a
}

// Run executes the agent with the configured query.
func (a *Agent) Run(ctx context.Context) error {
	a.startedAt = time.Now()

	// Initialize results with query and state.
	agent.Store(a.results, KeyQuery, a.config.Query)
	agent.Store(a.results, KeyState, a.state)

	a.emit(AgentEvent{
		Type: EventStart,
		Time: time.Now(),
		Data: map[string]any{
			"query":         a.config.Query,
			"maxIterations": a.config.MaxIterations,
			"enabledTools":  a.config.EnabledTools,
		},
	})

	// Execute ReAct loop using neng's ReactLoop.
	// Note: ReactLoop internally drains events, so we emit our own
	// agent-specific events via the callback mechanism.
	err := agent.ReactLoop(
		ctx,
		a.results,
		a.config.MaxIterations,
		a.buildIterationPlan,
		a.isDone,
	)

	if err != nil {
		a.updatePhase(PhaseError)
		a.emit(AgentEvent{
			Type:  EventError,
			Time:  time.Now(),
			Error: err.Error(),
		})
		return fmt.Errorf("agent run failed: %w", err)
	}

	// Generate final response with optional streaming.
	if genErr := a.generateResponse(ctx); genErr != nil {
		return fmt.Errorf("response generation failed: %w", genErr)
	}

	a.updatePhase(PhaseDone)
	a.emit(AgentEvent{
		Type: EventComplete,
		Time: time.Now(),
		Data: map[string]any{
			"duration":   time.Since(a.startedAt).String(),
			"iterations": a.getState().Iteration + 1,
		},
	})

	return nil
}

// buildIterationPlan creates the plan for a single ReAct iteration.
func (a *Agent) buildIterationPlan(iteration int, _ *agent.Results) *neng.Plan {
	// Create targets for think→act→observe sequence.
	// We apply retry and timeout wrappers as appropriate.

	thinkTarget := a.createThinkTarget(iteration)
	actTarget := a.createActTarget(iteration)
	observeTarget := a.createObserveTarget(iteration)

	plan, err := neng.BuildPlan(thinkTarget, actTarget, observeTarget)
	if err != nil {
		// BuildPlan errors indicate programming bugs (cycles, missing deps).
		// Log and return nil to signal ReactLoop.
		a.emit(AgentEvent{
			Type:      EventError,
			Time:      time.Now(),
			Iteration: iteration,
			Error:     fmt.Sprintf("failed to build iteration plan: %v", err),
		})
		return nil
	}

	return plan
}

// createThinkTarget builds the think phase target with retry wrapper.
func (a *Agent) createThinkTarget(iteration int) neng.Task {
	target := neng.Task{
		Name: fmt.Sprintf("think_%d", iteration),
		Desc: "Analyze state and decide next action",
		Run:  a.thinkFn(iteration),
	}

	// Apply retry wrapper if configured.
	if a.config.RetryConfig != nil {
		policy := agent.RetryPolicy{
			MaxRetries: a.config.RetryConfig.MaxRetries,
			Backoff: agent.ExponentialBackoff{
				Base: a.config.RetryConfig.InitialBackoff,
				Max:  a.config.RetryConfig.MaxBackoff,
			},
			ShouldRetry: IsRetryable,
		}
		target = agent.WithRetry(target, policy)
	}

	return target
}

// createActTarget builds the act phase target with timeout wrapper.
func (a *Agent) createActTarget(iteration int) neng.Task {
	target := neng.Task{
		Name: fmt.Sprintf("act_%d", iteration),
		Desc: "Execute chosen action",
		Deps: []string{fmt.Sprintf("think_%d", iteration)},
		Run:  a.actFn(iteration),
	}

	// Apply timeout wrapper if configured.
	if a.config.TimeoutPerAction > 0 {
		target = agent.WithTimeout(target, a.config.TimeoutPerAction)
	}

	return target
}

// createObserveTarget builds the observe phase target.
func (a *Agent) createObserveTarget(iteration int) neng.Task {
	return neng.Task{
		Name: fmt.Sprintf("observe_%d", iteration),
		Desc: "Process action results",
		Deps: []string{fmt.Sprintf("act_%d", iteration)},
		Run:  a.observeFn(iteration),
	}
}

// thinkFn returns the think phase work function.
func (a *Agent) thinkFn(iteration int) func(context.Context) error {
	return func(ctx context.Context) error {
		a.updatePhase(PhaseThink)
		a.emit(AgentEvent{
			Type:      EventThink,
			Time:      time.Now(),
			Iteration: iteration,
			Phase:     PhaseThink,
		})

		// Build messages for LLM.
		messages := a.buildThinkMessages(iteration)

		// Call LLM and parse structured output.
		output, err := CompleteJSON[ThinkOutput](ctx, a.llm, messages)
		if err != nil {
			return fmt.Errorf("think phase failed: %w", err)
		}

		// Store think output for act phase.
		agent.Store(a.results, IterationKey("think", iteration), output)

		// Update observable state.
		a.mu.Lock()
		a.state.Iteration = iteration
		a.state.ThinkHistory = append(a.state.ThinkHistory, output)
		a.mu.Unlock()

		a.emit(AgentEvent{
			Type:      EventThink,
			Time:      time.Now(),
			Iteration: iteration,
			Phase:     PhaseThink,
			Data:      output,
		})

		return nil
	}
}

// buildThinkMessages constructs the conversation for the think phase.
func (a *Agent) buildThinkMessages(iteration int) []Message {
	tools := a.getEnabledTools()
	systemPrompt := BuildSystemPrompt(a.config.SystemPrompt, tools)

	messages := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: a.config.Query},
	}

	// Add history from previous iterations.
	a.mu.RLock()
	defer a.mu.RUnlock()

	for i := range iteration {
		if i < len(a.state.ThinkHistory) {
			think := a.state.ThinkHistory[i]
			messages = append(messages, Message{
				Role:    "assistant",
				Content: fmt.Sprintf("Reasoning: %s\nAction: %s", think.Reasoning, think.Action),
			})
		}
		if i < len(a.state.ActionHistory) {
			action := a.state.ActionHistory[i]
			if action.Error != "" {
				messages = append(messages, Message{
					Role:    "user",
					Content: fmt.Sprintf("Tool %s failed: %s", action.ToolName, action.Error),
				})
			} else {
				messages = append(messages, Message{
					Role:    "user",
					Content: fmt.Sprintf("Tool %s result: %v", action.ToolName, action.Output),
				})
			}
		}
	}

	return messages
}

// actFn returns the act phase work function.
func (a *Agent) actFn(iteration int) func(context.Context) error {
	return func(ctx context.Context) error {
		a.updatePhase(PhaseAct)

		// Load think output from previous phase.
		think, ok := agent.Load[ThinkOutput](a.results, IterationKey("think", iteration))
		if !ok {
			return errors.New("act phase: missing think output")
		}

		// Check if agent decided to respond (no tool execution needed).
		if think.Action == actionRespond {
			a.emit(AgentEvent{
				Type:      EventAction,
				Time:      time.Now(),
				Iteration: iteration,
				Phase:     PhaseAct,
				Data:      map[string]any{"action": actionRespond, "skipped": true},
			})

			// Store empty action result to mark completion.
			agent.Store(a.results, IterationKey("action", iteration), ActionResult{
				ToolName: actionRespond,
			})
			return nil
		}

		// Get and execute the tool.
		tool, ok := a.tools.Get(think.Action)
		if !ok {
			return fmt.Errorf("act phase: unknown tool %q", think.Action)
		}

		a.emit(AgentEvent{
			Type:      EventAction,
			Time:      time.Now(),
			Iteration: iteration,
			Phase:     PhaseAct,
			Data:      map[string]any{"tool": think.Action, "input": think.ActionInput},
		})

		start := time.Now()
		output, err := tool.Execute(ctx, think.ActionInput)
		duration := time.Since(start)

		result := ActionResult{
			ToolName: think.Action,
			Output:   output,
			Duration: duration,
		}
		if err != nil {
			result.Error = err.Error()
		}

		agent.Store(a.results, IterationKey("action", iteration), result)

		// Update observable state.
		a.mu.Lock()
		a.state.ActionHistory = append(a.state.ActionHistory, result)
		a.mu.Unlock()

		a.emit(AgentEvent{
			Type:      EventAction,
			Time:      time.Now(),
			Iteration: iteration,
			Phase:     PhaseAct,
			Data:      result,
		})

		return nil
	}
}

// observeFn returns the observe phase work function.
func (a *Agent) observeFn(iteration int) func(context.Context) error {
	return func(_ context.Context) error {
		a.updatePhase(PhaseObserve)

		// Load action result.
		action, ok := agent.Load[ActionResult](a.results, IterationKey("action", iteration))
		if !ok {
			return errors.New("observe phase: missing action result")
		}

		// Generate observation based on action result.
		var obs Observation
		switch {
		case action.ToolName == actionRespond:
			obs = Observation{
				Summary: "Agent decided to generate final response",
				IsFinal: true,
			}
		case action.Error != "":
			obs = Observation{
				Summary:  fmt.Sprintf("Tool %s failed: %s", action.ToolName, action.Error),
				IsFinal:  false,
				Feedback: "Consider trying a different approach or tool.",
			}
		default:
			obs = Observation{
				Summary:  fmt.Sprintf("Tool %s completed successfully", action.ToolName),
				IsFinal:  false,
				Feedback: fmt.Sprintf("Result: %v", action.Output),
			}
		}

		agent.Store(a.results, IterationKey("observe", iteration), obs)

		a.emit(AgentEvent{
			Type:      EventObserve,
			Time:      time.Now(),
			Iteration: iteration,
			Phase:     PhaseObserve,
			Data:      obs,
		})

		return nil
	}
}

// isDone checks if the agent should stop iterating.
func (a *Agent) isDone(results *agent.Results) bool {
	a.mu.RLock()
	iteration := a.state.Iteration
	a.mu.RUnlock()

	obs, ok := agent.Load[Observation](results, IterationKey("observe", iteration))
	if !ok {
		return false
	}
	return obs.IsFinal
}

// generateResponse generates the final response, optionally with streaming.
func (a *Agent) generateResponse(ctx context.Context) error {
	a.updatePhase(PhaseGenerate)

	a.emit(AgentEvent{
		Type:  EventGenerate,
		Time:  time.Now(),
		Phase: PhaseGenerate,
	})

	// Build final generation prompt.
	messages := a.buildGenerateMessages()

	if a.config.StreamTokens {
		return a.generateStreamingResponse(ctx, messages)
	}

	// Non-streaming response.
	response, err := a.llm.Complete(ctx, messages)
	if err != nil {
		return err
	}

	agent.Store(a.results, KeyFinalResponse, response)
	return nil
}

// generateStreamingResponse uses StreamingTarget for token-by-token output.
func (a *Agent) generateStreamingResponse(ctx context.Context, messages []Message) error {
	// Use the correct StreamingTarget API from neng/agent.
	target, chunks := agent.StreamingTarget(
		"generate",
		"Generate final response",
		nil, // no deps
		func(ctx context.Context, emit func([]byte)) error {
			return a.llm.CompleteStream(ctx, messages, func(token string) {
				emit([]byte(token))

				// Emit token event for observability.
				a.emit(AgentEvent{
					Type: EventToken,
					Time: time.Now(),
					Data: token,
				})
			})
		},
	)

	// Start consumer BEFORE exec.Run() per the API contract.
	var responseBuilder strings.Builder
	var wg sync.WaitGroup
	wg.Go(func() {
		for chunk := range chunks {
			responseBuilder.Write(chunk)
		}
	})

	plan, err := neng.BuildPlan(target)
	if err != nil {
		return err
	}

	exec := neng.NewExecutor(plan, neng.WithFailFast())
	// Drain neng events (we emit our own token events).
	go drainEvents(exec.Events())
	go drainResults(exec.Results())

	_, runErr := exec.Run(ctx)
	wg.Wait()

	// Store the final response.
	agent.Store(a.results, KeyFinalResponse, responseBuilder.String())

	return runErr
}

// buildGenerateMessages constructs the final response generation prompt.
func (a *Agent) buildGenerateMessages() []Message {
	a.mu.RLock()
	defer a.mu.RUnlock()

	var sb strings.Builder
	sb.WriteString("Based on the following reasoning and tool results, provide a final answer:\n\n")

	for i, think := range a.state.ThinkHistory {
		sb.WriteString(fmt.Sprintf("Step %d reasoning: %s\n", i+1, think.Reasoning))
		if i < len(a.state.ActionHistory) {
			action := a.state.ActionHistory[i]
			if action.Error != "" {
				sb.WriteString(fmt.Sprintf("Step %d result: Error - %s\n", i+1, action.Error))
			} else {
				sb.WriteString(fmt.Sprintf("Step %d result: %v\n", i+1, action.Output))
			}
		}
		sb.WriteString("\n")
	}

	return []Message{
		{Role: "system", Content: "Provide a clear, helpful answer based on the research steps."},
		{Role: "user", Content: a.config.Query},
		{Role: "assistant", Content: sb.String()},
		{Role: "user", Content: "Now provide the final answer."},
	}
}

// getEnabledTools returns tools filtered by config.EnabledTools.
func (a *Agent) getEnabledTools() []Tool {
	all := a.tools.All()

	if len(a.config.EnabledTools) == 0 {
		return all
	}

	enabled := make(map[string]bool)
	for _, name := range a.config.EnabledTools {
		enabled[name] = true
	}

	filtered := make([]Tool, 0, len(a.config.EnabledTools))
	for _, tool := range all {
		if enabled[tool.Name] {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

// updatePhase updates the agent's current phase.
func (a *Agent) updatePhase(phase AgentPhase) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state.Phase = phase
	a.state.TotalDuration = time.Since(a.startedAt)
}

// getState returns a copy of the current state.
func (a *Agent) getState() AgentState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return *a.state
}

// emit sends an agent event if a handler is configured.
func (a *Agent) emit(event AgentEvent) {
	if a.onEvent != nil {
		a.onEvent(event)
	}
}

// GetResults returns the results container for inspection.
func (a *Agent) GetResults() *agent.Results {
	return a.results
}

// GetState returns a copy of the current agent state.
func (a *Agent) GetState() AgentState {
	return a.getState()
}

// GetFinalResponse retrieves the final response if available.
func (a *Agent) GetFinalResponse() (string, bool) {
	return agent.Load[string](a.results, KeyFinalResponse)
}

// drainEvents consumes all events from the channel to prevent blocking.
func drainEvents(events <-chan neng.Event) {
	//nolint:revive // empty block intentional - just draining the channel
	for range events {
	}
}

// drainResults consumes all results from the channel to prevent blocking.
func drainResults(results <-chan neng.Result) {
	//nolint:revive // empty block intentional - just draining the channel
	for range results {
	}
}
