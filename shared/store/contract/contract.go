// Package contract holds the behaviour suite that every implementation of the
// write-side ports must satisfy. Running the same suite against the in-memory
// store and the Postgres adapter is what keeps the mock from drifting.
package contract

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/domain/restaurant"
	"github.com/zed1995/platepilot/shared/domain/review"
	"github.com/zed1995/platepilot/shared/store"
)

// Stores bundles the ports under test.
type Stores struct {
	Restaurants store.RestaurantStore
	Reviews     store.ReviewStore
	Pipeline    store.PipelineStore
	Knowledge   store.KnowledgeStore

	// M4 agent runtime ports.
	Runs          store.RunRepository
	Conversations store.ConversationRepository
	Memories      store.MemoryRepository

	// M5 mock reservation port.
	Reservations store.ReservationRepository
}

// Factory returns a fresh, empty Stores for one subtest.
type Factory func(t *testing.T) Stores

// Run executes the full contract suite against the given factory.
func Run(t *testing.T, newStores Factory) {
	t.Helper()
	t.Run("RestaurantStore", func(t *testing.T) { runRestaurantStore(t, newStores(t).Restaurants) })
	t.Run("ReviewStore", func(t *testing.T) { runReviewStore(t, newStores(t)) })
	t.Run("PipelineStore", func(t *testing.T) { runPipelineStore(t, newStores(t).Pipeline) })
	t.Run("ReviewKnowledgeMethods", func(t *testing.T) { runReviewKnowledgeMethods(t, newStores(t)) })
	t.Run("KnowledgeStore", func(t *testing.T) { runKnowledgeStore(t, newStores(t)) })
	t.Run("RunRepository", func(t *testing.T) { runRunRepositoryContract(t, newStores(t).Runs) })
	t.Run("ConversationRepository", func(t *testing.T) { runConversationRepositoryContract(t, newStores(t).Conversations) })
	t.Run("MemoryRepository", func(t *testing.T) { runMemoryRepositoryContract(t, newStores(t).Memories) })
	t.Run("ReservationRepository", func(t *testing.T) { runReservationRepositoryContract(t, newStores(t).Reservations) })
}

var baseTime = time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC)

// RestaurantFixture builds a source-derived restaurant with ID zero, for tests
// outside this package that need the same shape.
//
// It is exported so the Postgres index tests can seed through the real store
// methods with rows identical to the ones the contract exercises; a hand-rolled
// fixture could differ in a column the partial index predicates read.
func RestaurantFixture(sourceRecordID, name string, createdAt time.Time) restaurant.Restaurant {
	price := 2
	avg := 4.5
	return restaurant.Restaurant{
		Source:         restaurant.SourceGoogleLocal2021,
		SourceRecordID: sourceRecordID,
		Name:           name,
		Address:        "7 Carmine St, New York, NY",
		CuisineTags:    []string{"pizza"},
		Price:          restaurant.Price{Raw: "$$", Level: &price},
		Rating:         restaurant.Rating{SourceAvg: &avg},
		SnapshotStatus: restaurant.StatusOpen,
		ObservedAt:     baseTime,
		CreatedAt:      createdAt,
		UpdatedAt:      createdAt,
		ReviewStats: restaurant.ReviewStats{
			SourceReviewCount:       9998,
			SourceReviewCountCapped: true,
		},
	}
}

func runRestaurantStore(t *testing.T, s store.RestaurantStore) {
	ctx := context.Background()

	first := RestaurantFixture("gmap-1", "Joe's Pizza", baseTime)
	if err := s.UpsertRestaurant(ctx, first); err != nil {
		t.Fatalf("UpsertRestaurant: %v", err)
	}
	got, err := s.GetBySourceRecordID(ctx, "gmap-1")
	if err != nil {
		t.Fatalf("GetBySourceRecordID: %v", err)
	}
	// The store assigns the id; a zero id must never reach the caller.
	if got.ID == 0 || got.Name != "Joe's Pizza" {
		t.Fatalf("stored = %+v", got)
	}
	id1 := got.ID
	// The source review count comes from Meta and must survive the insert.
	if got.ReviewStats.SourceReviewCount != 9998 || !got.ReviewStats.SourceReviewCountCapped {
		t.Errorf("source review stats not persisted on insert: %+v", got.ReviewStats)
	}

	// A re-import must not mint a new id for the same place: reviews point at
	// restaurant_id, so a new value would orphan the whole review corpus. The
	// creation time is ignored for the same reason.
	later := baseTime.Add(48 * time.Hour)
	update := RestaurantFixture("gmap-1", "Joe's Pizza Updated", later)
	if err := s.UpsertRestaurant(ctx, update); err != nil {
		t.Fatalf("re-UpsertRestaurant: %v", err)
	}
	got, err = s.GetBySourceRecordID(ctx, "gmap-1")
	if err != nil {
		t.Fatalf("GetBySourceRecordID after update: %v", err)
	}
	if got.ID != id1 {
		t.Errorf("id changed on re-import: got %d want %d", got.ID, id1)
	}
	if got.Name != "Joe's Pizza Updated" {
		t.Errorf("name not updated: got %q", got.Name)
	}
	if !got.CreatedAt.Equal(baseTime) {
		t.Errorf("created_at not preserved: got %s want %s", got.CreatedAt, baseTime)
	}

	if _, err := s.GetBySourceRecordID(ctx, "missing"); !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("missing lookup want ErrNotFound, got %v", err)
	}

	written, err := s.UpsertRestaurants(ctx, []restaurant.Restaurant{
		RestaurantFixture("gmap-a", "A", baseTime),
		RestaurantFixture("gmap-b", "B", baseTime),
	})
	if err != nil {
		t.Fatalf("UpsertRestaurants: %v", err)
	}
	if written != 2 {
		t.Errorf("UpsertRestaurants wrote %d want 2", written)
	}

	byID, err := s.GetByID(ctx, id1)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if byID.SourceRecordID != "gmap-1" {
		t.Errorf("GetByID source = %q", byID.SourceRecordID)
	}
	if _, err := s.GetByID(ctx, 987654321); !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("GetByID missing want ErrNotFound, got %v", err)
	}
	listed, err := s.ListRestaurants(ctx, 0)
	if err != nil {
		t.Fatalf("ListRestaurants: %v", err)
	}
	if len(listed) != 3 {
		t.Errorf("ListRestaurants = %d want 3", len(listed))
	}
	for i := 1; i < len(listed); i++ {
		if listed[i-1].SourceRecordID > listed[i].SourceRecordID {
			t.Errorf("ListRestaurants not sorted: %v", listed)
			break
		}
	}

	// The preprocessing path depends on this returning every id, sorted, so the
	// same contract drives the Postgres projection query and the memory store.
	sourceIDs, err := s.ListSourceRecordIDs(ctx)
	if err != nil {
		t.Fatalf("ListSourceRecordIDs: %v", err)
	}
	if len(sourceIDs) != len(listed) {
		t.Fatalf("ListSourceRecordIDs = %d want %d", len(sourceIDs), len(listed))
	}
	for i, r := range listed {
		if sourceIDs[i] != r.SourceRecordID {
			t.Errorf("ListSourceRecordIDs[%d] = %q want %q", i, sourceIDs[i], r.SourceRecordID)
		}
	}

	mapped, err := s.MapSourceRecordIDs(ctx, []string{"gmap-1", "gmap-a", "nope"})
	if err != nil {
		t.Fatalf("MapSourceRecordIDs: %v", err)
	}
	if mapped["gmap-1"] != id1 || mapped["gmap-a"] == 0 {
		t.Errorf("mapped = %v", mapped)
	}
	idA := mapped["gmap-a"]
	if _, ok := mapped["nope"]; ok {
		t.Errorf("unexpected mapping for missing id: %v", mapped)
	}

	stats := restaurant.ReviewStats{StoredReviewCount: 7, TextReviewCount: 5, StatsUpdatedAt: baseTime}
	computedAvg := 4.42
	computed := restaurant.Rating{ComputedAvg: &computedAvg, RatingCountForComputedAvg: 7}
	if err := s.UpdateReviewStats(ctx, id1, stats, computed); err != nil {
		t.Fatalf("UpdateReviewStats: %v", err)
	}
	got, _ = s.GetBySourceRecordID(ctx, "gmap-1")
	if got.ReviewStats.StoredReviewCount != 7 || got.ReviewStats.TextReviewCount != 5 {
		t.Errorf("review stats not written: %+v", got.ReviewStats)
	}
	if got.Rating.ComputedAvg == nil || *got.Rating.ComputedAvg != 4.42 || got.Rating.RatingCountForComputedAvg != 7 {
		t.Errorf("computed rating not written: %+v", got.Rating)
	}
	if err := s.UpdateReviewStats(ctx, 987654321, stats, computed); !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("UpdateReviewStats missing want ErrNotFound, got %v", err)
	}

	// The narrow setter must leave the other rollup columns exactly as the wide
	// update left them. The embedding stage depends on that: it only ever knows
	// about embedded_review_count, and a setter that reset the rest would erase
	// whatever the stats stage computed.
	if err := s.UpdateEmbeddedReviewCount(ctx, id1, 4); err != nil {
		t.Fatalf("UpdateEmbeddedReviewCount: %v", err)
	}
	got, _ = s.GetBySourceRecordID(ctx, "gmap-1")
	if got.ReviewStats.EmbeddedReviewCount != 4 {
		t.Errorf("embedded_review_count = %d, want 4", got.ReviewStats.EmbeddedReviewCount)
	}
	if got.ReviewStats.StoredReviewCount != 7 || got.ReviewStats.TextReviewCount != 5 {
		t.Errorf("narrow update clobbered the other rollups: %+v", got.ReviewStats)
	}
	if got.Rating.ComputedAvg == nil || *got.Rating.ComputedAvg != 4.42 {
		t.Errorf("narrow update clobbered the computed rating: %+v", got.Rating)
	}
	if err := s.UpdateEmbeddedReviewCount(ctx, 987654321, 4); !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("UpdateEmbeddedReviewCount missing want ErrNotFound, got %v", err)
	}

	if err := s.UpdateScores(ctx, map[int64]float64{id1: 9.5, idA: 3.0}, map[int64]bool{id1: true}); err != nil {
		t.Fatalf("UpdateScores: %v", err)
	}
	n, err := s.CountActiveForDemo(ctx)
	if err != nil {
		t.Fatalf("CountActiveForDemo: %v", err)
	}
	if n != 1 {
		t.Errorf("active count = %d want 1", n)
	}
	active, err := s.SelectForDemo(ctx, 0)
	if err != nil {
		t.Fatalf("SelectForDemo: %v", err)
	}
	if len(active) != 1 || active[0].ID != id1 {
		t.Fatalf("SelectForDemo = %+v", active)
	}
	if active[0].KnowledgeScore != 9.5 {
		t.Errorf("knowledge_score = %v want 9.5", active[0].KnowledgeScore)
	}

	// Re-running the meta import must not wipe what the stats/score jobs wrote.
	if err := s.UpsertRestaurant(ctx, RestaurantFixture("gmap-1", "Joe's Pizza Re-imported", later)); err != nil {
		t.Fatalf("re-import after stats: %v", err)
	}
	got, _ = s.GetBySourceRecordID(ctx, "gmap-1")
	if got.ReviewStats.StoredReviewCount != 7 {
		t.Errorf("meta re-import wiped review_stats: %+v", got.ReviewStats)
	}
	if got.ReviewStats.SourceReviewCount != 9998 {
		t.Errorf("meta re-import dropped the source review count: %+v", got.ReviewStats)
	}
	if got.KnowledgeScore != 9.5 || !got.IsActiveForDemo {
		t.Errorf("meta re-import wiped score/flag: score=%v active=%v", got.KnowledgeScore, got.IsActiveForDemo)
	}
	if got.Rating.ComputedAvg == nil || *got.Rating.ComputedAvg != 4.42 {
		t.Errorf("meta re-import wiped computed rating: %+v", got.Rating)
	}

}

