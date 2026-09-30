package port

import (
	"context"

	"github.com/zed/platepilot/shared/domain/evidence"
	"github.com/zed/platepilot/shared/domain/restaurant"
	"github.com/zed/platepilot/shared/domain/review"
)

// RestaurantStore is the write-side restaurant port used by the data pipeline.
//
// It is deliberately separate from RestaurantRepository (the read/search port):
// the implementation plan requires the write path (data-pipeline) and the read
// path (chat-service) to stay decoupled. Both may target the same database,
// but neither service implements the other's port.
type RestaurantStore interface {
	// UpsertRestaurant inserts or replaces one restaurant keyed by
	// source_record_id, preserving created_at on update.
	UpsertRestaurant(ctx context.Context, r restaurant.Restaurant) error
	// UpsertRestaurants upserts a batch and returns the number of documents
	// written (inserted or updated).
	UpsertRestaurants(ctx context.Context, rs []restaurant.Restaurant) (int, error)
	// GetByID returns the curated restaurant by its internal id.
	GetByID(ctx context.Context, restaurantID int64) (restaurant.Restaurant, error)
	// GetBySourceRecordID returns the curated restaurant for a gmap_id.
	GetBySourceRecordID(ctx context.Context, sourceRecordID string) (restaurant.Restaurant, error)
	// ListRestaurants returns restaurants ordered by source_record_id. A
	// non-positive limit means "all".
	ListRestaurants(ctx context.Context, limit int) ([]restaurant.Restaurant, error)
	// ListSourceRecordIDs returns every gmap_id in the store. Preprocessing
	// stages need the whole id set to decide which source rows are joinable,
	// and they must fetch it in one projection-only pass: resolving ids one
	// review at a time would issue millions of point queries.
	ListSourceRecordIDs(ctx context.Context) ([]string, error)
	// MapSourceRecordIDs resolves gmap_ids to restaurant ids in bulk so the
	// review join does not issue one query per review.
	MapSourceRecordIDs(ctx context.Context, sourceRecordIDs []string) (map[string]int64, error)
	// UpdateReviewStats writes the materialised review_stats and the computed
	// rating for one restaurant. The two are written together because they are
	// derived from the same review sample.
	UpdateReviewStats(ctx context.Context, restaurantID int64, stats restaurant.ReviewStats, computed restaurant.Rating) error
	// UpdateScores writes knowledge_score and is_active_for_demo for a batch.
	UpdateScores(ctx context.Context, scores map[int64]float64, active map[int64]bool) error
	// SelectForDemo returns active-for-demo restaurants ordered by score.
	SelectForDemo(ctx context.Context, limit int) ([]restaurant.Restaurant, error)
	// CountActiveForDemo reports how many restaurants are active for the demo.
	CountActiveForDemo(ctx context.Context) (int64, error)
}

// ReviewStore is the write-side review port used by the data pipeline.
type ReviewStore interface {
	// UpsertReviews upserts a batch keyed by the deterministic review ID and
	// returns the number of documents written.
	UpsertReviews(ctx context.Context, items []review.Review) (int, error)
	// ListByRestaurant returns a restaurant's reviews, newest first.
	ListByRestaurant(ctx context.Context, restaurantID int64, limit int) ([]review.Review, error)
	// CountByRestaurant returns the materialisable rollup for one restaurant.
	CountByRestaurant(ctx context.Context, restaurantID int64) (review.Counts, error)
	// AggregateStats returns rollups for the given restaurant IDs.
	AggregateStats(ctx context.Context, restaurantIDs []int64) (map[int64]review.Counts, error)
	// RestaurantIDsWithReviews returns restaurant IDs that have at least one
	// stored review, so the pipeline can rebuild review_stats in batches.
	RestaurantIDsWithReviews(ctx context.Context) ([]int64, error)

	// MarkRepresentative flags reviews as representative.
	//
	// The flag is relative, not permanent: clearing the restaurant's other
	// rows and setting the selected ones happens in the same call, because a
	// review left flagged from an earlier selection would keep inflating the
	// representative count.
	MarkRepresentative(ctx context.Context, reviewIDs []int64) (int, error)

	// UpsertSummaries writes per-topic review rollups. Re-running the summary
	// stage replaces the rows for the given keys rather than accumulating.
	UpsertSummaries(ctx context.Context, items []review.Summary) (int, error)

	// GetSummaries returns the stored rollups for one restaurant.
	GetSummaries(ctx context.Context, restaurantID int64) ([]review.Summary, error)
}

