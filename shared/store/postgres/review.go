package postgres

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/review"
)

// ReviewStore is the PostgreSQL implementation of store.ReviewStore.
type ReviewStore struct {
	client *Client
}

// NewReviewStore returns a Postgres-backed review store.
func NewReviewStore(client *Client) *ReviewStore {
	return &ReviewStore{client: client}
}

// reviewUpsertSQL writes a review, letting the database assign the id.
//
// Idempotency comes from the (restaurant_id, text_hash, rating, reviewed_at)
// unique key rather than from a hash-derived id, because id is now an identity
// column. Leaving id out of the column list is what makes a re-import update
// the existing row instead of inserting a second copy of the same review.
const reviewUpsertSQL = `
INSERT INTO reviews (
	restaurant_id, rating, reviewed_at, text, language, text_hash,
	is_representative, topic_tags, source_observed_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (restaurant_id, text_hash, rating, reviewed_at) DO UPDATE SET
	rating            = EXCLUDED.rating,
	reviewed_at       = EXCLUDED.reviewed_at,
	text              = EXCLUDED.text,
	language          = EXCLUDED.language,
	text_hash         = EXCLUDED.text_hash,
	is_representative = EXCLUDED.is_representative,
	topic_tags        = EXCLUDED.topic_tags
RETURNING id`

func reviewUpsertArgs(r review.Review) []any {
	tags := r.TopicTags
	if tags == nil {
		tags = []string{}
	}
	return []any{
		r.RestaurantID, int16(r.Rating), r.ReviewedAt, r.Text,
		nullString(r.Language), r.TextHash, r.IsRepresentative, tags, r.SourceObservedAt,
	}
}

// UpsertReviews upserts a batch and returns the number of rows written.
//
// Reviews are the highest-volume table in the corpus, so the write is a single
// pipelined batch rather than a transaction: one round trip, and Postgres does
// not need the durability boundary that a remote cluster did.
func (s *ReviewStore) UpsertReviews(ctx context.Context, items []review.Review) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	batch := pgx.Batch{}
	for _, item := range items {
		batch.Queue(reviewUpsertSQL, reviewUpsertArgs(item)...)
	}
	results := s.client.pool.SendBatch(ctx, &batch)
	defer results.Close()
	written := 0
	for range items {
		if _, err := results.Exec(); err != nil {
			return written, operationError("postgres: bulk upsert reviews", err)
		}
		written++
	}
	return written, nil
}

// scanReviews reads rows into domain values.
func (s *ReviewStore) scanReviews(rows pgx.Rows) ([]review.Review, error) {
	defer rows.Close()
	out := make([]review.Review, 0)
	for rows.Next() {
		var row reviewRow
		if err := rows.Scan(
			&row.ID, &row.RestaurantID, &row.Rating, &row.ReviewedAt, &row.Text,
			&row.Language, &row.TextHash, &row.IsRepresentative, &row.TopicTags,
			&row.SourceObservedAt,
		); err != nil {
			return nil, operationError("postgres: scan review", err)
		}
		out = append(out, row.toDomain())
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate reviews", err)
	}
	return out, nil
}

// ListByRestaurant returns a restaurant's reviews, newest first.
func (s *ReviewStore) ListByRestaurant(ctx context.Context, restaurantID int64, limit int) ([]review.Review, error) {
	query := "SELECT " + reviewColumns + " FROM reviews WHERE restaurant_id = $1 ORDER BY reviewed_at DESC"
	args := []any{restaurantID}
	if limit > 0 {
		query += " LIMIT $2"
		args = append(args, limit)
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	rows, err := s.client.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, operationError("postgres: list reviews", err)
	}
	return s.scanReviews(rows)
}

