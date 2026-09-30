package pipeline

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zed/platepilot/data-pipeline/internal/config"
	"github.com/zed/platepilot/data-pipeline/internal/pipeline/knowledge"
	"github.com/zed/platepilot/data-pipeline/internal/pipeline/report"
	sharedcfg "github.com/zed/platepilot/shared/config"

	"github.com/zed/platepilot/shared/adapter/embedding/fake"
	"github.com/zed/platepilot/shared/adapter/embedding/ollama"
	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/evidence"
	"github.com/zed/platepilot/shared/domain/review"
	"github.com/zed/platepilot/shared/port"
)

// EmbedOptions controls one embed invocation.
type EmbedOptions struct {
	// Limit caps how many documents are embedded in this run. Zero means every
	// pending document.
	Limit int
	// BatchSize is how many texts go into one provider request.
	BatchSize int
	// Workers is the embedding concurrency.
	Workers int
	// RestaurantID embeds one restaurant's documents.
	RestaurantID int64
	// DryRun embeds and counts without writing.
	DryRun bool
	// ForceModelChange allows writing vectors from a different model than the
	// one already recorded on live documents.
	ForceModelChange bool
	// Timeout bounds one provider request.
	Timeout time.Duration
}

// duplicateSet tracks the vectors already written in this run.
//
// It is shared by every batch, including the concurrent ones, so a repeated
// vector is caught whichever worker happens to receive it. A plain map would
// not do: the workers run at the same time, and a map written from several
// goroutines is a data race even when every key is distinct.
type duplicateSet struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

// add records a fingerprint and reports whether it was already present.
//
// The check and the insert happen under one lock. Doing them separately would
// let two workers both observe "not present" and both keep their vector, which
// is the failure this type exists to prevent.
func (d *duplicateSet) add(fingerprint string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen == nil {
		d.seen = make(map[string]struct{})
	}
	if _, exists := d.seen[fingerprint]; exists {
		return false
	}
	d.seen[fingerprint] = struct{}{}
	return true
}

// duplicateReason is the audit key for a repeated vector. It is the code's
// string value, so the batch report and the error taxonomy cannot drift apart.
var duplicateReason = string(errs.CodeEmbeddingDuplicate)

// newDuplicateSet returns an empty set sized for one run.
func newDuplicateSet(size int) *duplicateSet {
	return &duplicateSet{seen: make(map[string]struct{}, size)}
}

// pendingDocument is one document awaiting a vector.
type pendingDocument struct {
	id      int64
	content string
}

// EmbedResult reports what one run did.
type EmbedResult struct {
	Documents     int
	Embedded      int
	Skipped       int
	Rejected      int
	Failed        int
	Model         string
	Dimensions    int
	DurationMS    int64
	RejectReasons map[string]int
}

// SortedRejectReasons lists rejection reasons in a stable order.
func SortedRejectReasons(counts map[string]int) []string {
	out := make([]string, 0, len(counts))
	for reason := range counts {
		out = append(out, reason)
	}
	sort.Strings(out)
	return out
}

