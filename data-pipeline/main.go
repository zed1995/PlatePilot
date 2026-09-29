// Command data-pipeline runs PlatePilot's data production jobs.
//
// It is a batch CLI, independent of the chat service: import raw Google Local
// data, build knowledge documents, and generate embeddings. In M0 the corpus
// stages are declared seams; `version`, `help`, and `check-config` work today.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/zed/platepilot/data-pipeline/internal/config"
	"github.com/zed/platepilot/data-pipeline/internal/pipeline"
	"github.com/zed/platepilot/shared/observability/logging"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `platepilot data-pipeline - data production jobs

Usage:
  data-pipeline <command>

Commands:
  version          print the build version
  check-config     load and validate configuration, then print a summary
  import           import raw Meta/Review data into Atlas      (M1-04 / M1-05)
  build-documents  build restaurant and evidence documents     (M2-03 / M2-04)
  embed            generate and write embeddings               (M2-06)
  help             show this message
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "platepilot data-pipeline: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	command := ""
	if len(args) > 0 {
		command = args[0]
	}

	// version and help must work even with a broken environment.
	switch command {
	case "version":
		fmt.Printf("platepilot data-pipeline %s\n", version)
		return nil
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	case "":
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("no command given")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	logger := logging.New(cfg.Log.Level)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch command {
	case "check-config":
		logger.Info("configuration ok", slog.Any("config", cfg.Redacted().Summary()))
		fmt.Println(pipeline.Report(cfg))
		return nil
	case "import":
		return pipeline.Import(ctx, cfg)
	case "build-documents":
		return pipeline.BuildDocuments(ctx, cfg)
	case "embed":
		return pipeline.Embed(ctx, cfg)
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", command)
	}
}