func runReviewStore(t *testing.T, stores Stores) {
	ctx := context.Background()
	s := stores.Reviews

	// Reviews reference a restaurant, so the parents must exist first. A
	// document store accepts a dangling reference silently; a relational one
	// rejects it, and that difference is exactly what the suite exists to
	// expose. Seeding the parents keeps both implementations honest about the
	// invariant the pipeline actually relies on: a review is only ever written
	// after its restaurant joined.
	seeded := map[string]int64{}
	for _, r := range []restaurant.Restaurant{
		RestaurantFixture("gmap-r1", "R1", baseTime),
		RestaurantFixture("gmap-r2", "R2", baseTime),
	} {
		if err := stores.Restaurants.UpsertRestaurant(ctx, r); err != nil {
			t.Fatalf("seed restaurant %s: %v", r.SourceRecordID, err)
		}
		stored, err := stores.Restaurants.GetBySourceRecordID(ctx, r.SourceRecordID)
		if err != nil {
			t.Fatalf("read back seeded restaurant %s: %v", r.SourceRecordID, err)
		}
		seeded[r.SourceRecordID] = stored.ID
	}
	r1, r2 := seeded["gmap-r1"], seeded["gmap-r2"]

	// Review ids are assigned by the store, so the fixtures leave them zero.
	// Idempotency now comes from the (restaurant_id, text_hash, rating,
	// reviewed_at) key, which is what the re-upsert below exercises.
	items := []review.Review{
		{RestaurantID: r1, Rating: 5, ReviewedAt: baseTime.Add(2 * time.Hour), Text: "great", TextHash: "h1"},
		{RestaurantID: r1, Rating: 3, ReviewedAt: baseTime.Add(1 * time.Hour), Text: "", TextHash: "h2"},
		{RestaurantID: r1, Rating: 5, ReviewedAt: baseTime, Text: "ok", TextHash: "h3", IsRepresentative: true},
		{RestaurantID: r2, Rating: 4, ReviewedAt: baseTime, Text: "other", TextHash: "h9"},
	}
	written, err := s.UpsertReviews(ctx, items)
	if err != nil {
		t.Fatalf("UpsertReviews: %v", err)
	}
	if written != len(items) {
		t.Errorf("wrote %d want %d", written, len(items))
	}
	// Re-upsert is idempotent: the idempotency key means the second pass updates
	// the same rows instead of inserting new ones.
	if _, err := s.UpsertReviews(ctx, items); err != nil {
		t.Fatalf("re-UpsertReviews: %v", err)
	}

	got, err := s.ListByRestaurant(ctx, r1, 0)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("r1 reviews = %d want 3", len(got))
	}
	if !got[0].ReviewedAt.After(got[1].ReviewedAt) {
		t.Errorf("reviews not sorted newest first: %v", got)
	}

	counts, err := s.CountByRestaurant(ctx, r1)
	if err != nil {
		t.Fatalf("CountByRestaurant: %v", err)
	}
	if counts.StoredCount != 3 {
		t.Errorf("stored = %d want 3", counts.StoredCount)
	}
	if counts.TextCount != 2 {
		t.Errorf("text = %d want 2", counts.TextCount)
	}
	if counts.RepresentativeCount != 1 {
		t.Errorf("representative = %d want 1", counts.RepresentativeCount)
	}
	if counts.ComputedAvg == nil || *counts.ComputedAvg < 4.33 || *counts.ComputedAvg > 4.34 {
		t.Errorf("computed avg = %v want ~4.333", counts.ComputedAvg)
	}
	if counts.LastReviewedAt == nil || !counts.LastReviewedAt.Equal(baseTime.Add(2*time.Hour)) {
		t.Errorf("last reviewed = %v", counts.LastReviewedAt)
	}
	if counts.RatingDistribution[5] != 2 || counts.RatingDistribution[3] != 1 {
		t.Errorf("rating distribution = %v", counts.RatingDistribution)
	}

	agg, err := s.AggregateStats(ctx, []int64{r1, r2, 987654321})
	if err != nil {
		t.Fatalf("AggregateStats: %v", err)
	}
	if agg[r1].StoredCount != 3 || agg[r2].StoredCount != 1 || agg[987654321].StoredCount != 0 {
		t.Errorf("aggregate = %+v", agg)
	}

	ids, err := s.RestaurantIDsWithReviews(ctx)
	if err != nil {
		t.Fatalf("RestaurantIDsWithReviews: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("ids with reviews = %v want 2", ids)
	}

	empty, err := s.CountByRestaurant(ctx, 987654321)
	if err != nil {
		t.Fatalf("CountByRestaurant empty: %v", err)
	}
	if empty.StoredCount != 0 || empty.ComputedAvg != nil {
		t.Errorf("empty counts = %+v", empty)
	}
}

