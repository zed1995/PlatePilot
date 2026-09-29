// Package evidence defines the cited, retrievable knowledge DTOs.
package evidence

import "time"

// RetrievalScope separates restaurant-level recall documents from evidence
// documents. Queries must always filter on exactly one scope.
type RetrievalScope string

const (
	ScopeRestaurant RetrievalScope = "restaurant"
	ScopeEvidence   RetrievalScope = "evidence"
)

// DocType enumerates the knowledge document kinds defined in the PRD.
type DocType string

const (
	DocTypeRestaurantProfile               DocType = "restaurant_profile"
	DocTypeRestaurantAttributes            DocType = "restaurant_attributes"
	DocTypeRestaurantHours                 DocType = "restaurant_hours"
	DocTypeRestaurantReviewSummary         DocType = "restaurant_review_summary"
	DocTypeRestaurantRepresentativeReviews DocType = "restaurant_representative_reviews"
)

// Evidence is a cited fragment returned by retrieval. Every value carries its
// source and snapshot time so answers can be grounded and explained.
type Evidence struct {
	EvidenceID      string    `json:"evidence_id"`
	RestaurantID    string    `json:"restaurant_id"`
	DocType         DocType   `json:"doc_type"`
	Title           string    `json:"title,omitempty"`
	Content         string    `json:"content"`
	SourceRecordIDs []string  `json:"source_record_ids,omitempty"`
	Source          string    `json:"source"`
	SnapshotAt      time.Time `json:"snapshot_at"`
	Score           float64   `json:"score,omitempty"`
}

// KnowledgeDocument is an embeddable knowledge chunk stored in Atlas.
type KnowledgeDocument struct {
	DocumentID          string         `json:"document_id"`
	RestaurantID        string         `json:"restaurant_id"`
	Scope               RetrievalScope `json:"retrieval_scope"`
	DocType             DocType        `json:"doc_type"`
	Title               string         `json:"title,omitempty"`
	Content             string         `json:"content"`
	ContentHash         string         `json:"content_hash,omitempty"`
	Embedding           []float32      `json:"embedding,omitempty"`
	EmbeddingModel      string         `json:"embedding_model,omitempty"`
	EmbeddingDimensions int            `json:"embedding_dimensions,omitempty"`
	Metadata            map[string]any `json:"metadata,omitempty"`
	SourceRecordIDs     []string       `json:"source_record_ids,omitempty"`
	SnapshotAt          time.Time      `json:"snapshot_at,omitempty"`
	Version             int            `json:"version,omitempty"`
	IsActive            bool           `json:"is_active"`
}

// ToEvidence projects a knowledge document onto a citable evidence fragment.
// The source falls back to the document metadata so citations always resolve.
func (d KnowledgeDocument) ToEvidence(score float64) Evidence {
	source, _ := d.Metadata["source"].(string)
	return Evidence{
		EvidenceID:      d.DocumentID,
		RestaurantID:    d.RestaurantID,
		DocType:         d.DocType,
		Title:           d.Title,
		Content:         d.Content,
		SourceRecordIDs: d.SourceRecordIDs,
		Source:          source,
		SnapshotAt:      d.SnapshotAt,
		Score:           score,
	}
}
