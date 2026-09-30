package pipeline

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/zed/platepilot/data-pipeline/internal/config"
	"github.com/zed/platepilot/shared/adapter/repository/memory"
	sharedcfg "github.com/zed/platepilot/shared/config"
	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/evidence"
	"github.com/zed/platepilot/shared/domain/restaurant"
	"github.com/zed/platepilot/shared/domain/review"
	"github.com/zed/platepilot/shared/testkit"
)

func testConfig() config.Config {
	return config.Config{
		Pipeline:  config.PipelineConfig{BatchSize: 500, Workers: 4},
		Embedding: sharedcfg.EmbeddingConfig{Dimensions: 1024},
	}
}

func TestBuildDocumentsOptionsDefaults(t *testing.T) {
	opts, err := BuildDocumentsOptions(nil, testConfig())
	if err != nil {
		t.Fatalf("BuildDocumentsOptions: %v", err)
	}
	if opts.BatchSize != 500 {
		t.Errorf("batch = %d, want the configured 500", opts.BatchSize)
	}
	if opts.Scope != evidence.ScopeRestaurant {
		t.Errorf("scope = %q, want %q", opts.Scope, evidence.ScopeRestaurant)
	}
	if opts.DryRun || opts.Limit != 0 || opts.RestaurantID != 0 {
		t.Errorf("unexpected defaults: %+v", opts)
	}
}

func TestBuildDocumentsOptionsParsesFlags(t *testing.T) {
	opts, err := BuildDocumentsOptions([]string{
		"--limit=25", "--batch=10", "--restaurant-id=42",
		"--scope=evidence", "--dry-run",
	}, testConfig())
	if err != nil {
		t.Fatalf("BuildDocumentsOptions: %v", err)
	}
	if opts.Limit != 25 || opts.BatchSize != 10 || opts.RestaurantID != 42 {
		t.Errorf("numeric flags parsed wrong: %+v", opts)
	}
	if opts.Scope != evidence.ScopeEvidence {
		t.Errorf("scope = %q, want evidence", opts.Scope)
	}
	if !opts.DryRun {
		t.Error("--dry-run not parsed")
	}
}

func TestBuildDocumentsOptionsRejectsBadInput(t *testing.T) {
	cases := [][]string{
		{"--nope"},
		{"--limit=abc"},
		{"--limit="},
		{"--scope=nonsense"},
		{"--limit=-5"},
	}
	for _, args := range cases {
		if _, err := BuildDocumentsOptions(args, testConfig()); err == nil {
			t.Errorf("BuildDocumentsOptions(%v) accepted bad input", args)
		} else if errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Errorf("BuildDocumentsOptions(%v) code = %q, want invalid_argument", args, errs.CodeOf(err))
		}
	}
}

func TestEmbedOptionsDefaultsAndParsing(t *testing.T) {
	cfg := testConfig()
	opts, err := ParseEmbedOptions(nil, cfg)
	if err != nil {
		t.Fatalf("ParseEmbedOptions: %v", err)
	}
	if opts.BatchSize != sharedcfg.DefaultMaxBatch {
		t.Errorf("batch = %d, want the configured default %d", opts.BatchSize, sharedcfg.DefaultMaxBatch)
	}
	if opts.Workers != cfg.Pipeline.Workers {
		t.Errorf("workers = %d, want %d", opts.Workers, cfg.Pipeline.Workers)
	}

	opts, err = ParseEmbedOptions([]string{
		"--limit=100", "--batch=8", "--workers=2", "--force-model-change", "--dry-run",
	}, cfg)
	if err != nil {
		t.Fatalf("ParseEmbedOptions: %v", err)
	}
	if opts.Limit != 100 || opts.BatchSize != 8 || opts.Workers != 2 {
		t.Errorf("numeric flags parsed wrong: %+v", opts)
	}
	if !opts.ForceModelChange || !opts.DryRun {
		t.Errorf("boolean flags parsed wrong: %+v", opts)
	}
}

