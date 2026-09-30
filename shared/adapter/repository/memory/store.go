package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/restaurant"
	"github.com/zed/platepilot/shared/domain/review"
)

// RestaurantStore is an in-memory port.RestaurantStore. It mirrors the Postgres
// adapter's semantics (uniqueness by source_record_id, stable _id across
// re-imports) so that the same contract suite can drive both.
type RestaurantStore struct {
	mu       sync.RWMutex
	bySource map[string]restaurant.Restaurant
	byID     map[int64]string
	nextID   int64
}

// NewRestaurantStore returns an empty in-memory restaurant store.
func NewRestaurantStore() *RestaurantStore {
	return &RestaurantStore{
		bySource: make(map[string]restaurant.Restaurant),
		byID:     make(map[int64]string),
	}
}

// UpsertRestaurant inserts or replaces a restaurant keyed by source_record_id.
func (s *RestaurantStore) UpsertRestaurant(_ context.Context, r restaurant.Restaurant) error {
	if err := validateRestaurant(r); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upsertLocked(r)
	return nil
}

// UpsertRestaurants upserts a batch and returns the number of documents written.
func (s *RestaurantStore) UpsertRestaurants(_ context.Context, rs []restaurant.Restaurant) (int, error) {
	for _, r := range rs {
		if err := validateRestaurant(r); err != nil {
			return 0, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range rs {
		s.upsertLocked(r)
	}
	return len(rs), nil
}

func (s *RestaurantStore) upsertLocked(r restaurant.Restaurant) {
	now := time.Now().UTC()
	if existing, ok := s.bySource[r.SourceRecordID]; ok {
		// Preserve stable identity and creation time across re-imports, exactly
		// like an INSERT ... ON CONFLICT DO NOTHING on id and created_at.
		r.ID = existing.ID
		if !existing.CreatedAt.IsZero() {
			r.CreatedAt = existing.CreatedAt
		}
		// The sampled counts, score, and active flag are owned by the stats and
		// scoring jobs and must survive a meta re-import. The source review count
		// is owned by the meta import, so it comes from the incoming record.
		r.ReviewStats.StoredReviewCount = existing.ReviewStats.StoredReviewCount
		r.ReviewStats.TextReviewCount = existing.ReviewStats.TextReviewCount
		r.ReviewStats.RepresentativeReviewCount = existing.ReviewStats.RepresentativeReviewCount
		r.ReviewStats.EmbeddedReviewCount = existing.ReviewStats.EmbeddedReviewCount
		r.ReviewStats.LastReviewedAt = existing.ReviewStats.LastReviewedAt
		r.ReviewStats.StatsUpdatedAt = existing.ReviewStats.StatsUpdatedAt
		r.KnowledgeScore = existing.KnowledgeScore
		r.IsActiveForDemo = existing.IsActiveForDemo
		r.Rating.ComputedAvg = existing.Rating.ComputedAvg
		r.Rating.RatingCountForComputedAvg = existing.Rating.RatingCountForComputedAvg
	} else {
		// The database assigns ids from an identity column, so a re-import
		// never proposes one. The store mints them the same way: a small
		// positive counter, with the caller-supplied id ignored.
		s.nextID++
		r.ID = s.nextID
		s.byID[r.ID] = r.SourceRecordID
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	r.UpdatedAt = now
	s.bySource[r.SourceRecordID] = r
}

// GetByID returns the restaurant for an internal id or not_found.
func (s *RestaurantStore) GetByID(_ context.Context, restaurantID int64) (restaurant.Restaurant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key, ok := s.byID[restaurantID]
	if !ok {
		return restaurant.Restaurant{}, errs.Newf(errs.CodeNotFound, "restaurant %d not found", restaurantID)
	}
	return s.bySource[key], nil
}

// ListRestaurants returns restaurants ordered by source_record_id.
func (s *RestaurantStore) ListRestaurants(_ context.Context, limit int) ([]restaurant.Restaurant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]restaurant.Restaurant, 0, len(s.bySource))
	for _, r := range s.bySource {
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].SourceRecordID < out[j].SourceRecordID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ListSourceRecordIDs returns every gmap_id, sorted, mirroring the Postgres
// adapter's projection-only ordering.
func (s *RestaurantStore) ListSourceRecordIDs(_ context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.bySource))
	for id := range s.bySource {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// GetBySourceRecordID returns the restaurant for a gmap_id or not_found.
func (s *RestaurantStore) GetBySourceRecordID(_ context.Context, sourceRecordID string) (restaurant.Restaurant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.bySource[sourceRecordID]
	if !ok {
		return restaurant.Restaurant{}, errs.Newf(errs.CodeNotFound, "restaurant with source_record_id %q not found", sourceRecordID)
	}
	return r, nil
}

// MapSourceRecordIDs resolves gmap_ids to restaurant ids in bulk.
func (s *RestaurantStore) MapSourceRecordIDs(_ context.Context, sourceRecordIDs []string) (map[string]int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]int64, len(sourceRecordIDs))
	for _, sourceRecordID := range sourceRecordIDs {
		if r, ok := s.bySource[sourceRecordID]; ok {
			out[sourceRecordID] = r.ID
		}
	}
	return out, nil
}

