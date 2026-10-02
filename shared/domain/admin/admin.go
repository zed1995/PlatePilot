// Package admin defines the read-only view DTOs served by the administration
// console: dashboard counters, paged table listings, full detail projections,
// and the environment description.
//
// The package depends only on the standard library so the view shapes can be
// reasoned about and tested without a running service. Every collection is
// rendered as an array in the wire form, never as a null value.
package admin

import (
	"encoding/json"
	"time"
)

// ---------------------------------------------------------------------------
// Dashboard
// ---------------------------------------------------------------------------

// Overview is the whole dashboard payload. The counters are taken as of one
// read; the backing store may accept writes concurrently, so the numbers are
// not promised to be mutually consistent.
type Overview struct {
	Tables TableCounts `json:"tables"`
	// DocumentBreakdown counts active documents grouped by scope and kind.
	DocumentBreakdown []DocumentCount `json:"document_breakdown"`
	// ActiveWithoutVector counts active documents that carry no vector. A
	// healthy store reports zero; any other value means the data was changed
	// outside the writer.
	ActiveWithoutVector int64 `json:"active_documents_without_vector"`
	// RecentBatches carries the five most recent audit records.
	RecentBatches []BatchListItem `json:"recent_batches"`
	Migrations    []Migration     `json:"migrations"`
	Environment   Environment     `json:"environment"`
}

// TableCounts are the table-scale cards. The reviews figure is an estimate
// sourced from the planner's own row statistics rather than a full scan.
type TableCounts struct {
	RestaurantsTotal  int64 `json:"restaurants_total"`
	RestaurantsActive int64 `json:"restaurants_active"`
	ReviewsEstimate   int64 `json:"reviews_estimate"`
	ReviewsEstimated  bool  `json:"reviews_estimated"`
	DocumentsActive   int64 `json:"documents_active"`
	BatchesTotal      int64 `json:"batches_total"`
}

// DocumentCount is one row of the scope by kind breakdown.
type DocumentCount struct {
	RetrievalScope string `json:"retrieval_scope"`
	DocType        string `json:"doc_type"`
	Count          int64  `json:"count"`
}

// Migration identifies one applied schema change.
type Migration struct {
	Version   string    `json:"version"`
	AppliedAt time.Time `json:"applied_at"`
}

// Environment describes the serving process, not the stored data. It is filled
// in by the application layer from its own configuration.
type Environment struct {
	EmbeddingModel      string `json:"embedding_model"`
	EmbeddingDimensions int    `json:"embedding_dimensions"`
}

// ---------------------------------------------------------------------------
// Restaurants
// ---------------------------------------------------------------------------

// RestaurantQuery is one page of the restaurant listing. A zero value in an
// optional text field means that field is not filtered.
type RestaurantQuery struct {
	Cursor  string
	Limit   int
	Borough string
	Cuisine string
	// Active uses a pointer so "unset" differs from an explicit false.
	Active *bool
	Q      string
	// AfterID is the decoded keyset position: only rows with a smaller id are
	// returned. The application layer sets it from Cursor.
	AfterID int64
}

// RestaurantPage is one keyset page.
type RestaurantPage struct {
	Items         []RestaurantListItem `json:"items"`
	NextCursor    string               `json:"next_cursor,omitempty"`
	TotalEstimate int64                `json:"total_estimate"`
	// HasMore reports whether another page exists. It is not part of the
	// wire payload; the application layer uses it to emit the cursor.
	HasMore bool `json:"-"`
}

// RestaurantListItem is the lightweight projection of the listing. It carries
// neither large json objects nor review text.
type RestaurantListItem struct {
	RestaurantID   int64     `json:"restaurant_id"`
	Name           string    `json:"name"`
	Address        string    `json:"address,omitempty"`
	Borough        string    `json:"borough,omitempty"`
	Cuisines       []string  `json:"cuisines"`
	PriceLevel     *int      `json:"price_level,omitempty"`
	RatingComputed *float64  `json:"rating_computed_avg,omitempty"`
	RatingCount    int       `json:"rating_count"`
	ActiveForDemo  bool      `json:"is_active_for_demo"`
	KnowledgeScore float64   `json:"knowledge_score"`
	ObservedAt     time.Time `json:"observed_at"`
}

