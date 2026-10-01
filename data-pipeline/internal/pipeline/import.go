package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zed/platepilot/data-pipeline/internal/config"
	"github.com/zed/platepilot/data-pipeline/internal/pipeline/curate"
	"github.com/zed/platepilot/data-pipeline/internal/pipeline/raw"
	"github.com/zed/platepilot/data-pipeline/internal/pipeline/report"
	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/restaurant"
	"github.com/zed/platepilot/shared/domain/review"
	"github.com/zed/platepilot/shared/store"
)

// Source file names inside PIPELINE_DATA_DIR.
const (
	MetaFileName   = "meta-New_York.json.gz"
	ReviewFileName = "review-New_York.json.gz"
)

// SnapshotObservedAt is the Google Local 2021-09 snapshot time.
var SnapshotObservedAt = time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC)

// Stores bundles the write-side ports the import stages need. A nil Pipeline
// store turns auditing into a no-op, which is what --dry-run uses.
type Stores struct {
	Restaurants store.RestaurantStore
	Reviews     store.ReviewStore
	Pipeline    store.PipelineStore
	// Knowledge is the M2 write side for retrieval documents. It is nil until
	// M2 wires it, and the import stages never touch it.
	Knowledge store.KnowledgeStore
}

// ImportOptions controls one import invocation.
type ImportOptions struct {
	Stage     string
	Limit     int
	BatchSize int
	DataDir   string
	// ReviewFile overrides the review source file name inside DataDir. It lets
	// the importer read a prefiltered corpus (produced by `prefilter`) without
	// renaming it to match the raw source name. Empty uses ReviewFileName.
	ReviewFile   string
	DryRun       bool
	MinTextChars int
	DemoTarget   int
	// ProgressOut receives the in-place progress line for the streaming stages.
	// Nil disables progress output, which is what the tests and --quiet use.
	ProgressOut io.Writer
	// ServiceArea is "south,west,north,east". Empty uses the NYC default.
	ServiceArea string
	// BoundaryFile is the administrative boundary geometry used to label
	// borough_guess. Empty disables boundaries and falls back to boxes.
	BoundaryFile string
	// RequireBoundaries turns a missing or unrecognised boundary file into an
	// error instead of a silent fallback to approximate labels.
	RequireBoundaries bool
	SkipFileHash      bool
}

// DefaultImportOptions fills options from configuration.
func DefaultImportOptions(cfg config.Config) ImportOptions {
	batch := cfg.Pipeline.BatchSize
	if batch <= 0 {
		batch = 1000
	}
	minChars := cfg.Pipeline.MinReviewChars
	if minChars <= 0 {
		minChars = 20
	}
	demoTarget := cfg.Pipeline.DemoTarget
	if demoTarget <= 0 {
		demoTarget = curate.DefaultDemoRestaurants
	}
	return ImportOptions{
		Stage:             review.StageAll,
		BatchSize:         batch,
		DataDir:           cfg.Pipeline.DataDir,
		MinTextChars:      minChars,
		DemoTarget:        demoTarget,
		ServiceArea:       cfg.Pipeline.ServiceArea,
		BoundaryFile:      cfg.Pipeline.BoundaryFile,
		RequireBoundaries: cfg.Pipeline.RequireBoundaries,
	}
}

// RunImport executes the requested stage(s) and returns one report per stage.
func RunImport(ctx context.Context, stores Stores, opts ImportOptions) ([]review.BatchReport, error) {
	stage := strings.ToLower(strings.TrimSpace(opts.Stage))
	if stage == "" {
		stage = review.StageAll
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 1000
	}

	switch stage {
	case review.StageMeta:
		r, err := runMeta(ctx, stores, opts)
		return []review.BatchReport{r}, err
	case review.StageReview:
		r, err := runReview(ctx, stores, opts)
		return []review.BatchReport{r}, err
	case review.StageStats:
		r, err := runStats(ctx, stores, opts)
		return []review.BatchReport{r}, err
	case "score":
		r, err := runScore(ctx, stores, opts)
		return []review.BatchReport{r}, err
	case review.StageAll:
		reports := make([]review.BatchReport, 0, 4)
		for _, step := range []func(context.Context, Stores, ImportOptions) (review.BatchReport, error){
			runMeta, runReview, runStats, runScore,
		} {
			r, err := step(ctx, stores, opts)
			reports = append(reports, r)
			if err != nil {
				return reports, err
			}
		}
		return reports, nil
	default:
		return nil, errs.Newf(errs.CodeInvalidArgument, "unknown import stage %q (want meta|review|stats|score|all)", opts.Stage)
	}
}

