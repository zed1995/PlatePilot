// Command data-pipeline runs PlatePilot's data production jobs.
//
// It is a batch CLI, independent of the chat service: import raw Google Local
// data into PostgreSQL, rebuild review stats, score restaurants, then (in M2) build
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
	"strconv"
	"syscall"

	"github.com/zed/platepilot/data-pipeline/internal/config"
	"github.com/zed/platepilot/data-pipeline/internal/pipeline"
	"github.com/zed/platepilot/shared/adapter/repository/postgres"
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
  migrate          apply SQL migrations and create indexes   (M1-02 / M1-03)
  import           import raw data and rebuild derived state (M1-04 .. M1-10)
  prefilter        write a smaller local review corpus for fast import    (M1-04)
  report           show recent import batch reports           (M1-09)
  build-documents  build restaurant and evidence documents    (M2-03 / M2-04)
  embed            generate and write embeddings              (M2-06)
  help             show this message

Migrate flags:
  --drop                              drop every table first (destructive)
  --status                            report applied migrations, change nothing

Import flags:
  --stage=meta|review|stats|score|all   default all
  --limit=N                             read at most N source rows (0 = all)
  --batch=N                             write batch size
  --data-dir=DIR                        directory holding the raw gz files
  --dry-run                             parse and count without writing
  --min-review-chars=N                  minimum usable review text length
  --demo-target=N                       target number of demo restaurants
  --bbox=S,W,N,E                        ingestion bounding box (default NYC)
  --boundary-file=PATH                  borough boundary geometry for
                                       borough_guess (default NYC DCP boroughs,
                                       water included)
  --require-boundaries                  fail rather than fall back to the
                                       approximate borough bounding boxes
  --skip-file-hash                      skip the source file SHA-256 (faster start)
  --quiet                              suppress the live progress line

Prefilter flags:
  --data-dir=DIR                       raw data dir (default PIPELINE_DATA_DIR)
  --review-file=NAME                   review file name inside the data dir (a
                                       prefiltered corpus is review-filtered.json.gz)
  --out-dir=DIR                        output dir (default data/processed)
  --out-file=NAME                      output file (default review-filtered.json.gz)
  --min-review-chars=N                 drop reviews with less usable text (default 20)
  --keep-short-text                    keep short-text reviews instead of dropping
  --limit=N                            read at most N source rows (0 = all)

Progress:
  The streaming stages (meta, review) print a rewritten status line to stderr
  with percent complete, throughput, ETA, and the accept/filter/reject
  breakdown. Progress is measured in compressed bytes read, so it is accurate
  even though the total line count of the 2.5 GB review file is unknown.
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
	case "prefilter":
		return runPrefilter(ctx, cfg, rest)
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

// openStores connects to PostgreSQL and builds the write-side stores.
func openStores(ctx context.Context, cfg config.Config) (*postgres.Client, pipeline.Stores, error) {
	client, err := postgres.Connect(ctx, postgres.ConfigFromPostgres(cfg.Postgres))
	if err != nil {
		return nil, pipeline.Stores{}, err
	}
	return client, pipeline.Stores{
		Restaurants: postgres.NewRestaurantStore(client),
		Reviews:     postgres.NewReviewStore(client),
		Pipeline:    postgres.NewPipelineStore(client),
	}, nil
}

