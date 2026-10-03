// Command chat-service runs the PlatePilot conversational HTTP service.
//
// It serves /healthz, the read-only /v1 retrieval API, the conversational /v1
// surface, and — when explicitly enabled — the loopback-only administration API.
// It runs independently of the data pipeline.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/zed1995/platepilot/chat-service/internal/app"
	"github.com/zed1995/platepilot/chat-service/internal/config"
	"github.com/zed1995/platepilot/shared/observability/logging"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `platepilot chat-service - conversational HTTP service

Usage:
  chat-service [serve]        run the HTTP server (default)
  chat-service check-config   load and validate configuration, then print a summary
  chat-service version        print the build version
  chat-service help           show this message
`

// Command names the CLI recognizes.
const (
	commandServe       = "serve"
	commandCheckConfig = "check-config"
	commandVersion     = "version"
	commandHelp        = "help"
)

func main() {
	if err := dispatch(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "platepilot chat-service: %v\n", err)
		os.Exit(1)
	}
}

// dispatch routes the command line.
func dispatch(args []string) error {
	command, err := resolveCommand(args)
	if err != nil {
		return err
	}
	switch command {
	case commandServe:
		return serve()
	case commandCheckConfig:
		return checkConfig()
	case commandVersion:
		fmt.Println(version)
		return nil
	case commandHelp:
		fmt.Print(usage)
		return nil
	default:
		return fmt.Errorf("unhandled command %q", command)
	}
}

// resolveCommand maps the command line onto a command name.
//
// Serving is the default so the deployed binary keeps working under an unchanged
// command line: adding subcommands must not turn an existing invocation into an
// error. It is separated from dispatch so the routing can be tested without
// starting a server.
func resolveCommand(args []string) (string, error) {
	if len(args) == 0 {
		return commandServe, nil
	}
	switch args[0] {
	case commandServe, "run":
		return commandServe, nil
	case commandCheckConfig:
		return commandCheckConfig, nil
	case commandVersion:
		return commandVersion, nil
	case commandHelp, "-h", "--help":
		return commandHelp, nil
	default:
		return "", fmt.Errorf("unknown command %q\n\n%s", args[0], usage)
	}
}

// checkConfig loads, validates, and prints the resolved configuration.
//
// It answers the question an operator actually has before a deployment — "what
// will this process use?" — without starting one, and it answers it with the
// same values the server would use: the same loader, the same .env layering, and
// the same validation. A summary printed by a second code path would be a
// summary that can disagree with reality.
//
// Secrets are redacted before anything is printed. The point of the command is
// to be readable and pasteable, and a summary that leaks an API key is worse
// than no summary at all.
func checkConfig() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(cfg.Redacted().Summary(), "", "  ")
	if err != nil {
		return fmt.Errorf("encode configuration summary: %w", err)
	}
	fmt.Printf("version: %s\nconfiguration:\n%s\n", version, encoded)
	return nil
}

// serve runs the HTTP server until a signal arrives.
func serve() error {
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