// UpdateReviewStats writes the materialised review stats for one restaurant.
func (s *RestaurantStore) UpdateReviewStats(_ context.Context, restaurantID int64, stats restaurant.ReviewStats, computed restaurant.Rating) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, r, ok := s.sourceByIDLocked(restaurantID)
	if !ok {
		return errs.Newf(errs.CodeNotFound, "restaurant %d not found", restaurantID)
	}
	r.ReviewStats = stats
	r.Rating.ComputedAvg = computed.ComputedAvg
	r.Rating.RatingCountForComputedAvg = computed.RatingCountForComputedAvg
	r.UpdatedAt = time.Now().UTC()
	s.bySource[key] = r
	return nil
}

// UpdateEmbeddedReviewCount writes only the embedded review count, mirroring
// the narrow Postgres statement rather than the full UpdateReviewStats.
func (s *RestaurantStore) UpdateEmbeddedReviewCount(_ context.Context, restaurantID int64, count int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, r, ok := s.sourceByIDLocked(restaurantID)
	if !ok {
		return errs.Newf(errs.CodeNotFound, "restaurant %d not found", restaurantID)
	}
	r.ReviewStats.EmbeddedReviewCount = count
	r.UpdatedAt = time.Now().UTC()
	s.bySource[key] = r
	return nil
}

// UpdateScores writes knowledge_score and is_active_for_demo for a batch.
func (s *RestaurantStore) UpdateScores(_ context.Context, scores map[int64]float64, active map[int64]bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, score := range scores {
		key, r, ok := s.sourceByIDLocked(id)
		if !ok {
			continue
		}
		r.KnowledgeScore = score
		if isActive, ok := active[id]; ok {
			r.IsActiveForDemo = isActive
		}
		r.UpdatedAt = time.Now().UTC()
		s.bySource[key] = r
	}
	return nil
}

// SelectForDemo returns active restaurants ordered by score, then source id.
func (s *RestaurantStore) SelectForDemo(_ context.Context, limit int) ([]restaurant.Restaurant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]restaurant.Restaurant, 0)
	for _, r := range s.bySource {
		if r.IsActiveForDemo {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].KnowledgeScore != out[j].KnowledgeScore {
			return out[i].KnowledgeScore > out[j].KnowledgeScore
		}
		return out[i].SourceRecordID < out[j].SourceRecordID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// CountActiveForDemo reports how many restaurants are active for the demo.
func (s *RestaurantStore) CountActiveForDemo(_ context.Context) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var n int64
	for _, r := range s.bySource {
		if r.IsActiveForDemo {
			n++
		}
	}
	return n, nil
}

func (s *RestaurantStore) sourceByIDLocked(id int64) (string, restaurant.Restaurant, bool) {
	key, ok := s.byID[id]
	if !ok {
		return "", restaurant.Restaurant{}, false
	}
	return key, s.bySource[key], true
}

func validateRestaurant(r restaurant.Restaurant) error {
	if strings.TrimSpace(r.SourceRecordID) == "" {
		return errs.New(errs.CodeInvalidArgument, "source_record_id is required")
	}
	return nil
}

// ReviewStore is an in-memory port.ReviewStore.
type ReviewStore struct {
	mu   sync.RWMutex
	byID map[int64]review.Review
	// byKey maps the (restaurant_id, text_hash, rating, reviewed_at)
	// idempotency key onto the review id, mirroring the unique index the
	// Postgres schema declares. Review ids are assigned by the store, so this
	// key is the only thing that makes a re-import idempotent.
	byKey  map[string]int64
	nextID int64
	// summaries is keyed by (restaurant_id, topic), mirroring the composite
	// primary key of review_summaries.
	summaries map[summaryKey]review.Summary
}

