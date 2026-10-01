package postgres

import (
	"context"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/restaurant"
)

// RestaurantStore is the PostgreSQL implementation of store.RestaurantStore.
type RestaurantStore struct {
	client *Client
}

// NewRestaurantStore returns a Postgres-backed restaurant store.
func NewRestaurantStore(client *Client) *RestaurantStore {
	return &RestaurantStore{client: client}
}

// restaurantUpsertSQL writes the source-derived columns of a restaurant.
//
// The conflict target is source_record_id rather than id, because a re-import
// proposes a fresh id while the row must keep its original one: reviews point at
// restaurant_id, so letting it change would orphan the whole review corpus.
//
// The update list deliberately omits created_at, the review rollups, and the
// score columns. Those are written by the stats and score stages, and a meta
// re-import must not roll them back.
const restaurantUpsertSQL = `
INSERT INTO restaurants (
	source, source_record_id, name, address, borough, location,
	categories, cuisine_tags, description,
	price_raw, price_level,
	rating_source_avg,
	source_review_count, source_review_count_capped,
	attributes, attributes_raw, hours, relative_results,
	snapshot_status, observed_at, source_url,
	created_at, updated_at
) VALUES (
	$1, $2, $3, $4, $5, $6,
	$7, $8, $9,
	$10, $11,
	$12,
	$13, $14,
	$15, $16, $17, $18,
	$19, $20, $21,
	$22, now()
)
ON CONFLICT (source_record_id) DO UPDATE SET
	name                = EXCLUDED.name,
	address             = EXCLUDED.address,
	borough             = EXCLUDED.borough,
	location            = EXCLUDED.location,
	categories          = EXCLUDED.categories,
	cuisine_tags        = EXCLUDED.cuisine_tags,
	description         = EXCLUDED.description,
	price_raw           = EXCLUDED.price_raw,
	price_level         = EXCLUDED.price_level,
	rating_source_avg   = EXCLUDED.rating_source_avg,
	source_review_count = EXCLUDED.source_review_count,
	source_review_count_capped = EXCLUDED.source_review_count_capped,
	attributes          = EXCLUDED.attributes,
	attributes_raw      = EXCLUDED.attributes_raw,
	hours               = EXCLUDED.hours,
	relative_results    = EXCLUDED.relative_results,
	snapshot_status     = EXCLUDED.snapshot_status,
	observed_at         = EXCLUDED.observed_at,
	source_url          = EXCLUDED.source_url,
	updated_at          = now()
RETURNING id`

// args renders one restaurant into the statement's parameters.
func restaurantUpsertArgs(r restaurant.Restaurant) ([]any, error) {
	attributes, err := marshalOrNil(r.Attributes)
	if err != nil {
		return nil, err
	}
	attributesRaw, err := marshalOrNil(r.AttributesRaw)
	if err != nil {
		return nil, err
	}
	hours, err := marshalOrNil(r.Hours)
	if err != nil {
		return nil, err
	}
	categories := r.Categories
	if categories == nil {
		categories = []string{}
	}
	cuisineTags := r.CuisineTags
	if cuisineTags == nil {
		cuisineTags = []string{}
	}
	relative := r.RelativeResults
	if relative == nil {
		relative = []string{}
	}
	var ratingSourceAvg any
	if r.Rating.SourceAvg != nil {
		ratingSourceAvg = *r.Rating.SourceAvg
	}
	var priceLevel any
	if r.Price.Level != nil {
		priceLevel = int16(*r.Price.Level)
	}
	// id is deliberately absent: it is an identity column, and on conflict the
	// existing row keeps the id its reviews already point at.
	return []any{
		r.Source, r.SourceRecordID, r.Name,
		nullString(r.Address), nullString(r.BoroughGuess), newLocation(r.Location),
		categories, cuisineTags, nullString(r.Description),
		nullString(r.Price.Raw), priceLevel,
		ratingSourceAvg,
		r.ReviewStats.SourceReviewCount, r.ReviewStats.SourceReviewCountCapped,
		attributes, attributesRaw, hours, relative,
		string(r.SnapshotStatus), r.ObservedAt, nullString(r.SourceURL),
		r.CreatedAt,
	}, nil
}

