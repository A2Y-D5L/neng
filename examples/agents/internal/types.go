// Package internal provides core types and infrastructure for the neng agent example.
//
// This package implements a ReAct (Reasoning + Acting) agent that demonstrates
// neng's execution engine capabilities for LLM-powered workflows.
package internal

import (
	"context"
	"time"
)

// Tool represents an executable tool the agent can invoke.
//
// Tools are the actions an agent can take during its reasoning loop.
// Each tool has a name, description, parameters, and an execution function.
type Tool struct {
	// Name uniquely identifies the tool.
	Name string `json:"name"`

	// Description explains what the tool does (used in LLM prompts).
	Description string `json:"description"`

	// Parameters describes the expected input arguments.
	Parameters map[string]ParamDef `json:"parameters"`

	// Execute performs the tool's action.
	// This field is not serialized as it's a function.
	Execute func(ctx context.Context, args map[string]any) (any, error) `json:"-"`
}

// ParamDef describes a tool parameter for schema documentation.
type ParamDef struct {
	Type        string `json:"type"` // "string", "number", "boolean", "array", "object"
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// ThinkOutput is the structured result of the Think phase.
//
// The LLM produces this output to communicate its reasoning and next action.
type ThinkOutput struct {
	// Reasoning is the chain-of-thought explaining the decision.
	Reasoning string `json:"reasoning"`

	// Action is the tool name to invoke, or "respond" to generate final output.
	Action string `json:"action"`

	// ActionInput contains the arguments to pass to the chosen tool.
	ActionInput map[string]any `json:"actionInput,omitempty"`

	// Confidence is an optional self-reported confidence score [0, 1].
	Confidence float64 `json:"confidence,omitempty"`
}

// ActionResult captures the outcome of executing an action.
type ActionResult struct {
	// ToolName is the name of the tool that was executed.
	ToolName string `json:"toolName"`

	// Output is the successful result from the tool.
	Output any `json:"output,omitempty"`

	// Error is set if the tool execution failed.
	Error string `json:"error,omitempty"`

	// Duration is how long the tool took to execute.
	Duration time.Duration `json:"duration"`
}

// Observation summarizes the action result for the next iteration.
//
// This is what gets fed back to the LLM to continue reasoning.
type Observation struct {
	// Summary is a concise description of what happened.
	Summary string `json:"summary"`

	// IsFinal indicates the agent should stop and generate a response.
	IsFinal bool `json:"isFinal"`

	// Feedback provides hints for the next iteration (if not final).
	Feedback string `json:"feedback,omitempty"`
}

// AgentConfig holds configuration for an agent run.
type AgentConfig struct {
	// Query is the user's input question or task.
	Query string `json:"query"`

	// SystemPrompt customizes the agent's behavior.
	SystemPrompt string `json:"systemPrompt,omitempty"`

	// MaxIterations limits the ReAct loop iterations.
	// A reasonable default is 10.
	MaxIterations int `json:"maxIterations"`

	// EnabledTools lists which tools the agent can use.
	// If empty, all registered tools are available.
	EnabledTools []string `json:"enabledTools,omitempty"`

	// TimeoutPerAction sets a per-tool execution timeout.
	TimeoutPerAction time.Duration `json:"timeoutPerAction,omitempty"`

	// RetryConfig controls retry behavior for LLM calls.
	RetryConfig *RetryConfig `json:"retryConfig,omitempty"`

	// StreamTokens enables token-by-token streaming for the final response.
	StreamTokens bool `json:"streamTokens"`
}

// RetryConfig controls retry behavior for individual operations.
// This is used to configure agent.WithRetry at the target level.
type RetryConfig struct {
	// MaxRetries is the maximum number of retry attempts.
	MaxRetries int `json:"maxRetries"`

	// InitialBackoff is the delay before the first retry.
	InitialBackoff time.Duration `json:"initialBackoff"`

	// MaxBackoff caps the exponential backoff delay.
	MaxBackoff time.Duration `json:"maxBackoff"`
}

// Default retry configuration values.
const (
	defaultMaxRetries     = 3
	defaultInitialBackoff = 500 * time.Millisecond
	defaultMaxBackoff     = 10 * time.Second
)

// DefaultRetryConfig returns sensible defaults for LLM retry behavior.
func DefaultRetryConfig() *RetryConfig {
	return &RetryConfig{
		MaxRetries:     defaultMaxRetries,
		InitialBackoff: defaultInitialBackoff,
		MaxBackoff:     defaultMaxBackoff,
	}
}

// AgentState represents the observable state during execution.
//
// This is updated after each phase and can be observed by event handlers.
type AgentState struct {
	// Iteration is the current ReAct loop iteration (0-indexed).
	Iteration int `json:"iteration"`

	// Phase indicates what the agent is currently doing.
	Phase AgentPhase `json:"phase"`

	// ThinkHistory contains all ThinkOutput from previous iterations.
	ThinkHistory []ThinkOutput `json:"thinkHistory,omitempty"`

	// ActionHistory contains all ActionResult from previous iterations.
	ActionHistory []ActionResult `json:"actionHistory,omitempty"`

	// TokensGenerated tracks streaming output progress.
	TokensGenerated int `json:"tokensGenerated"`

	// TotalDuration tracks elapsed time since agent start.
	TotalDuration time.Duration `json:"totalDuration"`

	// Error is set if the agent encountered a terminal error.
	Error string `json:"error,omitempty"`
}

// AgentPhase describes what the agent is currently doing.
type AgentPhase string

const (
	PhaseInit     AgentPhase = "init"
	PhaseThink    AgentPhase = "think"
	PhaseAct      AgentPhase = "act"
	PhaseObserve  AgentPhase = "observe"
	PhaseGenerate AgentPhase = "generate"
	PhaseDone     AgentPhase = "done"
	PhaseError    AgentPhase = "error"
)

// Message represents a chat message for LLM interactions.
type Message struct {
	Role    string `json:"role"` // "system", "user", "assistant"
	Content string `json:"content"`
}

// --- Keys for Results container ---
// Using constants prevents typos and documents the data flow.

const (
	// KeyQuery stores the original user query.
	KeyQuery = "agent:query"

	// KeyState stores the current AgentState.
	KeyState = "agent:state"

	// KeyFinalResponse stores the generated response.
	KeyFinalResponse = "agent:response"
)

// IterationKey returns the key for storing iteration-specific data.
// Format: "agent:<phase>:<iteration>".
func IterationKey(phase string, iteration int) string {
	return "agent:" + phase + ":" + itoa(iteration)
}

// itoa is a simple int to string conversion without importing strconv.
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