// summaryKey identifies one topic rollup of one restaurant.
type summaryKey struct {
	restaurantID int64
	topic        string
}

// NewReviewStore returns an empty in-memory review store.
func NewReviewStore() *ReviewStore {
	return &ReviewStore{
		summaries: make(map[summaryKey]review.Summary),
		byID:      make(map[int64]review.Review),
		byKey:     make(map[string]int64),
	}
}

// UpsertReviews upserts a batch keyed by the review idempotency key.
func (s *ReviewStore) UpsertReviews(_ context.Context, items []review.Review) (int, error) {
	for _, r := range items {
		if r.RestaurantID == 0 {
			return 0, errs.New(errs.CodeInvalidArgument, "restaurant_id is required")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range items {
		key := reviewKey(r)
		if existingID, ok := s.byKey[key]; ok {
			// A re-import updates the row that already carries this review
			// instead of writing a second copy, exactly like the ON CONFLICT
			// DO UPDATE the Postgres upsert performs.
			r.ID = existingID
			s.byID[existingID] = r
			continue
		}
		s.nextID++
		r.ID = s.nextID
		s.byID[r.ID] = r
		s.byKey[key] = r.ID
	}
	return len(items), nil
}

// reviewKey renders the (restaurant_id, text_hash, rating, reviewed_at) key.
func reviewKey(r review.Review) string {
	return fmt.Sprintf("%d\x00%s\x00%d\x00%s", r.RestaurantID, r.TextHash, r.Rating, r.ReviewedAt.UTC().Format(time.RFC3339Nano))
}

// ListByRestaurant returns a restaurant's reviews, newest first.
func (s *ReviewStore) ListByRestaurant(_ context.Context, restaurantID int64, limit int) ([]review.Review, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]review.Review, 0)
	for _, r := range s.byID {
		if r.RestaurantID == restaurantID {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].ReviewedAt.Equal(out[j].ReviewedAt) {
			return out[i].ReviewedAt.After(out[j].ReviewedAt)
		}
		return out[i].ID < out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// CountByRestaurant returns the rollup for one restaurant.
func (s *ReviewStore) CountByRestaurant(_ context.Context, restaurantID int64) (review.Counts, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]review.Review, 0)
	for _, r := range s.byID {
		if r.RestaurantID == restaurantID {
			items = append(items, r)
		}
	}
	return computeCounts(items), nil
}

// AggregateStats returns rollups for the given restaurant IDs.
func (s *ReviewStore) AggregateStats(_ context.Context, restaurantIDs []int64) (map[int64]review.Counts, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	grouped := make(map[int64][]review.Review, len(restaurantIDs))
	for _, id := range restaurantIDs {
		grouped[id] = nil
	}
	for _, r := range s.byID {
		if _, ok := grouped[r.RestaurantID]; ok {
			grouped[r.RestaurantID] = append(grouped[r.RestaurantID], r)
		}
	}
	out := make(map[int64]review.Counts, len(grouped))
	for id, items := range grouped {
		out[id] = computeCounts(items)
	}
	return out, nil
}

