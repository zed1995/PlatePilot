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
	"sort"
	"strconv"
	"syscall"

	"github.com/zed/platepilot/data-pipeline/internal/config"
	"github.com/zed/platepilot/data-pipeline/internal/pipeline"
	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/review"
	"github.com/zed/platepilot/shared/observability/logging"
	"github.com/zed/platepilot/shared/store/postgres"
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

Build-documents flags:
  --limit=N                            build at most N restaurants (0 = all)
  --restaurant-id=ID                   build one restaurant, for debugging
  --scope=restaurant|evidence          restrict to one retrieval scope
  --batch=N                            documents written per upsert
  --dry-run                            build and count without writing

Embed flags:
  --limit=N                            embed at most N pending documents
  --batch=N                            texts per provider request
  --workers=N                          embedding concurrency (default 4; a local CPU
                                       model does not scale past a handful)
  --restaurant-id=ID                   embed one restaurant
  --dry-run                            embed and count without writing
  --force-model-change                 write vectors from a different model,
                                       superseding the ones already stored

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
		return runBuildDocuments(ctx, cfg, rest)
	case "embed":
		return runEmbed(ctx, cfg, rest)
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
		Knowledge:   postgres.NewKnowledgeStore(client),
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
	stage := fs.String("stage", "", "only show batches of this stage")
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
	// The stage filter is applied here rather than in SQL because the batch
	// list is already bounded by --last; filtering a handful of rows in memory
	// is cheaper than another query, and it keeps ListBatches unchanged for the
	// import stages that do not need it.
	shown := 0
	for _, report := range batches {
		if *stage != "" && report.Stage != *stage {
			continue
		}
		printReport(report)
		shown++
	}
	if *stage != "" && shown == 0 {
		fmt.Printf("no batches with stage=%q in the last %d\n", *stage, *last)
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
	printStageReport(report)
}

// printStageReport prints the M2 document and embedding counters.
//
// The two sections are printed only when the batch carries them, because an
// import batch leaving them out is not a run that counted zero documents. The
// pointers on the report make that distinction, which a plain int would lose.
func printStageReport(report review.BatchReport) {
	if report.DocumentsBuilt != nil || report.DocumentsEmbedded != nil || report.DocumentsRejected != nil {
		fmt.Printf("  documents built=%s embedded=%s rejected=%s\n",
			optionalCount(report.DocumentsBuilt),
			optionalCount(report.DocumentsEmbedded),
			optionalCount(report.DocumentsRejected))
	}
	if report.EmbeddingModel != "" {
		fmt.Printf("  model %s dimensions=%s\n", report.EmbeddingModel,
			optionalCount(report.EmbeddingDimensions))
	}
	for _, reason := range sortedReasons(report.RejectReasons) {
		fmt.Printf("    rejected %-26s %d\n", reason, report.RejectReasons[reason])
	}
}

// optionalCount renders a nullable counter, spelling out "not reported" so a
// missing value is not read as a zero.
func optionalCount[T any](v *T) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprint(*v)
}

func sortedReasons(counts map[string]int64) []string {
	out := make([]string, 0, len(counts))
	for reason := range counts {
		out = append(out, reason)
	}
	sort.Strings(out)
	return out
}

// runBuildDocuments builds the knowledge documents for the demo restaurants.
func runBuildDocuments(ctx context.Context, cfg config.Config, args []string) error {
	opts, err := pipeline.BuildDocumentsOptions(args, cfg)
	if err != nil {
		return err
	}
	if cfg.Postgres.DSN == "" {
		return errs.New(errs.CodeInvalidArgument, "POSTGRES_DSN is required for build-documents")
	}
	_, stores, err := openStores(ctx, cfg)
	if err != nil {
		return err
	}
	result, err := pipeline.RunBuildDocuments(ctx, stores, opts)
	if err != nil {
		return err
	}
	fmt.Printf("restaurants=%d inserted=%d skipped=%d summaries=%d representative_reviews=%d\n",
		result.Restaurants, result.Inserted, result.Skipped, result.Summaries,
		result.RepresentativeReviews)
	for _, docType := range pipeline.SortedDocTypes(result.ByDocType) {
		fmt.Printf("    %-34s %d\n", docType, result.ByDocType[docType])
	}
	return nil
}

// runEmbed generates vectors for documents that do not have one yet.
func runEmbed(ctx context.Context, cfg config.Config, args []string) error {
	opts, err := pipeline.ParseEmbedOptions(args, cfg)
	if err != nil {
		return err
	}
	if cfg.Postgres.DSN == "" {
		return errs.New(errs.CodeInvalidArgument, "POSTGRES_DSN is required for embed")
	}
	if !cfg.Embedding.Enabled() {
		return errs.New(errs.CodeInvalidArgument,
			"EMBEDDING_PROVIDER is required for embed; run check-config to see what is missing")
	}
	_, stores, err := openStores(ctx, cfg)
	if err != nil {
		return err
	}
	result, err := pipeline.RunEmbed(ctx, stores, cfg, opts)
	if err != nil {
		return err
	}
	fmt.Printf("documents=%d embedded=%d skipped=%d rejected=%d failed=%d model=%s dims=%d duration_ms=%d\n",
		result.Documents, result.Embedded, result.Skipped, result.Rejected, result.Failed,
		result.Model, result.Dimensions, result.DurationMS)
	for _, reason := range pipeline.SortedRejectReasons(result.RejectReasons) {
		fmt.Printf("    rejected %-22s %d\n", reason, result.RejectReasons[reason])
	}
	return nil
}
