// Package contract holds the behaviour suite that every implementation of the
// write-side ports must satisfy. Running the same suite against the in-memory
// store and the Mongo adapter is what keeps the mock from drifting from Atlas.
package contract

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/restaurant"
	"github.com/zed/platepilot/shared/domain/review"
	"github.com/zed/platepilot/shared/port"
)

// Stores bundles the three write-side ports under test.
type Stores struct {
	Restaurants port.RestaurantStore
	Reviews     port.ReviewStore
	Pipeline    port.PipelineStore
}

// Factory returns a fresh, empty Stores for one subtest.
type Factory func(t *testing.T) Stores

// Run executes the full contract suite against the given factory.
func Run(t *testing.T, newStores Factory) {
	t.Helper()
	t.Run("RestaurantStore", func(t *testing.T) { runRestaurantStore(t, newStores(t).Restaurants) })
	t.Run("ReviewStore", func(t *testing.T) { runReviewStore(t, newStores(t).Reviews) })
	t.Run("PipelineStore", func(t *testing.T) { runPipelineStore(t, newStores(t).Pipeline) })
}

var baseTime = time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC)

func restaurantFixture(id, sourceRecordID, name string, createdAt time.Time) restaurant.Restaurant {
	price := 2
	avg := 4.5
	return restaurant.Restaurant{
		ID:             id,
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

	first := restaurantFixture("id-1", "gmap-1", "Joe's Pizza", baseTime)
	if err := s.UpsertRestaurant(ctx, first); err != nil {
		t.Fatalf("UpsertRestaurant: %v", err)
	}
	got, err := s.GetBySourceRecordID(ctx, "gmap-1")
	if err != nil {
		t.Fatalf("GetBySourceRecordID: %v", err)
	}
	if got.ID != "id-1" || got.Name != "Joe's Pizza" {
		t.Fatalf("stored = %+v", got)
	}
	// The source review count comes from Meta and must survive the insert.
	if got.ReviewStats.SourceReviewCount != 9998 || !got.ReviewStats.SourceReviewCountCapped {
		t.Errorf("source review stats not persisted on insert: %+v", got.ReviewStats)
	}

	// Re-import proposes a new ID and a new creation time; both must be ignored
	// so that reviews keep pointing at a stable restaurant_id.
	later := baseTime.Add(48 * time.Hour)
	update := restaurantFixture("id-2", "gmap-1", "Joe's Pizza Updated", later)
	if err := s.UpsertRestaurant(ctx, update); err != nil {
		t.Fatalf("re-UpsertRestaurant: %v", err)
	}
	got, err = s.GetBySourceRecordID(ctx, "gmap-1")
	if err != nil {
		t.Fatalf("GetBySourceRecordID after update: %v", err)
	}
	if got.ID != "id-1" {
		t.Errorf("id changed on re-import: got %q want %q", got.ID, "id-1")
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
		restaurantFixture("id-a", "gmap-a", "A", baseTime),
		restaurantFixture("id-b", "gmap-b", "B", baseTime),
	})
	if err != nil {
		t.Fatalf("UpsertRestaurants: %v", err)
	}
	if written != 2 {
		t.Errorf("UpsertRestaurants wrote %d want 2", written)
	}

	byID, err := s.GetByID(ctx, "id-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if byID.SourceRecordID != "gmap-1" {
		t.Errorf("GetByID source = %q", byID.SourceRecordID)
	}
	if _, err := s.GetByID(ctx, "missing"); !errors.Is(err, errs.ErrNotFound) {
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

	mapped, err := s.MapSourceRecordIDs(ctx, []string{"gmap-1", "gmap-a", "nope"})
	if err != nil {
		t.Fatalf("MapSourceRecordIDs: %v", err)
	}
	if mapped["gmap-1"] != "id-1" || mapped["gmap-a"] != "id-a" {
		t.Errorf("mapped = %v", mapped)
	}
	if _, ok := mapped["nope"]; ok {
		t.Errorf("unexpected mapping for missing id: %v", mapped)
	}

	stats := restaurant.ReviewStats{StoredReviewCount: 7, TextReviewCount: 5, StatsUpdatedAt: baseTime}
	computedAvg := 4.42
	computed := restaurant.Rating{ComputedAvg: &computedAvg, RatingCountForComputedAvg: 7}
	if err := s.UpdateReviewStats(ctx, "id-1", stats, computed); err != nil {
		t.Fatalf("UpdateReviewStats: %v", err)
	}
	got, _ = s.GetBySourceRecordID(ctx, "gmap-1")
	if got.ReviewStats.StoredReviewCount != 7 || got.ReviewStats.TextReviewCount != 5 {
		t.Errorf("review stats not written: %+v", got.ReviewStats)
	}
	if got.Rating.ComputedAvg == nil || *got.Rating.ComputedAvg != 4.42 || got.Rating.RatingCountForComputedAvg != 7 {
		t.Errorf("computed rating not written: %+v", got.Rating)
	}
	if err := s.UpdateReviewStats(ctx, "does-not-exist", stats, computed); !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("UpdateReviewStats missing want ErrNotFound, got %v", err)
	}

	if err := s.UpdateScores(ctx, map[string]float64{"id-1": 9.5, "id-a": 3.0}, map[string]bool{"id-1": true}); err != nil {
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
	if len(active) != 1 || active[0].ID != "id-1" {
		t.Fatalf("SelectForDemo = %+v", active)
	}
	if active[0].KnowledgeScore != 9.5 {
		t.Errorf("knowledge_score = %v want 9.5", active[0].KnowledgeScore)
	}

	// Re-running the meta import must not wipe what the stats/score jobs wrote.
	if err := s.UpsertRestaurant(ctx, restaurantFixture("id-77", "gmap-1", "Joe's Pizza Re-imported", later)); err != nil {
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

func runReviewStore(t *testing.T, s port.ReviewStore) {
	ctx := context.Background()

	items := []review.Review{
		{ID: "rv-1", RestaurantID: "r1", Rating: 5, ReviewedAt: baseTime.Add(2 * time.Hour), Text: "great", TextHash: "h1"},
		{ID: "rv-2", RestaurantID: "r1", Rating: 3, ReviewedAt: baseTime.Add(1 * time.Hour), Text: "", TextHash: "h2"},
		{ID: "rv-3", RestaurantID: "r1", Rating: 5, ReviewedAt: baseTime, Text: "ok", TextHash: "h3", IsRepresentative: true},
		{ID: "rv-9", RestaurantID: "r2", Rating: 4, ReviewedAt: baseTime, Text: "other", TextHash: "h9"},
	}
	written, err := s.UpsertReviews(ctx, items)
	if err != nil {
		t.Fatalf("UpsertReviews: %v", err)
	}
	if written != len(items) {
		t.Errorf("wrote %d want %d", written, len(items))
	}
	// Re-upsert is idempotent: same IDs, no growth.
	if _, err := s.UpsertReviews(ctx, items); err != nil {
		t.Fatalf("re-UpsertReviews: %v", err)
	}

	got, err := s.ListByRestaurant(ctx, "r1", 0)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("r1 reviews = %d want 3", len(got))
	}
	if !got[0].ReviewedAt.After(got[1].ReviewedAt) {
		t.Errorf("reviews not sorted newest first: %v", got)
	}

	counts, err := s.CountByRestaurant(ctx, "r1")
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

	agg, err := s.AggregateStats(ctx, []string{"r1", "r2", "r3"})
	if err != nil {
		t.Fatalf("AggregateStats: %v", err)
	}
	if agg["r1"].StoredCount != 3 || agg["r2"].StoredCount != 1 || agg["r3"].StoredCount != 0 {
		t.Errorf("aggregate = %+v", agg)
	}

	ids, err := s.RestaurantIDsWithReviews(ctx)
	if err != nil {
		t.Fatalf("RestaurantIDsWithReviews: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("ids with reviews = %v want 2", ids)
	}

	empty, err := s.CountByRestaurant(ctx, "nobody")
	if err != nil {
		t.Fatalf("CountByRestaurant empty: %v", err)
	}
	if empty.StoredCount != 0 || empty.ComputedAvg != nil {
		t.Errorf("empty counts = %+v", empty)
	}
}

func runPipelineStore(t *testing.T, s port.PipelineStore) {
	ctx := context.Background()

	report := review.BatchReport{BatchID: "b-1", Stage: review.StageMeta, StartedAt: baseTime, Status: review.StatusRunning}
	if err := s.StartBatch(ctx, report); err != nil {
		t.Fatalf("StartBatch: %v", err)
	}
	report.Status = review.StatusSucceeded
	report.RowsRead = 100
	report.Written = 90
	report.Rejected = 10
	report.FinishedAt = baseTime.Add(time.Minute)
	if err := s.FinishBatch(ctx, report); err != nil {
		t.Fatalf("FinishBatch: %v", err)
	}

	rejections := []review.Rejection{
		{BatchID: "b-1", Stage: review.StageMeta, LineNo: 3, Reason: "invalid coordinates"},
		{BatchID: "b-1", Stage: review.StageMeta, LineNo: 7, Reason: "missing gmap_id"},
	}
	if err := s.RecordRejections(ctx, rejections); err != nil {
		t.Fatalf("RecordRejections: %v", err)
	}

	batches, err := s.ListBatches(ctx, 10)
	if err != nil {
		t.Fatalf("ListBatches: %v", err)
	}
	if len(batches) != 1 || batches[0].BatchID != "b-1" || batches[0].Status != review.StatusSucceeded {
		t.Fatalf("batches = %+v", batches)
	}

	got, gotRej, err := s.BatchDetail(ctx, "b-1")
	if err != nil {
		t.Fatalf("BatchDetail: %v", err)
	}
	if got.Written != 90 {
		t.Errorf("written = %d want 90", got.Written)
	}
	if len(gotRej) != 2 {
		t.Errorf("rejections = %d want 2", len(gotRej))
	}

	if _, _, err := s.BatchDetail(ctx, "nope"); !errors.Is(err, errs.ErrNotFound) {
		t.Errorf("BatchDetail missing want ErrNotFound, got %v", err)
	}
}
