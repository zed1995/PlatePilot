package memory

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/restaurant"
	"github.com/zed/platepilot/shared/domain/review"
)

// RestaurantStore is an in-memory port.RestaurantStore. It mirrors the Mongo
// adapter's semantics (uniqueness by source_record_id, stable _id across
// re-imports) so that the same contract suite can drive both.
type RestaurantStore struct {
	mu       sync.RWMutex
	bySource map[string]restaurant.Restaurant
	docs     map[string]restaurant.Document
}

// NewRestaurantStore returns an empty in-memory restaurant store.
func NewRestaurantStore() *RestaurantStore {
	return &RestaurantStore{
		bySource: make(map[string]restaurant.Restaurant),
		docs:     make(map[string]restaurant.Document),
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
		// like a Mongo $setOnInsert on _id and created_at.
		r.ID = existing.ID
		if !existing.CreatedAt.IsZero() {
			r.CreatedAt = existing.CreatedAt
		}
		// review_stats, knowledge_score and is_active_for_demo are owned by the
		// stats and scoring jobs, so a meta re-import must not reset them.
		r.ReviewStats = existing.ReviewStats
		r.KnowledgeScore = existing.KnowledgeScore
		r.IsActiveForDemo = existing.IsActiveForDemo
		r.Rating.ComputedAvg = existing.Rating.ComputedAvg
		r.Rating.RatingCountForComputedAvg = existing.Rating.RatingCountForComputedAvg
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	r.UpdatedAt = now
	s.bySource[r.SourceRecordID] = r
}

// GetByID returns the restaurant for an internal id or not_found.
func (s *RestaurantStore) GetByID(_ context.Context, restaurantID string) (restaurant.Restaurant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, r, ok := s.sourceByIDLocked(restaurantID); ok {
		return r, nil
	}
	return restaurant.Restaurant{}, errs.Newf(errs.CodeNotFound, "restaurant %q not found", restaurantID)
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
func (s *RestaurantStore) MapSourceRecordIDs(_ context.Context, sourceRecordIDs []string) (map[string]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(sourceRecordIDs))
	for _, sourceRecordID := range sourceRecordIDs {
		if r, ok := s.bySource[sourceRecordID]; ok {
			out[sourceRecordID] = r.ID
		}
	}
	return out, nil
}

// UpdateReviewStats writes the materialised review stats for one restaurant.
func (s *RestaurantStore) UpdateReviewStats(_ context.Context, restaurantID string, stats restaurant.ReviewStats, computed restaurant.Rating) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, r, ok := s.sourceByIDLocked(restaurantID)
	if !ok {
		return errs.Newf(errs.CodeNotFound, "restaurant %q not found", restaurantID)
	}
	r.ReviewStats = stats
	r.Rating.ComputedAvg = computed.ComputedAvg
	r.Rating.RatingCountForComputedAvg = computed.RatingCountForComputedAvg
	r.UpdatedAt = time.Now().UTC()
	s.bySource[key] = r
	return nil
}

// UpdateScores writes knowledge_score and is_active_for_demo for a batch.
func (s *RestaurantStore) UpdateScores(_ context.Context, scores map[string]float64, active map[string]bool) error {
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

// UpsertDocuments inserts or replaces auxiliary restaurant documents.
func (s *RestaurantStore) UpsertDocuments(_ context.Context, docs []restaurant.Document) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range docs {
		if strings.TrimSpace(d.RestaurantID) == "" || strings.TrimSpace(string(d.DocumentType)) == "" {
			return errs.New(errs.CodeInvalidArgument, "restaurant document requires restaurant_id and document_type")
		}
		s.docs[d.RestaurantID+"|"+string(d.DocumentType)] = d
	}
	return nil
}

// Documents returns the stored auxiliary documents for a restaurant. It is a
// test helper, not part of the port.
func (s *RestaurantStore) Documents(restaurantID string) []restaurant.Document {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]restaurant.Document, 0)
	for _, d := range s.docs {
		if d.RestaurantID == restaurantID {
			out = append(out, d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].DocumentType < out[j].DocumentType })
	return out
}

