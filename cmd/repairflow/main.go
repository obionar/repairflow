// Command repairflow is the single RepairFlow executable. It embeds all web
// assets and needs no external runtime: CLI, and later the web UI, are served
// from this binary against one portable data workspace.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"repairflow/internal/cli"
)

func main() {
	// SIGINT/SIGTERM cancel the context so long-running commands (serve)
	// shut down gracefully instead of dying mid-request.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}