// countsSelect is the rollup behind CountByRestaurant and AggregateStats.
//
// The counts are computed in SQL rather than in Go because the stats stage
// rolls up millions of rows per restaurant; doing it in the database is what
// makes the stage a single pass instead of a full table transfer.
//
// text_count counts non-empty text, which is the distinction the knowledge base
// cares about: a rating-only review is kept for the rating sample but is not
// evidence.
const countsSelect = `
	restaurant_id,
	count(*)::bigint                                            AS stored_count,
	count(*) FILTER (WHERE text <> '')::bigint                  AS text_count,
	count(*) FILTER (WHERE is_representative)::bigint           AS representative_count,
	max(reviewed_at)                                            AS last_reviewed_at,
	avg(rating)::double precision                               AS computed_avg`

// countsRow mirrors countsSelect.
type countsRow struct {
	RestaurantID        int64
	StoredCount         int64
	TextCount           int64
	RepresentativeCount int64
	LastReviewedAt      *time.Time
	ComputedAvg         *float64
}

func (c countsRow) toDomain() review.Counts {
	out := review.Counts{
		StoredCount:         c.StoredCount,
		TextCount:           c.TextCount,
		RepresentativeCount: c.RepresentativeCount,
		LastReviewedAt:      c.LastReviewedAt,
		RatingDistribution:  map[int]int64{},
	}
	if c.ComputedAvg != nil {
		out.ComputedAvg = c.ComputedAvg
	}
	return out
}

// ratingDistribution returns the per-star histogram for one restaurant.
func (s *ReviewStore) ratingDistribution(ctx context.Context, restaurantID int64) (map[int]int64, error) {
	rows, err := s.client.pool.Query(ctx, `
		SELECT rating, count(*)::bigint
		FROM reviews WHERE restaurant_id = $1
		GROUP BY rating`, restaurantID)
	if err != nil {
		return nil, operationError("postgres: rating distribution", err)
	}
	defer rows.Close()
	out := map[int]int64{}
	for rows.Next() {
		var rating int16
		var n int64
		if err := rows.Scan(&rating, &n); err != nil {
			return nil, operationError("postgres: scan rating distribution", err)
		}
		out[int(rating)] = n
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate rating distribution", err)
	}
	return out, nil
}

// CountByRestaurant returns the materialisable rollup for one restaurant.
//
// A restaurant with no reviews is not an error: the stats stage asks about
// every restaurant, and "zero reviews" is a normal answer.
func (s *ReviewStore) CountByRestaurant(ctx context.Context, restaurantID int64) (review.Counts, error) {
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	row := s.client.pool.QueryRow(ctx,
		"SELECT "+countsSelect+" FROM reviews WHERE restaurant_id = $1 GROUP BY restaurant_id",
		restaurantID)
	var counts countsRow
	err := row.Scan(&counts.RestaurantID, &counts.StoredCount, &counts.TextCount,
		&counts.RepresentativeCount, &counts.LastReviewedAt, &counts.ComputedAvg)
	switch {
	case pgErrNoRows(err):
		return review.Counts{StoredCount: 0, RatingDistribution: map[int]int64{}}, nil
	case err != nil:
		return review.Counts{}, operationError("postgres: count reviews", err)
	}
	counts.RestaurantID = restaurantID
	out := counts.toDomain()
	distribution, err := s.ratingDistribution(ctx, restaurantID)
	if err != nil {
		return review.Counts{}, err
	}
	out.RatingDistribution = distribution
	return out, nil
}

