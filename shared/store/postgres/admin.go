package postgres

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/zed1995/platepilot/shared/domain/admin"
	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/review"
	"github.com/zed1995/platepilot/shared/domain/search"
	"github.com/zed1995/platepilot/shared/store"
)

var _ store.AdminStore = (*AdminStore)(nil)

// AdminStore is the read-only query store behind the administration console.
// It shares one client with the read-side repositories but answers a different
// shape of question: wide, shallow listings and counters rather than narrow,
// deep candidate recalls.
type AdminStore struct {
	client *Client
}

// NewAdminStore builds an inspect store on an existing client.
func NewAdminStore(client *Client) *AdminStore {
	return &AdminStore{client: client}
}

// vectorPreviewLimit bounds how many components a document preview returns.
const vectorPreviewLimit = 16

// defaultMaxRejections is the fallback rejection-item cap when the caller does
// not supply one.
const defaultMaxRejections = 200

// ---------------------------------------------------------------------------
// Projection constants
// ---------------------------------------------------------------------------

// documentListColumns is the document projection of the listing. It carries
// neither body text nor the stored vector, only vector presence and metadata.
const documentListColumns = `
	document_id, restaurant_id, retrieval_scope, doc_type, title, content_hash,
	version, is_active, embedding IS NOT NULL AS has_embedding,
	embedding_model, embedding_dimensions, snapshot_at`

// batchListColumns is the audit-record projection of the listing.
const batchListColumns = `
	id, stage, status, started_at, finished_at, duration_ms,
	rows_read, accepted, written, deduped, filtered, rejected, unmatched,
	documents_built, documents_embedded, documents_rejected,
	embedding_model, embedding_dimensions`

// ---------------------------------------------------------------------------
// Dashboard
// ---------------------------------------------------------------------------

// Overview assembles the dashboard payload from several light reads.
func (s *AdminStore) Overview(ctx context.Context) (admin.Overview, error) {
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	var overview admin.Overview

	// Restaurant counters in one indexed scan.
	if err := s.client.pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE is_active_for_demo)
		FROM restaurants`,
	).Scan(&overview.Tables.RestaurantsTotal,
		&overview.Tables.RestaurantsActive); err != nil {
		return admin.Overview{}, operationError("postgres: overview restaurant counts", err)
	}

	// The reviews figure is estimated from the planner's own statistics.
	var reltuples float32
	if err := s.client.pool.QueryRow(ctx, `
		SELECT reltuples FROM pg_class WHERE relname = 'reviews'`,
	).Scan(&reltuples); err != nil {
		return admin.Overview{}, operationError("postgres: overview review estimate", err)
	}
	overview.Tables.ReviewsEstimate = int64(reltuples)
	overview.Tables.ReviewsEstimated = true

	// Active documents and the vector-health counter in one scan.
	if err := s.client.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE is_active),
		       count(*) FILTER (WHERE is_active AND embedding IS NULL)
		FROM knowledge_documents`,
	).Scan(&overview.Tables.DocumentsActive,
		&overview.ActiveWithoutVector); err != nil {
		return admin.Overview{}, operationError("postgres: overview document counts", err)
	}

	// Audit-record total.
	if err := s.client.pool.QueryRow(ctx,
		`SELECT count(*) FROM ingestion_batches`,
	).Scan(&overview.Tables.BatchesTotal); err != nil {
		return admin.Overview{}, operationError("postgres: overview batch count", err)
	}

	breakdown, err := s.documentBreakdown(ctx)
	if err != nil {
		return admin.Overview{}, err
	}
	overview.DocumentBreakdown = breakdown

	rows, err := s.client.pool.Query(ctx, `
		SELECT `+batchListColumns+`
		FROM ingestion_batches
		ORDER BY started_at DESC, id DESC
		LIMIT 5`)
	if err != nil {
		return admin.Overview{}, operationError("postgres: overview recent batches", err)
	}
	recent, err := scanBatchList(rows)
	if err != nil {
		return admin.Overview{}, err
	}
	overview.RecentBatches = recent

	migrations, err := s.listMigrations(ctx)
	if err != nil {
		return admin.Overview{}, err
	}
	overview.Migrations = migrations

	return overview, nil
}