// auditStore returns the audit store to use, or nil when the run is a
// dry-run (a dry-run must leave no trace in the database).
func auditStore(stores Stores, opts ImportOptions) store.PipelineStore {
	if opts.DryRun {
		return nil
	}
	return stores.Pipeline
}

// flushAttempts is how many times a bulk write is retried on a transient
// provider error. The upserts are idempotent, so replaying a batch is safe.
const flushAttempts = 4

// retryFlush re-runs an idempotent flush on transient provider failures with a
// linear backoff. Remote clusters drop long bulk writes far more often than a
// local server does, and aborting a 30-minute import on one blip is worse than
// replaying one batch.
func retryFlush(ctx context.Context, flush func() error) error {
	var err error
	for attempt := 0; attempt < flushAttempts; attempt++ {
		if err = flush(); err == nil {
			return nil
		}
		switch errs.CodeOf(err) {
		case errs.CodeProviderTimeout, errs.CodeProviderUnavailable:
		default:
			return err
		}
		if ctx.Err() != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Duration(attempt+1) * 3 * time.Second):
		}
	}
	return err
}

func runMeta(ctx context.Context, stores Stores, opts ImportOptions) (review.BatchReport, error) {
	area, err := curate.ParseServiceArea(opts.ServiceArea)
	if err != nil {
		return review.BatchReport{}, errs.Wrap(errs.CodeInvalidArgument, "invalid service area", err)
	}
	boroughs, err := loadBoroughs(opts)
	if err != nil {
		return review.BatchReport{}, err
	}
	path := filepath.Join(opts.DataDir, MetaFileName)
	reader, err := raw.Open(path)
	if err != nil {
		return review.BatchReport{}, errs.Wrap(errs.CodeNotFound, "open meta file "+path, err)
	}
	defer reader.Close()

	observedAt := SnapshotObservedAt
	col := report.New(auditStore(stores, opts), review.StageMeta, curate.CurationVersion, MetaFileName, time.Now().UTC())
	if boroughs != nil {
		col.SetBoundaryVersion(boroughs.Version())
	}
	if err := col.Start(ctx); err != nil {
		return col.Report(), err
	}
	progress := newProgressPrinter(review.StageMeta, reader, opts.ProgressOut)
	defer func() { progress.Finish(col.Report().RowsRead, progressSnapshot(col.Report())) }()
	if !opts.DryRun && !opts.SkipFileHash {
		if hash, err := raw.FileSHA256(path); err == nil {
			col.SetSourceSHA256(hash)
		}
	}

	batch := make([]restaurant.Restaurant, 0, opts.BatchSize)
	dedup := newDedupTracker(0)

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if !opts.DryRun {
			if stores.Restaurants == nil {
				return errs.New(errs.CodeInvalidArgument, "no restaurant store configured; set POSTGRES_DSN or use --dry-run")
			}
			written, err := stores.Restaurants.UpsertRestaurants(ctx, batch)
			if err != nil {
				return err
			}
			col.Written(written)
		}
		batch = batch[:0]
		return nil
	}

	for reader.Next() {
		if opts.Limit > 0 && col.Report().RowsRead >= int64(opts.Limit) {
			break
		}
		if err := ctx.Err(); err != nil {
			_ = col.Finish(context.WithoutCancel(ctx), review.StatusFailed, codeOf(err), time.Now().UTC())
			return col.Report(), err
		}
		col.RowRead()
		progress.Tick(col.Report().RowsRead, progressSnapshot(col.Report()))

		var record raw.Meta
		if err := reader.Decode(&record); err != nil {
			col.Reject(review.StageMeta, reader.LineNo(), "decode_error", "")
			continue
		}
		result, err := curate.NormalizeMeta(record, curate.MetaOptions{
			ObservedAt:  observedAt,
			ServiceArea: area,
			Boroughs:    boroughs,
		})
		switch {
		case errors.Is(err, curate.ErrFiltered):
			col.Filtered()
			continue
		case err != nil:
			col.Reject(review.StageMeta, reader.LineNo(), reasonOf(err), record.GmapID)
			continue
		}
		for _, field := range curate.MissingMetaFields(record) {
			col.MissingField(field)
		}
		col.Accepted(1)
		if dedup.Observe(result.SourceRecordID) {
			col.Deduped(1)
		}
		batch = append(batch, result)
		if len(batch) >= opts.BatchSize {
			if err := retryFlush(ctx, flush); err != nil {
				_ = col.Finish(context.WithoutCancel(ctx), review.StatusFailed, codeOf(err), time.Now().UTC())
				return col.Report(), err
			}
		}
	}
	if err := reader.Err(); err != nil {
		_ = col.Finish(context.WithoutCancel(ctx), review.StatusFailed, codeOf(err), time.Now().UTC())
		return col.Report(), errs.Wrap(errs.CodeInternal, "read meta file", err)
	}
	if err := retryFlush(ctx, flush); err != nil {
		_ = col.Finish(context.WithoutCancel(ctx), review.StatusFailed, codeOf(err), time.Now().UTC())
		return col.Report(), err
	}
	if err := col.Finish(ctx, review.StatusSucceeded, "", time.Now().UTC()); err != nil {
		return col.Report(), err
	}
	return col.Report(), nil
}