// ParseEmbedOptions parses the command flags.
func ParseEmbedOptions(args []string, cfg config.Config) (EmbedOptions, error) {
	opts := EmbedOptions{
		// The configured batch size is the default so throughput can be tuned
		// by restarting with a different EMBEDDING_MAX_BATCH; --batch still
		// wins for a one-off measurement.
		BatchSize: cfg.Embedding.BatchSize(),
		Workers:   cfg.Pipeline.Workers,
		Timeout:   cfg.Timeout.Request,
	}
	for _, arg := range args {
		switch {
		case arg == "--dry-run":
			opts.DryRun = true
		case arg == "--force-model-change":
			opts.ForceModelChange = true
		case hasFlag(arg, "--limit"):
			n, err := parsePositive(argValue(arg, "--limit"), "--limit")
			if err != nil {
				return EmbedOptions{}, err
			}
			opts.Limit = n
		case hasFlag(arg, "--batch"):
			n, err := parsePositive(argValue(arg, "--batch"), "--batch")
			if err != nil {
				return EmbedOptions{}, err
			}
			opts.BatchSize = n
		case hasFlag(arg, "--workers"):
			n, err := parsePositive(argValue(arg, "--workers"), "--workers")
			if err != nil {
				return EmbedOptions{}, err
			}
			opts.Workers = n
		case hasFlag(arg, "--restaurant-id"):
			n, err := parsePositive(argValue(arg, "--restaurant-id"), "--restaurant-id")
			if err != nil {
				return EmbedOptions{}, err
			}
			opts.RestaurantID = int64(n)
		default:
			return EmbedOptions{}, errs.Newf(errs.CodeInvalidArgument, "embed: unknown argument %q", arg)
		}
	}
	if opts.Workers <= 0 {
		opts.Workers = 1
	}
	return opts, nil
}

// newProvider builds the embedding provider named by the configuration.
//
// It is a variable so the pipeline tests can substitute a deterministic
// provider. The stage's own logic — batching, the quality gate, the write-back
// — is worth testing without a local model running, and a test that needs a
// 639MB download to check a counter is a test nobody runs.
var newProvider = embeddingProvider

// embeddingProvider constructs the configured provider.
func embeddingProvider(cfg config.Config, opts EmbedOptions) (port.EmbeddingProvider, error) {
	switch cfg.Embedding.Provider {
	case sharedcfg.ProviderFake:
		return fake.New(cfg.Embedding.Dimensions), nil
	default:
		return ollama.NewWithOptions(ollama.Options{
			BaseURL:    cfg.Embedding.BaseURL,
			Model:      cfg.Embedding.Model,
			Dimensions: cfg.Embedding.Dimensions,
			Timeout:    opts.Timeout,
		})
	}
}

// pendingPageSize is how many pending documents are claimed per store call
// when the run is unbounded. It only bounds memory and the size of one result
// set; the loop keeps going until nothing is left, so it does not cap the run.
const pendingPageSize = 500

// hasFlag reports whether arg is flag with a value attached.
func hasFlag(arg, flag string) bool { return len(arg) > len(flag) && arg[:len(flag)] == flag }

// argValue returns the value after a flag, without the "=" separator.
//
// The separator is stripped here rather than in each caller: one of them used
// to forget it, and "--scope=evidence" silently became the scope "=evidence".
func argValue(arg, flag string) string {
	return strings.TrimPrefix(arg[len(flag):], "=")
}

