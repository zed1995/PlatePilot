package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/zed/platepilot/shared/domain/restaurant"
	"github.com/zed/platepilot/shared/store"

	"github.com/zed/platepilot/data-pipeline/internal/config"
	"github.com/zed/platepilot/data-pipeline/internal/pipeline/curate"
	"github.com/zed/platepilot/data-pipeline/internal/pipeline/knowledge"
	"github.com/zed/platepilot/data-pipeline/internal/pipeline/report"
	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/evidence"
	"github.com/zed/platepilot/shared/domain/review"
)

// DocumentsOptions controls one build-documents invocation.
type DocumentsOptions struct {
	// Limit caps how many restaurants are processed. Zero means all of them.
	Limit int
	// RestaurantID builds one restaurant, for debugging a single case.
	RestaurantID int64
	// Scope restricts the build to one retrieval scope.
	//
	// The zero value is not a scope. A struct literal that omits it builds
	// nothing and reports nothing, which reads as a successful no-op rather
	// than a misconfiguration, so RunBuildDocuments refuses it.
	Scope evidence.RetrievalScope
	// BatchSize is how many documents are written per upsert.
	BatchSize int
	// DryRun builds and counts without writing.
	DryRun bool
}

// DocumentsResult reports what one build produced.
type DocumentsResult struct {
	Restaurants           int
	Inserted              int
	Skipped               int
	ByDocType             map[evidence.DocType]int
	RepresentativeReviews int
	Summaries             int
}

// BuildDocumentsOptions parses the command flags.
func BuildDocumentsOptions(args []string, cfg config.Config) (DocumentsOptions, error) {
	opts := DocumentsOptions{
		BatchSize: cfg.Pipeline.BatchSize,
		Scope:     evidence.ScopeRestaurant,
	}
	for _, arg := range args {
		switch {
		case arg == "--dry-run":
			opts.DryRun = true
		case hasFlag(arg, "--limit"):
			n, err := parsePositive(argValue(arg, "--limit"), "--limit")
			if err != nil {
				return DocumentsOptions{}, err
			}
			opts.Limit = n
		case hasFlag(arg, "--batch"):
			n, err := parsePositive(argValue(arg, "--batch"), "--batch")
			if err != nil {
				return DocumentsOptions{}, err
			}
			opts.BatchSize = n
		case hasFlag(arg, "--restaurant-id"):
			n, err := parsePositive(argValue(arg, "--restaurant-id"), "--restaurant-id")
			if err != nil {
				return DocumentsOptions{}, err
			}
			opts.RestaurantID = int64(n)
		case hasFlag(arg, "--scope"):
			scope := evidence.RetrievalScope(argValue(arg, "--scope"))
			if scope != evidence.ScopeRestaurant && scope != evidence.ScopeEvidence {
				return DocumentsOptions{}, errs.Newf(errs.CodeInvalidArgument,
					"build-documents: unknown scope %q", scope)
			}
			opts.Scope = scope
		default:
			return DocumentsOptions{}, errs.Newf(errs.CodeInvalidArgument,
				"build-documents: unknown argument %q", arg)
		}
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 1
	}
	return opts, nil
}

