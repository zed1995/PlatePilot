package memory

import (
	"context"
	"math"
	"sort"
	"sync"

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/evidence"
)

// KnowledgeRepository is an in-memory port.KnowledgeRepository.
type KnowledgeRepository struct {
	mu   sync.RWMutex
	docs map[int64]evidence.KnowledgeDocument
}

// NewKnowledgeRepository returns an empty in-memory knowledge repository.
func NewKnowledgeRepository() *KnowledgeRepository {
	return &KnowledgeRepository{docs: make(map[int64]evidence.KnowledgeDocument)}
}

// UpsertDocuments inserts or replaces knowledge documents by document ID.
func (r *KnowledgeRepository) UpsertDocuments(_ context.Context, docs []evidence.KnowledgeDocument) error {
	for _, doc := range docs {
		if doc.DocumentID <= 0 {
			return errs.New(errs.CodeInvalidArgument, "knowledge document_id is required")
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, doc := range docs {
		r.docs[doc.DocumentID] = doc
	}
	return nil
}

// FindEvidenceByRestaurant returns active evidence documents for one restaurant.
func (r *KnowledgeRepository) FindEvidenceByRestaurant(_ context.Context, restaurantID int64) ([]evidence.Evidence, error) {
	if restaurantID == 0 {
		return nil, errs.New(errs.CodeInvalidArgument, "restaurant_id is required")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]evidence.Evidence, 0)
	for _, doc := range r.docs {
		if doc.RestaurantID != restaurantID || doc.Scope != evidence.ScopeEvidence || !doc.IsActive {
			continue
		}
		out = append(out, doc.ToEvidence(0))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].EvidenceID < out[j].EvidenceID })
	return out, nil
}

// VectorSearch returns the closest active documents in one retrieval scope.
// Scoring is cosine similarity so the behaviour mirrors the pgvector operator.
func (r *KnowledgeRepository) VectorSearch(_ context.Context, scope evidence.RetrievalScope, query []float32, topK int, filter map[string]any) ([]evidence.Evidence, error) {
	if len(query) == 0 {
		return nil, errs.New(errs.CodeInvalidArgument, "query vector must not be empty")
	}
	if scope != evidence.ScopeRestaurant && scope != evidence.ScopeEvidence {
		return nil, errs.Newf(errs.CodeInvalidArgument, "unknown retrieval scope %q", scope)
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	type scored struct {
		ev    evidence.Evidence
		score float64
	}
	matches := make([]scored, 0)
	for _, doc := range r.docs {
		if doc.Scope != scope || !doc.IsActive || len(doc.Embedding) != len(query) {
			continue
		}
		if !matchesMetadata(doc, filter) {
			continue
		}
		similarity := cosine(query, doc.Embedding)
		matches = append(matches, scored{ev: doc.ToEvidence(similarity), score: similarity})
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].score > matches[j].score })

	if topK > 0 && len(matches) > topK {
		matches = matches[:topK]
	}
	out := make([]evidence.Evidence, 0, len(matches))
	for _, match := range matches {
		out = append(out, match.ev)
	}
	return out, nil
}

func matchesMetadata(doc evidence.KnowledgeDocument, filter map[string]any) bool {
	for key, want := range filter {
		if key == "restaurant_id" {
			// The port carries a restaurant id as int64, so a caller that
			// built the filter from a JSON payload may have typed it loosely.
			id, ok := toRestaurantID(want)
			if !ok || doc.RestaurantID != id {
				return false
			}
			continue
		}
		if doc.Metadata == nil || doc.Metadata[key] != want {
			return false
		}
	}
	return true
}

// toRestaurantID normalises the loosely typed values a metadata filter can
// carry into the int64 the document stores.
func toRestaurantID(want any) (int64, bool) {
	switch value := want.(type) {
	case int64:
		return value, true
	case int:
		return int64(value), true
	case float64:
		// A whole number survives the round trip that JSON decoding does; a
		// fractional value is a type error, not a rounded id.
		if value != math.Trunc(value) {
			return 0, false
		}
		return int64(value), true
	default:
		return 0, false
	}
}

func cosine(a, b []float32) float64 {
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