// RestaurantDetail is the full projection for the detail page.
type RestaurantDetail struct {
	RestaurantID   int64    `json:"restaurant_id"`
	Source         string   `json:"source"`
	SourceRecordID string   `json:"source_record_id"`
	Name           string   `json:"name"`
	Address        string   `json:"address,omitempty"`
	Borough        string   `json:"borough,omitempty"`
	Longitude      *float64 `json:"longitude,omitempty"`
	Latitude       *float64 `json:"latitude,omitempty"`
	Description    string   `json:"description,omitempty"`

	Categories []string `json:"categories"`
	Cuisines   []string `json:"cuisines"`

	PriceRaw   string `json:"price_raw,omitempty"`
	PriceLevel *int   `json:"price_level,omitempty"`

	RatingSourceAvg *float64 `json:"rating_source_avg,omitempty"`
	RatingComputed  *float64 `json:"rating_computed_avg,omitempty"`
	RatingCount     int      `json:"rating_count"`

	SourceReviewCount         int        `json:"source_review_count"`
	SourceReviewCountCapped   bool       `json:"source_review_count_capped"`
	StoredReviewCount         int        `json:"stored_review_count"`
	TextReviewCount           int        `json:"text_review_count"`
	RepresentativeReviewCount int        `json:"representative_review_count"`
	EmbeddedReviewCount       int        `json:"embedded_review_count"`
	LastReviewedAt            *time.Time `json:"last_reviewed_at,omitempty"`
	StatsUpdatedAt            *time.Time `json:"stats_updated_at,omitempty"`

	// Attributes and Hours are passed through untouched for the JSON viewer.
	Attributes json.RawMessage `json:"attributes"`
	Hours      json.RawMessage `json:"hours"`

	// ActiveForDemo mirrors the listing projection so the detail page's
	// status badge and the list never disagree about the same row.
	ActiveForDemo  bool      `json:"is_active_for_demo"`
	SnapshotStatus string    `json:"snapshot_status"`
	KnowledgeScore float64   `json:"knowledge_score"`
	ObservedAt     time.Time `json:"observed_at"`
	SourceURL      string    `json:"source_url,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// Reviews
// ---------------------------------------------------------------------------

// ReviewQuery is one page of one restaurant's reviews.
type ReviewQuery struct {
	Cursor       string
	Limit        int
	RestaurantID int64
	// AfterReviewedAt and AfterID carry the decoded keyset position. The
	// application layer sets them from Cursor.
	AfterReviewedAt time.Time
	AfterID         int64
}

// ReviewPage is one keyset page of reviews.
type ReviewPage struct {
	Items      []ReviewListItem `json:"items"`
	NextCursor string           `json:"next_cursor,omitempty"`
	// HasMore reports whether another page exists. It is not serialized.
	HasMore bool `json:"-"`
}

// ReviewListItem is one curated review. It carries no reviewer identity.
type ReviewListItem struct {
	ReviewID         int64     `json:"review_id"`
	RestaurantID     int64     `json:"restaurant_id"`
	Rating           int       `json:"rating"`
	ReviewedAt       time.Time `json:"reviewed_at"`
	Text             string    `json:"text"`
	Language         string    `json:"language,omitempty"`
	IsRepresentative bool      `json:"is_representative"`
	TopicTags        []string  `json:"topic_tags"`
}

// ---------------------------------------------------------------------------
// Review summaries
// ---------------------------------------------------------------------------

// ReviewSummary is one per-topic rollup.
type ReviewSummary struct {
	RestaurantID  int64     `json:"restaurant_id"`
	Topic         string    `json:"topic"`
	Sentiment     float64   `json:"sentiment"`
	PositiveRatio float64   `json:"positive_ratio"`
	Summary       string    `json:"summary"`
	EvidenceCount int       `json:"evidence_count"`
	GeneratedBy   string    `json:"generated_by"`
	GeneratedAt   time.Time `json:"generated_at"`
}

// ---------------------------------------------------------------------------
// Knowledge documents
// ---------------------------------------------------------------------------

// DocumentQuery is one page of the document listing. Inactive documents are
// included by default: one purpose of this console is to inspect superseded
// versions, which is the opposite of the recall path's default.
type DocumentQuery struct {
	Cursor       string
	Limit        int
	RestaurantID int64
	Scope        string
	DocType      string
	// IsActive and HasEmbedding use pointers so "unset" is a distinct state.
	IsActive     *bool
	HasEmbedding *bool
	// AfterID is the decoded keyset position, set from Cursor by the
	// application layer.
	AfterID int64
}

// DocumentPage is one keyset page of documents.
type DocumentPage struct {
	Items      []DocumentListItem `json:"items"`
	NextCursor string             `json:"next_cursor,omitempty"`
	// HasMore reports whether another page exists. It is not serialized.
	HasMore bool `json:"-"`
}

// DocumentListItem is the listing projection. It carries no body text.
type DocumentListItem struct {
	DocumentID          int64     `json:"document_id"`
	RestaurantID        int64     `json:"restaurant_id"`
	Scope               string    `json:"retrieval_scope"`
	DocType             string    `json:"doc_type"`
	Title               string    `json:"title,omitempty"`
	ContentHash         string    `json:"content_hash"`
	Version             int       `json:"version"`
	IsActive            bool      `json:"is_active"`
	HasEmbedding        bool      `json:"has_embedding"`
	EmbeddingModel      string    `json:"embedding_model,omitempty"`
	EmbeddingDimensions int       `json:"embedding_dimensions,omitempty"`
	SnapshotAt          time.Time `json:"snapshot_at,omitempty"`
}

// DocumentSummary is the shape used when listing one restaurant's documents.
// It is the same lightweight projection as the global listing.
type DocumentSummary = DocumentListItem

// DocumentDetail is the full projection for one document.
type DocumentDetail struct {
	DocumentListItem
	// Content is the full body, loaded on demand.
	Content string `json:"content"`
	// Metadata is passed through untouched for the JSON viewer.
	Metadata json.RawMessage `json:"metadata"`
	// SourceRecordIDs names the source records the document was derived from.
	SourceRecordIDs []string `json:"source_record_ids"`
	// VectorPreview carries the first few components of the stored vector,
	// populated only when the caller asks for it.
	VectorPreview []float32 `json:"vector_preview,omitempty"`
}

// ---------------------------------------------------------------------------
// Ingestion audit
// ---------------------------------------------------------------------------

// BatchQuery is one page of the batch listing.
type BatchQuery struct {
	Cursor string
	Limit  int
	Stage  string
	// AfterStartedAt and AfterID carry the decoded keyset position. The
	// application layer sets them from Cursor.
	AfterStartedAt time.Time
	AfterID        int64
}

// BatchPage is one keyset page of batches.
type BatchPage struct {
	Items      []BatchListItem `json:"items"`
	NextCursor string          `json:"next_cursor,omitempty"`
	// HasMore reports whether another page exists. It is not serialized.
	HasMore bool `json:"-"`
}

// BatchListItem is the listing projection of one audit record.
type BatchListItem struct {
	BatchID    int64      `json:"batch_id"`
	Stage      string     `json:"stage"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	DurationMS int64      `json:"duration_ms"`

	RowsRead  int64 `json:"rows_read"`
	Accepted  int64 `json:"accepted"`
	Written   int64 `json:"written"`
	Deduped   int64 `json:"deduped"`
	Filtered  int64 `json:"filtered"`
	Rejected  int64 `json:"rejected"`
	Unmatched int64 `json:"unmatched"`

	// The document and vector counters are null for the stages they do not
	// apply to.
	DocumentsBuilt      *int64 `json:"documents_built,omitempty"`
	DocumentsEmbedded   *int64 `json:"documents_embedded,omitempty"`
	DocumentsRejected   *int64 `json:"documents_rejected,omitempty"`
	EmbeddingModel      string `json:"embedding_model,omitempty"`
	EmbeddingDimensions *int   `json:"embedding_dimensions,omitempty"`
}

