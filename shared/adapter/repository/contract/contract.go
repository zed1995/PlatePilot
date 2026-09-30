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

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/evidence"
	"github.com/zed/platepilot/shared/domain/restaurant"
	"github.com/zed/platepilot/shared/domain/review"
	"github.com/zed/platepilot/shared/port"
)

// Stores bundles the three write-side ports under test.
type Stores struct {
	Restaurants port.RestaurantStore
	Reviews     port.ReviewStore
	Pipeline    port.PipelineStore
	Knowledge   port.KnowledgeStore
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
}

var baseTime = time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC)

// restaurantFixture builds a source-derived restaurant. It deliberately leaves
// ID zero: the store assigns it, exactly as the database identity column does.
func restaurantFixture(sourceRecordID, name string, createdAt time.Time) restaurant.Restaurant {
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

func runRestaurantStore(t *testing.T, s port.RestaurantStore) {
	ctx := context.Background()

	first := restaurantFixture("gmap-1", "Joe's Pizza", baseTime)
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
	update := restaurantFixture("gmap-1", "Joe's Pizza Updated", later)
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
		restaurantFixture("gmap-a", "A", baseTime),
		restaurantFixture("gmap-b", "B", baseTime),
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
	if err := s.UpsertRestaurant(ctx, restaurantFixture("gmap-1", "Joe's Pizza Re-imported", later)); err != nil {
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
		restaurantFixture("gmap-r1", "R1", baseTime),
		restaurantFixture("gmap-r2", "R2", baseTime),
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

func runPipelineStore(t *testing.T, s port.PipelineStore) {
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
}

// runReviewKnowledgeMethods covers the M2 additions to ReviewStore: the
// representative flag and the per-topic rollups.
func runReviewKnowledgeMethods(t *testing.T, stores Stores) {
	ctx := context.Background()

	parent := restaurantFixture("review-knowledge", "Review Knowledge", baseTime)
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
	parent := restaurantFixture("contract-parent", "Contract Parent", baseTime)
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

	// PendingDocuments only reports live documents without a vector, which is
	// what makes an embedding re-run a safe no-op.
	pending, err := s.PendingDocuments(ctx, 100)
	if err != nil {
		t.Fatalf("PendingDocuments: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("pending = %d, want 0 (nothing is active yet)", len(pending))
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
	pending, _ = s.PendingDocuments(ctx, 100)
	if len(pending) != 0 {
		t.Errorf("pending after embedding = %d, want 0", len(pending))
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
}