// RunEmbed fills in vectors for every active document that has none.
//
// The stage is a loop over batches rather than one pass: documents that were
// built after this run started, or that failed a batch, are picked up by the
// next iteration, and the loop ends when nothing is pending.
func RunEmbed(ctx context.Context, stores Stores, cfg config.Config, opts EmbedOptions) (EmbedResult, error) {
	result := EmbedResult{
		Model:         cfg.Embedding.Model,
		Dimensions:    cfg.Embedding.Dimensions,
		RejectReasons: make(map[string]int),
	}
	if err := checkKnowledgeStores(stores); err != nil {
		return result, err
	}
	collector := newStageCollector(stores, opts.DryRun, review.StageEmbedding)
	if err := collector.Start(ctx); err != nil {
		return result, err
	}

	provider, err := newProvider(cfg, opts)
	if err != nil {
		finishStage(collector, err, func(c *report.Collector) {
			c.SetEmbedding(cfg.Embedding.Model, cfg.Embedding.Dimensions)
		})
		return result, err
	}

	// The batch id is the pipeline's trace identifier: the audit row is the only
	// durable record of a run, and a log line without it cannot be tied back to
	// one. It is logged after Start because before that the run has no id.
	batchID := collector.BatchID()
	slog.Info("embedding stage started",
		slog.Int64("batch_id", batchID),
		slog.String("model", cfg.Embedding.Model),
		slog.Int("dimensions", cfg.Embedding.Dimensions),
		slog.Int("batch_size", opts.BatchSize),
		slog.Int("workers", opts.Workers),
		slog.Bool("dry_run", opts.DryRun))

	run := embedDocuments(ctx, stores, provider, cfg, opts, &result, collector)
	result = run.value

	// Only counts and reasons are logged. Vectors are never written to a log:
	// 1024 floats per document would make the output unreadable and enormous.
	slog.Info("embedding stage finished",
		slog.Int64("batch_id", batchID),
		slog.Int("documents", result.Documents),
		slog.Int("embedded", result.Embedded),
		slog.Int("rejected", result.Rejected),
		slog.Int("failed", result.Failed),
		slog.Int("skipped", result.Skipped),
		slog.Int64("duration_ms", result.DurationMS))

	// The model is stamped before the counts so the audit row records which
	// model this run claimed to use even when it rejected everything.
	finishStage(collector, run.err, func(c *report.Collector) {
		c.SetEmbedding(cfg.Embedding.Model, cfg.Embedding.Dimensions)
		c.DocumentsEmbedded(result.Embedded)
		c.DocumentsRejected(result.Rejected)
		// The reason counts are not copied here: RejectDocument already
		// recorded each one as it happened, and adding them again would double
		// every rejection in the audit row.
	})
	return result, run.err
}

// embedRun carries the result and error together so the audit record can be
// finished with the same counts on either path.
type embedRun struct {
	value EmbedResult
	err   error
}

// checkKnowledgeStores rejects an incompletely wired store set before the run
// starts, so a missing store is a configuration error rather than a panic after
// the first batch.
func checkKnowledgeStores(stores Stores) error {
	switch {
	case stores.Knowledge == nil:
		return errs.New(errs.CodeInternal, "embed: knowledge store is not configured")
	case stores.Restaurants == nil:
		return errs.New(errs.CodeInternal, "embed: restaurant store is not configured")
	}
	return nil
}

// embedDocuments runs the provider loop and reports the rejected documents to
// the audit record.
func embedDocuments(
	ctx context.Context,
	stores Stores,
	provider port.EmbeddingProvider,
	cfg config.Config,
	opts EmbedOptions,
	result *EmbedResult,
	collector *report.Collector,
) embedRun {
	if opts.RestaurantID > 0 {
		value, err := embedOneRestaurant(ctx, stores, provider, cfg, opts, opts.RestaurantID, result, collector)
		return embedRun{value: value, err: err}
	}

	if err := guardModelChange(ctx, stores, cfg, opts); err != nil {
		return embedRun{value: *result, err: err}
	}

	started := time.Now()
	// rejected holds the documents this run refused, so the next page skips
	// them instead of asking the provider for the same unusable vector again.
	// Without it a document whose text yields a zero vector is re-read on every
	// pass, the rejection count climbs forever, and the loop never terminates.
	rejected := make(map[int64]struct{})
	// The store's limit is a page size, not a run total, and it must never be
	// zero: PendingDocuments reads zero as "return nothing", which would make
	// the default (unlimited) run a silent no-op.
	pageSize := opts.Limit
	if pageSize <= 0 {
		pageSize = pendingPageSize
	}
	for {
		pending, err := stores.Knowledge.PendingDocuments(ctx, pageSize)
		if err != nil {
			return embedRun{value: *result, err: err}
		}
		if len(pending) == 0 {
			break
		}
		page := make([]evidence.KnowledgeDocument, 0, len(pending))
		for _, doc := range pending {
			if _, skip := rejected[doc.DocumentID]; skip {
				continue
			}
			page = append(page, doc)
		}
		if len(page) == 0 {
			// Everything still pending has already been refused by this run.
			// They stay in the database without a vector, which is the honest
			// outcome; re-reading them would only re-reject them.
			break
		}
		result.Documents += len(page)
		refused, err := embedPageConcurrently(ctx, stores, provider, page, cfg, opts, result, collector)
		for _, id := range refused {
			rejected[id] = struct{}{}
		}
		// Activation runs before the error check, and this is not a tidiness
		// decision. A page is embedded batch by batch, and a failure in a later
		// batch does not undo the batches that already wrote their vectors.
		// Returning on the error first would leave those documents vectored but
		// inactive — and since they now carry a vector they have left the
		// pending set, so no later run would ever pick them up again. They
		// would be permanently invisible: in no HNSW index, unreachable by
		// retrieval, and reported by no count. activatePage is idempotent and
		// filters the page down to what actually received a vector, so calling
		// it on a half-finished page activates the successful batches and
		// leaves the rest alone.
		if !opts.DryRun {
			if activateErr := activatePage(ctx, stores, page); activateErr != nil {
				return embedRun{value: *result, err: activateErr}
			}
		}
		if err != nil {
			return embedRun{value: *result, err: err}
		}
		if opts.Limit > 0 && result.Documents >= opts.Limit {
			break
		}
		if opts.DryRun {
			break
		}
	}
	result.DurationMS = time.Since(started).Milliseconds()

	// The count is written only after the loop finished, so a run that died
	// part-way leaves the previous value in place rather than a partial one.
	if !opts.DryRun && result.Embedded > 0 {
		if err := writeBackEmbeddedReviewCount(ctx, stores, opts); err != nil {
			return embedRun{value: *result, err: err}
		}
	}
	return embedRun{value: *result}
}

