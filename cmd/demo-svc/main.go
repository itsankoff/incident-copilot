// Command demo-svc is the demo workload that incident-copilot investigates.
// One binary runs one of three roles: orders, inventory, or loadgen.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/itsankoff/incident-copilot/internal/demo"
)

// Set with -ldflags at build time.
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	role := flag.String("role", "", "service role: orders | inventory | loadgen")
	flag.Parse()

	cfg, err := demo.LoadConfig(*role, os.Getenv)
	if err != nil {
		// Written as JSON so the failure is queryable in Loki like any other log line.
		logger := demo.NewLogger(*role, version, commit)
		logger.Error(fmt.Sprintf("fatal: load config: %v", err))
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger := demo.NewLogger(string(cfg.Role), version, commit)
	if err := demo.Run(ctx, cfg, logger, demo.BuildInfo{Version: version, Commit: commit}); err != nil {
		logger.Error("fatal: " + err.Error())
		os.Exit(1)
	}
}
