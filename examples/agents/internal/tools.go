package internal

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ToolRegistry provides thread-safe access to registered tools.
//
// This addresses the review's concern about global mutable maps.
// Tools can be registered at startup or dynamically at runtime.
type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewToolRegistry creates an empty tool registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools: make(map[string]Tool),
	}
}

// Register adds a tool to the registry.
// If a tool with the same name exists, it is replaced.
func (r *ToolRegistry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Name] = tool
}

// Get retrieves a tool by name.
func (r *ToolRegistry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool, ok
}

// List returns all registered tool names.
func (r *ToolRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}

// All returns all registered tools.
func (r *ToolRegistry) All() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tools := make([]Tool, 0, len(r.tools))
	for _, tool := range r.tools {
		tools = append(tools, tool)
	}
	return tools
}

// Clone creates a copy of the registry.
// Useful for creating agent-specific tool sets.
func (r *ToolRegistry) Clone() *ToolRegistry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	clone := NewToolRegistry()
	maps.Copy(clone.tools, r.tools)
	return clone
}

// --- Built-in Tools ---

// SearchResult represents a simulated search result.
type SearchResult struct {
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
	URL     string `json:"url,omitempty"`
}

// Memory provides a simple key-value store for agents.
type Memory struct {
	mu   sync.RWMutex
	data map[string]any
}

// NewMemory creates an empty memory store.
func NewMemory() *Memory {
	return &Memory{
		data: make(map[string]any),
	}
}

// Store saves a value.
func (m *Memory) Store(key string, value any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
}

// Load retrieves a value.
func (m *Memory) Load(key string) (any, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.data[key]
	return v, ok
}

// BuiltinTools returns a registry populated with demo tools.
func BuiltinTools(memory *Memory) *ToolRegistry {
	registry := NewToolRegistry()

	registry.Register(searchTool())
	registry.Register(calculateTool())
	registry.Register(browseTool())
	registry.Register(memoryStoreTool(memory))
	registry.Register(memoryLoadTool(memory))

	return registry
}

// searchTool simulates a web search.
func searchTool() Tool {
	return Tool{
		Name:        "search",
		Description: "Search the web for information. Returns a list of relevant results.",
		Parameters: map[string]ParamDef{
			"query": {
				Type:        "string",
				Description: "The search query",
				Required:    true,
			},
		},
		Execute: func(ctx context.Context, args map[string]any) (any, error) {
			query, ok := args["query"].(string)
			if !ok || query == "" {
				return nil, errors.New("search: query parameter required")
			}

			// Simulate network latency.
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(simulatedSearchDelay):
			}

			// Return mock results based on query.
			return []SearchResult{
				{
					Title:   fmt.Sprintf("Result 1 for: %s", query),
					Snippet: "This is a simulated search result with relevant information...",
					URL:     "https://example.com/result1",
				},
				{
					Title:   fmt.Sprintf("Result 2 for: %s", query),
					Snippet: "Another simulated result containing useful details...",
					URL:     "https://example.com/result2",
				},
			}, nil
		},
	}
}

// Simulated delays for demo purposes.
const (
	simulatedSearchDelay    = 200 * time.Millisecond
	simulatedBrowseDelay    = 300 * time.Millisecond
	simulatedCalculateDelay = 50 * time.Millisecond
)

// calculateTool evaluates simple mathematical expressions.
func calculateTool() Tool {
	return Tool{
		Name:        "calculate",
		Description: "Evaluate a mathematical expression. Supports +, -, *, /, ^, and parentheses.",
		Parameters: map[string]ParamDef{
			"expression": {
				Type:        "string",
				Description: "The mathematical expression to evaluate",
				Required:    true,
			},
		},
		Execute: func(ctx context.Context, args map[string]any) (any, error) {
			expr, ok := args["expression"].(string)
			if !ok || expr == "" {
				return nil, errors.New("calculate: expression parameter required")
			}

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(simulatedCalculateDelay):
			}

			result, err := evaluateExpression(expr)
			if err != nil {
				return nil, fmt.Errorf("calculate: %w", err)
			}

			return map[string]any{
				"expression": expr,
				"result":     result,
			}, nil
		},
	}
}

// evaluateExpression parses and evaluates a simple math expression.
// Supports: +, -, *, /, ^, parentheses, and numeric literals.
func evaluateExpression(expr string) (float64, error) {
	// Remove whitespace.
	expr = strings.ReplaceAll(expr, " ", "")
	if expr == "" {
		return 0, errors.New("empty expression")
	}

	p := &parser{input: expr}
	result, err := p.parseExpr()
	if err != nil {
		return 0, err
	}
	if p.pos < len(p.input) {
		return 0, fmt.Errorf("unexpected character at position %d", p.pos)
	}
	return result, nil
}

type parser struct {
	input string
	pos   int
}

func (p *parser) parseExpr() (float64, error) {
	return p.parseAddSub()
}

func (p *parser) parseAddSub() (float64, error) {
	left, err := p.parseMulDiv()
	if err != nil {
		return 0, err
	}

	for p.pos < len(p.input) {
		op := p.input[p.pos]
		if op != '+' && op != '-' {
			break
		}
		p.pos++
		right, rightErr := p.parseMulDiv()
		if rightErr != nil {
			return 0, rightErr
		}
		if op == '+' {
			left += right
		} else {
			left -= right
		}
	}
	return left, nil
}