func runReview(ctx context.Context, stores Stores, opts ImportOptions) (review.BatchReport, error) {
	if stores.Restaurants == nil {
		return review.BatchReport{}, errs.New(errs.CodeInvalidArgument, "review import needs a restaurant store; set POSTGRES_DSN")
	}
	reviewFile := opts.ReviewFile
	if reviewFile == "" {
		reviewFile = ReviewFileName
	}
	path := filepath.Join(opts.DataDir, reviewFile)
	reader, err := raw.Open(path)
	if err != nil {
		return review.BatchReport{}, errs.Wrap(errs.CodeNotFound, "open review file "+path+notFoundHint(opts.DataDir, reviewFile), err)
	}
	defer reader.Close()

	observedAt := SnapshotObservedAt
	col := report.New(auditStore(stores, opts), review.StageReview, curate.CurationVersion, reviewFile, time.Now().UTC())
	if err := col.Start(ctx); err != nil {
		return col.Report(), err
	}
	progress := newProgressPrinter(review.StageReview, reader, opts.ProgressOut)
	defer func() { progress.Finish(col.Report().RowsRead, progressSnapshot(col.Report())) }()
	if !opts.DryRun && !opts.SkipFileHash {
		if hash, err := raw.FileSHA256(path); err == nil {
			col.SetSourceSHA256(hash)
		}
	}

	type pending struct {
		record raw.Review
		lineNo int64
	}
	batch := make([]pending, 0, opts.BatchSize)
	dedup := newDedupTracker(0)
	reviewOpts := curate.ReviewOptions{MinTextChars: opts.MinTextChars, ObservedAt: observedAt}

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		sourceIDs := make([]string, 0, len(batch))
		seen := map[string]bool{}
		for _, item := range batch {
			if item.record.GmapID != "" && !seen[item.record.GmapID] {
				seen[item.record.GmapID] = true
				sourceIDs = append(sourceIDs, item.record.GmapID)
			}
		}
		mapping, err := stores.Restaurants.MapSourceRecordIDs(ctx, sourceIDs)
		if err != nil {
			return err
		}
		curated := make([]review.Review, 0, len(batch))
		for _, item := range batch {
			restaurantID, ok := mapping[item.record.GmapID]
			if !ok {
				col.Unmatched()
				continue
			}
			normalized, err := curate.NormalizeReview(item.record, restaurantID, reviewOpts)
			if err != nil {
				col.Reject(review.StageReview, item.lineNo, reasonOf(err), item.record.GmapID)
				continue
			}
			col.Accepted(1)
			if dedup.Observe(curate.ReviewDedupKey(item.record.GmapID, item.record.UserID, item.record.Time, normalized.TextHash)) {
				col.Deduped(1)
			}
			curated = append(curated, normalized)
		}
		if !opts.DryRun && len(curated) > 0 {
			if stores.Reviews == nil {
				return errs.New(errs.CodeInvalidArgument, "no review store configured")
			}
			written, err := stores.Reviews.UpsertReviews(ctx, curated)
			if err != nil {
				return err
			}
			col.Written(written)
		}
		batch = batch[:0]
		return nil
	}

	for reader.Next() {
		if opts.Limit > 0 && col.Report().RowsRead >= int64(opts.Limit) {
			break
		}
		if err := ctx.Err(); err != nil {
			_ = col.Finish(context.WithoutCancel(ctx), review.StatusFailed, codeOf(err), time.Now().UTC())
			return col.Report(), err
		}
		col.RowRead()
		progress.Tick(col.Report().RowsRead, progressSnapshot(col.Report()))
		var record raw.Review
		if err := reader.Decode(&record); err != nil {
			col.Reject(review.StageReview, reader.LineNo(), "decode_error", "")
			continue
		}
		batch = append(batch, pending{record: record, lineNo: reader.LineNo()})
		if len(batch) >= opts.BatchSize {
			if err := retryFlush(ctx, flush); err != nil {
				_ = col.Finish(context.WithoutCancel(ctx), review.StatusFailed, codeOf(err), time.Now().UTC())
				return col.Report(), err
			}
		}
	}
	if err := reader.Err(); err != nil {
		_ = col.Finish(context.WithoutCancel(ctx), review.StatusFailed, codeOf(err), time.Now().UTC())
		return col.Report(), errs.Wrap(errs.CodeInternal, "read review file", err)
	}
	if err := retryFlush(ctx, flush); err != nil {
		_ = col.Finish(context.WithoutCancel(ctx), review.StatusFailed, codeOf(err), time.Now().UTC())
		return col.Report(), err
	}
	if err := col.Finish(ctx, review.StatusSucceeded, "", time.Now().UTC()); err != nil {
		return col.Report(), err
	}
	return col.Report(), nil
}