// The batch size is the knob that decides throughput, so it has to be settable
// without recompiling. This test fails if the config is ignored.
func TestEmbedOptionsBatchSizeComesFromConfig(t *testing.T) {
	cfg := testConfig()
	cfg.Embedding.MaxBatch = 64
	opts, err := ParseEmbedOptions(nil, cfg)
	if err != nil {
		t.Fatalf("ParseEmbedOptions: %v", err)
	}
	if opts.BatchSize != 64 {
		t.Errorf("batch = %d, want the configured 64", opts.BatchSize)
	}
}

// The flag has to beat the configuration, otherwise a measurement taken with
// --batch is silently ignored and the operator tunes the wrong knob.
func TestEmbedOptionsBatchFlagOverridesConfig(t *testing.T) {
	cfg := testConfig()
	cfg.Embedding.MaxBatch = 64
	opts, err := ParseEmbedOptions([]string{"--batch=8"}, cfg)
	if err != nil {
		t.Fatalf("ParseEmbedOptions: %v", err)
	}
	if opts.BatchSize != 8 {
		t.Errorf("batch = %d, want the flag value 8", opts.BatchSize)
	}
}

func TestEmbedOptionsRejectsBadInput(t *testing.T) {
	for _, args := range [][]string{{"--nope"}, {"--batch=x"}, {"--workers="}} {
		if _, err := ParseEmbedOptions(args, testConfig()); err == nil {
			t.Errorf("ParseEmbedOptions(%v) accepted bad input", args)
		}
	}
}

// A zero worker count would deadlock the run, so it is clamped rather than
// passed to the pool.
func TestEmbedOptionsClampsWorkers(t *testing.T) {
	cfg := testConfig()
	cfg.Pipeline.Workers = 0
	opts, err := ParseEmbedOptions(nil, cfg)
	if err != nil {
		t.Fatalf("ParseEmbedOptions: %v", err)
	}
	if opts.Workers != 1 {
		t.Errorf("workers = %d, want 1", opts.Workers)
	}
}

func TestSortedHelpersAreStable(t *testing.T) {
	types := SortedDocTypes(map[evidence.DocType]int{
		evidence.DocTypeRestaurantHours:      1,
		evidence.DocTypeRestaurantAttributes: 2,
	})
	if len(types) != 2 || types[0] > types[1] {
		t.Errorf("SortedDocTypes = %v, want ascending", types)
	}
	reasons := SortedRejectReasons(map[string]int{"z": 1, "a": 2})
	if len(reasons) != 2 || reasons[0] != "a" {
		t.Errorf("SortedRejectReasons = %v, want ascending", reasons)
	}
}

func TestAverageOf(t *testing.T) {
	if got := averageOf(nil); got != nil {
		t.Errorf("averageOf(nil) = %v, want nil", got)
	}
	reviews := []review.Review{
		{Rating: 5}, {Rating: 4}, {Rating: 3}, {Rating: 4},
	}
	got := averageOf(reviews)
	if got == nil || *got != 4.0 {
		t.Errorf("averageOf = %v, want 4.0", got)
	}
}

func TestOperationErrorKeepsDomainCodes(t *testing.T) {
	original := errs.New(errs.CodeEmbeddingModelMismatch, "model mismatch")
	if got := operationError("embed", original); got != original {
		t.Errorf("operationError wrapped a domain error: %v", got)
	}
	if got := operationError("embed", nil); got != nil {
		t.Errorf("operationError(nil) = %v, want nil", got)
	}
}

// The zero scope is a misconfiguration, not "build everything". Silently
// building nothing would report a successful run that wrote no documents.
func TestRunBuildDocumentsRejectsEmptyScope(t *testing.T) {
	ctx := context.Background()
	stores, _ := stageStores()
	_, err := RunBuildDocuments(ctx, stores, DocumentsOptions{RestaurantID: 1, BatchSize: 10})
	if err == nil {
		t.Fatal("want an error when no scope was selected")
	}
}