func runPipelineStore(t *testing.T, s store.PipelineStore) {
	ctx := context.Background()

	// BatchID is assigned by the store, so StartBatch must hand it back: every
	// rejection recorded during the run needs it to link back to this batch.
	report := review.BatchReport{Stage: review.StageMeta, StartedAt: baseTime, Status: review.StatusRunning, BoundaryVersion: "test-boundary-v1"}
	batchID, err := s.StartBatch(ctx, report)
	if err != nil {
		t.Fatalf("StartBatch: %v", err)
	}
	if batchID == 0 {
		t.Fatalf("StartBatch returned id 0, want a real assigned id")
	}
	report.BatchID = batchID
	report.Status = review.StatusSucceeded
	report.RowsRead = 100
	report.Written = 90
	report.Rejected = 10
	report.MissingFields = []review.FieldMissing{{Field: "description", Count: 17}}
	report.FinishedAt = baseTime.Add(time.Minute)
	if err := s.FinishBatch(ctx, report); err != nil {
		t.Fatalf("FinishBatch: %v", err)
	}

	rejections := []review.Rejection{
		{BatchID: batchID, Stage: review.StageMeta, LineNo: 3, Reason: "invalid coordinates"},
		{BatchID: batchID, Stage: review.StageMeta, LineNo: 7, Reason: "missing gmap_id"},
	}
	if err := s.RecordRejections(ctx, rejections); err != nil {
		t.Fatalf("RecordRejections: %v", err)
	}

	batches, err := s.ListBatches(ctx, 10)
	if err != nil {
		t.Fatalf("ListBatches: %v", err)
	}
	if len(batches) != 1 || batches[0].BatchID != batchID || batches[0].Status != review.StatusSucceeded {
		t.Fatalf("batches = %+v", batches)
	}
	// The boundary version has to survive the round trip: it is the only record
	// of which geometry release produced the borough labels in this batch.
	if batches[0].BoundaryVersion != "test-boundary-v1" {
		t.Errorf("boundary version = %q, want %q", batches[0].BoundaryVersion, "test-boundary-v1")
	}
	// FinishBatch passes missing_fields by position among the update parameters.
	// A column dropped from the statement shifts every argument after it, so this
	// asserts the value lands in its own column and not in a neighbouring one.
	if len(batches[0].MissingFields) != 1 || batches[0].MissingFields[0].Field != "description" || batches[0].MissingFields[0].Count != 17 {
		t.Errorf("missing fields = %+v, want one description=17", batches[0].MissingFields)
	}
	if batches[0].Status != review.StatusSucceeded || batches[0].Written != 90 || batches[0].Rejected != 10 {
		t.Errorf("counters shifted into the wrong columns: %+v", batches[0])
	}

	// A stage that reports no M2 counters must read back as null, not zero: an
	// import batch that never touched a vector is not a run that embedded none.
	if batches[0].DocumentsBuilt != nil || batches[0].DocumentsEmbedded != nil ||
		batches[0].DocumentsRejected != nil || batches[0].EmbeddingModel != "" ||
		batches[0].EmbeddingDimensions != nil || batches[0].RejectReasons != nil {
		t.Errorf("M1 batch reported M2 values it never set: %+v", batches[0])
	}

	got, gotRej, err := s.BatchDetail(ctx, batchID)
	if err != nil {
		t.Fatalf("BatchDetail: %v", err)
	}
	if got.Written != 90 {
		t.Errorf("written = %d want 90", got.Written)
	}
	if len(gotRej) != 2 {
		t.Errorf("rejections = %d want 2", len(gotRej))
	}

	if _, _, err := s.BatchDetail(ctx, 987654321); !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("BatchDetail missing want ErrNotFound, got %v", err)
	}

	runEmbeddingStageReport(t, s)
}

// runEmbeddingStageReport covers the M2 batch columns end to end: the values a
// document or embedding run records have to survive the same positional update
// that the M1 columns go through, and a second run of the same stage must not
// leave the previous run's values behind.
func runEmbeddingStageReport(t *testing.T, s store.PipelineStore) {
	ctx := context.Background()

	built, embedded, rejected := int64(12), int64(9), int64(3)
	dimensions := 1024
	report := review.BatchReport{
		Stage:               review.StageEmbedding,
		StartedAt:           baseTime.Add(2 * time.Hour),
		Status:              review.StatusRunning,
		DocumentsBuilt:      &built,
		DocumentsEmbedded:   &embedded,
		DocumentsRejected:   &rejected,
		EmbeddingModel:      "test-model",
		EmbeddingDimensions: &dimensions,
		RejectReasons:       map[string]int64{"embedding_zero_vector": 2, "embedding_duplicate": 1},
	}
	batchID, err := s.StartBatch(ctx, report)
	if err != nil {
		t.Fatalf("StartBatch (embedding): %v", err)
	}
	report.BatchID = batchID
	report.Status = review.StatusSucceeded
	report.FinishedAt = baseTime.Add(2*time.Hour + 30*time.Second)
	report.DurationMS = 30_000
	if err := s.FinishBatch(ctx, report); err != nil {
		t.Fatalf("FinishBatch (embedding): %v", err)
	}

	got, _, err := s.BatchDetail(ctx, batchID)
	if err != nil {
		t.Fatalf("BatchDetail (embedding): %v", err)
	}
	if got.DocumentsBuilt == nil || *got.DocumentsBuilt != built {
		t.Errorf("documents_built = %v want %d", got.DocumentsBuilt, built)
	}
	if got.DocumentsEmbedded == nil || *got.DocumentsEmbedded != embedded {
		t.Errorf("documents_embedded = %v want %d", got.DocumentsEmbedded, embedded)
	}
	if got.DocumentsRejected == nil || *got.DocumentsRejected != rejected {
		t.Errorf("documents_rejected = %v want %d", got.DocumentsRejected, rejected)
	}
	if got.EmbeddingModel != "test-model" {
		t.Errorf("embedding_model = %q want %q", got.EmbeddingModel, "test-model")
	}
	if got.EmbeddingDimensions == nil || *got.EmbeddingDimensions != dimensions {
		t.Errorf("embedding_dimensions = %v want %d", got.EmbeddingDimensions, dimensions)
	}
	if got.RejectReasons["embedding_zero_vector"] != 2 || got.RejectReasons["embedding_duplicate"] != 1 {
		t.Errorf("reject_reasons = %+v, want zero_vector=2 duplicate=1", got.RejectReasons)
	}

	// A second stage that reports no quality counts must clear the previous
	// stage's map rather than inherit it, otherwise the report answers for a
	// check that this run never performed.
	plain := review.BatchReport{
		Stage:     review.StageDocuments,
		StartedAt: baseTime.Add(3 * time.Hour),
		Status:    review.StatusRunning,
	}
	secondID, err := s.StartBatch(ctx, plain)
	if err != nil {
		t.Fatalf("StartBatch (documents): %v", err)
	}
	plain.BatchID = secondID
	plain.Status = review.StatusSucceeded
	plain.FinishedAt = baseTime.Add(3*time.Hour + time.Second)
	if err := s.FinishBatch(ctx, plain); err != nil {
		t.Fatalf("FinishBatch (documents): %v", err)
	}
	second, _, err := s.BatchDetail(ctx, secondID)
	if err != nil {
		t.Fatalf("BatchDetail (documents): %v", err)
	}
	if second.RejectReasons != nil {
		t.Errorf("reject_reasons = %+v, want nil for a stage with no quality gate", second.RejectReasons)
	}
	if second.DocumentsBuilt != nil {
		t.Errorf("documents_built = %v, want nil for a stage that reported none", second.DocumentsBuilt)
	}
}