func runStats(ctx context.Context, stores Stores, opts ImportOptions) (review.BatchReport, error) {
	if stores.Restaurants == nil || stores.Reviews == nil {
		return review.BatchReport{}, errs.New(errs.CodeInvalidArgument, "stats rebuild needs a restaurant store; set POSTGRES_DSN")
	}
	col := report.New(auditStore(stores, opts), review.StageStats, curate.CurationVersion, "", time.Now().UTC())
	if err := col.Start(ctx); err != nil {
		return col.Report(), err
	}

	ids, err := stores.Reviews.RestaurantIDsWithReviews(ctx)
	if err != nil {
		_ = col.Finish(context.WithoutCancel(ctx), review.StatusFailed, codeOf(err), time.Now().UTC())
		return col.Report(), err
	}
	now := time.Now().UTC()
	for start := 0; start < len(ids); start += opts.BatchSize {
		end := min(start+opts.BatchSize, len(ids))
		chunk := ids[start:end]
		counts, err := stores.Reviews.AggregateStats(ctx, chunk)
		if err != nil {
			_ = col.Finish(context.WithoutCancel(ctx), review.StatusFailed, codeOf(err), time.Now().UTC())
			return col.Report(), err
		}
		for _, id := range chunk {
			current, err := stores.Restaurants.GetByID(ctx, id)
			if err != nil {
				col.Reject(review.StageStats, 0, reasonOf(err), strconv.FormatInt(id, 10))
				continue
			}
			rollup := counts[id]
			stats := curate.BuildReviewStats(current.ReviewStats, rollup, now)
			computed := curate.ComputedRating(current.Rating, rollup)
			if opts.DryRun {
				col.Accepted(1)
				continue
			}
			if err := stores.Restaurants.UpdateReviewStats(ctx, id, stats, computed); err != nil {
				col.Reject(review.StageStats, 0, reasonOf(err), strconv.FormatInt(id, 10))
				continue
			}
			col.Accepted(1)
			col.Written(1)
		}
	}
	if err := col.Finish(ctx, review.StatusSucceeded, "", time.Now().UTC()); err != nil {
		return col.Report(), err
	}
	return col.Report(), nil
}

