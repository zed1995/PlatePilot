// Command data-pipeline runs PlatePilot's data production jobs.
//
// It is a batch CLI, independent of the chat service: import raw Google Local
// data into Atlas, rebuild review stats, score restaurants, then (in M2) build
// knowledge documents and embeddings.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/zed/platepilot/data-pipeline/internal/config"
	"github.com/zed/platepilot/data-pipeline/internal/pipeline"
	"github.com/zed/platepilot/shared/adapter/repository/mongo"
	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/review"
	"github.com/zed/platepilot/shared/observability/logging"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `platepilot data-pipeline - data production jobs

Usage:
  data-pipeline <command> [flags]

Commands:
  version          print the build version
  check-config     load and validate configuration, then print a summary
  migrate          create collections and indexes             (M1-02 / M1-03)
  import           import raw data and rebuild derived state (M1-04 .. M1-10)
  report           show recent import batch reports           (M1-09)
  build-documents  build restaurant and evidence documents    (M2-03 / M2-04)
  embed            generate and write embeddings              (M2-06)
  help             show this message

Import flags:
  --stage=meta|review|stats|score|all   default all
  --limit=N                             read at most N source rows (0 = all)
  --batch=N                             write batch size
  --data-dir=DIR                        directory holding the raw gz files
  --dry-run                             parse and count without writing
  --min-review-chars=N                  minimum usable review text length
  --demo-target=N                       target number of demo restaurants
  --bbox=S,W,N,E                        ingestion bounding box (default NYC)
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "platepilot data-pipeline: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return errors.New("no command given")
	}
	command, rest := args[0], args[1:]

	// version and help must work even with a broken environment.
	switch command {
	case "version":
		fmt.Printf("platepilot data-pipeline %s\n", version)
		return nil
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
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
		fmt.Println(pipeline.ConfigSummary(cfg))
		return nil
	case "migrate":
		return runMigrate(ctx, cfg, rest)
	case "import":
		return runImport(ctx, cfg, rest)
	case "report":
		return runReport(ctx, cfg, rest)
	case "build-documents":
		return pipeline.BuildDocuments(ctx, cfg)
	case "embed":
		return pipeline.Embed(ctx, cfg)
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", command)
	}
}

// openStores connects to Atlas and builds the write-side stores.
func openStores(ctx context.Context, cfg config.Config) (*mongo.Client, pipeline.Stores, error) {
	client, err := mongo.Connect(ctx, mongo.ConfigFromMongo(cfg.Mongo))
	if err != nil {
		return nil, pipeline.Stores{}, err
	}
	return client, pipeline.Stores{
		Restaurants: mongo.NewRestaurantStore(client),
		Reviews:     mongo.NewReviewStore(client),
		Pipeline:    mongo.NewPipelineStore(client),
	}, nil
}

func runMigrate(ctx context.Context, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	drop := fs.Bool("drop", false, "drop collections before creating them (destructive)")
	indexesOnly := fs.Bool("indexes-only", false, "only ensure indexes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if cfg.Mongo.URI == "" {
		return errs.New(errs.CodeInvalidArgument, "MONGO_URI is required for migrate")
	}

	client, _, err := openStores(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close(context.Background()) }()

	result, err := pipeline.Migrate(ctx, client, pipeline.MigrateOptions{Drop: *drop, IndexesOnly: *indexesOnly})
	if err != nil {
		return err
	}
	fmt.Printf("database: %s (dropped=%t)\n", result.Database, result.Dropped)
	for _, collection := range result.Collections {
		state := "existing"
		if collection.Created {
			state = "created"
		}
		fmt.Printf("  collection %-28s %s\n", collection.Name, state)
	}
	for _, index := range result.Indexes {
		state := "existing"
		if index.Created {
			state = "created"
		}
		fmt.Printf("  index      %-28s %s.%s\n", index.Name, index.Collection, state)
	}
	return nil
}

func runImport(ctx context.Context, cfg config.Config, args []string) error {
	opts := pipeline.DefaultImportOptions(cfg)
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.StringVar(&opts.Stage, "stage", opts.Stage, "meta|review|stats|score|all")
	fs.IntVar(&opts.Limit, "limit", 0, "read at most N source rows (0 = all)")
	fs.IntVar(&opts.BatchSize, "batch", opts.BatchSize, "write batch size")
	fs.StringVar(&opts.DataDir, "data-dir", opts.DataDir, "directory holding the raw gz files")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "parse and count without writing")
	fs.IntVar(&opts.MinTextChars, "min-review-chars", opts.MinTextChars, "minimum usable review text length")
	fs.IntVar(&opts.DemoTarget, "demo-target", opts.DemoTarget, "target number of demo restaurants")
	fs.StringVar(&opts.ServiceArea, "bbox", opts.ServiceArea, "ingestion bounding box as south,west,north,east")
	fs.BoolVar(&opts.SkipFileHash, "skip-file-hash", false, "skip computing the source file SHA-256")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var stores pipeline.Stores
	if cfg.Mongo.URI != "" {
		client, s, err := openStores(ctx, cfg)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close(context.Background()) }()
		stores = s
	} else if !opts.DryRun {
		return errs.New(errs.CodeInvalidArgument, "MONGO_URI is required unless --dry-run is set")
	}

	reports, err := pipeline.RunImport(ctx, stores, opts)
	for _, report := range reports {
		printReport(report)
	}
	return err
}

func runReport(ctx context.Context, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	last := fs.Int("last", 5, "number of recent batches to print")
	batchID := fs.String("batch-id", "", "print one batch and its rejections")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if cfg.Mongo.URI == "" {
		return errs.New(errs.CodeInvalidArgument, "MONGO_URI is required for report")
	}

	client, stores, err := openStores(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close(context.Background()) }()

	if *batchID != "" {
		report, rejections, err := stores.Pipeline.BatchDetail(ctx, *batchID)
		if err != nil {
			return err
		}
		printReport(report)
		for _, rejection := range rejections {
			fmt.Printf("    line %d %-16s %s %s\n", rejection.LineNo, rejection.Reason, rejection.Stage, rejection.SourceRecordID)
		}
		return nil
	}

	batches, err := stores.Pipeline.ListBatches(ctx, *last)
	if err != nil {
		return err
	}
	for _, report := range batches {
		printReport(report)
	}
	return nil
}

func printReport(report review.BatchReport) {
	if report.BatchID == "" {
		return // the stage failed before it could start; the error is reported separately
	}
	fmt.Printf("batch %s stage=%s status=%s rows=%d accepted=%d written=%d deduped=%d filtered=%d rejected=%d unmatched=%d duration_ms=%d\n",
		report.BatchID, report.Stage, report.Status, report.RowsRead, report.Accepted,
		report.Written, report.Deduped, report.Filtered, report.Rejected, report.Unmatched, report.DurationMS)
	for _, missing := range report.MissingFields {
		fmt.Printf("    missing %-16s %d\n", missing.Field, missing.Count)
	}
}