func TestRunBuildDocumentsRejectsMissingStore(t *testing.T) {
	// A nil Knowledge store would panic deep inside the loop; the run should
	// fail with a clear error instead.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := RunBuildDocuments(ctx, Stores{}, DocumentsOptions{RestaurantID: 1, BatchSize: 10})
	if err == nil {
		t.Fatal("want an error when the knowledge store is absent")
	}
}

// stageStores wires the four stores the M2 stages need.
func stageStores() (Stores, *memory.PipelineStore) {
	pipelineStore := testkit.NewPipelineStore()
	return Stores{
		Restaurants: testkit.NewRestaurantStore(),
		Reviews:     testkit.NewReviewStore(),
		Pipeline:    pipelineStore,
		Knowledge:   testkit.NewKnowledgeStore(),
	}, pipelineStore
}

// seedRestaurant stores one restaurant and returns its assigned id.
func seedRestaurant(t *testing.T, stores Stores, sourceID string) int64 {
	t.Helper()
	price := 2
	avg := 4.5
	created := time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC)
	r := restaurant.Restaurant{
		Source:         restaurant.SourceGoogleLocal2021,
		SourceRecordID: sourceID,
		Name:           "Test Diner " + sourceID,
		Address:        "7 Carmine St, New York, NY",
		CuisineTags:    []string{"diner"},
		Price:          restaurant.Price{Raw: "$$", Level: &price},
		Rating:         restaurant.Rating{SourceAvg: &avg},
		SnapshotStatus: restaurant.StatusOpen,
		ObservedAt:     created,
		CreatedAt:      created,
		UpdatedAt:      created,
	}
	if err := stores.Restaurants.UpsertRestaurant(context.Background(), r); err != nil {
		t.Fatalf("UpsertRestaurant: %v", err)
	}
	got, err := stores.Restaurants.GetBySourceRecordID(context.Background(), sourceID)
	if err != nil {
		t.Fatalf("GetBySourceRecordID: %v", err)
	}
	// SelectForDemo only returns restaurants flagged for the demo set, and a
	// freshly inserted row is not one. Without this the build stage sees an
	// empty restaurant list and reports a successful run that built nothing —
	// which is what the stage's own tests did until they checked the output
	// instead of only the audit row.
	// UpdateScores iterates the scores map, so the restaurant has to appear in
	// it or the active flag is never reached.
	if err := stores.Restaurants.UpdateScores(context.Background(),
		map[int64]float64{got.ID: 1}, map[int64]bool{got.ID: true}); err != nil {
		t.Fatalf("UpdateScores: %v", err)
	}
	return got.ID
}

func TestRunBuildDocumentsRecordsABatch(t *testing.T) {
	ctx := context.Background()
	stores, pipelineStore := stageStores()
	id := seedRestaurant(t, stores, "gmap-docs")

	result, err := RunBuildDocuments(ctx, stores, DocumentsOptions{
		RestaurantID: id, BatchSize: 10, Scope: evidence.ScopeRestaurant,
	})
	if err != nil {
		t.Fatalf("RunBuildDocuments: %v", err)
	}
	if result.Inserted == 0 {
		t.Fatalf("no documents built: %+v", result)
	}

	batches, err := pipelineStore.ListBatches(ctx, 10)
	if err != nil {
		t.Fatalf("ListBatches: %v", err)
	}
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want exactly one for the stage", len(batches))
	}
	got := batches[0]
	if got.Stage != review.StageDocuments {
		t.Errorf("stage = %q, want %q", got.Stage, review.StageDocuments)
	}
	if got.Status != review.StatusSucceeded {
		t.Errorf("status = %q, want succeeded", got.Status)
	}
	if got.DocumentsBuilt == nil || *got.DocumentsBuilt != int64(result.Inserted) {
		t.Errorf("documents_built = %v, want %d", got.DocumentsBuilt, result.Inserted)
	}
	// The document stage calls no model, so the report must not name one.
	if got.EmbeddingModel != "" || got.EmbeddingDimensions != nil {
		t.Errorf("document stage reported an embedding identity: %+v", got)
	}
}