// BatchDetail is the full projection for one audit record.
type BatchDetail struct {
	BatchListItem

	CurationVersion string `json:"curation_version"`
	SourceFile      string `json:"source_file,omitempty"`
	SourceSHA256    string `json:"source_sha256,omitempty"`
	BoundaryVersion string `json:"boundary_version,omitempty"`
	ErrorCode       string `json:"error_code,omitempty"`

	// MissingFields is passed through untouched for the JSON viewer.
	MissingFields json.RawMessage `json:"missing_fields"`
	// RejectReasons counts rejected rows by reason. A null map means the
	// stage ran no quality check, which differs from an empty one.
	RejectReasons map[string]int64 `json:"reject_reasons,omitempty"`

	Rejections          []RejectionItem `json:"rejections"`
	RejectionsTruncated bool            `json:"rejections_truncated"`
}

// RejectionItem is one rejected source line, without any content from it.
type RejectionItem struct {
	Stage          string `json:"stage"`
	LineNo         int64  `json:"line_no"`
	Reason         string `json:"reason"`
	SourceRecordID string `json:"source_record_id,omitempty"`
}

// ---------------------------------------------------------------------------
// Boundaries
// ---------------------------------------------------------------------------

// Boundary is one administrative area. The area geometry is intentionally
// omitted; the console only needs the identifying fields.
type Boundary struct {
	ID        int64     `json:"boundary_id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	SourceURL string    `json:"source_url,omitempty"`
	Checksum  string    `json:"checksum,omitempty"`
	LoadedAt  time.Time `json:"loaded_at"`
}