// documentBreakdown groups active documents by scope and kind.
func (s *AdminStore) documentBreakdown(ctx context.Context) ([]admin.DocumentCount, error) {
	rows, err := s.client.pool.Query(ctx, `
		SELECT retrieval_scope, doc_type, count(*)::bigint
		FROM knowledge_documents
		WHERE is_active
		GROUP BY retrieval_scope, doc_type
		ORDER BY retrieval_scope, doc_type`)
	if err != nil {
		return nil, operationError("postgres: overview document breakdown", err)
	}
	defer rows.Close()

	out := make([]admin.DocumentCount, 0)
	for rows.Next() {
		var item admin.DocumentCount
		if err := rows.Scan(&item.RetrievalScope, &item.DocType, &item.Count); err != nil {
			return nil, operationError("postgres: scan document breakdown", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate document breakdown", err)
	}
	return out, nil
}

// listMigrations reads every applied schema change in version order.
func (s *AdminStore) listMigrations(ctx context.Context) ([]admin.Migration, error) {
	rows, err := s.client.pool.Query(ctx, `
		SELECT version, applied_at
		FROM schema_migrations
		ORDER BY version`)
	if err != nil {
		return nil, operationError("postgres: overview migrations", err)
	}
	defer rows.Close()

	out := make([]admin.Migration, 0)
	for rows.Next() {
		var item admin.Migration
		if err := rows.Scan(&item.Version, &item.AppliedAt); err != nil {
			return nil, operationError("postgres: scan migration", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate migrations", err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Restaurants
// ---------------------------------------------------------------------------

// Restaurants lists restaurants with a keyset cursor, newest id first.
func (s *AdminStore) Restaurants(
	ctx context.Context, q admin.RestaurantQuery,
) (admin.RestaurantPage, error) {
	clauses := make([]string, 0, 5)
	args := make([]any, 0, 6)
	bind := func(value any) string {
		args = append(args, value)
		return "$" + strconv.Itoa(len(args))
	}

	if q.Borough != "" {
		clauses = append(clauses, "borough = "+bind(search.CanonicalBorough(q.Borough)))
	}
	if q.Cuisine != "" {
		clauses = append(clauses,
			"EXISTS (SELECT 1 FROM unnest(cuisine_tags) AS t WHERE t = "+bind(q.Cuisine)+")")
	}
	if q.Active != nil {
		clauses = append(clauses, "is_active_for_demo = "+bind(*q.Active))
	}
	if q.Q != "" {
		clauses = append(clauses,
			"name ILIKE "+bind("%"+escapeLike(q.Q)+"%")+` ESCAPE '\'`)
	}
	if q.AfterID > 0 {
		clauses = append(clauses, "id < "+bind(q.AfterID))
	}

	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}

	var total int64
	if err := s.client.pool.QueryRow(ctx,
		"SELECT count(*) FROM restaurants"+where, args...,
	).Scan(&total); err != nil {
		return admin.RestaurantPage{}, operationError("postgres: count restaurants", err)
	}

	statement := `SELECT ` + restaurantSearchColumns + `
		FROM restaurants` + where + `
		ORDER BY id DESC
		LIMIT ` + strconv.Itoa(q.Limit+1)

	rows, err := s.client.pool.Query(ctx, statement, args...)
	if err != nil {
		return admin.RestaurantPage{}, operationError("postgres: list restaurants", err)
	}
	items, err := scanRestaurantList(rows)
	if err != nil {
		return admin.RestaurantPage{}, err
	}

	hasNext := len(items) == q.Limit+1
	if hasNext {
		items = items[:q.Limit]
	}
	return admin.RestaurantPage{
		Items:         items,
		TotalEstimate: total,
		HasMore:       hasNext,
	}, nil
}

// RestaurantDetail returns one restaurant's full row.
func (s *AdminStore) RestaurantDetail(
	ctx context.Context, restaurantID int64,
) (admin.RestaurantDetail, error) {
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	row := s.client.pool.QueryRow(ctx, `
		SELECT `+restaurantColumns+`
		FROM restaurants
		WHERE id = $1`, restaurantID)

	var r restaurantRow
	if err := row.Scan(
		&r.ID, &r.Source, &r.SourceRecordID, &r.Name, &r.Address, &r.Borough,
		&r.LocationText, &r.Categories, &r.CuisineTags, &r.Description,
		&r.PriceRaw, &r.PriceLevel,
		&r.RatingSourceAvg, &r.RatingComputedAvg, &r.RatingCount,
		&r.SourceReviewCount, &r.SourceReviewCountCapped, &r.StoredReviewCount,
		&r.TextReviewCount, &r.RepresentativeReviewCount, &r.EmbeddedReviewCount,
		&r.LastReviewedAt, &r.StatsUpdatedAt,
		&r.Attributes, &r.AttributesRaw, &r.Hours, &r.RelativeResults,
		&r.SnapshotStatus, &r.KnowledgeScore, &r.IsActiveForDemo,
		&r.ObservedAt, &r.SourceURL, &r.CreatedAt, &r.UpdatedAt,
	); err != nil {
		if pgErrNoRows(err) {
			return admin.RestaurantDetail{}, errs.Newf(errs.CodeNotFound,
				"restaurant %d not found", restaurantID)
		}
		return admin.RestaurantDetail{}, operationError("postgres: restaurant detail", err)
	}

	return restaurantDetailFromRow(r)
}

// restaurantDetailFromRow maps a full table row onto the inspect projection.
func restaurantDetailFromRow(r restaurantRow) (admin.RestaurantDetail, error) {
	out := admin.RestaurantDetail{
		RestaurantID:              r.ID,
		Source:                    r.Source,
		SourceRecordID:            r.SourceRecordID,
		Name:                      r.Name,
		Categories:                nonNilStrings(r.Categories),
		Cuisines:                  nonNilStrings(r.CuisineTags),
		RatingCount:               int(r.RatingCount),
		SourceReviewCount:         int(r.SourceReviewCount),
		SourceReviewCountCapped:   r.SourceReviewCountCapped,
		StoredReviewCount:         int(r.StoredReviewCount),
		TextReviewCount:           int(r.TextReviewCount),
		RepresentativeReviewCount: int(r.RepresentativeReviewCount),
		EmbeddedReviewCount:       int(r.EmbeddedReviewCount),
		Attributes:                jsonRaw(orEmptyObject(r.Attributes)),
		Hours:                     jsonRaw(orEmptyArray(r.Hours)),
		ActiveForDemo:             r.IsActiveForDemo,
		SnapshotStatus:            r.SnapshotStatus,
		KnowledgeScore:            r.KnowledgeScore,
		ObservedAt:                r.ObservedAt,
		CreatedAt:                 r.CreatedAt,
		UpdatedAt:                 r.UpdatedAt,
	}
	if r.Address != nil {
		out.Address = *r.Address
	}
	if r.Borough != nil {
		out.Borough = *r.Borough
	}
	if r.Description != nil {
		out.Description = *r.Description
	}
	if r.LocationText != nil {
		lon, lat, err := parsePoint(*r.LocationText)
		if err != nil {
			return admin.RestaurantDetail{}, errs.Wrap(errs.CodeInternal,
				"decode restaurant location", err)
		}
		out.Longitude = &lon
		out.Latitude = &lat
	}
	if r.PriceRaw != nil {
		out.PriceRaw = *r.PriceRaw
	}
	if r.PriceLevel != nil {
		level := int(*r.PriceLevel)
		out.PriceLevel = &level
	}
	if r.RatingSourceAvg != nil {
		out.RatingSourceAvg = r.RatingSourceAvg
	}
	if r.RatingComputedAvg != nil {
		out.RatingComputed = r.RatingComputedAvg
	}
	if r.LastReviewedAt != nil {
		out.LastReviewedAt = r.LastReviewedAt
	}
	if r.StatsUpdatedAt != nil {
		out.StatsUpdatedAt = r.StatsUpdatedAt
	}
	if r.SourceURL != nil {
		out.SourceURL = *r.SourceURL
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Reviews
// ---------------------------------------------------------------------------

// Reviews lists one restaurant's reviews, newest first.
func (s *AdminStore) Reviews(
	ctx context.Context, q admin.ReviewQuery,
) (admin.ReviewPage, error) {
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	clauses := []string{"restaurant_id = $1"}
	args := []any{q.RestaurantID}
	if !q.AfterReviewedAt.IsZero() {
		// Row-value keyset; both columns descend, so the tuple comparison
		// matches the ordering exactly.
		clauses = append(clauses, "(reviewed_at, id) < ($2, $3)")
		args = append(args, q.AfterReviewedAt, q.AfterID)
	}

	statement := `SELECT ` + reviewColumns + `
		FROM reviews
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY reviewed_at DESC, id DESC
		LIMIT ` + strconv.Itoa(q.Limit+1)

	rows, err := s.client.pool.Query(ctx, statement, args...)
	if err != nil {
		return admin.ReviewPage{}, operationError("postgres: list reviews", err)
	}
	items, err := scanReviewList(rows)
	if err != nil {
		return admin.ReviewPage{}, err
	}

	hasNext := len(items) == q.Limit+1
	if hasNext {
		items = items[:q.Limit]
	}
	return admin.ReviewPage{Items: items, HasMore: hasNext}, nil
}

// ---------------------------------------------------------------------------
// Review summaries
// ---------------------------------------------------------------------------

// Summaries lists one restaurant's per-topic review rollups.
func (s *AdminStore) Summaries(
	ctx context.Context, restaurantID int64,
) ([]admin.ReviewSummary, error) {
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	rows, err := s.client.pool.Query(ctx, `
		SELECT restaurant_id, topic, sentiment, positive_ratio, summary,
		       evidence_count, generated_by, generated_at
		FROM review_summaries
		WHERE restaurant_id = $1
		ORDER BY topic`, restaurantID)
	if err != nil {
		return nil, operationError("postgres: list review summaries", err)
	}
	defer rows.Close()

	out := make([]admin.ReviewSummary, 0)
	for rows.Next() {
		var item admin.ReviewSummary
		if err := rows.Scan(
			&item.RestaurantID, &item.Topic, &item.Sentiment,
			&item.PositiveRatio, &item.Summary, &item.EvidenceCount,
			&item.GeneratedBy, &item.GeneratedAt,
		); err != nil {
			return nil, operationError("postgres: scan review summary", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate review summaries", err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Knowledge documents
// ---------------------------------------------------------------------------

// Documents lists documents across restaurants, newest id first.
func (s *AdminStore) Documents(
	ctx context.Context, q admin.DocumentQuery,
) (admin.DocumentPage, error) {
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	clauses := make([]string, 0, 6)
	args := make([]any, 0, 7)
	bind := func(value any) string {
		args = append(args, value)
		return "$" + strconv.Itoa(len(args))
	}

	if q.RestaurantID > 0 {
		clauses = append(clauses, "restaurant_id = "+bind(q.RestaurantID))
	}
	if q.Scope != "" {
		clauses = append(clauses, "retrieval_scope = "+bind(q.Scope))
	}
	if q.DocType != "" {
		clauses = append(clauses, "doc_type = "+bind(q.DocType))
	}
	if q.IsActive != nil {
		clauses = append(clauses, "is_active = "+bind(*q.IsActive))
	}
	if q.HasEmbedding != nil {
		if *q.HasEmbedding {
			clauses = append(clauses, "embedding IS NOT NULL")
		} else {
			clauses = append(clauses, "embedding IS NULL")
		}
	}
	if q.AfterID > 0 {
		clauses = append(clauses, "document_id < "+bind(q.AfterID))
	}

	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}

	statement := `SELECT ` + documentListColumns + `
		FROM knowledge_documents` + where + `
		ORDER BY document_id DESC
		LIMIT ` + strconv.Itoa(q.Limit+1)

	rows, err := s.client.pool.Query(ctx, statement, args...)
	if err != nil {
		return admin.DocumentPage{}, operationError("postgres: list documents", err)
	}
	items, err := scanDocumentList(rows)
	if err != nil {
		return admin.DocumentPage{}, err
	}

	hasNext := len(items) == q.Limit+1
	if hasNext {
		items = items[:q.Limit]
	}
	return admin.DocumentPage{Items: items, HasMore: hasNext}, nil
}

// DocumentDetail returns one document including vector metadata.
func (s *AdminStore) DocumentDetail(
	ctx context.Context, documentID int64, includeVectorPreview bool,
) (admin.DocumentDetail, error) {
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	// The vector text is read only when a preview was asked for.
	statement := `
		SELECT document_id, restaurant_id, retrieval_scope, doc_type, title,
		       content_hash, version, is_active, embedding IS NOT NULL,
		       embedding_model, embedding_dimensions, snapshot_at,
		       content, metadata, source_record_ids, source_review_ids,
		       CASE WHEN $2 THEN embedding::text ELSE NULL::text END
		FROM knowledge_documents
		WHERE document_id = $1`

	row := s.client.pool.QueryRow(ctx, statement, documentID, includeVectorPreview)

	var (
		out          admin.DocumentDetail
		item         = &out.DocumentListItem
		title        *string
		model        *string
		dimensions   *int32
		snapshotAt   *time.Time
		metadataRaw  []byte
		embeddingTxt *string
	)
	if err := row.Scan(
		&item.DocumentID, &item.RestaurantID, &item.Scope, &item.DocType, &title,
		&item.ContentHash, &item.Version, &item.IsActive, &item.HasEmbedding,
		&model, &dimensions, &snapshotAt,
		&out.Content, &metadataRaw, &out.SourceRecordIDs, &out.SourceReviewIDs,
		&embeddingTxt,
	); err != nil {
		if pgErrNoRows(err) {
			return admin.DocumentDetail{}, errs.Newf(errs.CodeNotFound,
				"document %d not found", documentID)
		}
		return admin.DocumentDetail{}, operationError("postgres: document detail", err)
	}

	if title != nil {
		item.Title = *title
	}
	if model != nil {
		item.EmbeddingModel = *model
	}
	if dimensions != nil {
		item.EmbeddingDimensions = int(*dimensions)
	}
	if snapshotAt != nil {
		item.SnapshotAt = *snapshotAt
	}
	out.Metadata = jsonRaw(orEmptyObject(metadataRaw))
	out.SourceRecordIDs = nonNilStrings(out.SourceRecordIDs)

	if includeVectorPreview && embeddingTxt != nil {
		vector, err := parseVectorLiteral(embeddingTxt)
		if err != nil {
			return admin.DocumentDetail{}, err
		}
		if len(vector) > vectorPreviewLimit {
			vector = vector[:vectorPreviewLimit]
		}
		out.VectorPreview = vector
	}
	return out, nil
}

// DocumentSourceReviews resolves the digest's stored source_review_ids to the
// review rows they name.
//
// The join is on both restaurant id and id membership: source_review_ids only
// ever names reviews of the digest's own restaurant, and the restaurant
// condition keeps an id collision between restaurants from resolving to the
// wrong row. Non-digest documents carry an empty array, so the same query
// answers them with an empty list.
func (s *AdminStore) DocumentSourceReviews(
	ctx context.Context, documentID int64,
) ([]admin.ReviewListItem, error) {
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	rows, err := s.client.pool.Query(ctx, `
		SELECT
			r.id, r.restaurant_id, r.rating, r.reviewed_at, r.text, r.language,
			r.text_hash, r.is_representative, r.topic_tags, r.source_observed_at
		FROM reviews r
		JOIN knowledge_documents d
		  ON d.restaurant_id = r.restaurant_id
		 AND r.id = ANY(d.source_review_ids)
		WHERE d.document_id = $1
		ORDER BY r.reviewed_at DESC, r.id DESC`, documentID)
	if err != nil {
		return nil, operationError("postgres: document source reviews", err)
	}
	return scanReviewList(rows)
}

// DocumentsByRestaurant lists one restaurant's documents, active first.
func (s *AdminStore) DocumentsByRestaurant(
	ctx context.Context, restaurantID int64,
) ([]admin.DocumentSummary, error) {
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	rows, err := s.client.pool.Query(ctx, `
		SELECT `+documentListColumns+`
		FROM knowledge_documents
		WHERE restaurant_id = $1
		ORDER BY is_active DESC, document_id DESC`, restaurantID)
	if err != nil {
		return nil, operationError("postgres: list documents by restaurant", err)
	}
	return scanDocumentList(rows)
}

// ---------------------------------------------------------------------------
// Ingestion audit
// ---------------------------------------------------------------------------

// Batches lists audit records, newest start time first.
func (s *AdminStore) Batches(
	ctx context.Context, q admin.BatchQuery,
) (admin.BatchPage, error) {
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	clauses := make([]string, 0, 2)
	args := make([]any, 0, 3)
	bind := func(value any) string {
		args = append(args, value)
		return "$" + strconv.Itoa(len(args))
	}

	if q.Stage != "" {
		clauses = append(clauses, "stage = "+bind(q.Stage))
	}
	if !q.AfterStartedAt.IsZero() {
		clauses = append(clauses, "(started_at, id) < ("+bind(q.AfterStartedAt)+", "+bind(q.AfterID)+")")
	}

	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}

	statement := `SELECT ` + batchListColumns + `
		FROM ingestion_batches` + where + `
		ORDER BY started_at DESC, id DESC
		LIMIT ` + strconv.Itoa(q.Limit+1)

	rows, err := s.client.pool.Query(ctx, statement, args...)
	if err != nil {
		return admin.BatchPage{}, operationError("postgres: list batches", err)
	}
	items, err := scanBatchList(rows)
	if err != nil {
		return admin.BatchPage{}, err
	}

	hasNext := len(items) == q.Limit+1
	if hasNext {
		items = items[:q.Limit]
	}
	return admin.BatchPage{Items: items, HasMore: hasNext}, nil
}

// BatchDetail returns one audit record with its rejection breakdown.
func (s *AdminStore) BatchDetail(
	ctx context.Context, batchID int64, maxRejections int,
) (admin.BatchDetail, error) {
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	row := s.client.pool.QueryRow(ctx, `
		SELECT `+batchColumns+`
		FROM ingestion_batches
		WHERE id = $1`, batchID)

	var br batchRow
	if err := row.Scan(
		&br.ID, &br.Stage, &br.CurationVersion, &br.SourceFile, &br.SourceSHA256,
		&br.BoundaryVersion, &br.StartedAt, &br.FinishedAt, &br.DurationMS,
		&br.RowsRead, &br.Accepted, &br.Written, &br.Deduped, &br.Filtered,
		&br.Rejected, &br.Unmatched, &br.MissingFields, &br.Status, &br.ErrorCode,
		&br.DocumentsBuilt, &br.DocumentsEmbedded, &br.DocumentsRejected,
		&br.EmbeddingModel, &br.EmbeddingDimensions, &br.RejectReasons,
	); err != nil {
		if pgErrNoRows(err) {
			return admin.BatchDetail{}, errs.Newf(errs.CodeNotFound,
				"batch %d not found", batchID)
		}
		return admin.BatchDetail{}, operationError("postgres: batch detail", err)
	}

	report, err := br.toDomain()
	if err != nil {
		return admin.BatchDetail{}, err
	}

	out := admin.BatchDetail{
		BatchListItem:   batchListItemFromReport(report),
		CurationVersion: report.CurationVersion,
		ErrorCode:       report.ErrorCode,
		MissingFields:   jsonRaw(orEmptyArray(br.MissingFields)),
		RejectReasons:   report.RejectReasons,
		Rejections:      []admin.RejectionItem{},
	}
	if report.SourceFile != "" {
		out.SourceFile = report.SourceFile
	}
	if report.SourceSHA256 != "" {
		out.SourceSHA256 = report.SourceSHA256
	}
	if report.BoundaryVersion != "" {
		out.BoundaryVersion = report.BoundaryVersion
	}

	limit := maxRejections
	if limit <= 0 {
		limit = defaultMaxRejections
	}

	rejectionRows, err := s.client.pool.Query(ctx, `
		SELECT stage, line_no, reason, source_record_id
		FROM ingestion_rejections
		WHERE batch_id = $1
		ORDER BY line_no, id
		LIMIT `+strconv.Itoa(limit+1), batchID)
	if err != nil {
		return admin.BatchDetail{}, operationError("postgres: list rejections", err)
	}
	items, err := scanRejections(rejectionRows)
	if err != nil {
		return admin.BatchDetail{}, err
	}
	if len(items) == limit+1 {
		out.RejectionsTruncated = true
		items = items[:limit]
	}
	out.Rejections = items
	return out, nil
}

// ---------------------------------------------------------------------------
// Boundaries
// ---------------------------------------------------------------------------

// Boundaries lists administrative areas without their geometry.
func (s *AdminStore) Boundaries(ctx context.Context) ([]admin.Boundary, error) {
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	rows, err := s.client.pool.Query(ctx, `
		SELECT id, name, kind, source_url, checksum, loaded_at
		FROM boundaries
		ORDER BY name`)
	if err != nil {
		return nil, operationError("postgres: list boundaries", err)
	}
	defer rows.Close()

	out := make([]admin.Boundary, 0)
	for rows.Next() {
		var (
			item      admin.Boundary
			sourceURL *string
			checksum  *string
		)
		if err := rows.Scan(
			&item.ID, &item.Name, &item.Kind, &sourceURL, &checksum, &item.LoadedAt,
		); err != nil {
			return nil, operationError("postgres: scan boundary", err)
		}
		if sourceURL != nil {
			item.SourceURL = *sourceURL
		}
		if checksum != nil {
			item.Checksum = *checksum
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate boundaries", err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Row scanners and small mappers
// ---------------------------------------------------------------------------

// scanRestaurantList decodes a projected restaurant listing.
func scanRestaurantList(rows pgx.Rows) ([]admin.RestaurantListItem, error) {
	defer rows.Close()
	out := make([]admin.RestaurantListItem, 0)
	for rows.Next() {
		detail, err := scanRestaurantDetail(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, admin.RestaurantListItem{
			RestaurantID:   detail.RestaurantID,
			Name:           detail.Name,
			Address:        detail.Address,
			Borough:        detail.Borough,
			Cuisines:       nonNilStrings(detail.Cuisines),
			PriceLevel:     detail.PriceLevel,
			RatingComputed: detail.Rating,
			RatingCount:    detail.RatingCount,
			ActiveForDemo:  detail.IsActiveForDemo,
			KnowledgeScore: detail.KnowledgeScore,
			ObservedAt:     detail.SnapshotAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate restaurant listing", err)
	}
	return out, nil
}

// scanReviewList decodes a projected review listing.
func scanReviewList(rows pgx.Rows) ([]admin.ReviewListItem, error) {
	defer rows.Close()
	out := make([]admin.ReviewListItem, 0)
	for rows.Next() {
		var row reviewRow
		if err := rows.Scan(
			&row.ID, &row.RestaurantID, &row.Rating, &row.ReviewedAt, &row.Text,
			&row.Language, &row.TextHash, &row.IsRepresentative, &row.TopicTags,
			&row.SourceObservedAt,
		); err != nil {
			return nil, operationError("postgres: scan review listing", err)
		}
		domainReview := row.toDomain()
		out = append(out, admin.ReviewListItem{
			ReviewID:         domainReview.ID,
			RestaurantID:     domainReview.RestaurantID,
			Rating:           domainReview.Rating,
			ReviewedAt:       domainReview.ReviewedAt,
			Text:             domainReview.Text,
			Language:         domainReview.Language,
			IsRepresentative: domainReview.IsRepresentative,
			TopicTags:        nonNilStrings(domainReview.TopicTags),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate review listing", err)
	}
	return out, nil
}

// scanDocumentList decodes a projected document listing.
func scanDocumentList(rows pgx.Rows) ([]admin.DocumentListItem, error) {
	defer rows.Close()
	out := make([]admin.DocumentListItem, 0)
	for rows.Next() {
		var (
			item       admin.DocumentListItem
			title      *string
			model      *string
			dimensions *int32
			snapshotAt *time.Time
		)
		if err := rows.Scan(
			&item.DocumentID, &item.RestaurantID, &item.Scope, &item.DocType,
			&title, &item.ContentHash, &item.Version, &item.IsActive,
			&item.HasEmbedding, &model, &dimensions, &snapshotAt,
		); err != nil {
			return nil, operationError("postgres: scan document listing", err)
		}
		if title != nil {
			item.Title = *title
		}
		if model != nil {
			item.EmbeddingModel = *model
		}
		if dimensions != nil {
			item.EmbeddingDimensions = int(*dimensions)
		}
		if snapshotAt != nil {
			item.SnapshotAt = *snapshotAt
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate document listing", err)
	}
	return out, nil
}

// scanBatchList decodes a projected batch listing.
func scanBatchList(rows pgx.Rows) ([]admin.BatchListItem, error) {
	defer rows.Close()
	out := make([]admin.BatchListItem, 0)
	for rows.Next() {
		var (
			item       admin.BatchListItem
			finishedAt *time.Time
			built      *int64
			embedded   *int64
			rejected   *int64
			model      *string
			dimensions *int32
		)
		if err := rows.Scan(
			&item.BatchID, &item.Stage, &item.Status, &item.StartedAt,
			&finishedAt, &item.DurationMS,
			&item.RowsRead, &item.Accepted, &item.Written, &item.Deduped,
			&item.Filtered, &item.Rejected, &item.Unmatched,
			&built, &embedded, &rejected, &model, &dimensions,
		); err != nil {
			return nil, operationError("postgres: scan batch listing", err)
		}
		item.FinishedAt = finishedAt
		item.DocumentsBuilt = built
		item.DocumentsEmbedded = embedded
		item.DocumentsRejected = rejected
		if model != nil {
			item.EmbeddingModel = *model
		}
		item.EmbeddingDimensions = int32PtrToInt(dimensions)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate batch listing", err)
	}
	return out, nil
}

// batchListItemFromReport maps a domain audit report onto the list projection.
func batchListItemFromReport(r review.BatchReport) admin.BatchListItem {
	return admin.BatchListItem{
		BatchID:             r.BatchID,
		Stage:               r.Stage,
		Status:              r.Status,
		StartedAt:           r.StartedAt,
		FinishedAt:          finishTime(r),
		DurationMS:          r.DurationMS,
		RowsRead:            r.RowsRead,
		Accepted:            r.Accepted,
		Written:             r.Written,
		Deduped:             r.Deduped,
		Filtered:            r.Filtered,
		Rejected:            r.Rejected,
		Unmatched:           r.Unmatched,
		DocumentsBuilt:      r.DocumentsBuilt,
		DocumentsEmbedded:   r.DocumentsEmbedded,
		DocumentsRejected:   r.DocumentsRejected,
		EmbeddingModel:      r.EmbeddingModel,
		EmbeddingDimensions: r.EmbeddingDimensions,
	}
}

// finishTime returns a pointer to the finish time when it is set.
func finishTime(r review.BatchReport) *time.Time {
	if r.FinishedAt.IsZero() {
		return nil
	}
	finished := r.FinishedAt
	return &finished
}

// scanRejections decodes a rejection listing.
func scanRejections(rows pgx.Rows) ([]admin.RejectionItem, error) {
	defer rows.Close()
	out := make([]admin.RejectionItem, 0)
	for rows.Next() {
		var item admin.RejectionItem
		if err := rows.Scan(
			&item.Stage, &item.LineNo, &item.Reason, &item.SourceRecordID,
		); err != nil {
			return nil, operationError("postgres: scan rejection", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate rejections", err)
	}
	return out, nil
}

// nonNilStrings returns values as a non-nil slice so the wire form is an array.
func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// jsonRaw converts stored bytes into a raw JSON value, defaulting the zero
// case to an empty object payload supplied by the caller.
func jsonRaw(raw []byte) json.RawMessage {
	return json.RawMessage(raw)
}