func runMigrate(ctx context.Context, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	drop := fs.Bool("drop", false, "drop every table before migrating (destructive)")
	statusOnly := fs.Bool("status", false, "report which migrations are applied without changing anything")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if cfg.Postgres.DSN == "" {
		return errs.New(errs.CodeInvalidArgument, "POSTGRES_DSN is required for migrate")
	}

	client, _, err := openStores(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close(context.Background()) }()

	result, err := pipeline.Migrate(ctx, client, pipeline.MigrateOptions{Drop: *drop, StatusOnly: *statusOnly})
	if err != nil {
		return err
	}
	fmt.Printf("database: %s (dropped=%t, applied now=%d)\n", result.Database, result.Dropped, result.AppliedNow)
	for _, migration := range result.Migrations {
		state := "applied"
		switch {
		case !migration.Applied:
			state = "pending"
		case migration.AppliedAt != nil:
			state = "applied " + migration.AppliedAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		fmt.Printf("  migration %-24s %s\n", migration.Version, state)
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
	fs.StringVar(&opts.ReviewFile, "review-file", "", "review file name inside the data dir (default "+pipeline.ReviewFileName+"; a prefiltered corpus is "+pipeline.DefaultFilteredReviewFile+")")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "parse and count without writing")
	fs.IntVar(&opts.MinTextChars, "min-review-chars", opts.MinTextChars, "minimum usable review text length")
	fs.IntVar(&opts.DemoTarget, "demo-target", opts.DemoTarget, "target number of demo restaurants")
	fs.StringVar(&opts.ServiceArea, "bbox", opts.ServiceArea, "ingestion bounding box as south,west,north,east")
	fs.StringVar(&opts.BoundaryFile, "boundary-file", opts.BoundaryFile, "administrative boundary geometry for borough_guess (empty falls back to approximate boxes)")
	fs.BoolVar(&opts.RequireBoundaries, "require-boundaries", opts.RequireBoundaries, "fail instead of falling back to approximate borough boxes")
	fs.BoolVar(&opts.SkipFileHash, "skip-file-hash", false, "skip computing the source file SHA-256")
	quiet := fs.Bool("quiet", false, "suppress the live progress line")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Progress goes to stderr so stdout stays a clean report stream that can be
	// piped or captured without the in-place status redraw.
	if !*quiet {
		opts.ProgressOut = os.Stderr
	}

	var stores pipeline.Stores
	if cfg.Postgres.DSN != "" {
		client, s, err := openStores(ctx, cfg)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close(context.Background()) }()
		stores = s
	} else if !opts.DryRun {
		return errs.New(errs.CodeInvalidArgument, "POSTGRES_DSN is required unless --dry-run is set")
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
	if cfg.Postgres.DSN == "" {
		return errs.New(errs.CodeInvalidArgument, "POSTGRES_DSN is required for report")
	}

	client, stores, err := openStores(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close(context.Background()) }()

	if *batchID != "" {
		id, err := strconv.ParseInt(*batchID, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid --batch-id %q: %w", *batchID, err)
		}
		report, rejections, err := stores.Pipeline.BatchDetail(ctx, id)
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

// runPrefilter writes a filtered review corpus to the processed directory so
// the import stage does not have to push dead rows through the database.
func runPrefilter(ctx context.Context, cfg config.Config, args []string) error {
	opts := pipeline.PrefilterOptions{
		DataDir:      cfg.Pipeline.DataDir,
		MinTextChars: cfg.Pipeline.MinReviewChars,
		OutDir:       pipeline.DefaultProcessedDir,
		OutFile:      pipeline.DefaultFilteredReviewFile,
	}
	fs := flag.NewFlagSet("prefilter", flag.ContinueOnError)
	fs.StringVar(&opts.DataDir, "data-dir", opts.DataDir, "directory holding the raw gz files")
	fs.StringVar(&opts.OutDir, "out-dir", opts.OutDir, "directory to write the filtered corpus into")
	fs.StringVar(&opts.OutFile, "out-file", opts.OutFile, "filtered corpus file name")
	fs.IntVar(&opts.MinTextChars, "min-review-chars", opts.MinTextChars, "drop reviews with less usable text")
	fs.BoolVar(&opts.KeepShortText, "keep-short-text", false, "keep short-text reviews instead of dropping them")
	fs.IntVar(&opts.Limit, "limit", 0, "read at most N source rows (0 = all)")
	quiet := fs.Bool("quiet", false, "suppress the live progress line")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*quiet {
		opts.ProgressOut = os.Stderr
	}
	if cfg.Postgres.DSN == "" {
		return errs.New(errs.CodeInvalidArgument, "POSTGRES_DSN is required for prefilter (it reads the ingested restaurant set)")
	}

	client, stores, err := openStores(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close(context.Background()) }()

	result, err := pipeline.Prefilter(ctx, stores, opts)
	// A cancelled run still reports what it managed to produce.
	printPrefilter(result)
	return err
}

func printPrefilter(r pipeline.PrefilterResult) {
	if r.RowsRead == 0 {
		return
	}
	fmt.Printf("prefilter %s -> %s\n", r.SourceFile, r.OutputFile)
	fmt.Printf("  restaurants available  %d\n", r.RestaurantsAvailable)
	fmt.Printf("  rows read              %d\n", r.RowsRead)
	fmt.Printf("  kept                   %d  (%.2f%% of rows, %d covered)\n", r.Kept, r.KeptFraction(), r.RestaurantsCovered)
	fmt.Printf("  dropped unmatched      %d  (place not ingested)\n", r.Unmatched)
	fmt.Printf("  dropped no text        %d\n", r.NoText)
	fmt.Printf("  dropped short text     %d\n", r.ShortText)
	fmt.Printf("  rejected               %d\n", r.Rejected)
	if r.OutputBytes > 0 {
		fmt.Printf("  output                 %s (%d bytes)\n", humanBytes(r.OutputBytes), r.OutputBytes)
	}
	fmt.Printf("  duration               %d ms\n", r.DurationMS)
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGT"[exp])
}

func printReport(report review.BatchReport) {
	if report.BatchID == 0 {
		return // the stage failed before it could start; the error is reported separately
	}
	fmt.Printf("batch %d stage=%s status=%s rows=%d accepted=%d written=%d deduped=%d filtered=%d rejected=%d unmatched=%d duration_ms=%d\n",
		report.BatchID, report.Stage, report.Status, report.RowsRead, report.Accepted,
		report.Written, report.Deduped, report.Filtered, report.Rejected, report.Unmatched, report.DurationMS)
	for _, missing := range report.MissingFields {
		fmt.Printf("    missing %-16s %d\n", missing.Field, missing.Count)
	}
}