// A failed run still writes a report. The partial count is the whole reason to
// look at a failed batch, and a report that only appears on success cannot
// answer "how far did it get".
func TestRunBuildDocumentsRecordsAFailedBatch(t *testing.T) {
	stores, pipelineStore := stageStores()
	seedRestaurant(t, stores, "gmap-fail")

	// An unknown scope reaches the store as an empty scope, which no document
	// can match, so the run fails on the upsert rather than on validation.
	_, err := RunBuildDocuments(context.Background(), stores, DocumentsOptions{
		RestaurantID: 999999, BatchSize: 10, Scope: evidence.ScopeRestaurant,
	})
	if err == nil {
		t.Fatal("want an error for a restaurant that does not exist")
	}
	batches, listErr := pipelineStore.ListBatches(context.Background(), 10)
	if listErr != nil {
		t.Fatalf("ListBatches: %v", listErr)
	}
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want the failed run to be recorded", len(batches))
	}
	if batches[0].Status != review.StatusFailed {
		t.Errorf("status = %q, want failed", batches[0].Status)
	}
}

// A dry-run must leave no trace. A "succeeded" row from a run that wrote
// nothing would be indistinguishable from a real build in the audit table.
func TestRunBuildDocumentsDryRunWritesNoBatch(t *testing.T) {
	ctx := context.Background()
	stores, pipelineStore := stageStores()
	id := seedRestaurant(t, stores, "gmap-dry")

	result, err := RunBuildDocuments(ctx, stores, DocumentsOptions{
		RestaurantID: id, BatchSize: 10, Scope: evidence.ScopeRestaurant, DryRun: true,
	})
	if err != nil {
		t.Fatalf("RunBuildDocuments: %v", err)
	}
	if result.Inserted == 0 {
		t.Errorf("dry run counted no documents: %+v", result)
	}
	batches, err := pipelineStore.ListBatches(ctx, 10)
	if err != nil {
		t.Fatalf("ListBatches: %v", err)
	}
	if len(batches) != 0 {
		t.Errorf("dry run wrote %d batch rows", len(batches))
	}
}

// seedReviews stores n text reviews for one restaurant, spread across the
// rating range so the representative selector has more than one sentiment band
// to work with. A corpus of identical five-star reviews would exercise none of
// the stratification.
func seedReviews(t *testing.T, stores Stores, restaurantID int64, n int) {
	t.Helper()
	base := time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC)
	items := make([]review.Review, 0, n)
	for i := range n {
		rating := 1 + i%5
		text := fmt.Sprintf("review number %d: the food was %s and the service was %s",
			i, []string{"terrible", "poor", "fine", "good", "excellent"}[rating-1],
			[]string{"slow", "plain", "normal", "quick", "attentive"}[rating-1])
		items = append(items, review.Review{
			RestaurantID:     restaurantID,
			Rating:           rating,
			ReviewedAt:       base.Add(time.Duration(i) * time.Hour),
			Text:             text,
			TextHash:         fmt.Sprintf("review-hash-%04d", i),
			SourceObservedAt: base,
		})
	}
	if _, err := stores.Reviews.UpsertReviews(context.Background(), items); err != nil {
		t.Fatalf("UpsertReviews: %v", err)
	}
}