func (s *RestaurantStore) sourceByIDLocked(id string) (string, restaurant.Restaurant, bool) {
	for key, r := range s.bySource {
		if r.ID == id {
			return key, r, true
		}
	}
	return "", restaurant.Restaurant{}, false
}

func validateRestaurant(r restaurant.Restaurant) error {
	if strings.TrimSpace(r.ID) == "" {
		return errs.New(errs.CodeInvalidArgument, "restaurant_id is required")
	}
	if strings.TrimSpace(r.SourceRecordID) == "" {
		return errs.New(errs.CodeInvalidArgument, "source_record_id is required")
	}
	return nil
}

// ReviewStore is an in-memory port.ReviewStore.
type ReviewStore struct {
	mu   sync.RWMutex
	byID map[string]review.Review
}

// NewReviewStore returns an empty in-memory review store.
func NewReviewStore() *ReviewStore {
	return &ReviewStore{byID: make(map[string]review.Review)}
}

// UpsertReviews upserts a batch keyed by review ID.
func (s *ReviewStore) UpsertReviews(_ context.Context, items []review.Review) (int, error) {
	for _, r := range items {
		if strings.TrimSpace(r.ID) == "" {
			return 0, errs.New(errs.CodeInvalidArgument, "review_id is required")
		}
		if strings.TrimSpace(r.RestaurantID) == "" {
			return 0, errs.New(errs.CodeInvalidArgument, "restaurant_id is required")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range items {
		s.byID[r.ID] = r
	}
	return len(items), nil
}

// ListByRestaurant returns a restaurant's reviews, newest first.
func (s *ReviewStore) ListByRestaurant(_ context.Context, restaurantID string, limit int) ([]review.Review, error) {
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
func (s *ReviewStore) CountByRestaurant(_ context.Context, restaurantID string) (review.Counts, error) {
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
func (s *ReviewStore) AggregateStats(_ context.Context, restaurantIDs []string) (map[string]review.Counts, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	grouped := make(map[string][]review.Review, len(restaurantIDs))
	for _, id := range restaurantIDs {
		grouped[id] = nil
	}
	for _, r := range s.byID {
		if _, ok := grouped[r.RestaurantID]; ok {
			grouped[r.RestaurantID] = append(grouped[r.RestaurantID], r)
		}
	}
	out := make(map[string]review.Counts, len(grouped))
	for id, items := range grouped {
		out[id] = computeCounts(items)
	}
	return out, nil
}

// RestaurantIDsWithReviews returns the distinct restaurant IDs that have reviews.
func (s *ReviewStore) RestaurantIDsWithReviews(_ context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[string]struct{})
	for _, r := range s.byID {
		seen[r.RestaurantID] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
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
	batches    map[string]review.BatchReport
	rejections map[string][]review.Rejection
}

// NewPipelineStore returns an empty in-memory pipeline store.
func NewPipelineStore() *PipelineStore {
	return &PipelineStore{
		batches:    make(map[string]review.BatchReport),
		rejections: make(map[string][]review.Rejection),
	}
}

// StartBatch records a running batch report.
func (s *PipelineStore) StartBatch(_ context.Context, report review.BatchReport) error {
	if strings.TrimSpace(report.BatchID) == "" {
		return errs.New(errs.CodeInvalidArgument, "batch_id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches[report.BatchID] = report
	return nil
}

// FinishBatch replaces a batch report with its final state.
func (s *PipelineStore) FinishBatch(_ context.Context, report review.BatchReport) error {
	if strings.TrimSpace(report.BatchID) == "" {
		return errs.New(errs.CodeInvalidArgument, "batch_id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
func (s *PipelineStore) BatchDetail(_ context.Context, batchID string) (review.BatchReport, []review.Rejection, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.batches[batchID]
	if !ok {
		return review.BatchReport{}, nil, errs.Newf(errs.CodeNotFound, "batch %q not found", batchID)
	}
	return b, append([]review.Rejection(nil), s.rejections[batchID]...), nil
}
