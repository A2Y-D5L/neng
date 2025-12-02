package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Default delays for MockLLMClient.
const (
	defaultMockTokenDelay    = 20 * time.Millisecond
	defaultMockCompleteDelay = 50 * time.Millisecond
)

// LLMClient abstracts LLM API calls for testability and provider flexibility.
//
// Implementations should handle:
//   - Rate limiting and retries (or delegate to callers)
//   - Token counting if needed
//   - Provider-specific authentication
type LLMClient interface {
	// Complete sends messages and returns the full response.
	Complete(ctx context.Context, messages []Message) (string, error)

	// CompleteStream sends messages and streams tokens via callback.
	// The onToken callback is invoked for each token as it arrives.
	CompleteStream(ctx context.Context, messages []Message, onToken func(string)) error
}

// CompleteJSON is a generic helper that parses the LLM response as JSON.
// This is a standalone function because Go doesn't support generic methods.
//
// Example:
//
//	var output ThinkOutput
//	output, err := CompleteJSON[ThinkOutput](ctx, client, messages)
func CompleteJSON[T any](ctx context.Context, client LLMClient, messages []Message) (T, error) {
	var zero T

	response, err := client.Complete(ctx, messages)
	if err != nil {
		return zero, err
	}

	// Try to extract JSON from the response.
	// LLMs sometimes wrap JSON in markdown code blocks.
	jsonStr := extractJSON(response)

	var result T
	if unmarshalErr := json.Unmarshal([]byte(jsonStr), &result); unmarshalErr != nil {
		return zero, fmt.Errorf("failed to parse LLM response as JSON: %w\nResponse: %s", unmarshalErr, response)
	}

	return result, nil
}

// extractJSON attempts to find JSON content in a response.
// Handles common cases where LLMs wrap JSON in markdown code blocks.
func extractJSON(response string) string {
	response = strings.TrimSpace(response)

	// Try to find JSON in code blocks.
	if start := strings.Index(response, "```json"); start != -1 {
		start += len("```json")
		if end := strings.Index(response[start:], "```"); end != -1 {
			return strings.TrimSpace(response[start : start+end])
		}
	}

	// Try plain code blocks.
	if start := strings.Index(response, "```"); start != -1 {
		start += len("```")
		if end := strings.Index(response[start:], "```"); end != -1 {
			candidate := strings.TrimSpace(response[start : start+end])
			if strings.HasPrefix(candidate, "{") || strings.HasPrefix(candidate, "[") {
				return candidate
			}
		}
	}

	// Try to find raw JSON object or array.
	if start := strings.Index(response, "{"); start != -1 {
		// Find matching closing brace.
		depth := 0
		for i := start; i < len(response); i++ {
			switch response[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					return response[start : i+1]
				}
			}
		}
	}

	// Return as-is, let json.Unmarshal handle errors.
	return response
}

// --- Mock LLM Client for Testing ---

// MockLLMClient provides predictable responses for testing and demos.
//
// It can be configured with canned responses or generate simple mock outputs.
type MockLLMClient struct {
	// ThinkResponses are returned in order for Think phase calls.
	// After exhausting these, it generates a "respond" action.
	ThinkResponses []ThinkOutput

	// ResponseText is the final response to stream.
	ResponseText string

	// TokenDelay controls the delay between streamed tokens.
	TokenDelay time.Duration

	// CompleteDelay controls the delay for Complete calls.
	CompleteDelay time.Duration

	// Error, if set, is returned from all calls.
	Error error

	mu          sync.Mutex
	thinkIndex  int
	callHistory []MockCall
}

// MockCall records a call to the mock client for verification.
type MockCall struct {
	Method   string
	Messages []Message
	Time     time.Time
}

// NewMockLLMClient creates a MockLLMClient with sensible defaults for demos.
func NewMockLLMClient() *MockLLMClient {
	return &MockLLMClient{
		ThinkResponses: []ThinkOutput{
			{
				Reasoning:   "I should search for information about this topic first.",
				Action:      "search",
				ActionInput: map[string]any{"query": "relevant information"},
			},
			{
				Reasoning:   "Let me store this information for later use.",
				Action:      "memory_store",
				ActionInput: map[string]any{"key": "research", "value": "key findings from research"},
			},
			{
				Reasoning: "I have gathered enough information. Time to formulate a response.",
				Action:    "respond",
			},
		},
		ResponseText: "Based on my research, here are the key findings:\n\n" +
			"1. **Important Point One**: This is a significant insight from the analysis.\n\n" +
			"2. **Important Point Two**: Another key observation worth noting.\n\n" +
			"3. **Important Point Three**: A final consideration for the topic.\n\n" +
			"In conclusion, this demonstrates the agent's ability to research, " +
			"reason through problems, and provide structured responses.",
		TokenDelay:    defaultMockTokenDelay,
		CompleteDelay: defaultMockCompleteDelay,
	}
}

