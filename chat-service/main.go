// Command chat-service runs the PlatePilot conversational HTTP service.
//
// It serves the /healthz liveness probe today; the agent, RAG, and /v1 endpoints
// arrive in M3-M5. It runs independently of the data pipeline.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/zed/platepilot/chat-service/internal/app"
	"github.com/zed/platepilot/chat-service/internal/config"
	"github.com/zed/platepilot/shared/observability/logging"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "platepilot chat-service: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	logger := logging.New(cfg.Log.Level)
	slog.SetDefault(logger)
	logger.Info("configuration loaded", slog.Any("config", cfg.Redacted().Summary()))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	application, err := app.Connect(ctx, cfg, logger, version)
	if err != nil {
		return err
	}

	runErr := make(chan error, 1)
	go func() { runErr <- application.Run() }()

	select {
	case err := <-runErr:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	case <-ctx.Done():
		stop()
		logger.Info("shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
		defer cancel()
		if err := application.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	}
}
