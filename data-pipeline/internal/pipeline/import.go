package pipeline

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/zed/platepilot/data-pipeline/internal/config"
	"github.com/zed/platepilot/data-pipeline/internal/pipeline/curate"
	"github.com/zed/platepilot/data-pipeline/internal/pipeline/raw"
	"github.com/zed/platepilot/data-pipeline/internal/pipeline/report"
	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/restaurant"
	"github.com/zed/platepilot/shared/domain/review"
	"github.com/zed/platepilot/shared/idgen"
	"github.com/zed/platepilot/shared/port"
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
	Restaurants port.RestaurantStore
	Reviews     port.ReviewStore
	Pipeline    port.PipelineStore
}

// ImportOptions controls one import invocation.
type ImportOptions struct {
	Stage        string
	Limit        int
	BatchSize    int
	DataDir      string
	DryRun       bool
	MinTextChars int
	DemoTarget   int
	SkipFileHash bool
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
		Stage:        review.StageAll,
		BatchSize:    batch,
		DataDir:      cfg.Pipeline.DataDir,
		MinTextChars: minChars,
		DemoTarget:   demoTarget,
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
// dry-run (a dry-run must leave no trace in Atlas).
func auditStore(stores Stores, opts ImportOptions) port.PipelineStore {
	if opts.DryRun {
		return nil
	}
	return stores.Pipeline
}

func runMeta(ctx context.Context, stores Stores, opts ImportOptions) (review.BatchReport, error) {
	path := filepath.Join(opts.DataDir, MetaFileName)
	reader, err := raw.Open(path)
	if err != nil {
		return review.BatchReport{}, errs.Wrap(errs.CodeNotFound, "open meta file "+path, err)
	}
	defer reader.Close()

	observedAt := SnapshotObservedAt
	col := report.New(auditStore(stores, opts), review.StageMeta, curate.CurationVersion, MetaFileName, time.Now().UTC())
	if err := col.Start(ctx); err != nil {
		return col.Report(), err
	}
	if !opts.DryRun && !opts.SkipFileHash {
		if hash, err := raw.FileSHA256(path); err == nil {
			col.SetSourceSHA256(hash)
		}
	}

	batch := make([]restaurant.Restaurant, 0, opts.BatchSize)
	docs := make([]restaurant.Document, 0, opts.BatchSize)

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if !opts.DryRun {
			if stores.Restaurants == nil {
				return errs.New(errs.CodeInvalidArgument, "no restaurant store configured; set MONGO_URI or use --dry-run")
			}
			written, err := stores.Restaurants.UpsertRestaurants(ctx, batch)
			if err != nil {
				return err
			}
			if err := stores.Restaurants.UpsertDocuments(ctx, docs); err != nil {
				return err
			}
			col.Written(written)
		}
		batch = batch[:0]
		docs = docs[:0]
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

		var record raw.Meta
		if err := reader.Decode(&record); err != nil {
			col.Reject(review.StageMeta, reader.LineNo(), "decode_error", "")
			continue
		}
		result, err := curate.NormalizeMeta(record, idgen.NewUUID(), observedAt)
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
		batch = append(batch, result.Restaurant)
		docs = append(docs, result.Documents...)
		if len(batch) >= opts.BatchSize {
			if err := flush(); err != nil {
				_ = col.Finish(context.WithoutCancel(ctx), review.StatusFailed, codeOf(err), time.Now().UTC())
				return col.Report(), err
			}
		}
	}
	if err := reader.Err(); err != nil {
		_ = col.Finish(context.WithoutCancel(ctx), review.StatusFailed, codeOf(err), time.Now().UTC())
		return col.Report(), errs.Wrap(errs.CodeInternal, "read meta file", err)
	}
	if err := flush(); err != nil {
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
		return review.BatchReport{}, errs.New(errs.CodeInvalidArgument, "review import needs a restaurant store; set MONGO_URI")
	}
	path := filepath.Join(opts.DataDir, ReviewFileName)
	reader, err := raw.Open(path)
	if err != nil {
		return review.BatchReport{}, errs.Wrap(errs.CodeNotFound, "open review file "+path, err)
	}
	defer reader.Close()

	observedAt := SnapshotObservedAt
	col := report.New(auditStore(stores, opts), review.StageReview, curate.CurationVersion, ReviewFileName, time.Now().UTC())
	if err := col.Start(ctx); err != nil {
		return col.Report(), err
	}
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
		var record raw.Review
		if err := reader.Decode(&record); err != nil {
			col.Reject(review.StageReview, reader.LineNo(), "decode_error", "")
			continue
		}
		batch = append(batch, pending{record: record, lineNo: reader.LineNo()})
		if len(batch) >= opts.BatchSize {
			if err := flush(); err != nil {
				_ = col.Finish(context.WithoutCancel(ctx), review.StatusFailed, codeOf(err), time.Now().UTC())
				return col.Report(), err
			}
		}
	}
	if err := reader.Err(); err != nil {
		_ = col.Finish(context.WithoutCancel(ctx), review.StatusFailed, codeOf(err), time.Now().UTC())
		return col.Report(), errs.Wrap(errs.CodeInternal, "read review file", err)
	}
	if err := flush(); err != nil {
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
		return review.BatchReport{}, errs.New(errs.CodeInvalidArgument, "stats rebuild needs Mongo; set MONGO_URI")
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
				col.Reject(review.StageStats, 0, reasonOf(err), id)
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
				col.Reject(review.StageStats, 0, reasonOf(err), id)
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
		return review.BatchReport{}, errs.New(errs.CodeInvalidArgument, "scoring needs a restaurant store; set MONGO_URI")
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
	activeSet := make(map[string]bool, len(activeIDs))
	for _, id := range activeIDs {
		activeSet[id] = true
	}
	scores := make(map[string]float64, len(all))
	active := make(map[string]bool, len(all))
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