// embedPageConcurrently splits one page into batches and embeds them in
// parallel.
//
// Concurrency is bounded by opts.Workers rather than by the batch count, and
// deliberately so: the provider is a local CPU model, so extra in-flight
// requests do not raise total throughput — they just make each one slower and
// hold more text in memory. Four is the measured default; the flag exists so
// the effect can be demonstrated, not so it can be maximised.
//
// The first error wins and the remaining workers still finish their current
// batch before returning. Letting them run to completion rather than cancelling
// mid-request means a partially written page is never left half-done, which is
// what makes an interrupted run resumable.
func embedPageConcurrently(
	ctx context.Context,
	stores Stores,
	provider port.EmbeddingProvider,
	page []evidence.KnowledgeDocument,
	cfg config.Config,
	opts EmbedOptions,
	result *EmbedResult,
	collector *report.Collector,
) ([]int64, error) {
	// One set for the whole page, not one per worker: duplicate detection that
	// resets per worker only catches the duplicates inside a single batch, and
	// a provider that returns the same vector for everything is exactly the
	// fault the check exists to catch.
	seen := newDuplicateSet(len(page))

	workers := opts.Workers
	if workers <= 1 || len(page) <= opts.BatchSize {
		// Nothing to gain: one batch, or the caller asked for one worker.
		return embedBatch(ctx, stores, provider, page, cfg, opts, result, collector, seen)
	}

	batches := make([][]evidence.KnowledgeDocument, 0, len(page)/opts.BatchSize+1)
	for start := 0; start < len(page); start += opts.BatchSize {
		end := start + opts.BatchSize
		if end > len(page) {
			end = len(page)
		}
		batches = append(batches, page[start:end])
	}
	if workers > len(batches) {
		workers = len(batches)
	}

	type outcome struct {
		refused    []int64
		rejections []review.Rejection
		err        error
	}
	outcomes := make([]outcome, len(batches))
	queue := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range queue {
				batchResult := EmbedResult{RejectReasons: make(map[string]int)}
				// Each worker gets its own collector so they do not race on a
				// shared map. Its rejections are handed to the run's collector
				// after the join, which is the only point where the report is
				// written anyway.
				workerCollector := newIsolatedCollector()
				refused, err := embedBatch(ctx, stores, provider, batches[index], cfg, opts,
					&batchResult, workerCollector, seen)
				outcomes[index] = outcome{
					refused:    refused,
					rejections: workerCollector.Rejections(),
					err:        err,
				}
				// The counters are merged once, under the lock, rather than
				// accumulated by the workers directly.
				mu.Lock()
				result.merge(&batchResult)
				if err != nil && firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}()
	}
	for index := range batches {
		queue <- index
	}
	close(queue)
	wg.Wait()

	refused := make([]int64, 0, len(page))
	for _, item := range outcomes {
		refused = append(refused, item.refused...)
		for _, rejection := range item.rejections {
			// RejectDocument, not Reject: the worker already decided the reason
			// code, and Reject would re-derive a generic one and would not touch
			// the reject_reasons map the quality report is built from.
			collector.RejectDocument(rejection.Stage, rejection.LineNo, rejection.Reason)
		}
	}
	return refused, firstErr
}