// PipelineStore persists ingestion audit records (batch reports + rejections).
type PipelineStore interface {
	// StartBatch records the start of a run and returns the database-assigned
	// batch id, which every rejection of that run must reference.
	StartBatch(ctx context.Context, report review.BatchReport) (int64, error)
	FinishBatch(ctx context.Context, report review.BatchReport) error
	RecordRejections(ctx context.Context, items []review.Rejection) error
	ListBatches(ctx context.Context, limit int) ([]review.BatchReport, error)
	BatchDetail(ctx context.Context, batchID int64) (review.BatchReport, []review.Rejection, error)
}

// KnowledgeStore is the write-side port for the M2 retrieval documents.
//
// It is deliberately separate from KnowledgeRepository, which is the read
// path the chat service uses: that one answers "what did we learn about this
// restaurant", while this one owns versioning and vector writes for the
// pipeline. Mixing them would put document lifecycle rules behind a port whose
// job is to look things up.
type KnowledgeStore interface {
	// UpsertDocuments inserts new document versions and returns how many rows
	// were inserted, updated, and skipped as already-present.
	//
	// Implementations must be idempotent on (restaurant_id, retrieval_scope,
	// doc_type, content_hash): an unchanged re-run writes nothing. Changed
	// content becomes a new version rather than overwriting the old row, so a
	// citation issued before the change still resolves.
	UpsertDocuments(ctx context.Context, docs []evidence.KnowledgeDocument) (UpsertResult, error)

	// PendingDocuments returns active documents with no vector, oldest first,
	// for the embedding stage to fill.
	PendingDocuments(ctx context.Context, limit int) ([]evidence.KnowledgeDocument, error)

	// SetEmbedding writes vectors for existing document ids and stamps the
	// model and dimension that produced them.
	//
	// The dimensions argument is checked against the table's vector width by
	// the database; the caller validates it first so a mismatch is reported
	// against the configuration instead of the driver.
	SetEmbedding(ctx context.Context, docIDs []int64, vectors [][]float32, model string, dimensions int) (int, error)

	// ActivateDocuments switches which version of a (restaurant, scope,
	// doc_type) group is live.
	//
	// Callers must deactivate the superseded rows and activate the new ones in
	// the same transaction: a window where a group has no active row is a
	// retrieval black hole, and a window where it has two is a contradiction
	// the table cannot detect.
	ActivateDocuments(ctx context.Context, docIDs []int64, active bool) (int, error)

	// ListByRestaurant returns a restaurant's documents in one scope, newest
	// version first. It exists for verification and debugging.
	ListByRestaurant(ctx context.Context, restaurantID int64, scope evidence.RetrievalScope) ([]evidence.KnowledgeDocument, error)

	// DistinctEmbeddingModels returns the distinct model identifiers currently
	// recorded on active documents, so the embedding stage can refuse to mix
	// two models in one live set.
	DistinctEmbeddingModels(ctx context.Context) ([]EmbeddingModelInfo, error)
}

// UpsertResult reports what UpsertDocuments actually did.
type UpsertResult struct {
	Inserted int
	Updated  int
	Skipped  int
}

// EmbeddingModelInfo is one model/dimension pair present in the store.
type EmbeddingModelInfo struct {
	Model      string
	Dimensions int
	Documents  int
}
