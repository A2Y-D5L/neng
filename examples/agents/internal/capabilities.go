package internal

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/a2y-d5l/neng"
	"github.com/a2y-d5l/neng/agent"
)

// --- Complex Tool Interface ---

// ComplexTool is a tool that requires its own execution plan.
//
// Some tools involve multiple steps that can be parallelized or have
// complex dependencies. Rather than executing sequentially, they
// build a neng.Plan that the agent executes via SubPlanExecutor.
//
// Example use cases:
//   - Multi-source search (search web + search database in parallel).
//   - Document processing (extract → transform → validate).
//   - API orchestration (authenticate → fetch → aggregate).
type ComplexTool interface {
	// GetTool returns the Tool struct for this complex tool.
	// This provides the basic tool metadata and execution function.
	GetTool() Tool

	// BuildPlan creates an execution plan for this tool.
	// The plan's targets should store results in the shared Results container.
	BuildPlan(ctx context.Context, args map[string]any, results *agent.Results) (*neng.Plan, error)

	// IsComplex returns true to indicate this tool uses sub-plan execution.
	IsComplex() bool
}

// --- Multi-Source Search Tool (Complex Tool Example) ---

// MultiSearchTool searches multiple sources in parallel.
type MultiSearchTool struct {
	sources []SearchSource
}

// SearchSource represents a searchable data source.
type SearchSource struct {
	Name   string
	Search func(ctx context.Context, query string) ([]SearchResult, error)
}

// NewMultiSearchTool creates a tool that searches multiple sources.
func NewMultiSearchTool(sources ...SearchSource) *MultiSearchTool {
	return &MultiSearchTool{sources: sources}
}

// GetTool returns the Tool representation.
func (m *MultiSearchTool) GetTool() Tool {
	return Tool{
		Name:        "multi_search",
		Description: "Search multiple sources in parallel and aggregate results.",
		Parameters: map[string]ParamDef{
			"query": {
				Type:        "string",
				Description: "The search query",
				Required:    true,
			},
		},
		Execute: m.Execute,
	}
}

// Execute runs the multi-search synchronously (for simple tool interface).
func (m *MultiSearchTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return nil, errors.New("multi_search: query parameter required")
	}

	// For simple execution, run sources sequentially.
	var allResults []SearchResult
	for _, source := range m.sources {
		results, err := source.Search(ctx, query)
		if err != nil {
			continue // Skip failed sources
		}
		allResults = append(allResults, results...)
	}
	return allResults, nil
}

// IsComplex returns true - this tool benefits from sub-plan execution.
func (m *MultiSearchTool) IsComplex() bool {
	return true
}

// BuildPlan creates a parallel search plan.
func (m *MultiSearchTool) BuildPlan(
	_ context.Context,
	args map[string]any,
	results *agent.Results,
) (*neng.Plan, error) {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return nil, errors.New("multi_search: query parameter required")
	}

	targets := make([]neng.Task, 0, len(m.sources)+1)

	// Create a target for each source (no dependencies = parallel execution).
	for _, source := range m.sources {
		src := source // Capture for closure
		targets = append(targets, neng.Task{
			Name: fmt.Sprintf("search:%s", src.Name),
			Desc: fmt.Sprintf("Search %s", src.Name),
			Run: func(_ context.Context) error {
				searchResults, err := src.Search(context.Background(), query)
				if err != nil {
					// Store empty results on error, don't fail the whole plan.
					agent.Store(
						results,
						fmt.Sprintf("search:%s:results", src.Name),
						[]SearchResult{},
					)
					agent.Store(results, fmt.Sprintf("search:%s:error", src.Name), err.Error())
					return nil
				}
				agent.Store(results, fmt.Sprintf("search:%s:results", src.Name), searchResults)
				return nil
			},
		})
	}

	// Add aggregation target that depends on all searches.
	searchDeps := make([]string, 0, len(m.sources))
	for _, source := range m.sources {
		searchDeps = append(searchDeps, fmt.Sprintf("search:%s", source.Name))
	}

	targets = append(targets, neng.Task{
		Name: "aggregate",
		Desc: "Aggregate search results from all sources",
		Deps: searchDeps,
		Run: func(_ context.Context) error {
			var aggregated []SearchResult
			for _, source := range m.sources {
				sourceResults, loaded := agent.Load[[]SearchResult](
					results,
					fmt.Sprintf("search:%s:results", source.Name),
				)
				if loaded {
					aggregated = append(aggregated, sourceResults...)
				}
			}
			agent.Store(results, "multi_search:results", aggregated)
			return nil
		},
	})

	return neng.BuildPlan(targets...)
}