// merge folds a batch's counts into the run total.
func (r *EmbedResult) merge(other *EmbedResult) {
	r.Embedded += other.Embedded
	r.Skipped += other.Skipped
	r.Rejected += other.Rejected
	r.Failed += other.Failed
	for reason, count := range other.RejectReasons {
		if r.RejectReasons == nil {
			r.RejectReasons = make(map[string]int)
		}
		r.RejectReasons[reason] += count
	}
}

// newIsolatedCollector returns a collector that buffers rejections in memory.
//
// A nil store makes it write nothing, which is what is wanted: the batch report
// is written once, at the end of the run, by the run's own collector. Handing
// each worker that one would make them race on its internal maps, and would
// append rejections that Finish rewrites anyway.
func newIsolatedCollector() *report.Collector {
	return report.New(nil, review.StageEmbedding, "", "", time.Time{})
}

// embedOneRestaurant embeds a single restaurant's documents.
func embedOneRestaurant(
	ctx context.Context,
	stores Stores,
	provider port.EmbeddingProvider,
	cfg config.Config,
	opts EmbedOptions,
	restaurantID int64,
	result *EmbedResult,
	collector *report.Collector,
) (EmbedResult, error) {
	if err := guardModelChange(ctx, stores, cfg, opts); err != nil {
		return *result, err
	}
	// Both scopes have to be read explicitly. ListByRestaurant matches the
	// scope exactly — it was called with "" here, which matches no document
	// and so returned an empty list for every restaurant. Combined with the
	// is_active filter that has since been removed, `embed --restaurant-id`
	// reported a clean run having done nothing at all, twice over.
	documents := make([]evidence.KnowledgeDocument, 0, 16)
	for _, scope := range []evidence.RetrievalScope{evidence.ScopeRestaurant, evidence.ScopeEvidence} {
		scoped, err := stores.Knowledge.ListByRestaurant(ctx, restaurantID, scope)
		if err != nil {
			return *result, err
		}
		documents = append(documents, scoped...)
	}
	// The same predicate PendingDocuments uses, and deliberately not the one
	// this function used to have. Documents are written inactive and only go
	// live once vectored, so requiring is_active selected a state the CHECK
	// constraint makes impossible — `embed --restaurant-id` was a silent
	// no-op on real data, reporting a successful run that embedded nothing.
	// E.6 is the same defect on the batch path.
	page := make([]evidence.KnowledgeDocument, 0, len(documents))
	for _, doc := range documents {
		if len(doc.Embedding) == 0 {
			page = append(page, doc)
		}
	}
	pending := make([]pendingDocument, 0, len(page))
	for _, doc := range page {
		pending = append(pending, pendingDocument{id: doc.DocumentID, content: doc.Content})
	}
	started := time.Now()
	result.Documents = len(pending)
	seen := newDuplicateSet(len(pending))
	refused, err := embedPending(ctx, stores, provider, pending, cfg, opts, result, collector, seen)
	if err != nil {
		// Activation runs before the error check, exactly as in the batch
		// path: the batches that did succeed are already durable, and a
		// vector on an inactive document is unreachable from every later run
		// (E.18).
		if !opts.DryRun {
			if activateErr := activatePage(ctx, stores, page); activateErr != nil {
				return *result, activateErr
			}
		}
		return *result, err
	}
	if !opts.DryRun {
		if err := activatePage(ctx, stores, page); err != nil {
			return *result, err
		}
	}
	_ = refused
	result.DurationMS = time.Since(started).Milliseconds()
	return *result, nil
}