// AggregateStats returns rollups for the given restaurant IDs.
func (s *ReviewStore) AggregateStats(ctx context.Context, restaurantIDs []int64) (map[int64]review.Counts, error) {
	out := make(map[int64]review.Counts, len(restaurantIDs))
	if len(restaurantIDs) == 0 {
		return out, nil
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	rows, err := s.client.pool.Query(ctx,
		"SELECT "+countsSelect+" FROM reviews WHERE restaurant_id = ANY($1) GROUP BY restaurant_id",
		restaurantIDs)
	if err != nil {
		return nil, operationError("postgres: aggregate review stats", err)
	}
	defer rows.Close()
	found := make([]int64, 0, len(restaurantIDs))
	for rows.Next() {
		var counts countsRow
		if err := rows.Scan(&counts.RestaurantID, &counts.StoredCount, &counts.TextCount,
			&counts.RepresentativeCount, &counts.LastReviewedAt, &counts.ComputedAvg); err != nil {
			return nil, operationError("postgres: scan review stats", err)
		}
		out[counts.RestaurantID] = counts.toDomain()
		found = append(found, counts.RestaurantID)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate review stats", err)
	}
	// The rating histogram is a second query rather than a window function so the
	// main rollup stays readable; it is only fetched for restaurants that
	// actually have reviews.
	distributions, err := s.ratingDistributions(ctx, found)
	if err != nil {
		return nil, err
	}
	for id, distribution := range distributions {
		counts := out[id]
		counts.RatingDistribution = distribution
		out[id] = counts
	}
	return out, nil
}

// ratingDistributions returns the per-star histogram for many restaurants in
// one query.
func (s *ReviewStore) ratingDistributions(ctx context.Context, restaurantIDs []int64) (map[int64]map[int]int64, error) {
	out := make(map[int64]map[int]int64, len(restaurantIDs))
	if len(restaurantIDs) == 0 {
		return out, nil
	}
	rows, err := s.client.pool.Query(ctx, `
		SELECT restaurant_id, rating, count(*)::bigint
		FROM reviews WHERE restaurant_id = ANY($1)
		GROUP BY restaurant_id, rating`, restaurantIDs)
	if err != nil {
		return nil, operationError("postgres: rating distributions", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var rating int16
		var n int64
		if err := rows.Scan(&id, &rating, &n); err != nil {
			return nil, operationError("postgres: scan rating distribution", err)
		}
		if out[id] == nil {
			out[id] = map[int]int64{}
		}
		out[id][int(rating)] = n
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate rating distributions", err)
	}
	return out, nil
}

// RestaurantIDsWithReviews returns restaurant IDs that have at least one stored
// review, so the pipeline can rebuild review_stats in batches.
func (s *ReviewStore) RestaurantIDsWithReviews(ctx context.Context) ([]int64, error) {
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	rows, err := s.client.pool.Query(ctx,
		"SELECT DISTINCT restaurant_id FROM reviews ORDER BY restaurant_id")
	if err != nil {
		return nil, operationError("postgres: restaurant ids with reviews", err)
	}
	defer rows.Close()
	out := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, operationError("postgres: scan restaurant id", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate restaurant ids", err)
	}
	return out, nil
}

// MarkRepresentative flags the given reviews and clears the flag on every other
// review of the same restaurants.
//
// Clearing and setting happen in one statement group so the flag can never be
// observed on a stale selection: a leftover flag from a previous run would keep
// contributing to representative_review_count and would make the M1 partial
// index return reviews that are no longer the chosen ones.
func (s *ReviewStore) MarkRepresentative(ctx context.Context, reviewIDs []int64) (int, error) {
	if len(reviewIDs) == 0 {
		return 0, nil
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	tag, err := s.client.pool.Exec(ctx, `
		WITH targets AS (
			SELECT DISTINCT restaurant_id FROM reviews WHERE id = ANY($1)
		)
		UPDATE reviews r
		SET is_representative = r.id = ANY($1)
		WHERE r.restaurant_id IN (SELECT restaurant_id FROM targets)`,
		reviewIDs)
	if err != nil {
		return 0, operationError("postgres: mark representative reviews", err)
	}
	return int(tag.RowsAffected()), nil
}

// UpsertSummaries writes per-topic rollups keyed by (restaurant_id, topic).
func (s *ReviewStore) UpsertSummaries(ctx context.Context, items []review.Summary) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}

	restaurantIDs := make([]int64, len(items))
	topics := make([]string, len(items))
	sentiments := make([]float64, len(items))
	ratios := make([]float64, len(items))
	summaries := make([]string, len(items))
	counts := make([]int32, len(items))
	validFrom := make([]time.Time, len(items))
	validTo := make([]*time.Time, len(items))
	generatedBy := make([]string, len(items))

	for i, item := range items {
		if item.RestaurantID <= 0 {
			return 0, errs.Newf(errs.CodeInvalidArgument, "postgres: summary %d has no restaurant id", i)
		}
		if strings.TrimSpace(item.Topic) == "" {
			return 0, errs.Newf(errs.CodeInvalidArgument, "postgres: summary %d has no topic", i)
		}
		if strings.TrimSpace(item.GeneratedBy) == "" {
			return 0, errs.Newf(errs.CodeInvalidArgument, "postgres: summary %d has no generated_by", i)
		}
		restaurantIDs[i] = item.RestaurantID
		topics[i] = item.Topic
		sentiments[i] = item.Sentiment
		ratios[i] = item.PositiveRatio
		summaries[i] = item.Summary
		counts[i] = int32(item.EvidenceCount)
		validFrom[i] = item.ValidFrom
		// valid_to is nullable: an open-ended summary is one that has not been
		// superseded. The zero time means "still valid", which is different
		// from "valid until the epoch".
		if !item.ValidTo.IsZero() {
			validTo[i] = &item.ValidTo
		}
		generatedBy[i] = item.GeneratedBy
	}

	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	tag, err := s.client.pool.Exec(ctx, `
		INSERT INTO review_summaries (
			restaurant_id, topic, sentiment, positive_ratio, summary,
			evidence_count, valid_from, valid_to, generated_by)
		SELECT * FROM unnest(
			$1::bigint[], $2::text[], $3::double precision[], $4::double precision[],
			$5::text[], $6::integer[], $7::timestamptz[], $8::timestamptz[], $9::text[])
		ON CONFLICT (restaurant_id, topic) DO UPDATE SET
			sentiment      = EXCLUDED.sentiment,
			positive_ratio = EXCLUDED.positive_ratio,
			summary        = EXCLUDED.summary,
			evidence_count = EXCLUDED.evidence_count,
			valid_from     = EXCLUDED.valid_from,
			valid_to       = EXCLUDED.valid_to,
			generated_by   = EXCLUDED.generated_by,
			generated_at   = now()`,
		restaurantIDs, topics, sentiments, ratios, summaries, counts, validFrom, validTo, generatedBy)
	if err != nil {
		return 0, operationError("postgres: upsert review summaries", err)
	}
	return int(tag.RowsAffected()), nil
}

// GetSummaries returns the stored rollups for one restaurant, ordered by topic
// so the generated documents are byte-stable across runs.
func (s *ReviewStore) GetSummaries(ctx context.Context, restaurantID int64) ([]review.Summary, error) {
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	rows, err := s.client.pool.Query(ctx, `
		SELECT restaurant_id, topic, sentiment, positive_ratio, summary,
		       evidence_count, valid_from, valid_to, generated_by, generated_at
		FROM review_summaries
		WHERE restaurant_id = $1
		ORDER BY topic`, restaurantID)
	if err != nil {
		return nil, operationError("postgres: get review summaries", err)
	}
	defer rows.Close()

	out := make([]review.Summary, 0, 8)
	for rows.Next() {
		var item review.Summary
		var validTo *time.Time
		if err := rows.Scan(&item.RestaurantID, &item.Topic, &item.Sentiment,
			&item.PositiveRatio, &item.Summary, &item.EvidenceCount,
			&item.ValidFrom, &validTo, &item.GeneratedBy, &item.GeneratedAt); err != nil {
			return nil, operationError("postgres: scan review summary", err)
		}
		if validTo != nil {
			item.ValidTo = *validTo
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate review summaries", err)
	}
	return out, nil
}