// UpsertRestaurant inserts or replaces one restaurant keyed by source_record_id.
func (s *RestaurantStore) UpsertRestaurant(ctx context.Context, r restaurant.Restaurant) error {
	if strings.TrimSpace(r.SourceRecordID) == "" {
		return errs.New(errs.CodeInvalidArgument, "source_record_id is required")
	}
	args, err := restaurantUpsertArgs(r)
	if err != nil {
		return err
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	if _, err := s.client.pool.Exec(ctx, restaurantUpsertSQL, args...); err != nil {
		return operationError("postgres: upsert restaurant", err)
	}
	return nil
}

// UpsertRestaurants upserts a batch in one round trip and returns the number of
// rows written.
//
// A single multi-row statement is used instead of a batched transaction: it is
// one network round trip, and Postgres writes the rows far faster than a remote
// cluster ever did, so the per-row overhead of a transaction buys nothing.
func (s *RestaurantStore) UpsertRestaurants(ctx context.Context, rs []restaurant.Restaurant) (int, error) {
	if len(rs) == 0 {
		return 0, nil
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	batch := pgx.Batch{}
	for _, r := range rs {
		if strings.TrimSpace(r.SourceRecordID) == "" {
			return 0, errs.New(errs.CodeInvalidArgument, "source_record_id is required")
		}
		args, err := restaurantUpsertArgs(r)
		if err != nil {
			return 0, err
		}
		batch.Queue(restaurantUpsertSQL, args...)
	}
	results := s.client.pool.SendBatch(ctx, &batch)
	defer results.Close()
	written := 0
	for range rs {
		if _, err := results.Exec(); err != nil {
			return written, operationError("postgres: bulk upsert restaurants", err)
		}
		written++
	}
	return written, nil
}

// scanRestaurants reads rows into domain values.
func (s *RestaurantStore) scanRestaurants(rows pgx.Rows) ([]restaurant.Restaurant, error) {
	defer rows.Close()
	out := make([]restaurant.Restaurant, 0)
	for rows.Next() {
		var row restaurantRow
		if err := rows.Scan(
			&row.ID, &row.Source, &row.SourceRecordID, &row.Name, &row.Address, &row.Borough,
			&row.LocationText,
			&row.Categories, &row.CuisineTags, &row.Description,
			&row.PriceRaw, &row.PriceLevel,
			&row.RatingSourceAvg, &row.RatingComputedAvg, &row.RatingCount,
			&row.SourceReviewCount, &row.SourceReviewCountCapped, &row.StoredReviewCount,
			&row.TextReviewCount, &row.RepresentativeReviewCount, &row.EmbeddedReviewCount,
			&row.LastReviewedAt, &row.StatsUpdatedAt,
			&row.Attributes, &row.AttributesRaw, &row.Hours, &row.RelativeResults,
			&row.SnapshotStatus, &row.KnowledgeScore, &row.IsActiveForDemo,
			&row.ObservedAt, &row.SourceURL, &row.CreatedAt, &row.UpdatedAt,
		); err != nil {
			return nil, operationError("postgres: scan restaurant", err)
		}
		item, err := row.toDomain()
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate restaurants", err)
	}
	return out, nil
}

// GetBySourceRecordID returns the curated restaurant for a gmap_id.
func (s *RestaurantStore) GetBySourceRecordID(ctx context.Context, sourceRecordID string) (restaurant.Restaurant, error) {
	if strings.TrimSpace(sourceRecordID) == "" {
		return restaurant.Restaurant{}, errs.New(errs.CodeInvalidArgument, "source_record_id is required")
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	rows, err := s.client.pool.Query(ctx,
		"SELECT "+restaurantColumns+" FROM restaurants WHERE source_record_id = $1", sourceRecordID)
	if err != nil {
		return restaurant.Restaurant{}, operationError("postgres: get restaurant", err)
	}
	out, err := s.scanRestaurants(rows)
	if err != nil {
		return restaurant.Restaurant{}, err
	}
	if len(out) == 0 {
		return restaurant.Restaurant{}, errs.Newf(errs.CodeNotFound,
			"postgres: no restaurant for source_record_id %q", sourceRecordID)
	}
	return out[0], nil
}

// GetByID returns the curated restaurant by its internal id.
func (s *RestaurantStore) GetByID(ctx context.Context, restaurantID int64) (restaurant.Restaurant, error) {
	if restaurantID <= 0 {
		return restaurant.Restaurant{}, errs.New(errs.CodeInvalidArgument, "restaurant_id is required")
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	rows, err := s.client.pool.Query(ctx,
		"SELECT "+restaurantColumns+" FROM restaurants WHERE id = $1", restaurantID)
	if err != nil {
		return restaurant.Restaurant{}, operationError("postgres: get restaurant by id", err)
	}
	out, err := s.scanRestaurants(rows)
	if err != nil {
		return restaurant.Restaurant{}, err
	}
	if len(out) == 0 {
		return restaurant.Restaurant{}, errs.Newf(errs.CodeNotFound,
			"postgres: no restaurant with id %d", restaurantID)
	}
	return out[0], nil
}

// ListRestaurants returns restaurants ordered by source_record_id. A
// non-positive limit means "all".
//
// Scoring loads every restaurant so it can rank them in memory; the read is
// therefore expected to be large, which is why it streams rather than buffering
// through an intermediate representation.
func (s *RestaurantStore) ListRestaurants(ctx context.Context, limit int) ([]restaurant.Restaurant, error) {
	query := "SELECT " + restaurantColumns + " FROM restaurants ORDER BY source_record_id"
	args := []any{}
	if limit > 0 {
		query += " LIMIT $1"
		args = append(args, limit)
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	rows, err := s.client.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, operationError("postgres: list restaurants", err)
	}
	return s.scanRestaurants(rows)
}

// ListSourceRecordIDs returns every gmap_id, ordered.
//
// The prefilter stage needs the whole joinable id set in one pass; doing this
// row by row would mean millions of point queries.
func (s *RestaurantStore) ListSourceRecordIDs(ctx context.Context) ([]string, error) {
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	rows, err := s.client.pool.Query(ctx, "SELECT source_record_id FROM restaurants ORDER BY source_record_id")
	if err != nil {
		return nil, operationError("postgres: list source record ids", err)
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, operationError("postgres: scan source record id", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate source record ids", err)
	}
	return out, nil
}

// MapSourceRecordIDs resolves gmap_ids to restaurant ids in one round trip so
// the review join does not issue a query per review.
func (s *RestaurantStore) MapSourceRecordIDs(ctx context.Context, sourceRecordIDs []string) (map[string]int64, error) {
	out := make(map[string]int64, len(sourceRecordIDs))
	if len(sourceRecordIDs) == 0 {
		return out, nil
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	rows, err := s.client.pool.Query(ctx,
		"SELECT source_record_id, id FROM restaurants WHERE source_record_id = ANY($1)",
		sourceRecordIDs)
	if err != nil {
		return nil, operationError("postgres: map source record ids", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sourceID string
		var id int64
		if err := rows.Scan(&sourceID, &id); err != nil {
			return nil, operationError("postgres: scan id mapping", err)
		}
		out[sourceID] = id
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate id mapping", err)
	}
	return out, nil
}

// UpdateReviewStats writes the materialised rollups and the computed rating.
//
// Both are written together because they come from the same review sample and
// must never disagree. The statement reports how many rows it touched so a
// missing restaurant is a not-found error rather than a silent no-op.
func (s *RestaurantStore) UpdateReviewStats(
	ctx context.Context,
	restaurantID int64,
	stats restaurant.ReviewStats,
	computed restaurant.Rating,
) error {
	var computedAvg any
	if computed.ComputedAvg != nil {
		computedAvg = *computed.ComputedAvg
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	tag, err := s.client.pool.Exec(ctx, `
		UPDATE restaurants SET
			stored_review_count          = $2,
			text_review_count            = $3,
			representative_review_count  = $4,
			embedded_review_count        = $5,
			last_reviewed_at             = $6,
			stats_updated_at             = $7,
			rating_computed_avg          = $8,
			rating_count                 = $9,
			updated_at                   = now()
		WHERE id = $1`,
		restaurantID,
		stats.StoredReviewCount, stats.TextReviewCount,
		stats.RepresentativeReviewCount, stats.EmbeddedReviewCount,
		stats.LastReviewedAt, stats.StatsUpdatedAt,
		computedAvg, computed.RatingCountForComputedAvg,
	)
	if err != nil {
		return operationError("postgres: update review stats", err)
	}
	if tag.RowsAffected() == 0 {
		return errs.Newf(errs.CodeNotFound, "postgres: no restaurant with id %d", restaurantID)
	}
	return nil
}

// UpdateEmbeddedReviewCount writes only the embedded_review_count column.
//
// The narrow update is deliberate. UpdateReviewStats rewrites all seven rollup
// columns, so using it from the embedding stage would mean reading the other
// six back first and writing them again — and any column that changed in
// between would be silently reverted.
func (s *RestaurantStore) UpdateEmbeddedReviewCount(
	ctx context.Context,
	restaurantID int64,
	count int,
) error {
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	tag, err := s.client.pool.Exec(ctx,
		`UPDATE restaurants SET embedded_review_count = $2, updated_at = now() WHERE id = $1`,
		restaurantID, count)
	if err != nil {
		return operationError("postgres: update embedded review count", err)
	}
	if tag.RowsAffected() == 0 {
		return errs.Newf(errs.CodeNotFound, "postgres: no restaurant with id %d", restaurantID)
	}
	return nil
}

// UpdateScores writes knowledge_score and is_active_for_demo for a batch.
//
// The two maps are independent: a restaurant may be scored without being
// selected for the demo, so each statement is built from whichever map has an
// entry.
//
// The batch is passed as two parallel arrays through unnest rather than as a
// VALUES list. A VALUES list costs two bind parameters per row, and the protocol
// caps a statement at 65535 parameters, which a full-corpus scoring run exceeds
// (36133 restaurants x 2). unnest costs one array parameter regardless of how
// many rows it carries, so the statement size no longer depends on the corpus.
func (s *RestaurantStore) UpdateScores(ctx context.Context, scores map[int64]float64, active map[int64]bool) error {
	if len(scores) == 0 && len(active) == 0 {
		return nil
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	tx, err := s.client.pool.Begin(ctx)
	if err != nil {
		return operationError("postgres: begin score update", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if len(scores) > 0 {
		ids, values := sortedIDKeys(scores), make([]float64, 0, len(scores))
		for _, id := range ids {
			values = append(values, scores[id])
		}
		stmt := `UPDATE restaurants r
			SET knowledge_score = v.score, updated_at = now()
			FROM unnest($1::bigint[], $2::double precision[]) AS v(id, score)
			WHERE r.id = v.id`
		if _, err := tx.Exec(ctx, stmt, ids, values); err != nil {
			return operationError("postgres: update knowledge scores", err)
		}
	}
	if len(active) > 0 {
		ids := sortedIDKeys(active)
		values := make([]bool, 0, len(active))
		for _, id := range ids {
			values = append(values, active[id])
		}
		stmt := `UPDATE restaurants r
			SET is_active_for_demo = v.active, updated_at = now()
			FROM unnest($1::bigint[], $2::boolean[]) AS v(id, active)
			WHERE r.id = v.id`
		if _, err := tx.Exec(ctx, stmt, ids, values); err != nil {
			return operationError("postgres: update demo flags", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return operationError("postgres: commit score update", err)
	}
	return nil
}

// SelectForDemo returns active-for-demo restaurants ordered by score.
func (s *RestaurantStore) SelectForDemo(ctx context.Context, limit int) ([]restaurant.Restaurant, error) {
	query := "SELECT " + restaurantColumns + " FROM restaurants WHERE is_active_for_demo ORDER BY knowledge_score DESC"
	args := []any{}
	if limit > 0 {
		query += " LIMIT $1"
		args = append(args, limit)
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	rows, err := s.client.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, operationError("postgres: select for demo", err)
	}
	return s.scanRestaurants(rows)
}

// CountActiveForDemo reports how many restaurants are active for the demo.
func (s *RestaurantStore) CountActiveForDemo(ctx context.Context) (int64, error) {
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	var n int64
	if err := s.client.pool.QueryRow(ctx,
		"SELECT count(*) FROM restaurants WHERE is_active_for_demo").Scan(&n); err != nil {
		return 0, operationError("postgres: count active for demo", err)
	}
	return n, nil
}

// sortedKeys returns a map's keys in a stable order so generated SQL is
// deterministic and therefore reproducible in tests and logs.
// sortedIDKeys returns the map keys in ascending order.
//
// Deterministic order matters here: the arrays are paired positionally by unnest,
// so a stable key order keeps the two arrays aligned.
func sortedIDKeys[V any](m map[int64]V) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