// guardModelChange refuses to mix two models in one live set.
//
// Vectors from different models are not comparable: their distance means
// something different, so a mixed index returns scores that cannot be
// interpreted and nothing reports it. The check is the only thing standing
// between a configuration edit and a silently broken index.
//
// --force-model-change does not merely wave the check through. It retires the
// documents produced by the other model, because leaving them live is the
// exact failure the guard exists to prevent: the run would go on to embed new
// documents from the current model and end with both live at once.
//
// The old documents are deactivated rather than deleted or overwritten. Their
// rows keep their vectors, so a citation issued before the switch still
// resolves, and the version mechanism stays the record of what changed.
func guardModelChange(ctx context.Context, stores Stores, cfg config.Config, opts EmbedOptions) error {
	existing, err := stores.Knowledge.DistinctEmbeddingModels(ctx)
	if err != nil {
		return err
	}
	stale, err := staleModelDocuments(ctx, stores, existing, cfg, opts)
	if err != nil {
		return err
	}
	if len(stale) == 0 {
		return nil
	}
	if !opts.ForceModelChange {
		return errs.Newf(errs.CodeEmbeddingModelMismatch,
			"embed: %d stored documents were produced by model %q (%d dimensions), "+
				"but this run uses %q (%d dimensions); re-run with --force-model-change to replace them",
			len(stale), stale[0].model, stale[0].dimensions, cfg.Embedding.Model, cfg.Embedding.Dimensions)
	}
	if _, err := stores.Knowledge.DeactivateStaleModels(ctx, cfg.Embedding.Model, cfg.Embedding.Dimensions); err != nil {
		return err
	}
	slog.Info("retired documents from a superseded embedding model",
		slog.String("current_model", cfg.Embedding.Model),
		slog.Int("retired", len(stale)))
	return nil
}

// staleModel identifies one superseded model's footprint.
type staleModel struct {
	model      string
	dimensions int
	documents  int
}

// staleModelDocuments lists the live models that disagree with this run's
// configuration.
//
// The store reports every model it has recorded, not only the live ones, so a
// model whose documents have all been superseded still shows up. Those are
// skipped: retiring them again would be a no-op that reports work it did not
// do, and a mismatch against a model that has no live document is not a
// reason to refuse a run.
func staleModelDocuments(
	ctx context.Context,
	stores Stores,
	existing []port.EmbeddingModelInfo,
	cfg config.Config,
	opts EmbedOptions,
) ([]staleModel, error) {
	out := make([]staleModel, 0, len(existing))
	for _, info := range existing {
		if info.Model == cfg.Embedding.Model && info.Dimensions == cfg.Embedding.Dimensions {
			continue
		}
		if info.Documents == 0 {
			continue
		}
		out = append(out, staleModel{model: info.Model, dimensions: info.Dimensions, documents: info.Documents})
	}
	return out, nil
}

// embedBatch takes one page of pending documents and writes their vectors.
func embedBatch(
	ctx context.Context,
	stores Stores,
	provider port.EmbeddingProvider,
	pending []evidence.KnowledgeDocument,
	cfg config.Config,
	opts EmbedOptions,
	result *EmbedResult,
	collector *report.Collector,
	seen *duplicateSet,
) ([]int64, error) {
	documents := make([]pendingDocument, len(pending))
	for i, doc := range pending {
		documents[i] = pendingDocument{id: doc.DocumentID, content: doc.Content}
	}
	return embedPending(ctx, stores, provider, documents, cfg, opts, result, collector, seen)
}