// --- RAG Pipeline Tool (Complex Tool with Conditional Steps) ---

// RAGPipelineTool implements Retrieve-Augment-Generate with optional reranking.
type RAGPipelineTool struct {
	retriever RetrievalFunc
	reranker  RerankFunc // Optional
	generator GenerateFunc
}

// RetrievalFunc retrieves relevant documents.
type RetrievalFunc func(ctx context.Context, query string, topK int) ([]Document, error)

// RerankFunc reranks documents for relevance.
type RerankFunc func(ctx context.Context, query string, docs []Document) ([]Document, error)

// GenerateFunc generates a response from documents.
type GenerateFunc func(ctx context.Context, query string, docs []Document) (string, error)

// Document represents a retrieved document.
type Document struct {
	ID      string  `json:"id"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

// NewRAGPipelineTool creates a RAG pipeline tool.
func NewRAGPipelineTool(
	retriever RetrievalFunc,
	reranker RerankFunc,
	generator GenerateFunc,
) *RAGPipelineTool {
	return &RAGPipelineTool{
		retriever: retriever,
		reranker:  reranker,
		generator: generator,
	}
}

// GetTool returns the Tool representation.
func (r *RAGPipelineTool) GetTool() Tool {
	return Tool{
		Name:        "rag_query",
		Description: "Query a knowledge base using RAG (Retrieve-Augment-Generate).",
		Parameters: map[string]ParamDef{
			"query": {
				Type:        "string",
				Description: "The question to answer",
				Required:    true,
			},
			"top_k": {
				Type:        "number",
				Description: "Number of documents to retrieve (default: 5)",
				Required:    false,
			},
		},
		Execute: r.Execute,
	}
}

// Default number of documents to retrieve.
const defaultTopK = 5

// Execute runs the RAG pipeline synchronously.
func (r *RAGPipelineTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return nil, errors.New("rag_query: query parameter required")
	}

	topK := defaultTopK
	if topKArg, hasTopK := args["top_k"].(float64); hasTopK {
		topK = int(topKArg)
	}

	// Retrieve
	docs, err := r.retriever(ctx, query, topK)
	if err != nil {
		return nil, fmt.Errorf("retrieval failed: %w", err)
	}

	// Rerank (if available)
	if r.reranker != nil && len(docs) > 0 {
		docs, err = r.reranker(ctx, query, docs)
		if err != nil {
			// Continue without reranking on error.
			_ = err
		}
	}

	// Generate
	response, err := r.generator(ctx, query, docs)
	if err != nil {
		return nil, fmt.Errorf("generation failed: %w", err)
	}

	return map[string]any{
		"response":  response,
		"documents": docs,
	}, nil
}

// IsComplex returns true.
func (r *RAGPipelineTool) IsComplex() bool {
	return true
}

// BuildPlan creates a RAG pipeline plan with conditional reranking.
//
// The reranking step is conditionally executed:
//   - If no reranker is configured, the step is skipped (at build time).
//   - If reranker is configured, it runs after retrieve (checked at runtime via WithCondition).
func (r *RAGPipelineTool) BuildPlan(
	_ context.Context,
	args map[string]any,
	results *agent.Results,
) (*neng.Plan, error) {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return nil, errors.New("rag_query: query parameter required")
	}

	topK := defaultTopK
	if topKArg, hasTopK := args["top_k"].(float64); hasTopK {
		topK = int(topKArg)
	}

	// Build targets directly to handle the conditional reranking correctly.
	// Note: PlanFactory.Condition is evaluated at Build time, not execution time.
	// For dynamic conditions that depend on prior target outputs, we use WithCondition.
	targets := []neng.Task{
		{
			Name: "retrieve",
			Desc: "Retrieve relevant documents",
			Run: func(ctx context.Context) error {
				docs, err := r.retriever(ctx, query, topK)
				if err != nil {
					return err
				}
				agent.Store(results, "rag:documents", docs)
				return nil
			},
		},
	}

	generateDeps := []string{"retrieve"}

	// Only include rerank target if reranker is configured.
	// This is a build-time condition (whether reranker exists).
	if r.reranker != nil {
		// Use WithCondition for runtime check (whether we have documents).
		rerankTarget := agent.WithCondition(
			neng.Task{
				Name: "rerank",
				Desc: "Rerank documents for relevance",
				Deps: []string{"retrieve"},
				Run: func(ctx context.Context) error {
					docs := agent.MustLoad[[]Document](results, "rag:documents")
					reranked, err := r.reranker(ctx, query, docs)
					if err != nil {
						return err
					}
					agent.Store(results, "rag:documents", reranked)
					return nil
				},
			},
			func() bool {
				docs, loaded := agent.Load[[]Document](results, "rag:documents")
				return loaded && len(docs) > 0
			},
		)
		targets = append(targets, rerankTarget)
		generateDeps = append(generateDeps, "rerank")
	}

	targets = append(targets, neng.Task{
		Name: "generate",
		Desc: "Generate response from documents",
		Deps: generateDeps,
		Run: func(ctx context.Context) error {
			docs := agent.MustLoad[[]Document](results, "rag:documents")
			response, err := r.generator(ctx, query, docs)
			if err != nil {
				return err
			}
			agent.Store(results, "rag:response", response)
			return nil
		},
	})

	return neng.BuildPlan(targets...)
}

// --- Enhanced Agent with Complex Tool Support ---

// ExecuteComplexTool runs a complex tool using SubPlanExecutor.
//
// This demonstrates the correct usage of SubPlanExecutor as per the review:
//   - Uses struct literal (not constructor).
//   - Calls Run() method (not Execute()).
//   - Supports event forwarding via RunWithEvents().
func ExecuteComplexTool(
	ctx context.Context,
	tool ComplexTool,
	args map[string]any,
	results *agent.Results,
	eventHandler neng.EventHandler,
) error {
	plan, err := tool.BuildPlan(ctx, args, results)
	if err != nil {
		return fmt.Errorf("failed to build tool plan: %w", err)
	}

	subExec := &agent.SubPlanExecutor{
		Results: results,
		Prefix:  fmt.Sprintf("tool:%s", tool.GetTool().Name),
	}

	if eventHandler != nil {
		_, err = subExec.RunWithEvents(ctx, plan, eventHandler)
	} else {
		_, err = subExec.Run(ctx, plan)
	}

	if err != nil {
		return fmt.Errorf("tool execution failed: %w", err)
	}

	return nil
}

// --- Streaming with Progress Tracking ---

// StreamingProgress tracks progress during streaming operations.
type StreamingProgress struct {
	TokenCount  atomic.Int64
	CharCount   atomic.Int64
	StartTime   time.Time
	LastTokenAt atomic.Value // stores time.Time
	OnProgress  func(tokens int64, chars int64, elapsed time.Duration)
}

// NewStreamingProgress creates a progress tracker.
func NewStreamingProgress(
	onProgress func(tokens int64, chars int64, elapsed time.Duration),
) *StreamingProgress {
	sp := &StreamingProgress{
		StartTime:  time.Now(),
		OnProgress: onProgress,
	}
	sp.LastTokenAt.Store(time.Now())
	return sp
}

// RecordToken records a streamed token.
func (sp *StreamingProgress) RecordToken(token string) {
	sp.TokenCount.Add(1)
	sp.CharCount.Add(int64(len(token)))
	sp.LastTokenAt.Store(time.Now())

	if sp.OnProgress != nil {
		sp.OnProgress(sp.TokenCount.Load(), sp.CharCount.Load(), time.Since(sp.StartTime))
	}
}

// Stats returns current streaming statistics.
func (sp *StreamingProgress) Stats() StreamingStats {
	elapsed := time.Since(sp.StartTime)
	tokens := sp.TokenCount.Load()
	chars := sp.CharCount.Load()

	tokensPerSec := float64(0)
	if elapsed > 0 {
		tokensPerSec = float64(tokens) / elapsed.Seconds()
	}

	return StreamingStats{
		TokenCount:   tokens,
		CharCount:    chars,
		Elapsed:      elapsed,
		TokensPerSec: tokensPerSec,
	}
}

// StreamingStats holds streaming statistics.
type StreamingStats struct {
	TokenCount   int64         `json:"tokenCount"`
	CharCount    int64         `json:"charCount"`
	Elapsed      time.Duration `json:"elapsed"`
	TokensPerSec float64       `json:"tokensPerSec"`
}

// --- Retry Helpers for LLM Calls ---

// Retry policy constants for LLM APIs.
const (
	llmRetryBaseDelay = 500 * time.Millisecond
	llmRetryMaxDelay  = 30 * time.Second
	llmRetryBackoff   = 2.0
	llmRetryJitter    = 0.2 // ±20% to prevent thundering herd
)

// LLMRetryPolicy creates a retry policy suitable for LLM API calls.
//
// LLM APIs often have rate limits and transient failures.
// This policy uses exponential backoff with jitter to handle:
//   - Rate limiting (429).
//   - Server errors (5xx).
//   - Network timeouts.
func LLMRetryPolicy(maxRetries int) agent.RetryPolicy {
	return agent.RetryPolicy{
		MaxRetries: maxRetries,
		Backoff: agent.ExponentialBackoff{
			Base:       llmRetryBaseDelay,
			Max:        llmRetryMaxDelay,
			Multiplier: llmRetryBackoff,
			Jitter:     llmRetryJitter,
		},
		ShouldRetry: IsRetryable, // Uses our LLM error classification
		OnRetry: func(attempt int, err error) {
			// Could add logging here in production.
			_ = attempt
			_ = err
		},
	}
}

// WrapWithLLMRetry creates a target with LLM-appropriate retry behavior.
func WrapWithLLMRetry(target neng.Task, maxRetries int) neng.Task {
	return agent.WithRetry(target, LLMRetryPolicy(maxRetries))
}

// --- Conditional Execution Helpers ---

// ConditionResult indicates whether to run a target based on previous results.
type ConditionResult int

const (
	// ConditionSkip skips the target (returns success without running).
	ConditionSkip ConditionResult = iota
	// ConditionRun runs the target normally.
	ConditionRun
	// ConditionFail fails the target with an error.
	ConditionFail
)

// ConditionalRunner wraps a run function with condition checking.
type ConditionalRunner struct {
	Condition func(results *agent.Results) ConditionResult
	Run       func(ctx context.Context, results *agent.Results) error
	OnSkip    func() // Optional callback when skipped
}

// ToTargetDef converts to a TargetDef for use with PlanFactory.
func (c *ConditionalRunner) ToTargetDef(name, desc string, deps []string) agent.TargetDef {
	return agent.TargetDef{
		Name: name,
		Desc: desc,
		Deps: deps,
		Run: func(ctx context.Context, results *agent.Results) error {
			switch c.Condition(results) {
			case ConditionSkip:
				if c.OnSkip != nil {
					c.OnSkip()
				}
				return nil
			case ConditionRun:
				return c.Run(ctx, results)
			case ConditionFail:
				return fmt.Errorf("condition check failed for %s", name)
			default:
				return c.Run(ctx, results)
			}
		},
		Condition: func(results *agent.Results) bool {
			// For PlanFactory, we only use the simple include/exclude.
			return c.Condition(results) != ConditionFail
		},
	}
}

// --- Helper to check if a tool is complex ---

// IsComplexToolInstance checks if a value implements the ComplexTool interface.
// Use this when you have an interface{} that might be a ComplexTool.
func IsComplexToolInstance(v any) (ComplexTool, bool) {
	ct, ok := v.(ComplexTool)
	return ct, ok
}
