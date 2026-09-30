// Package review defines the curated review, review-summary, and ingestion
// audit DTOs produced by the data pipeline.
//
// The package is part of the pure domain layer: it depends only on the standard
// library.
package review

import "time"

// Review is a curated review that is safe to store and to cite. Raw identity
// fields (user_id, name, pics) are deliberately absent: user_id only ever
// participates in the deterministic review ID and is never persisted here.
type Review struct {
	ID               int64     `json:"review_id"`
	RestaurantID     int64     `json:"restaurant_id"`
	Rating           int       `json:"rating"`
	ReviewedAt       time.Time `json:"reviewed_at"`
	Text             string    `json:"text"`
	Language         string    `json:"language,omitempty"`
	TextHash         string    `json:"text_hash"`
	IsRepresentative bool      `json:"is_representative"`
	TopicTags        []string  `json:"topic_tags,omitempty"`
	SourceObservedAt time.Time `json:"source_observed_at"`
}

// Counts is the rollup of stored reviews for a single restaurant.
type Counts struct {
	StoredCount         int64         `json:"stored_count"`
	TextCount           int64         `json:"text_count"`
	RepresentativeCount int64         `json:"representative_count"`
	LastReviewedAt      *time.Time    `json:"last_reviewed_at,omitempty"`
	ComputedAvg         *float64      `json:"computed_avg,omitempty"`
	RatingDistribution  map[int]int64 `json:"rating_distribution,omitempty"`
}

// Summary is a precomputed topic/sentiment rollup. The pipeline generates it
// from deterministic rules; an optional LLM enhancement records its provenance
// in GeneratedBy.
type Summary struct {
	RestaurantID  int64     `json:"restaurant_id"`
	Topic         string    `json:"topic"`
	Sentiment     float64   `json:"sentiment"`
	PositiveRatio float64   `json:"positive_ratio"`
	Summary       string    `json:"summary"`
	EvidenceCount int       `json:"evidence_count"`
	ValidFrom     time.Time `json:"valid_from"`
	ValidTo       time.Time `json:"valid_to"`
	GeneratedBy   string    `json:"generated_by"`
	GeneratedAt   time.Time `json:"generated_at"`
}

// Ingestion stage identifiers.
const (
	StageMeta      = "meta"
	StageReview    = "review"
	StageStats     = "stats"
	StageDocuments = "documents"
	StageEmbedding = "embedding"
	StageAll       = "all"
)

// Batch status values.
const (
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
)

// BatchReport is the queryable audit record for one import run. It records
// counts only: raw review text and PII are never written here.
type BatchReport struct {
	BatchID         int64          `json:"batch_id"`
	Stage           string         `json:"stage"`
	CurationVersion string         `json:"curation_version"`
	SourceFile      string         `json:"source_file,omitempty"`
	SourceSHA256    string         `json:"source_sha256,omitempty"`
	BoundaryVersion string         `json:"boundary_version,omitempty"`
	StartedAt       time.Time      `json:"started_at"`
	FinishedAt      time.Time      `json:"finished_at,omitempty"`
	DurationMS      int64          `json:"duration_ms"`
	RowsRead        int64          `json:"rows_read"`
	Accepted        int64          `json:"accepted"`
	Written         int64          `json:"written"`
	Deduped         int64          `json:"deduped"`
	Filtered        int64          `json:"filtered"`
	Rejected        int64          `json:"rejected"`
	Unmatched       int64          `json:"unmatched"`
	MissingFields   []FieldMissing `json:"missing_fields,omitempty"`
	Status          string         `json:"status"`
	ErrorCode       string         `json:"error_code,omitempty"`

	// The M2 fields below are populated only by the document and embedding
	// stages. They are pointers or nil maps so an M1 row reads as "this stage
	// did not apply" rather than as a run that produced zero documents.
	DocumentsBuilt      *int64           `json:"documents_built,omitempty"`
	DocumentsEmbedded   *int64           `json:"documents_embedded,omitempty"`
	DocumentsRejected   *int64           `json:"documents_rejected,omitempty"`
	EmbeddingModel      string           `json:"embedding_model,omitempty"`
	EmbeddingDimensions *int             `json:"embedding_dimensions,omitempty"`
	RejectReasons       map[string]int64 `json:"reject_reasons,omitempty"`
}

// FieldMissing counts how often a required field was absent in a batch.
type FieldMissing struct {
	Field string `json:"field"`
	Count int64  `json:"count"`
}

// Rejection records a single rejected source line. It stores only the line
// number, reason, and source identifier so that no PII is persisted.
type Rejection struct {
	BatchID        int64  `json:"batch_id"`
	Stage          string `json:"stage"`
	LineNo         int64  `json:"line_no"`
	Reason         string `json:"reason"`
	SourceRecordID string `json:"source_record_id,omitempty"`
}