// embedPending runs the provider over one page of documents in batches.
//
// Two failure classes are handled differently on purpose:
//
//   - A document whose vector fails the quality gate is rejected and the batch
//     continues. One bad document must not cost the other thirty-one.
//   - A provider failure fails the whole run. There is no partial result to
//     salvage, and writing half a page would leave the run resumable but the
//     report misleading.
func embedPending(
	ctx context.Context,
	stores Stores,
	provider port.EmbeddingProvider,
	documents []pendingDocument,
	cfg config.Config,
	opts EmbedOptions,
	result *EmbedResult,
	collector *report.Collector,
	seen *duplicateSet,
) ([]int64, error) {
	// refused collects the document ids this page rejected, so the caller can
	// stop asking for them.
	refused := make([]int64, 0, 8)
	// seen is supplied by the caller and is shared by every batch of the run,
	// including the ones running concurrently. It tracks exact duplicate
	// vectors: a repeat is almost always a provider cache fault rather than
	// genuinely identical text, and storing it would waste a row and skew the
	// counts.
	//
	// It cannot be a local. Concurrent workers each call this function, and a
	// per-call set sees only its own batch — which is exactly the case that
	// needs catching. Nine documents that all embed to the same vector were
	// written nine times, once per batch, with the report showing every batch
	// as successful.

	for start := 0; start < len(documents); start += opts.BatchSize {
		if err := ctx.Err(); err != nil {
			return refused, err
		}
		end := start + opts.BatchSize
		if end > len(documents) {
			end = len(documents)
		}
		window := documents[start:end]

		texts := make([]string, len(window))
		for i, doc := range window {
			texts[i] = doc.content
		}
		vectors, err := provider.EmbedDocuments(ctx, texts)
		if err != nil {
			result.Failed += len(window)
			return refused, err
		}

		accepted := make([]int64, 0, len(window))
		acceptedVectors := make([][]float32, 0, len(window))
		for i, vec := range vectors {
			if err := knowledge.Check(vec, cfg.Embedding.Dimensions); err != nil {
				result.Rejected++
				result.RejectReasons[string(errs.CodeOf(err))]++
				collector.RejectDocument(review.StageEmbedding, window[i].id, string(errs.CodeOf(err)))
				refused = append(refused, window[i].id)
				continue
			}
			// add is atomic, so two workers that receive the same vector at the
			// same time still produce exactly one winner.
			//
			// The reason is the errs constant rather than a literal, for the
			// same reason the quality gate's reasons are: these strings are
			// keys in the audit table's reject_reasons map, and a literal that
			// drifts from the constant would split one reason into two that
			// nothing reports together. CodeEmbeddingDuplicate otherwise had no
			// producer at all — the code existed and was unreachable.
			if !seen.add(knowledge.Fingerprint(vec)) {
				result.Rejected++
				result.RejectReasons[duplicateReason]++
				collector.RejectDocument(review.StageEmbedding, window[i].id, duplicateReason)
				refused = append(refused, window[i].id)
				continue
			}
			accepted = append(accepted, window[i].id)
			acceptedVectors = append(acceptedVectors, vec)
		}

		if len(accepted) == 0 || opts.DryRun {
			result.Embedded += len(accepted)
			continue
		}

		written, err := stores.Knowledge.SetEmbedding(
			ctx, accepted, acceptedVectors, cfg.Embedding.Model, cfg.Embedding.Dimensions)
		if err != nil {
			result.Failed += len(accepted)
			return refused, err
		}
		result.Embedded += written
		result.Skipped += len(accepted) - written

		// A vector does not make a document recallable; activation does. This is
		// the last step of the switch the plan requires, and it runs only after
		// the vector is durable, so a failure above leaves the previous version
		// live rather than leaving this group with nothing live.
		//
		// Activation is deliberately not conditional on the document being new.
		// Re-activating an already-live document is a no-op, and skipping the
		// call for existing rows would leave a re-embedded document live but
		// still carrying the superseded vector until something else touched it.
		// Activation and supersession are deliberately *not* done here. A batch
		// is not a group: several batches in one page routinely belong to the
		// same (restaurant, scope, doc_type), so a worker that deactivated the
		// rows it displaced would also deactivate its siblings' freshly written
		// documents. The switch is made once per page, after the join.
	}
	return refused, nil
}