// RestaurantIDsWithReviews returns the distinct restaurant IDs that have reviews.
func (s *ReviewStore) RestaurantIDsWithReviews(_ context.Context) ([]int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[int64]struct{})
	for _, r := range s.byID {
		seen[r.RestaurantID] = struct{}{}
	}
	out := make([]int64, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func computeCounts(items []review.Review) review.Counts {
	counts := review.Counts{RatingDistribution: make(map[int]int64)}
	if len(items) == 0 {
		return counts
	}
	var ratingSum int64
	counts.StoredCount = int64(len(items))
	for _, r := range items {
		counts.RatingDistribution[r.Rating]++
		ratingSum += int64(r.Rating)
		if strings.TrimSpace(r.Text) != "" {
			counts.TextCount++
		}
		if r.IsRepresentative {
			counts.RepresentativeCount++
		}
		if counts.LastReviewedAt == nil || r.ReviewedAt.After(*counts.LastReviewedAt) {
			t := r.ReviewedAt
			counts.LastReviewedAt = &t
		}
	}
	avg := float64(ratingSum) / float64(counts.StoredCount)
	counts.ComputedAvg = &avg
	return counts
}

// PipelineStore is an in-memory port.PipelineStore.
type PipelineStore struct {
	mu         sync.RWMutex
	batches    map[int64]review.BatchReport
	rejections map[int64][]review.Rejection
	nextID     int64
}

// NewPipelineStore returns an empty in-memory pipeline store.
func NewPipelineStore() *PipelineStore {
	return &PipelineStore{
		batches:    make(map[int64]review.BatchReport),
		rejections: make(map[int64][]review.Rejection),
	}
}

// StartBatch records a running batch report and returns its assigned id.
//
// The id comes from an identity column in Postgres, so it cannot be known
// before the insert; the caller adopts the returned value and every rejection
// of the run has to reference it.
func (s *PipelineStore) StartBatch(_ context.Context, report review.BatchReport) (int64, error) {
	if strings.TrimSpace(report.Stage) == "" {
		return 0, errs.New(errs.CodeInvalidArgument, "stage is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	report.BatchID = s.nextID
	s.batches[report.BatchID] = report
	return report.BatchID, nil
}

// FinishBatch replaces a batch report with its final state.
func (s *PipelineStore) FinishBatch(_ context.Context, report review.BatchReport) error {
	if report.BatchID == 0 {
		return errs.New(errs.CodeInvalidArgument, "batch_id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.batches[report.BatchID]; !ok {
		return errs.Newf(errs.CodeNotFound, "batch %d not found", report.BatchID)
	}
	s.batches[report.BatchID] = report
	return nil
}

// RecordRejections appends rejection records for a batch.
func (s *PipelineStore) RecordRejections(_ context.Context, items []review.Rejection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range items {
		s.rejections[item.BatchID] = append(s.rejections[item.BatchID], item)
	}
	return nil
}

// ListBatches returns recent batches, newest first.
func (s *PipelineStore) ListBatches(_ context.Context, limit int) ([]review.BatchReport, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]review.BatchReport, 0, len(s.batches))
	for _, b := range s.batches {
		out = append(out, b)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.After(out[j].StartedAt)
		}
		return out[i].BatchID > out[j].BatchID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// BatchDetail returns one batch and its rejections.
func (s *PipelineStore) BatchDetail(_ context.Context, batchID int64) (review.BatchReport, []review.Rejection, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.batches[batchID]
	if !ok {
		return review.BatchReport{}, nil, errs.Newf(errs.CodeNotFound, "batch %d not found", batchID)
	}
	return b, append([]review.Rejection(nil), s.rejections[batchID]...), nil
}

// MarkRepresentative flags the given reviews and clears the flag on every other
// review of the same restaurants, mirroring the Postgres statement group.
func (s *ReviewStore) MarkRepresentative(_ context.Context, reviewIDs []int64) (int, error) {
	if len(reviewIDs) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	selected := make(map[int64]struct{}, len(reviewIDs))
	for _, id := range reviewIDs {
		selected[id] = struct{}{}
	}
	restaurants := make(map[int64]struct{})
	for id := range selected {
		if r, ok := s.byID[id]; ok {
			restaurants[r.RestaurantID] = struct{}{}
		}
	}

	changed := 0
	for id, r := range s.byID {
		if _, inRestaurant := restaurants[r.RestaurantID]; !inRestaurant {
			continue
		}
		_, want := selected[id]
		if r.IsRepresentative != want {
			r.IsRepresentative = want
			s.byID[id] = r
			changed++
		}
	}
	return changed, nil
}

// UpsertSummaries writes per-topic rollups keyed by (restaurant_id, topic).
func (s *ReviewStore) UpsertSummaries(_ context.Context, items []review.Summary) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}
	for i, item := range items {
		if item.RestaurantID <= 0 {
			return 0, errs.Newf(errs.CodeInvalidArgument,
				"memory: summary %d has no restaurant id", i)
		}
		if strings.TrimSpace(item.Topic) == "" {
			return 0, errs.Newf(errs.CodeInvalidArgument,
				"memory: summary %d has no topic", i)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, item := range items {
		s.summaries[summaryKey{item.RestaurantID, item.Topic}] = item
	}
	return len(items), nil
}

// GetSummaries returns one restaurant's rollups, ordered by topic so the
// generated documents are byte-stable across runs.
func (s *ReviewStore) GetSummaries(_ context.Context, restaurantID int64) ([]review.Summary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]review.Summary, 0, len(s.summaries))
	for key, item := range s.summaries {
		if key.restaurantID == restaurantID {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Topic < out[j].Topic })
	return out, nil
}