// Complete returns a canned response based on message content.
func (m *MockLLMClient) Complete(ctx context.Context, messages []Message) (string, error) {
	m.mu.Lock()
	m.callHistory = append(m.callHistory, MockCall{
		Method:   "Complete",
		Messages: messages,
		Time:     time.Now(),
	})
	m.mu.Unlock()

	if m.Error != nil {
		return "", m.Error
	}

	if m.CompleteDelay > 0 {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(m.CompleteDelay):
		}
	}

	// If we have pre-configured think responses, use them.
	m.mu.Lock()
	hasThinkResponses := m.thinkIndex < len(m.ThinkResponses)
	m.mu.Unlock()

	if hasThinkResponses {
		return m.generateThinkResponse()
	}

	// Default response.
	if m.ResponseText != "" {
		return m.ResponseText, nil
	}
	return "This is a mock response from the LLM.", nil
}

// generateThinkResponse returns the next canned think response.
func (m *MockLLMClient) generateThinkResponse() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.thinkIndex < len(m.ThinkResponses) {
		resp := m.ThinkResponses[m.thinkIndex]
		m.thinkIndex++
		jsonBytes, err := json.Marshal(resp)
		if err != nil {
			return "", err
		}
		return string(jsonBytes), nil
	}

	// Default to responding action after configured responses.
	resp := ThinkOutput{
		Reasoning: "I have gathered sufficient information to respond.",
		Action:    "respond",
	}
	jsonBytes, err := json.Marshal(resp)
	if err != nil {
		return "", err
	}
	return string(jsonBytes), nil
}

// CompleteStream simulates streaming token output.
func (m *MockLLMClient) CompleteStream(ctx context.Context, messages []Message, onToken func(string)) error {
	m.mu.Lock()
	m.callHistory = append(m.callHistory, MockCall{
		Method:   "CompleteStream",
		Messages: messages,
		Time:     time.Now(),
	})
	m.mu.Unlock()

	if m.Error != nil {
		return m.Error
	}

	text := m.ResponseText
	if text == "" {
		text = "This is a simulated streaming response from the mock LLM."
	}

	// Stream word by word.
	words := strings.Fields(text)
	for i, word := range words {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if m.TokenDelay > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(m.TokenDelay):
			}
		}

		// Add space before words (except first).
		if i > 0 {
			onToken(" ")
		}
		onToken(word)
	}

	return nil
}

// CallHistory returns all recorded calls.
func (m *MockLLMClient) CallHistory() []MockCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	history := make([]MockCall, len(m.callHistory))
	copy(history, m.callHistory)
	return history
}

// Reset clears the call history and resets the think index.
func (m *MockLLMClient) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callHistory = nil
	m.thinkIndex = 0
}

// --- Prompt Building ---

// DefaultSystemPrompt provides a baseline system prompt for the agent.
const DefaultSystemPrompt = `You are an AI assistant that solves problems step by step using available tools.

For each step, respond with a JSON object containing:
- "reasoning": Your chain-of-thought explaining your decision
- "action": The tool to use, or "respond" to give a final answer
- "actionInput": The arguments to pass to the tool (if not "respond")
- "confidence": Your confidence level from 0 to 1

Available tools:
{{TOOLS}}

When you have enough information to answer, use action "respond".`

// FormatToolsForPrompt formats tool definitions for inclusion in prompts.
func FormatToolsForPrompt(tools []Tool) string {
	var sb strings.Builder
	for _, tool := range tools {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", tool.Name, tool.Description))
		if len(tool.Parameters) > 0 {
			sb.WriteString("  Parameters:\n")
			for name, param := range tool.Parameters {
				required := ""
				if param.Required {
					required = " (required)"
				}
				sb.WriteString(fmt.Sprintf("    - %s (%s): %s%s\n", name, param.Type, param.Description, required))
			}
		}
	}
	return sb.String()
}

// BuildSystemPrompt creates a system prompt with tool documentation.
func BuildSystemPrompt(basePrompt string, tools []Tool) string {
	if basePrompt == "" {
		basePrompt = DefaultSystemPrompt
	}
	toolsDoc := FormatToolsForPrompt(tools)
	return strings.Replace(basePrompt, "{{TOOLS}}", toolsDoc, 1)
}

// --- LLM Error Types ---

// LLMError wraps errors from LLM calls with additional context.
type LLMError struct {
	Message    string
	StatusCode int
	Retryable  bool
	Cause      error
}

func (e *LLMError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

func (e *LLMError) Unwrap() error {
	return e.Cause
}

// IsRetryable returns true if the error is likely transient.
func IsRetryable(err error) bool {
	var llmErr *LLMError
	if errors.As(err, &llmErr) {
		return llmErr.Retryable
	}
	// Context errors are not retryable.
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}