// activatePage switches every (restaurant, scope, doc_type) group in the page
// to the documents just vectored.
//
// The order is the whole point. The new versions are already vectored and
// durable by the time this runs, so activating them cannot leave a group with
// nothing live; the superseded rows are deactivated only afterwards. Doing it
// the other way round opens a window where a failure leaves the restaurant
// unretrievable — the retrieval black hole the plan warns about.
//
// It takes the whole page rather than one batch because the unit of switching
// is the group, and a group routinely spans several batches. Deactivating per
// batch would let a worker switch off documents another worker had just
// written for the same group, which is how nine documents end up with none of
// them live.
//
// Documents the quality gate refused are not in the page's written set, so they
// are never activated: ActivateDocuments refuses a document with no vector, and
// the schema enforces the same rule.
func activatePage(ctx context.Context, stores Stores, page []evidence.KnowledgeDocument) error {
	// Only documents that actually received a vector are candidates. The
	// rejected ones stay inactive and retrievable only after a later run.
	written := make([]int64, 0, len(page))
	for _, doc := range page {
		written = append(written, doc.DocumentID)
	}
	if len(written) == 0 {
		return nil
	}
	// ActivateDocuments refuses a document with no vector, and a rejected one
	// has none, so it is filtered out by asking the store which of the page's
	// documents now carry one.
	vectored, err := stores.Knowledge.VectoredDocumentIDs(ctx, written)
	if err != nil {
		return err
	}
	if len(vectored) == 0 {
		return nil
	}
	if _, err := stores.Knowledge.ActivateDocuments(ctx, vectored, true); err != nil {
		return operationError("embed: activate documents", err)
	}
	superseded, err := stores.Knowledge.SupersededDocumentIDs(ctx, vectored)
	if err != nil {
		return err
	}
	if len(superseded) == 0 {
		return nil
	}
	if _, err := stores.Knowledge.ActivateDocuments(ctx, superseded, false); err != nil {
		return operationError("embed: deactivate superseded documents", err)
	}
	return nil
}

// writeBackEmbeddedReviewCount refreshes restaurants.embedded_review_count from
// the documents that actually carry a vector.
//
// The value is recomputed from the database rather than incremented per write
// for two reasons. An increment would drift the moment a run was interrupted
// between the vector write and the count write, and it could not tell a
// re-embedded document from a newly embedded one. Reading the current state and
// writing it back makes the column a derived value that is always consistent
// with the documents, at the cost of one aggregate query per run.
//
// Only restaurants present in the result are written. A restaurant that is
// absent has no embedded document, and writing zero for it would erase a count
// that a previous run legitimately produced for a now-inactive document set.
func writeBackEmbeddedReviewCount(ctx context.Context, stores Stores, opts EmbedOptions) error {
	counts, err := stores.Knowledge.EmbeddedReviewCounts(ctx)
	if err != nil {
		return err
	}
	restaurantID := opts.RestaurantID
	for id, count := range counts {
		// A single-restaurant run must not write every restaurant in the table:
		// the value is a whole-corpus aggregate, so writing the restaurants that
		// this run did not touch would be correct only by coincidence.
		if restaurantID > 0 && id != restaurantID {
			continue
		}
		if err := stores.Restaurants.UpdateEmbeddedReviewCount(ctx, id, count); err != nil {
			return operationError("embed: write back embedded review count", err)
		}
	}
	return nil
}