// buildEvidenceDocuments is the path Gate B's "at least 5,000 evidence chunks"
// counts, and it had no test at all. The properties that matter are that it
// produces evidence documents, that it marks the reviews it quotes so the M1
// partial index stops being decorative, and that a re-run writes nothing new.
func TestBuildEvidenceDocumentsProducesChunksAndMarksReviews(t *testing.T) {
	ctx := context.Background()
	stores, _ := stageStores()
	id := seedRestaurant(t, stores, "gmap-evidence")
	seedReviews(t, stores, id, 40)

	result, err := RunBuildDocuments(ctx, stores, DocumentsOptions{
		Scope:     evidence.ScopeEvidence,
		BatchSize: 10,
	})
	if err != nil {
		t.Fatalf("RunBuildDocuments: %v", err)
	}
	if result.RepresentativeReviews == 0 {
		t.Error("no representative reviews were selected, so reviews.is_representative " +
			"stays false and representative_review_count can never be right")
	}

	stored, err := stores.Knowledge.ListByRestaurant(ctx, id, evidence.ScopeEvidence)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	if len(stored) == 0 {
		t.Fatal("no evidence documents were produced")
	}
	// Every evidence document has to be inactive and unvectored: they are
	// written before the embedding stage, and the table's CHECK constraint is
	// what enforces it.
	for _, doc := range stored {
		if len(doc.Embedding) != 0 {
			t.Errorf("document %d already carries a vector before the embed stage", doc.DocumentID)
		}
		if doc.IsActive {
			t.Errorf("document %d is active before the embed stage", doc.DocumentID)
		}
	}

	marked := 0
	reviews, err := stores.Reviews.ListByRestaurant(ctx, id, 1000)
	if err != nil {
		t.Fatalf("ListByRestaurant (reviews): %v", err)
	}
	for _, r := range reviews {
		if r.IsRepresentative {
			marked++
		}
	}
	if marked != result.RepresentativeReviews {
		t.Errorf("%d reviews carry is_representative but the run reported %d",
			marked, result.RepresentativeReviews)
	}
}

// The idempotency Gate B asks for by name: rebuilding the same restaurant must
// not add rows. Without it a re-run would double the evidence corpus and every
// count downstream would be wrong.
func TestBuildDocumentsRerunWritesNothingNew(t *testing.T) {
	ctx := context.Background()
	stores, _ := stageStores()
	id := seedRestaurant(t, stores, "gmap-rerun")
	seedReviews(t, stores, id, 30)

	first, err := RunBuildDocuments(ctx, stores, DocumentsOptions{Scope: evidence.ScopeEvidence, BatchSize: 10})
	if err != nil {
		t.Fatalf("first RunBuildDocuments: %v", err)
	}
	before, err := stores.Knowledge.ListByRestaurant(ctx, id, evidence.ScopeEvidence)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}

	second, err := RunBuildDocuments(ctx, stores, DocumentsOptions{Scope: evidence.ScopeEvidence, BatchSize: 10})
	if err != nil {
		t.Fatalf("second RunBuildDocuments: %v", err)
	}
	after, err := stores.Knowledge.ListByRestaurant(ctx, id, evidence.ScopeEvidence)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}

	if len(after) != len(before) {
		t.Errorf("document count went from %d to %d on a re-run with identical input",
			len(before), len(after))
	}
	if second.Inserted != 0 {
		t.Errorf("re-run inserted %d documents, want 0", second.Inserted)
	}
	if first.Inserted == 0 {
		t.Fatal("the first run inserted nothing, so the test proves nothing")
	}
}

// Paging must visit every selected restaurant exactly once. A page size of one
// is the case where an off-by-one in the offset either loops forever or skips a
// restaurant, and neither shows up with a page that covers everything.
func TestBuildDocumentsPagesThroughEveryRestaurant(t *testing.T) {
	ctx := context.Background()
	stores, _ := stageStores()
	ids := make([]int64, 0, 5)
	for i := range 5 {
		ids = append(ids, seedRestaurant(t, stores, fmt.Sprintf("gmap-page-%02d", i)))
	}

	result, err := RunBuildDocuments(ctx, stores, DocumentsOptions{
		Scope:     evidence.ScopeRestaurant,
		BatchSize: 1,
	})
	if err != nil {
		t.Fatalf("RunBuildDocuments: %v", err)
	}
	if result.Restaurants != len(ids) {
		t.Errorf("processed %d restaurants, want %d", result.Restaurants, len(ids))
	}
	for _, id := range ids {
		stored, err := stores.Knowledge.ListByRestaurant(ctx, id, evidence.ScopeRestaurant)
		if err != nil {
			t.Fatalf("ListByRestaurant %d: %v", id, err)
		}
		if len(stored) == 0 {
			t.Errorf("restaurant %d got no profile document", id)
		}
	}
}