func (p *parser) parseMulDiv() (float64, error) {
	left, err := p.parsePower()
	if err != nil {
		return 0, err
	}

	for p.pos < len(p.input) {
		op := p.input[p.pos]
		if op != '*' && op != '/' {
			break
		}
		p.pos++
		right, rightErr := p.parsePower()
		if rightErr != nil {
			return 0, rightErr
		}
		if op == '*' {
			left *= right
		} else {
			if right == 0 {
				return 0, errors.New("division by zero")
			}
			left /= right
		}
	}
	return left, nil
}

func (p *parser) parsePower() (float64, error) {
	base, err := p.parseUnary()
	if err != nil {
		return 0, err
	}

	if p.pos < len(p.input) && p.input[p.pos] == '^' {
		p.pos++
		expVal, expErr := p.parsePower() // Right-associative.
		if expErr != nil {
			return 0, expErr
		}
		return math.Pow(base, expVal), nil
	}
	return base, nil
}

func (p *parser) parseUnary() (float64, error) {
	if p.pos < len(p.input) && p.input[p.pos] == '-' {
		p.pos++
		val, err := p.parseUnary()
		if err != nil {
			return 0, err
		}
		return -val, nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (float64, error) {
	if p.pos >= len(p.input) {
		return 0, errors.New("unexpected end of expression")
	}

	// Parentheses.
	if p.input[p.pos] == '(' {
		p.pos++
		val, err := p.parseExpr()
		if err != nil {
			return 0, err
		}
		if p.pos >= len(p.input) || p.input[p.pos] != ')' {
			return 0, errors.New("missing closing parenthesis")
		}
		p.pos++
		return val, nil
	}

	// Number.
	start := p.pos
	for p.pos < len(p.input) && (isDigit(p.input[p.pos]) || p.input[p.pos] == '.') {
		p.pos++
	}
	if start == p.pos {
		return 0, fmt.Errorf("unexpected character: %c", p.input[start])
	}

	num, err := strconv.ParseFloat(p.input[start:p.pos], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number: %s", p.input[start:p.pos])
	}
	return num, nil
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

// HTTP status code for simulated browse results.
const httpStatusOK = 200

// browseTool simulates fetching content from a URL.
func browseTool() Tool {
	return Tool{
		Name:        "browse",
		Description: "Fetch content from a URL. Returns the page title and text excerpt.",
		Parameters: map[string]ParamDef{
			"url": {
				Type:        "string",
				Description: "The URL to fetch",
				Required:    true,
			},
		},
		Execute: func(ctx context.Context, args map[string]any) (any, error) {
			urlStr, ok := args["url"].(string)
			if !ok || urlStr == "" {
				return nil, errors.New("browse: url parameter required")
			}

			// Basic URL validation.
			if !isValidURL(urlStr) {
				return nil, errors.New("browse: invalid URL format")
			}

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(simulatedBrowseDelay):
			}

			// Return mock content.
			return map[string]any{
				"url":     urlStr,
				"title":   "Simulated Page Title",
				"content": "This is simulated page content. In a real implementation, this would fetch and parse the actual webpage.",
				"status":  httpStatusOK,
			}, nil
		},
	}
}

// isValidURL performs a basic URL validation.
func isValidURL(u string) bool {
	// Simple regex for demo purposes.
	pattern := `^https?://[a-zA-Z0-9][-a-zA-Z0-9]*(\.[a-zA-Z0-9][-a-zA-Z0-9]*)+.*$`
	matched, _ := regexp.MatchString(pattern, u)
	return matched
}

// memoryStoreTool allows the agent to persist information.
func memoryStoreTool(memory *Memory) Tool {
	return Tool{
		Name:        "memory_store",
		Description: "Store a value in memory for later retrieval.",
		Parameters: map[string]ParamDef{
			"key": {
				Type:        "string",
				Description: "The key to store the value under",
				Required:    true,
			},
			"value": {
				Type:        "any",
				Description: "The value to store",
				Required:    true,
			},
		},
		Execute: func(_ context.Context, args map[string]any) (any, error) {
			key, ok := args["key"].(string)
			if !ok || key == "" {
				return nil, errors.New("memory_store: key parameter required")
			}

			value, ok := args["value"]
			if !ok {
				return nil, errors.New("memory_store: value parameter required")
			}

			memory.Store(key, value)
			return map[string]any{
				"stored": true,
				"key":    key,
			}, nil
		},
	}
}

// memoryLoadTool allows the agent to retrieve stored information.
func memoryLoadTool(memory *Memory) Tool {
	return Tool{
		Name:        "memory_load",
		Description: "Retrieve a previously stored value from memory.",
		Parameters: map[string]ParamDef{
			"key": {
				Type:        "string",
				Description: "The key to retrieve",
				Required:    true,
			},
		},
		Execute: func(_ context.Context, args map[string]any) (any, error) {
			key, ok := args["key"].(string)
			if !ok || key == "" {
				return nil, errors.New("memory_load: key parameter required")
			}

			value, found := memory.Load(key)
			if !found {
				return map[string]any{
					"found": false,
					"key":   key,
				}, nil
			}

			return map[string]any{
				"found": true,
				"key":   key,
				"value": value,
			}, nil
		},
	}
}