// runReviewKnowledgeMethods covers the M2 additions to ReviewStore: the
// representative flag and the per-topic rollups.
func runReviewKnowledgeMethods(t *testing.T, stores Stores) {
	ctx := context.Background()

	parent := RestaurantFixture("review-knowledge", "Review Knowledge", baseTime)
	if err := stores.Restaurants.UpsertRestaurant(ctx, parent); err != nil {
		t.Fatalf("UpsertRestaurant: %v", err)
	}
	created, err := stores.Restaurants.GetBySourceRecordID(ctx, parent.SourceRecordID)
	if err != nil {
		t.Fatalf("GetBySourceRecordID: %v", err)
	}
	restaurantID := created.ID

	// Five reviews with distinct text so each gets its own idempotency key.
	reviews := make([]review.Review, 0, 5)
	for i := 1; i <= 5; i++ {
		reviews = append(reviews, review.Review{
			RestaurantID: restaurantID,
			Rating:       i,
			ReviewedAt:   baseTime.Add(time.Duration(i) * time.Hour),
			Text:         "review number " + strconv.Itoa(i) + " about the food and the service",
			TextHash:     "text-hash-" + strconv.Itoa(i),
			TopicTags:    []string{"food", "service"},
		})
	}
	if _, err := stores.Reviews.UpsertReviews(ctx, reviews); err != nil {
		t.Fatalf("UpsertReviews: %v", err)
	}
	stored, err := stores.Reviews.ListByRestaurant(ctx, restaurantID, 100)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	if len(stored) != 5 {
		t.Fatalf("stored %d reviews, want 5", len(stored))
	}

	// Flagging a subset must clear the flag everywhere else for that restaurant:
	// the flag is relative to the current selection, not a permanent property.
	chosen := []int64{stored[0].ID, stored[2].ID}
	if _, err := stores.Reviews.MarkRepresentative(ctx, chosen); err != nil {
		t.Fatalf("MarkRepresentative: %v", err)
	}
	stored, _ = stores.Reviews.ListByRestaurant(ctx, restaurantID, 100)
	flagged := 0
	for _, r := range stored {
		if r.IsRepresentative {
			flagged++
		}
	}
	if flagged != 2 {
		t.Errorf("flagged %d reviews, want 2", flagged)
	}

	counts, err := stores.Reviews.CountByRestaurant(ctx, restaurantID)
	if err != nil {
		t.Fatalf("CountByRestaurant: %v", err)
	}
	if counts.RepresentativeCount != 2 {
		t.Errorf("representative count = %d, want 2", counts.RepresentativeCount)
	}

	// Re-running with a different selection must not leave the old flags behind.
	if _, err := stores.Reviews.MarkRepresentative(ctx, []int64{stored[1].ID}); err != nil {
		t.Fatalf("MarkRepresentative (second selection): %v", err)
	}
	stored, _ = stores.Reviews.ListByRestaurant(ctx, restaurantID, 100)
	flagged = 0
	for _, r := range stored {
		if r.IsRepresentative {
			flagged++
		}
	}
	if flagged != 1 {
		t.Errorf("after re-selection %d reviews are flagged, want 1", flagged)
	}

	// Summaries are keyed by (restaurant_id, topic): writing the same key twice
	// replaces rather than accumulates.
	summaries := []review.Summary{
		{RestaurantID: restaurantID, Topic: "food", Sentiment: 0.4, PositiveRatio: 0.6,
			Summary: "food was good", EvidenceCount: 5, ValidFrom: baseTime, GeneratedBy: "rules:v1"},
		{RestaurantID: restaurantID, Topic: "service", Sentiment: 0.1, PositiveRatio: 0.4,
			Summary: "service was mixed", EvidenceCount: 3, ValidFrom: baseTime, GeneratedBy: "rules:v1"},
	}
	if _, err := stores.Reviews.UpsertSummaries(ctx, summaries); err != nil {
		t.Fatalf("UpsertSummaries: %v", err)
	}
	got, err := stores.Reviews.GetSummaries(ctx, restaurantID)
	if err != nil {
		t.Fatalf("GetSummaries: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("summaries = %d, want 2", len(got))
	}
	// Ordering by topic keeps the generated document byte-stable across runs.
	if got[0].Topic != "food" || got[1].Topic != "service" {
		t.Errorf("summaries out of order: %q, %q", got[0].Topic, got[1].Topic)
	}

	summaries[0].Summary = "food was great"
	summaries[0].EvidenceCount = 9
	if _, err := stores.Reviews.UpsertSummaries(ctx, summaries[:1]); err != nil {
		t.Fatalf("UpsertSummaries (update): %v", err)
	}
	got, _ = stores.Reviews.GetSummaries(ctx, restaurantID)
	if len(got) != 2 {
		t.Errorf("re-upserting one topic changed the row count to %d", len(got))
	}
	if got[0].Summary != "food was great" || got[0].EvidenceCount != 9 {
		t.Errorf("summary was not replaced: %+v", got[0])
	}

	// A summary that has not been superseded has no end date.
	if !got[0].ValidTo.IsZero() {
		t.Errorf("valid_to = %v, want the zero time for an open-ended summary", got[0].ValidTo)
	}

	bad := []review.Summary{{RestaurantID: restaurantID, Topic: "", GeneratedBy: "rules:v1"}}
	if _, err := stores.Reviews.UpsertSummaries(ctx, bad); err == nil {
		t.Error("UpsertSummaries accepted a summary with no topic")
	}
}

// knowledgeVectorDimensions is the width declared by knowledge_documents. The
// contract suite has to use the production width because the database rejects
// any other, which is exactly the guarantee the schema is there to give.
const knowledgeVectorDimensions = 1024

// knowledgeDoc builds a document fixture. hash distinguishes versions of the
// same restaurant/scope/doc type.
func knowledgeDoc(restaurantID int64, docType evidence.DocType, scope evidence.RetrievalScope, hash, content string) evidence.KnowledgeDocument {
	return evidence.KnowledgeDocument{
		RestaurantID: restaurantID,
		Scope:        scope,
		DocType:      docType,
		Title:        "fixture",
		Content:      content,
		ContentHash:  hash,
		Metadata:     map[string]any{"source": "google_local_2021"},
		SnapshotAt:   baseTime,
		Version:      1,
	}
}

// runKnowledgeStore pins the document lifecycle both adapters must share.
//
// The rules that matter are versioning (changed text becomes a new row, never
// an overwrite), idempotency (an unchanged rebuild writes nothing), and the
// activation order that keeps a group from ever having zero live rows.
func runKnowledgeStore(t *testing.T, stores Stores) {
	ctx := context.Background()
	s := stores.Knowledge

	// knowledge_documents.restaurant_id is a foreign key, so the suite needs a
	// real parent row before it can assert anything about documents.
	parent := RestaurantFixture("contract-parent", "Contract Parent", baseTime)
	if err := stores.Restaurants.UpsertRestaurant(ctx, parent); err != nil {
		t.Fatalf("UpsertRestaurant: %v", err)
	}
	created, err := stores.Restaurants.GetBySourceRecordID(ctx, parent.SourceRecordID)
	if err != nil {
		t.Fatalf("GetBySourceRecordID: %v", err)
	}
	restaurantID := created.ID

	first := knowledgeDoc(restaurantID, evidence.DocTypeRestaurantProfile, evidence.ScopeRestaurant, "hash-1", "original text")
	result, err := s.UpsertDocuments(ctx, []evidence.KnowledgeDocument{first})
	if err != nil {
		t.Fatalf("UpsertDocuments: %v", err)
	}
	if result.Inserted != 1 || result.Skipped != 0 {
		t.Fatalf("first upsert = %+v, want inserted=1", result)
	}

	stored, err := s.ListByRestaurant(ctx, restaurantID, evidence.ScopeRestaurant)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("stored %d documents, want 1", len(stored))
	}
	if stored[0].Version != 1 {
		t.Errorf("first version = %d, want 1", stored[0].Version)
	}
	if stored[0].Content != "original text" {
		t.Errorf("content = %q, want %q", stored[0].Content, "original text")
	}
	// A document arrives without a vector, so it must not be recallable yet.
	if stored[0].IsActive {
		t.Error("a document with no vector must not start active")
	}

	// An unchanged rebuild must be a no-op. Without this, every run would
	// duplicate the corpus.
	repeat, err := s.UpsertDocuments(ctx, []evidence.KnowledgeDocument{first})
	if err != nil {
		t.Fatalf("UpsertDocuments (repeat): %v", err)
	}
	if repeat.Inserted != 0 || repeat.Skipped != 1 {
		t.Errorf("repeat upsert = %+v, want inserted=0 skipped=1", repeat)
	}
	stored, _ = s.ListByRestaurant(ctx, restaurantID, evidence.ScopeRestaurant)
	if len(stored) != 1 {
		t.Fatalf("repeat upsert changed the row count to %d", len(stored))
	}

	// Changed content becomes a new version beside the old one: a citation
	// issued against the previous text has to keep resolving.
	second := knowledgeDoc(restaurantID, evidence.DocTypeRestaurantProfile, evidence.ScopeRestaurant, "hash-2", "revised text")
	if _, err := s.UpsertDocuments(ctx, []evidence.KnowledgeDocument{second}); err != nil {
		t.Fatalf("UpsertDocuments (revised): %v", err)
	}
	stored, _ = s.ListByRestaurant(ctx, restaurantID, evidence.ScopeRestaurant)
	if len(stored) != 2 {
		t.Fatalf("revised upsert produced %d documents, want 2 (history is kept)", len(stored))
	}
	versions := map[int]string{}
	for _, doc := range stored {
		versions[doc.Version] = doc.Content
	}
	if versions[1] != "original text" || versions[2] != "revised text" {
		t.Errorf("versions = %v, want v1=original text and v2=revised text", versions)
	}

	// A second document of a different type gets its own version sequence.
	other := knowledgeDoc(restaurantID, evidence.DocTypeRestaurantHours, evidence.ScopeEvidence, "hash-3", "mon 11-22")
	if _, err := s.UpsertDocuments(ctx, []evidence.KnowledgeDocument{other}); err != nil {
		t.Fatalf("UpsertDocuments (other type): %v", err)
	}
	evidenceDocs, err := s.ListByRestaurant(ctx, restaurantID, evidence.ScopeEvidence)
	if err != nil {
		t.Fatalf("ListByRestaurant (evidence): %v", err)
	}
	if len(evidenceDocs) != 1 || evidenceDocs[0].Version != 1 {
		t.Errorf("a new doc type must start at version 1, got %+v", evidenceDocs)
	}

	// Version numbers come from all history, not just live rows: a group whose
	// versions were all deactivated must not hand out version 2 twice.
	deactivateAll := []int64{}
	for _, doc := range stored {
		deactivateAll = append(deactivateAll, doc.DocumentID)
	}
	if _, err := s.ActivateDocuments(ctx, deactivateAll, false); err != nil {
		t.Fatalf("ActivateDocuments(false): %v", err)
	}
	replay := knowledgeDoc(restaurantID, evidence.DocTypeRestaurantProfile, evidence.ScopeRestaurant, "hash-4", "third text")
	if _, err := s.UpsertDocuments(ctx, []evidence.KnowledgeDocument{replay}); err != nil {
		t.Fatalf("UpsertDocuments (after deactivate): %v", err)
	}
	stored, _ = s.ListByRestaurant(ctx, restaurantID, evidence.ScopeRestaurant)
	seen := map[int]bool{}
	for _, doc := range stored {
		if seen[doc.Version] {
			t.Errorf("version %d reused within one group", doc.Version)
		}
		seen[doc.Version] = true
	}
	if len(seen) != 3 {
		t.Errorf("versions present = %v, want three distinct", seen)
	}

	// PendingDocuments reports every document without a vector, live or not.
	//
	// The live flag cannot be part of the predicate: documents are inserted
	// inactive and only go live once vectored, so requiring is_active would
	// select a state the table's CHECK constraint makes impossible and the
	// embedding stage would never see its own work.
	pending, err := s.PendingDocuments(ctx, 100)
	if err != nil {
		t.Fatalf("PendingDocuments: %v", err)
	}
	if len(pending) == 0 {
		t.Error("pending = 0, want the unvectored documents")
	}
	for _, doc := range pending {
		if len(doc.Embedding) != 0 {
			t.Errorf("document %d reported as pending but already has a vector", doc.DocumentID)
		}
	}
	// A zero limit is "return nothing", not "return everything": the caller
	// pages with an explicit size, and a zero would look like a finished run.
	none, err := s.PendingDocuments(ctx, 0)
	if err != nil {
		t.Fatalf("PendingDocuments(0): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("PendingDocuments(0) = %d documents, want none", len(none))
	}

	// A document cannot be activated before it has a vector: the table check
	// forbids a live row with no embedding, because an unembedded document in
	// an ANN index silently degrades recall instead of erroring.
	newestID := int64(0)
	for _, doc := range stored {
		if doc.Content == "third text" {
			newestID = doc.DocumentID
		}
	}
	if newestID == 0 {
		t.Fatal("could not find the newest document")
	}
	if _, err := s.ActivateDocuments(ctx, []int64{newestID}, true); err == nil {
		t.Error("activating a document with no vector must be refused by the schema")
	}

	vec := make([]float32, knowledgeVectorDimensions)
	for i := range vec {
		vec[i] = float32(i%7) + 1
	}
	written, err := s.SetEmbedding(ctx, []int64{newestID}, [][]float32{vec}, "test-model", knowledgeVectorDimensions)
	if err != nil {
		t.Fatalf("SetEmbedding: %v", err)
	}
	if written != 1 {
		t.Errorf("SetEmbedding wrote %d rows, want 1", written)
	}

	// Only now does activation succeed, and the document becomes pending work
	// exactly until its vector lands.
	if _, err := s.ActivateDocuments(ctx, []int64{newestID}, true); err != nil {
		t.Fatalf("ActivateDocuments(true) after embedding: %v", err)
	}
	// A vectored document leaves the pending set whether or not it is live,
	// which is what makes an embedding re-run a safe no-op.
	pending, _ = s.PendingDocuments(ctx, 100)
	pendingIDs := map[int64]bool{}
	for _, doc := range pending {
		pendingIDs[doc.DocumentID] = true
	}
	if pendingIDs[newestID] {
		t.Error("a vectored document is still reported as pending")
	}

	models, err := s.DistinctEmbeddingModels(ctx)
	if err != nil {
		t.Fatalf("DistinctEmbeddingModels: %v", err)
	}
	if len(models) != 1 || models[0].Model != "test-model" ||
		models[0].Dimensions != knowledgeVectorDimensions || models[0].Documents != 1 {
		t.Errorf("models = %+v, want one test-model entry at width %d", models, knowledgeVectorDimensions)
	}

	// The dimension is validated against the caller's own configuration before
	// the database ever sees the value, so a mismatch names the model rather
	// than the driver.
	shortVec := make([]float32, knowledgeVectorDimensions-1)
	if _, err := s.SetEmbedding(ctx, []int64{newestID}, [][]float32{shortVec}, "test-model", knowledgeVectorDimensions); err == nil {
		t.Error("SetEmbedding accepted a vector of the wrong width")
	}
	if _, err := s.SetEmbedding(ctx, []int64{newestID}, [][]float32{vec}, "", knowledgeVectorDimensions); err == nil {
		t.Error("SetEmbedding accepted an empty model name")
	}
	if _, err := s.SetEmbedding(ctx, []int64{newestID}, nil, "test-model", knowledgeVectorDimensions); err == nil {
		t.Error("SetEmbedding accepted mismatched ids and vectors")
	}

	// Documents that cannot be identified are rejected rather than stored.
	bad := knowledgeDoc(0, evidence.DocTypeRestaurantProfile, evidence.ScopeRestaurant, "hash-x", "text")
	if _, err := s.UpsertDocuments(ctx, []evidence.KnowledgeDocument{bad}); err == nil {
		t.Error("UpsertDocuments accepted a document with no restaurant id")
	}
	noHash := knowledgeDoc(restaurantID, evidence.DocTypeRestaurantProfile, evidence.ScopeRestaurant, "", "text")
	if _, err := s.UpsertDocuments(ctx, []evidence.KnowledgeDocument{noHash}); err == nil {
		t.Error("UpsertDocuments accepted a document with no content hash")
	}

	t.Run("digest_source_review_ids_round_trip", func(t *testing.T) {
		digestParent := RestaurantFixture("contract-digest-parent", "Contract Digest Parent", baseTime)
		if err := stores.Restaurants.UpsertRestaurant(ctx, digestParent); err != nil {
			t.Fatalf("UpsertRestaurant: %v", err)
		}
		createdParent, err := stores.Restaurants.GetBySourceRecordID(ctx, digestParent.SourceRecordID)
		if err != nil {
			t.Fatalf("GetBySourceRecordID: %v", err)
		}

		// Provenance points at real review rows. Two texts with their own
		// idempotency keys.
		sourceReviews := []review.Review{
			{RestaurantID: createdParent.ID, Rating: 5, ReviewedAt: baseTime,
				Text: "digest source review one", TextHash: "digest-src-1"},
			{RestaurantID: createdParent.ID, Rating: 2, ReviewedAt: baseTime.Add(time.Hour),
				Text: "digest source review two", TextHash: "digest-src-2"},
		}
		if _, err := stores.Reviews.UpsertReviews(ctx, sourceReviews); err != nil {
			t.Fatalf("UpsertReviews: %v", err)
		}
		reviewRows, err := stores.Reviews.ListByRestaurant(ctx, createdParent.ID, 10)
		if err != nil {
			t.Fatalf("ListByRestaurant reviews: %v", err)
		}
		if len(reviewRows) != 2 {
			t.Fatalf("stored %d reviews, want 2", len(reviewRows))
		}
		sourceIDs := []int64{reviewRows[0].ID, reviewRows[1].ID}

		digest := knowledgeDoc(createdParent.ID, evidence.DocTypeRestaurantReviewDigest,
			evidence.ScopeRestaurant, "digest-with-sources", "digest grounded in two reviews text")
		digest.SourceReviewIDs = sourceIDs
		if _, err := s.UpsertDocuments(ctx, []evidence.KnowledgeDocument{digest}); err != nil {
			t.Fatalf("UpsertDocuments: %v", err)
		}

		back, err := s.ListByRestaurant(ctx, createdParent.ID, evidence.ScopeRestaurant)
		if err != nil {
			t.Fatalf("ListByRestaurant: %v", err)
		}
		if len(back) != 1 {
			t.Fatalf("got %d documents, want 1", len(back))
		}
		if len(back[0].SourceReviewIDs) != 2 ||
			back[0].SourceReviewIDs[0] != sourceIDs[0] ||
			back[0].SourceReviewIDs[1] != sourceIDs[1] {
			t.Fatalf("source_review_ids = %v, want %v", back[0].SourceReviewIDs, sourceIDs)
		}

		// A non-digest document round-trips an empty set rather than NULL:
		// callers must be able to scan it without a nil distinction.
		plain := knowledgeDoc(createdParent.ID, evidence.DocTypeRestaurantProfile,
			evidence.ScopeRestaurant, "plain-profile", "profile without provenance")
		if _, err := s.UpsertDocuments(ctx, []evidence.KnowledgeDocument{plain}); err != nil {
			t.Fatalf("UpsertDocuments plain: %v", err)
		}
		plainRows, err := s.ListByRestaurant(ctx, createdParent.ID, evidence.ScopeRestaurant)
		if err != nil {
			t.Fatalf("ListByRestaurant after plain: %v", err)
		}
		var foundPlain bool
		for _, row := range plainRows {
			if row.ContentHash == "plain-profile" {
				foundPlain = true
				if len(row.SourceReviewIDs) != 0 {
					t.Errorf("plain profile carried source ids %v", row.SourceReviewIDs)
				}
			}
		}
		if !foundPlain {
			t.Fatal("plain profile was not stored")
		}
	})

	runSameGroupBatch(t, stores, restaurantID)
	runEmbeddedReviewCounts(t, stores, restaurantID)
	runVectoredDocumentIDs(t, stores, restaurantID)
	runSupersession(t, stores, restaurantID)
	runModelChange(t, stores, restaurantID)
	runVectorSearch(t, stores, restaurantID)
}

// runVectoredDocumentIDs pins the filter the activation step depends on.
//
// The embedding stage must not activate a document the quality gate refused:
// it has no vector, and both this store and the table's CHECK refuse to make it
// live. The stage therefore asks which of a page's documents actually received
// one, and this is that answer.
func runVectoredDocumentIDs(t *testing.T, stores Stores, restaurantID int64) {
	ctx := context.Background()
	s := stores.Knowledge

	pending := knowledgeDoc(restaurantID, evidence.DocTypeRestaurantProfile,
		evidence.ScopeRestaurant, "hash-vec-1", "vectored")
	unvectored := knowledgeDoc(restaurantID, evidence.DocTypeRestaurantHours,
		evidence.ScopeRestaurant, "hash-vec-2", "not vectored")
	if _, err := s.UpsertDocuments(ctx, []evidence.KnowledgeDocument{pending, unvectored}); err != nil {
		t.Fatalf("UpsertDocuments: %v", err)
	}
	stored, err := s.ListByRestaurant(ctx, restaurantID, evidence.ScopeRestaurant)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	byHash := map[string]int64{}
	for _, doc := range stored {
		byHash[doc.ContentHash] = doc.DocumentID
	}
	withVector, withoutVector := byHash["hash-vec-1"], byHash["hash-vec-2"]
	if withVector == 0 || withoutVector == 0 {
		t.Fatalf("fixtures were not stored: %v", byHash)
	}

	// Nothing has a vector yet.
	got, err := s.VectoredDocumentIDs(ctx, []int64{withVector, withoutVector})
	if err != nil {
		t.Fatalf("VectoredDocumentIDs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("VectoredDocumentIDs = %v, want none before any vector is written", got)
	}

	vec := make([]float32, knowledgeVectorDimensions)
	vec[1] = 1
	if _, err := s.SetEmbedding(ctx, []int64{withVector}, [][]float32{vec}, "test-model", knowledgeVectorDimensions); err != nil {
		t.Fatalf("SetEmbedding: %v", err)
	}

	got, err = s.VectoredDocumentIDs(ctx, []int64{withVector, withoutVector})
	if err != nil {
		t.Fatalf("VectoredDocumentIDs: %v", err)
	}
	if len(got) != 1 || got[0] != withVector {
		t.Errorf("VectoredDocumentIDs = %v, want only the vectored document %d", got, withVector)
	}
	// An id the store does not know is skipped rather than reported.
	got, err = s.VectoredDocumentIDs(ctx, []int64{987654321})
	if err != nil {
		t.Fatalf("VectoredDocumentIDs (unknown id): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("VectoredDocumentIDs = %v, want none for an unknown id", got)
	}
}

// runSameGroupBatch pins the version rule for documents that arrive together.
//
// The plan states that (restaurant_id, scope, doc_type) to document is 1:N —
// one restaurant can produce several evidence chunks of the same doc type in a
// single build. That makes a batch the unit where the version number is
// assigned, and the assignment has to see the other rows of its own batch.
//
// A per-row "max(version) + 1" read from the table cannot see rows that have
// not been inserted yet, so every document in the batch would be handed the
// same number. Nothing rejects that: the column has no uniqueness constraint,
// and the documents are distinct rows with distinct content hashes, so the
// idempotency key does not catch it either. The damage surfaces later, as a
// group whose version numbers are not a sequence — which is exactly the
// condition Gate B's version-hygiene query looks for.
func runSameGroupBatch(t *testing.T, stores Stores, restaurantID int64) {
	ctx := context.Background()
	s := stores.Knowledge

	docs := []evidence.KnowledgeDocument{
		knowledgeDoc(restaurantID, evidence.DocTypeRestaurantHours, evidence.ScopeEvidence,
			"same-group-hash-1", "monday to friday, opens at seven"),
		knowledgeDoc(restaurantID, evidence.DocTypeRestaurantHours, evidence.ScopeEvidence,
			"same-group-hash-2", "saturday and sunday, opens at nine"),
		knowledgeDoc(restaurantID, evidence.DocTypeRestaurantHours, evidence.ScopeEvidence,
			"same-group-hash-3", "closed on public holidays"),
	}
	result, err := s.UpsertDocuments(ctx, docs)
	if err != nil {
		t.Fatalf("UpsertDocuments (same group batch): %v", err)
	}
	if result.Inserted != len(docs) {
		t.Fatalf("inserted %d of %d documents in one batch", result.Inserted, len(docs))
	}

	stored, err := s.ListByRestaurant(ctx, restaurantID, evidence.ScopeEvidence)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	seen := make(map[int]bool, len(docs))
	versions := make([]int, 0, len(docs))
	for _, doc := range stored {
		if doc.DocType != evidence.DocTypeRestaurantHours {
			continue
		}
		if seen[doc.Version] {
			t.Errorf("version %d was handed to two documents of the same group in one batch; "+
				"versions = %v", doc.Version, versions)
		}
		seen[doc.Version] = true
		versions = append(versions, doc.Version)
	}
	// The group may already hold documents from an earlier subtest, so the
	// count is not this batch's size. What has to hold is that the three just
	// written are present and that the whole group has no reused number.
	if len(versions) < len(docs) {
		t.Fatalf("stored %d hours documents, want at least the %d just written",
			len(versions), len(docs))
	}
	// The sequence has to start above whatever the group already used, so the
	// batch continues the history rather than restarting it.
	for _, v := range versions {
		if v < 1 {
			t.Errorf("version %d is not positive", v)
		}
	}
}

// runSupersession pins the rule that a rebuilt document retires the one it
// replaces, and only then.
//
// The failure this guards against is silent: if the old version is never
// deactivated, both versions stay live, retrieval returns whichever the index
// happens to rank first, and the citation a user sees may quote text the
// pipeline has already replaced.
func runSupersession(t *testing.T, stores Stores, restaurantID int64) {
	ctx := context.Background()
	s := stores.Knowledge

	// Two versions of the same fact, same group, different content. The group
	// is a doc type no other section of the suite uses, so the assertions below
	// are about these two rows and not about a leftover from elsewhere.
	first := knowledgeDoc(restaurantID, evidence.DocTypeRestaurantAttributes,
		evidence.ScopeEvidence, "hash-sup-v1", "open 9 to 5")
	second := knowledgeDoc(restaurantID, evidence.DocTypeRestaurantAttributes,
		evidence.ScopeEvidence, "hash-sup-v2", "open 11 to 9")
	if _, err := s.UpsertDocuments(ctx, []evidence.KnowledgeDocument{first, second}); err != nil {
		t.Fatalf("UpsertDocuments (supersession): %v", err)
	}

	stored, err := s.ListByRestaurant(ctx, restaurantID, evidence.ScopeEvidence)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	byHash := map[string]int64{}
	versionOf := map[string]int{}
	for _, doc := range stored {
		byHash[doc.ContentHash] = doc.DocumentID
		versionOf[doc.ContentHash] = doc.Version
	}
	v1, v2 := byHash["hash-sup-v1"], byHash["hash-sup-v2"]
	if v1 == 0 || v2 == 0 {
		t.Fatalf("supersession fixtures were not stored: %v", byHash)
	}
	// The two must be different versions of one group, not two groups.
	if versionOf["hash-sup-v1"] == versionOf["hash-sup-v2"] {
		t.Errorf("both versions report version %d", versionOf["hash-sup-v1"])
	}

	// Nothing is live yet, so nothing is superseded.
	superseded, err := s.SupersededDocumentIDs(ctx, []int64{v2})
	if err != nil {
		t.Fatalf("SupersededDocumentIDs: %v", err)
	}
	if len(superseded) != 0 {
		t.Errorf("superseded = %v, want none while no version is live", superseded)
	}

	// Vector only the new one, exactly as the embedding stage would.
	vec := make([]float32, knowledgeVectorDimensions)
	vec[3] = 1
	if _, err := s.SetEmbedding(ctx, []int64{v2}, [][]float32{vec}, "test-model", knowledgeVectorDimensions); err != nil {
		t.Fatalf("SetEmbedding: %v", err)
	}
	if _, err := s.ActivateDocuments(ctx, []int64{v2}, true); err != nil {
		t.Fatalf("ActivateDocuments: %v", err)
	}

	// The new version being live is not enough: the old one is only displaced
	// once its group is identified, which is what SupersededDocumentIDs answers.
	superseded, err = s.SupersededDocumentIDs(ctx, []int64{v2})
	if err != nil {
		t.Fatalf("SupersededDocumentIDs (after activation): %v", err)
	}
	// v1 has no vector and so is not live, so it is not displaced. The
	// important assertion is that v2 itself is never in its own superseded set.
	for _, id := range superseded {
		if id == v2 {
			t.Error("a document was reported as superseded by itself")
		}
	}

	// Now the realistic case: both versions are live, which is what a rebuild
	// that skipped deactivation would leave behind. The new one must displace
	// the old one. The old version needs a vector to go live at all — that is
	// the schema refusing a recallable document it cannot rank.
	oldVec := make([]float32, knowledgeVectorDimensions)
	oldVec[5] = 1
	if _, err := s.SetEmbedding(ctx, []int64{v1}, [][]float32{oldVec}, "test-model", knowledgeVectorDimensions); err != nil {
		t.Fatalf("SetEmbedding (v1): %v", err)
	}
	if _, err := s.ActivateDocuments(ctx, []int64{v1}, true); err != nil {
		t.Fatalf("ActivateDocuments (v1): %v", err)
	}
	superseded, err = s.SupersededDocumentIDs(ctx, []int64{v2})
	if err != nil {
		t.Fatalf("SupersededDocumentIDs (both live): %v", err)
	}

	// The caller never passes one row at a time. The embedding stage hands over
	// a whole page, and a page routinely holds several rows of one group — the
	// three opening-hours chunks of a restaurant, or these two versions once
	// both are pending. Everything the caller listed is being switched on, so
	// none of it may be reported as displaced; only a live row the caller did
	// *not* list is an older version left behind.
	//
	// Excluding merely "a different document_id" returned the caller's own
	// siblings here, and activatePage deactivates whatever comes back — so the
	// stage would switch on a page and immediately switch off part of it.
	wholePage, err := s.SupersededDocumentIDs(ctx, []int64{v1, v2})
	if err != nil {
		t.Fatalf("SupersededDocumentIDs (whole page): %v", err)
	}
	for _, id := range wholePage {
		if id == v1 || id == v2 {
			t.Errorf("SupersededDocumentIDs returned %d, which the caller itself listed; "+
				"a page's own rows are being switched on, not displaced", id)
		}
	}
	found := false
	for _, id := range superseded {
		if id == v1 {
			found = true
		}
		if id == v2 {
			t.Error("the new version displaced itself")
		}
	}
	if !found {
		t.Errorf("superseded = %v, want it to contain the older live version %d", superseded, v1)
	}
	if _, err := s.ActivateDocuments(ctx, superseded, false); err != nil {
		t.Fatalf("ActivateDocuments(false): %v", err)
	}

	// After the switch exactly one version of the group is live.
	stored, _ = s.ListByRestaurant(ctx, restaurantID, evidence.ScopeEvidence)
	live := 0
	for _, doc := range stored {
		if doc.DocType == evidence.DocTypeRestaurantAttributes && doc.IsActive {
			live++
			if doc.ContentHash != "hash-sup-v2" {
				t.Errorf("live version is %q, want the newer one", doc.ContentHash)
			}
		}
	}
	if live != 1 {
		t.Errorf("live versions of the group = %d, want exactly 1", live)
	}
	// Superseded rows are retained, not deleted: a citation issued against the
	// old text still has to resolve.
	if _, err := s.ListByRestaurant(ctx, restaurantID, evidence.ScopeEvidence); err != nil {
		t.Fatalf("ListByRestaurant after switch: %v", err)
	}
}

// runModelChange pins how a model switch is applied without mixing models.
//
// Two rules, both easy to get backwards. The inventory that triggers the guard
// counts only live documents, so a superseded model cannot block later runs
// forever; and retiring a model keeps its rows and their vectors, because a
// citation issued before the switch has to keep resolving.
func runModelChange(t *testing.T, stores Stores, restaurantID int64) {
	ctx := context.Background()
	s := stores.Knowledge

	first := knowledgeDoc(restaurantID, evidence.DocTypeRestaurantProfile,
		evidence.ScopeRestaurant, "hash-model-v1", "the original profile")
	if _, err := s.UpsertDocuments(ctx, []evidence.KnowledgeDocument{first}); err != nil {
		t.Fatalf("UpsertDocuments: %v", err)
	}
	stored, err := s.ListByRestaurant(ctx, restaurantID, evidence.ScopeRestaurant)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	var oldID int64
	for _, doc := range stored {
		if doc.ContentHash == "hash-model-v1" {
			oldID = doc.DocumentID
		}
	}
	if oldID == 0 {
		t.Fatal("the model-change fixture was not stored")
	}
	vec := make([]float32, knowledgeVectorDimensions)
	vec[2] = 1
	if _, err := s.SetEmbedding(ctx, []int64{oldID}, [][]float32{vec}, "old-model", knowledgeVectorDimensions); err != nil {
		t.Fatalf("SetEmbedding: %v", err)
	}
	if _, err := s.ActivateDocuments(ctx, []int64{oldID}, true); err != nil {
		t.Fatalf("ActivateDocuments: %v", err)
	}

	// The live old model is what the guard sees.
	models, err := s.DistinctEmbeddingModels(ctx)
	if err != nil {
		t.Fatalf("DistinctEmbeddingModels: %v", err)
	}
	if !hasModel(models, "old-model") {
		t.Errorf("models = %+v, want it to include the live old-model document", models)
	}

	// Retirement is global, not per restaurant: a model switch replaces the
	// whole index, not one restaurant's documents. Earlier sections left live
	// documents behind, so the expected count is every live document that
	// disagrees with the incoming model.
	models, err = s.DistinctEmbeddingModels(ctx)
	if err != nil {
		t.Fatalf("DistinctEmbeddingModels: %v", err)
	}
	wantRetired := 0
	for _, info := range models {
		if info.Model != "new-model" || info.Dimensions != knowledgeVectorDimensions {
			wantRetired += info.Documents
		}
	}
	retired, err := s.DeactivateStaleModels(ctx, "new-model", knowledgeVectorDimensions)
	if err != nil {
		t.Fatalf("DeactivateStaleModels: %v", err)
	}
	if retired != wantRetired {
		t.Errorf("retired %d documents, want %d", retired, wantRetired)
	}
	stored, _ = s.ListByRestaurant(ctx, restaurantID, evidence.ScopeRestaurant)
	for _, doc := range stored {
		if doc.DocumentID != oldID {
			continue
		}
		if doc.IsActive {
			t.Error("the retired document is still live")
		}
		if len(doc.Embedding) == 0 {
			t.Error("the retired document lost its vector")
		}
	}

	// The inventory must no longer mention it. This is the rule that stops a
	// superseded model from blocking every future run.
	models, err = s.DistinctEmbeddingModels(ctx)
	if err != nil {
		t.Fatalf("DistinctEmbeddingModels (after retire): %v", err)
	}
	if hasModel(models, "old-model") {
		t.Errorf("models = %+v, want the retired model gone from the live inventory", models)
	}

	// Retiring again is a no-op rather than an error.
	again, err := s.DeactivateStaleModels(ctx, "new-model", knowledgeVectorDimensions)
	if err != nil {
		t.Fatalf("DeactivateStaleModels (repeat): %v", err)
	}
	if again != 0 {
		t.Errorf("a repeated retirement changed %d rows, want 0", again)
	}
}

// hasModel reports whether the inventory lists the given model.
func hasModel(models []store.EmbeddingModelInfo, name string) bool {
	for _, info := range models {
		if info.Model == name {
			return true
		}
	}
	return false
}

// runVectorSearch pins the retrieval guarantees M2-07 exists to prove.
//
// The rules are: results never cross a scope boundary, inactive and unembedded
// documents never appear, the ranking is by ascending distance, and top_k is
// honoured. The scope rule is the one that matters most — a search that leaked
// a restaurant profile into evidence results would let a fact document outrank
// the quote a caller meant to retrieve, and nothing downstream could tell.
func runVectorSearch(t *testing.T, stores Stores, restaurantID int64) {
	ctx := context.Background()
	s := stores.Knowledge

	// One document per scope, with vectors pointing in clearly different
	// directions so the ranking is not a tie.
	profile := knowledgeDoc(restaurantID, evidence.DocTypeRestaurantProfile,
		evidence.ScopeRestaurant, "hash-vs-profile", "a quiet diner")
	profile.Metadata["borough"] = "manhattan"
	quote := knowledgeDoc(restaurantID, evidence.DocTypeRestaurantRepresentativeReviews,
		evidence.ScopeEvidence, "hash-vs-evidence", "the burger was excellent")
	quote.Metadata["borough"] = "brooklyn"
	if _, err := s.UpsertDocuments(ctx, []evidence.KnowledgeDocument{profile, quote}); err != nil {
		t.Fatalf("UpsertDocuments: %v", err)
	}

	// ListByRestaurant filters on one scope, so the two fixtures are read
	// separately and merged into one id lookup.
	byHash := map[string]int64{}
	for _, scope := range []evidence.RetrievalScope{evidence.ScopeRestaurant, evidence.ScopeEvidence} {
		docs, err := s.ListByRestaurant(ctx, restaurantID, scope)
		if err != nil {
			t.Fatalf("ListByRestaurant(%s): %v", scope, err)
		}
		for _, doc := range docs {
			byHash[doc.ContentHash] = doc.DocumentID
		}
	}
	if byHash["hash-vs-profile"] == 0 || byHash["hash-vs-evidence"] == 0 {
		t.Fatalf("vector search fixtures were not stored: %v", byHash)
	}
	// One vector points along the first axis, the other along the last, so a
	// query built from the first must rank the profile strictly first.
	profileVec := make([]float32, knowledgeVectorDimensions)
	profileVec[0] = 1
	quoteVec := make([]float32, knowledgeVectorDimensions)
	quoteVec[knowledgeVectorDimensions-1] = 1
	ids := []int64{byHash["hash-vs-profile"], byHash["hash-vs-evidence"]}
	if _, err := s.SetEmbedding(ctx, ids,
		[][]float32{profileVec, quoteVec}, "test-model", knowledgeVectorDimensions); err != nil {
		t.Fatalf("SetEmbedding: %v", err)
	}
	if _, err := s.ActivateDocuments(ctx, ids, true); err != nil {
		t.Fatalf("ActivateDocuments: %v", err)
	}

	// A scope is mandatory. Defaulting it would let a caller that forgot to
	// filter receive both kinds of document with no error.
	if _, err := s.VectorSearch(ctx, "", profileVec, 10, store.VectorFilter{}); err == nil {
		t.Error("VectorSearch accepted an empty scope")
	}
	if _, err := s.VectorSearch(ctx, evidence.ScopeEvidence, nil, 10, store.VectorFilter{}); err == nil {
		t.Error("VectorSearch accepted an empty query vector")
	}
	if _, err := s.VectorSearch(ctx, evidence.ScopeEvidence, profileVec, 0, store.VectorFilter{}); err == nil {
		t.Error("VectorSearch accepted a non-positive top_k")
	}

	// Earlier sections of the suite left their own documents in the same
	// restaurant, so the assertions below identify the two documents by id
	// rather than assuming the store holds nothing else.
	for _, scope := range []evidence.RetrievalScope{evidence.ScopeRestaurant, evidence.ScopeEvidence} {
		// Each scope is queried with its own document's vector, so the exact
		// match must come back with distance 0 and rank first.
		own := byHash[hashForScope(scope)]
		queryVec := profileVec
		if scope == evidence.ScopeEvidence {
			queryVec = quoteVec
		}
		hits, err := s.VectorSearch(ctx, scope, queryVec, 100, store.VectorFilter{})
		if err != nil {
			t.Fatalf("VectorSearch(%s): %v", scope, err)
		}
		seen := 0
		for _, hit := range hits {
			// The scope guarantee is the load-bearing one: nothing from the
			// other scope may appear, whatever its distance.
			if hit.Scope != scope {
				t.Errorf("scope %s returned a %s document (%d)", scope, hit.Scope, hit.DocumentID)
			}
			if !hit.IsActive {
				t.Errorf("an inactive document was returned: %d", hit.DocumentID)
			}
			if hit.DocumentID == own {
				seen++
			}
		}
		if seen != 1 {
			t.Errorf("scope %s returned its own document %d times, want once (of %d hits)",
				scope, seen, len(hits))
		}
		// A query identical to a document's own vector has cosine distance 0,
		// and nothing is closer than 0, so it must rank first in its scope.
		if len(hits) > 0 && hits[0].DocumentID != own {
			t.Errorf("scope %s ranked document %d first, want the exact match %d",
				scope, hits[0].DocumentID, own)
		}
		if len(hits) > 0 && hits[0].Distance > 1e-6 {
			t.Errorf("self-match distance = %v, want ~0", hits[0].Distance)
		}
	}

	// The borough filter has to actually filter, not merely be accepted.
	hits, err := s.VectorSearch(ctx, evidence.ScopeEvidence, profileVec, 100,
		store.VectorFilter{Borough: "manhattan"})
	if err != nil {
		t.Fatalf("VectorSearch (wrong borough): %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("a brooklyn document was returned for a manhattan query: %+v", hits)
	}
	hits, err = s.VectorSearch(ctx, evidence.ScopeEvidence, quoteVec, 100,
		store.VectorFilter{Borough: "brooklyn"})
	if err != nil {
		t.Fatalf("VectorSearch (right borough): %v", err)
	}
	if len(hits) != 1 || hits[0].DocumentID != byHash["hash-vs-evidence"] {
		t.Errorf("matching borough returned %d hits, want only the brooklyn document", len(hits))
	}

	// top_k bounds the result set.
	many, err := s.VectorSearch(ctx, evidence.ScopeRestaurant, profileVec, 1, store.VectorFilter{})
	if err != nil {
		t.Fatalf("VectorSearch (top_k=1): %v", err)
	}
	if len(many) > 1 {
		t.Errorf("top_k=1 returned %d hits", len(many))
	}

	// An inactive document must vanish from results even though it still has a
	// vector: superseded versions keep their embeddings.
	if _, err := s.ActivateDocuments(ctx, []int64{byHash["hash-vs-evidence"]}, false); err != nil {
		t.Fatalf("ActivateDocuments(false): %v", err)
	}
	hits, err = s.VectorSearch(ctx, evidence.ScopeEvidence, quoteVec, 100,
		store.VectorFilter{Borough: "brooklyn"})
	if err != nil {
		t.Fatalf("VectorSearch after deactivation: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("an inactive document was still returned: %+v", hits)
	}
}

// hashForScope is the content hash of the vector-search fixture in one scope.
func hashForScope(scope evidence.RetrievalScope) string {
	if scope == evidence.ScopeRestaurant {
		return "hash-vs-profile"
	}
	return "hash-vs-evidence"
}

// runEmbeddedReviewCounts pins the aggregation the embedding stage writes back
// into restaurants.embedded_review_count.
//
// The three cases that can silently produce a wrong number are all covered: a
// document that was never vectored, a document of the wrong type that carries a
// representative count for some other reason, and a document with no count key
// at all.
func runEmbeddedReviewCounts(t *testing.T, stores Stores, restaurantID int64) {
	ctx := context.Background()
	s := stores.Knowledge

	// Nothing embedded yet: an absent entry, not a zero. The write-back treats
	// the two differently, and a map that reported zero would let it erase a
	// count a previous run legitimately produced.
	counts, err := s.EmbeddedReviewCounts(ctx)
	if err != nil {
		t.Fatalf("EmbeddedReviewCounts: %v", err)
	}
	if _, present := counts[restaurantID]; present {
		t.Errorf("counts = %+v, want no entry before anything is embedded", counts)
	}

	// The representative document is the only one whose quoted reviews are
	// reachable, so it is the only one that carries the count.
	representative := knowledgeDoc(restaurantID,
		evidence.DocTypeRestaurantRepresentativeReviews, evidence.ScopeEvidence,
		"hash-rep", "great food")
	representative.Metadata["representative_count"] = 7
	if _, err := s.UpsertDocuments(ctx, []evidence.KnowledgeDocument{representative}); err != nil {
		t.Fatalf("UpsertDocuments (representative): %v", err)
	}
	repDocs, err := s.ListByRestaurant(ctx, restaurantID, evidence.ScopeEvidence)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	var repID int64
	for _, doc := range repDocs {
		if doc.DocType == evidence.DocTypeRestaurantRepresentativeReviews {
			repID = doc.DocumentID
		}
	}
	if repID == 0 {
		t.Fatal("representative document was not stored")
	}

	// A document with a vector but no count key must not contribute. Counting
	// it would attribute reviews it never quoted.
	counted := knowledgeDoc(restaurantID, evidence.DocTypeRestaurantReviewSummary,
		evidence.ScopeEvidence, "hash-sum", "summary text")
	if _, err := s.UpsertDocuments(ctx, []evidence.KnowledgeDocument{counted}); err != nil {
		t.Fatalf("UpsertDocuments (summary): %v", err)
	}
	sumDocs, _ := s.ListByRestaurant(ctx, restaurantID, evidence.ScopeEvidence)
	var ids []int64
	var vectors [][]float32
	for _, doc := range sumDocs {
		ids = append(ids, doc.DocumentID)
		vectors = append(vectors, unitVector(doc.DocumentID))
	}
	if _, err := s.SetEmbedding(ctx, ids, vectors, "test-model", knowledgeVectorDimensions); err != nil {
		t.Fatalf("SetEmbedding: %v", err)
	}
	// A vector alone does not make a document reachable: an inactive row is
	// excluded from every partial index, so it must not be counted either.
	if _, err := s.ActivateDocuments(ctx, ids, true); err != nil {
		t.Fatalf("ActivateDocuments: %v", err)
	}

	counts, err = s.EmbeddedReviewCounts(ctx)
	if err != nil {
		t.Fatalf("EmbeddedReviewCounts: %v", err)
	}
	if counts[restaurantID] != 7 {
		t.Errorf("counts[%d] = %d, want 7", restaurantID, counts[restaurantID])
	}
}

// unitVector builds a distinct, non-zero vector of the contract width.
func unitVector(seed int64) []float32 {
	out := make([]float32, knowledgeVectorDimensions)
	out[seed%knowledgeVectorDimensions] = 1
	return out
}
