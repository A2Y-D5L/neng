package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/a2y-d5l/neng/examples/web/backend"
)

//go:embed all:frontend
var filesystem embed.FS

func main() {
	index, err := filesystem.ReadFile("frontend/index.html")
	if err != nil {
		log.Fatalf("Failed to read index.html: %v", err)
	}

	static, err := fs.Sub(filesystem, "frontend")
	if err != nil {
		log.Fatalf("Failed to create frontend sub-filesystem: %v", err)
	}

	// Create a context that is cancelled on SIGINT or SIGTERM
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := backend.Run(ctx, index, static); err != nil && err != context.Canceled {
		log.Fatalf("Server error: %v", err)
	}
}
