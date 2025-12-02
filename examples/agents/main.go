// Command agents provides a web-based agent dashboard demonstrating neng's
// execution engine capabilities for LLM-powered workflows.
//
// Usage:
//
//	go run . [flags]
//
// Flags:
//
//	-addr string
//	      Server address (default ":8080")
//	-api-key string
//	      API key for LLM service (or set LLM_API_KEY env var)
//	-mock
//	      Use mock LLM client for testing
package main

import (
	"embed"
	"flag"
	"io/fs"
	"log/slog"
	"os"

	"github.com/a2y-d5l/neng/examples/agents/backend"
	"github.com/a2y-d5l/neng/examples/agents/internal"
)

//go:embed all:frontend
var frontendFS embed.FS

func main() {
	// Parse flags
	addr := flag.String("addr", ":8080", "Server address")
	apiKey := flag.String("api-key", "", "API key for LLM service")
	useMock := flag.Bool("mock", false, "Use mock LLM client")
	flag.Parse()

	// Set up logger
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	slog.SetDefault(logger)

	// Get API key from flag or environment
	key := *apiKey
	if key == "" {
		key = os.Getenv("LLM_API_KEY")
	}

	// Create LLM client
	var llmClient internal.LLMClient
	switch {
	case *useMock:
		logger.Info("Using mock LLM client")
		llmClient = internal.NewMockLLMClient()
	case key != "":
		logger.Info("Using real LLM client")
		// In a real implementation, this would create an actual LLM client.
		// For now, we fall back to mock if no real client is available.
		llmClient = internal.NewMockLLMClient()
	default:
		logger.Warn("No LLM API key provided, agent runs will fail")
	}

	// Get frontend static files
	staticFS, err := fs.Sub(frontendFS, "frontend")
	if err != nil {
		logger.Error("Failed to get frontend filesystem", "error", err)
		os.Exit(1)
	}

	// Create and start server
	srv := backend.NewServer(
		backend.WithLogger(logger),
		backend.WithLLMClient(llmClient),
		backend.WithStaticFS(staticFS),
	)

	logger.Info("Starting agent server", "addr", *addr)
	if startErr := srv.ListenAndServeAddr(*addr); startErr != nil {
		logger.Error("Server failed", "error", startErr)
		os.Exit(1)
	}
}