// parsePositive reads a "=N" flag value.
func parsePositive(value, flag string) (int, error) {
	trimmed := strings.TrimPrefix(value, "=")
	if trimmed == "" {
		return 0, errs.Newf(errs.CodeInvalidArgument, "%s needs a number", flag)
	}
	n := 0
	for _, r := range trimmed {
		if r < '0' || r > '9' {
			return 0, errs.Newf(errs.CodeInvalidArgument, "%s=%q is not a number", flag, trimmed)
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

// RunBuildDocuments builds every knowledge document for the selected
// restaurants.
//
// Restaurants are processed in batches and each batch is written on its own,
// so a failure part-way through leaves the earlier work committed and
// idempotent: re-running resumes rather than starting over.
func RunBuildDocuments(ctx context.Context, stores Stores, opts DocumentsOptions) (DocumentsResult, error) {
	result := DocumentsResult{ByDocType: make(map[evidence.DocType]int)}
	if err := checkStores(stores); err != nil {
		return result, err
	}
	if opts.Scope != evidence.ScopeRestaurant && opts.Scope != evidence.ScopeEvidence {
		return result, errs.Newf(errs.CodeInvalidArgument,
			"build-documents: scope must be %q or %q, got %q; use BuildDocumentsOptions to parse the flags",
			evidence.ScopeRestaurant, evidence.ScopeEvidence, opts.Scope)
	}
	// A build that produced nothing is exactly the case a reviewer needs to see,
	// so the audit record is written on every path. A dry-run writes no batch
	// row at all, matching the import stages.
	collector := newStageCollector(stores, opts.DryRun, review.StageDocuments)
	if err := collector.Start(ctx); err != nil {
		return result, err
	}
	// The batch id ties every log line of this run to the audit row, which is
	// the only durable record that the run happened at all.
	batchID := collector.BatchID()
	slog.Info("document build started",
		slog.Int64("batch_id", batchID),
		slog.String("scope", string(opts.Scope)),
		slog.Int("limit", opts.Limit),
		slog.Int("batch_size", opts.BatchSize),
		slog.Bool("dry_run", opts.DryRun))

	started := time.Now()
	run := buildDocuments(ctx, stores, opts)
	finishStage(collector, run.err, func(c *report.Collector) {
		c.DocumentsBuilt(run.value.Inserted)
	})
	slog.Info("document build finished",
		slog.Int64("batch_id", batchID),
		slog.Int("restaurants", run.value.Restaurants),
		slog.Int("inserted", run.value.Inserted),
		slog.Int("skipped", run.value.Skipped),
		slog.Int("summaries", run.value.Summaries),
		slog.Int("representative_reviews", run.value.RepresentativeReviews),
		slog.Int64("duration_ms", time.Since(started).Milliseconds()),
		// The per-type counts go in as a group attribute rather than as a
		// marshalled map, so a log query can filter on a document type
		// without parsing a JSON blob out of the message.
		slog.Group("by_doc_type",
			docTypeAttrs(run.value.ByDocType)...))
	return run.value, run.err
}

// stageRun carries a stage's result together with its error, so both the
// success and the failure path can report the same counters before returning.
type stageRun struct {
	value DocumentsResult
	err   error
}

func buildDocuments(
	ctx context.Context,
	stores Stores,
	opts DocumentsOptions,
) stageRun {
	result := DocumentsResult{ByDocType: make(map[evidence.DocType]int)}
	if opts.RestaurantID > 0 {
		value, err := buildOneRestaurant(ctx, stores, opts, opts.RestaurantID)
		mergeResult(&result, value)
		return stageRun{value: result, err: err}
	}

	limit := opts.Limit
	offset := 0
	for {
		pageSize := opts.BatchSize
		if limit > 0 && offset+pageSize > limit {
			pageSize = limit - offset
		}
		if pageSize <= 0 {
			break
		}

		restaurants, err := selectRestaurants(ctx, stores.Restaurants, opts, offset, pageSize)
		if err != nil {
			return stageRun{value: result, err: err}
		}
		if len(restaurants) == 0 {
			break
		}
		for _, r := range restaurants {
			one, err := buildOneRestaurant(ctx, stores, opts, r.ID)
			if err != nil {
				return stageRun{value: result, err: err}
			}
			mergeResult(&result, one)
		}
		offset += len(restaurants)
		if len(restaurants) < pageSize {
			break
		}
	}
	return stageRun{value: result}
}

// selectRestaurants returns the restaurants to build documents for.
//
// Only restaurants selected for the demo set get documents: the demo set is
// what the product serves, and building all 36k would triple the vector count
// for places no query can reach.
func selectRestaurants(
	ctx context.Context,
	store store.RestaurantStore,
	opts DocumentsOptions,
	offset, limit int,
) ([]restaurant.Restaurant, error) {
	restaurants, err := store.SelectForDemo(ctx, offset+limit)
	if err != nil {
		return nil, err
	}
	if offset >= len(restaurants) {
		return nil, nil
	}
	window := restaurants[offset:]
	if len(window) > limit {
		window = window[:limit]
	}
	return window, nil
}

// buildOneRestaurant builds every document for one restaurant.
func buildOneRestaurant(
	ctx context.Context,
	stores Stores,
	opts DocumentsOptions,
	restaurantID int64,
) (DocumentsResult, error) {
	result := DocumentsResult{ByDocType: make(map[evidence.DocType]int)}

	r, err := stores.Restaurants.GetByID(ctx, restaurantID)
	if err != nil {
		return result, err
	}

	buildOptions := knowledge.ProfileOptions{
		CurationVersion: curate.CurationVersion,
		GeneratedAt:     time.Now().UTC(),
	}

	var docs []evidence.KnowledgeDocument

	if opts.Scope == evidence.ScopeRestaurant {
		profile, err := knowledge.BuildProfile(r, buildOptions)
		if err != nil {
			return result, operationError("build-documents: profile", err)
		}
		docs = append(docs, profile)
	}

	if opts.Scope == evidence.ScopeEvidence {
		evidenceDocs, counts, err := buildEvidenceDocuments(ctx, stores, r, buildOptions, opts.DryRun)
		if err != nil {
			return result, err
		}
		docs = append(docs, evidenceDocs...)
		result.RepresentativeReviews = counts.representative
		result.Summaries = counts.summaries
	}

	for _, doc := range docs {
		result.ByDocType[doc.DocType]++
	}

	if opts.DryRun {
		result.Restaurants = 1
		result.Inserted = len(docs)
		return result, nil
	}

	written := 0
	for start := 0; start < len(docs); start += opts.BatchSize {
		end := start + opts.BatchSize
		if end > len(docs) {
			end = len(docs)
		}
		upsert, err := stores.Knowledge.UpsertDocuments(ctx, docs[start:end])
		if err != nil {
			return result, operationError("build-documents: upsert", err)
		}
		written += upsert.Inserted
		result.Skipped += upsert.Skipped
	}
	result.Restaurants = 1
	result.Inserted = written
	return result, nil
}

// evidenceCounts reports what the evidence stage produced.
type evidenceCounts struct {
	representative int
	summaries      int
}

// buildEvidenceDocuments produces the four evidence-scope document types.
func buildEvidenceDocuments(
	ctx context.Context,
	stores Stores,
	r restaurant.Restaurant,
	build knowledge.ProfileOptions,
	dryRun bool,
) ([]evidence.KnowledgeDocument, evidenceCounts, error) {
	var counts evidenceCounts
	docs := make([]evidence.KnowledgeDocument, 0, 8)

	if attributes, ok, err := knowledge.BuildAttributes(r, build); err != nil {
		return nil, counts, operationError("build-documents: attributes", err)
	} else if ok {
		docs = append(docs, attributes)
	}
	if hours, ok, err := knowledge.BuildHours(r, build); err != nil {
		return nil, counts, operationError("build-documents: hours", err)
	} else if ok {
		docs = append(docs, hours)
	}

	reviews, err := stores.Reviews.ListByRestaurant(ctx, r.ID, reviewCandidateLimit)
	if err != nil {
		return nil, counts, err
	}
	withText := make([]review.Review, 0, len(reviews))
	for _, item := range reviews {
		if item.Text != "" {
			withText = append(withText, item)
		}
	}
	if len(withText) == 0 {
		return docs, counts, nil
	}

	// Summaries first: the rollup is stored so M3 can read it without
	// re-aggregating millions of reviews.
	average := averageOf(withText)
	stats := knowledge.SummarizeTopics(withText, average)
	if len(stats) > 0 {
		summaries := knowledge.BuildTopicSummaries(r.ID, stats, r.ObservedAt, build.GeneratedAt)
		if !dryRun {
			if _, err := stores.Reviews.UpsertSummaries(ctx, summaries); err != nil {
				return nil, counts, operationError("build-documents: summaries", err)
			}
		}
		docs = append(docs, knowledge.BuildSummaryDocuments(r, summaries, build)...)
		counts.summaries = len(summaries)
	}

	// Representative reviews last, because marking them changes what a later
	// stats stage would count.
	selected := knowledge.SelectRepresentative(withText, int64(len(withText)))
	doc, ok, err := knowledge.BuildRepresentativeDocument(
		r.Name, r.ID, r.BoroughGuess, r.ObservedAt, withText, selected, r.SourceRecordID, build)
	if err != nil {
		return nil, counts, operationError("build-documents: representative", err)
	}
	if ok {
		docs = append(docs, doc)
		counts.representative = len(selected)
		if !dryRun {
			ids := make([]int64, len(selected))
			for i, item := range selected {
				ids[i] = item.ID
			}
			if _, err := stores.Reviews.MarkRepresentative(ctx, ids); err != nil {
				return nil, counts, operationError("build-documents: mark representative", err)
			}
		}
	}

	return docs, counts, nil
}

// reviewCandidateLimit bounds how many reviews one restaurant contributes to
// the summary statistics. 4000 is far above the 30 that end up quoted and high
// enough that the topic ratios are not computed from a handful of rows.
const reviewCandidateLimit = 4000

// averageOf is the mean rating over the given reviews.
func averageOf(reviews []review.Review) *float64 {
	if len(reviews) == 0 {
		return nil
	}
	var sum float64
	for _, r := range reviews {
		sum += float64(r.Rating)
	}
	average := sum / float64(len(reviews))
	return &average
}

// checkStores rejects an incompletely wired store set.
//
// A nil store would otherwise surface as a nil-pointer panic several frames
// into the run, after the caller had already printed progress.
func checkStores(stores Stores) error {
	switch {
	case stores.Restaurants == nil:
		return errs.New(errs.CodeInternal, "build-documents: restaurant store is not configured")
	case stores.Reviews == nil:
		return errs.New(errs.CodeInternal, "build-documents: review store is not configured")
	case stores.Knowledge == nil:
		return errs.New(errs.CodeInternal, "build-documents: knowledge store is not configured")
	}
	return nil
}

// mergeResult folds one restaurant's counts into the run total.
func mergeResult(total *DocumentsResult, one DocumentsResult) {
	total.Restaurants += one.Restaurants
	total.Inserted += one.Inserted
	total.Skipped += one.Skipped
	total.RepresentativeReviews += one.RepresentativeReviews
	total.Summaries += one.Summaries
	for docType, count := range one.ByDocType {
		total.ByDocType[docType] += count
	}
}

// operationError wraps an error with an operation label, keeping an existing
// *errs.Error code intact so the caller still sees why it failed.
func operationError(op string, err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errs.As(err); ok {
		return err
	}
	return fmt.Errorf("%s: %w", op, err)
}

// docTypeAttrs renders the per-type counts as slog attributes in a stable
// order, so two runs of the same build produce comparable log lines.
func docTypeAttrs(counts map[evidence.DocType]int) []any {
	out := make([]any, 0, len(counts))
	for _, docType := range SortedDocTypes(counts) {
		out = append(out, slog.Int(string(docType), counts[docType]))
	}
	return out
}

// SortedDocTypes lists the produced document types in a stable order for
// reporting.
func SortedDocTypes(counts map[evidence.DocType]int) []evidence.DocType {
	out := make([]evidence.DocType, 0, len(counts))
	for docType := range counts {
		out = append(out, docType)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