func runScore(ctx context.Context, stores Stores, opts ImportOptions) (review.BatchReport, error) {
	if stores.Restaurants == nil {
		return review.BatchReport{}, errs.New(errs.CodeInvalidArgument, "scoring needs a restaurant store; set POSTGRES_DSN")
	}
	col := report.New(auditStore(stores, opts), "score", curate.CurationVersion, "", time.Now().UTC())
	if err := col.Start(ctx); err != nil {
		return col.Report(), err
	}

	all, err := stores.Restaurants.ListRestaurants(ctx, 0)
	if err != nil {
		_ = col.Finish(context.WithoutCancel(ctx), review.StatusFailed, codeOf(err), time.Now().UTC())
		return col.Report(), err
	}
	activeIDs := curate.SelectActiveForDemo(all, opts.DemoTarget)
	activeSet := make(map[int64]bool, len(activeIDs))
	for _, id := range activeIDs {
		activeSet[id] = true
	}
	scores := make(map[int64]float64, len(all))
	active := make(map[int64]bool, len(all))
	for _, r := range all {
		scores[r.ID] = curate.KnowledgeScore(r).Score
		active[r.ID] = activeSet[r.ID]
	}
	col.Accepted(int64(len(all)))
	if !opts.DryRun {
		if err := stores.Restaurants.UpdateScores(ctx, scores, active); err != nil {
			_ = col.Finish(context.WithoutCancel(ctx), review.StatusFailed, codeOf(err), time.Now().UTC())
			return col.Report(), err
		}
		col.Written(len(all))
	}
	if err := col.Finish(ctx, review.StatusSucceeded, "", time.Now().UTC()); err != nil {
		return col.Report(), err
	}
	return col.Report(), nil
}

// notFoundHint explains the most common cause of a missing review file: the
// corpus was prefiltered, which writes a differently named file. Without this,
// "open review file ...: no such file" leaves the reader guessing which flag or
// rename they are supposed to apply.
func notFoundHint(dataDir, reviewFile string) string {
	if reviewFile != ReviewFileName {
		return ""
	}
	filtered := filepath.Join(dataDir, DefaultFilteredReviewFile)
	if _, err := os.Stat(filtered); err != nil {
		return ""
	}
	return fmt.Sprintf("\n  hint: %s exists, so this is a prefiltered corpus; "+
		"import it with --data-dir=%s --review-file=%s",
		filtered, dataDir, DefaultFilteredReviewFile)
}

// progressSnapshot extracts the live counters for the progress line without
// copying the much wider full batch report.
func progressSnapshot(r review.BatchReport) ProgressSnapshot {
	return ProgressSnapshot{
		Accepted:  r.Accepted,
		Written:   r.Written,
		Deduped:   r.Deduped,
		Filtered:  r.Filtered,
		Rejected:  r.Rejected,
		Unmatched: r.Unmatched,
	}
}

// codeOf extracts the shared error code, defaulting to internal.
func codeOf(err error) string {
	if err == nil {
		return ""
	}
	return string(errs.CodeOf(err))
}

// reasonOf reduces an error to a stable, PII-free rejection reason.
func reasonOf(err error) string {
	code := errs.CodeOf(err)
	if code == "" {
		return "invalid"
	}
	return string(code)
}

// loadBoroughs resolves the boundary geometry used to label borough_guess.
//
// A missing boundary file degrades to approximate bounding boxes rather than
// failing the import, because the geometry is auxiliary fact data fetched
// separately from the corpus. That fallback is only safe while nobody notices:
// the boxes mislabel roughly 10% of restaurants, so the resolved version is
// returned too and recorded on the batch report. RequireBoundaries exists for
// scheduled runs that must never silently produce approximate labels.
func loadBoroughs(opts ImportOptions) (*curate.Boundaries, error) {
	if strings.TrimSpace(opts.BoundaryFile) == "" {
		if opts.RequireBoundaries {
			return nil, errs.New(errs.CodeInvalidArgument,
				"boundary file is required but PIPELINE_BOUNDARY_FILE is empty")
		}
		return nil, nil
	}
	boundaries, err := curate.LoadBoundaries(opts.BoundaryFile)
	switch {
	case err == nil:
		return boundaries, nil
	case opts.RequireBoundaries:
		return nil, err
	default:
		slog.Warn("borough labels fall back to approximate bounding boxes",
			slog.String("boundary_file", opts.BoundaryFile),
			slog.String("reason", err.Error()),
			slog.String("fix", "see data/boundaries/README.md"))
		return nil, nil
	}
}
