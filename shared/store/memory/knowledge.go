package memory

import (
	"context"
	"math"
	"sort"
	"sync"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/store"
)

// KnowledgeRepository is an in-memory store.KnowledgeRepository.
type KnowledgeRepository struct {
	mu   sync.RWMutex
	docs map[int64]evidence.KnowledgeDocument
}

// NewKnowledgeRepository returns an empty in-memory knowledge repository.
func NewKnowledgeRepository() *KnowledgeRepository {
	return &KnowledgeRepository{docs: make(map[int64]evidence.KnowledgeDocument)}
}

// UpsertDocuments inserts or replaces knowledge documents by document ID.
//
// It is not part of store.KnowledgeRepository: the read side of this system never
// writes. The method exists so tests can seed a repository through one call
// rather than reaching for the pipeline's store.
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

// FindEvidenceByRestaurant returns active evidence documents for one restaurant,
// optionally narrowed to one review topic.
func (r *KnowledgeRepository) FindEvidenceByRestaurant(_ context.Context, restaurantID int64, topic string) ([]evidence.Evidence, error) {
	if restaurantID <= 0 {
		return nil, errs.New(errs.CodeInvalidArgument, "restaurant_id is required")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]evidence.Evidence, 0)
	for _, doc := range r.docs {
		if doc.RestaurantID != restaurantID || doc.Scope != evidence.ScopeEvidence || !doc.IsActive {
			continue
		}
		ev := doc.ToEvidence(0)
		if topic != "" && ev.Topic != topic {
			continue
		}
		out = append(out, ev)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].EvidenceID < out[j].EvidenceID })
	return out, nil
}

// VectorSearch returns the closest active documents in one retrieval scope.
// Scoring is cosine similarity so the behaviour mirrors the store's operator.
func (r *KnowledgeRepository) VectorSearch(_ context.Context, req store.VectorSearchRequest) ([]store.ScoredDocument, error) {
	if len(req.Query) == 0 {
		return nil, errs.New(errs.CodeInvalidArgument, "query vector must not be empty")
	}
	if req.Scope != evidence.ScopeRestaurant && req.Scope != evidence.ScopeEvidence {
		return nil, errs.Newf(errs.CodeInvalidArgument, "unknown retrieval scope %q", req.Scope)
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	types := make(map[evidence.DocType]struct{}, len(req.DocTypes))
	for _, docType := range req.DocTypes {
		types[docType] = struct{}{}
	}
	ids := make(map[int64]struct{}, len(req.RestaurantIDs))
	for _, id := range req.RestaurantIDs {
		ids[id] = struct{}{}
	}

	matches := make([]store.ScoredDocument, 0)
	for _, doc := range r.docs {
		if doc.Scope != req.Scope || !doc.IsActive || len(doc.Embedding) != len(req.Query) {
			continue
		}
		if _, wanted := ids[doc.RestaurantID]; len(ids) > 0 && !wanted {
			continue
		}
		if _, wanted := types[doc.DocType]; len(types) > 0 && !wanted {
			continue
		}
		if req.Borough != "" && boroughOf(doc) != req.Borough {
			continue
		}
		if req.Topic != "" && topicOf(doc) != req.Topic {
			continue
		}
		similarity := cosine(req.Query, doc.Embedding)
		matches = append(matches, store.ScoredDocument{KnowledgeDocument: doc, Distance: 1 - similarity})
	}
	// Ties break on document id so the same seed returns the same order, which
	// is what lets the retrieval tests assert on ranks.
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Distance != matches[j].Distance {
			return matches[i].Distance < matches[j].Distance
		}
		return matches[i].DocumentID < matches[j].DocumentID
	})

	if req.TopK > 0 && len(matches) > req.TopK {
		matches = matches[:req.TopK]
	}
	return matches, nil
}

// RecallEvidence returns citable evidence for a bounded restaurant set.
//
// It mirrors the store's contract rather than inventing one for tests: the
// restaurant set is a precondition, an empty query is the unordered read, and
// the returned evidence carries the similarity the store would have computed.
func (r *KnowledgeRepository) RecallEvidence(
	_ context.Context, req store.EvidenceRequest,
) ([]evidence.Evidence, error) {
	if len(req.RestaurantIDs) == 0 {
		return nil, errs.New(errs.CodeRetrievalNoScope,
			"evidence recall must name the restaurants it is asking about")
	}

	if len(req.Query) == 0 {
		ids := make(map[int64]struct{}, len(req.RestaurantIDs))
		for _, id := range req.RestaurantIDs {
			ids[id] = struct{}{}
		}
		types := make(map[evidence.DocType]struct{}, len(req.DocTypes))
		for _, docType := range req.DocTypes {
			types[docType] = struct{}{}
		}

		r.mu.RLock()
		defer r.mu.RUnlock()
		out := make([]evidence.Evidence, 0)
		for _, doc := range r.docs {
			if _, wanted := ids[doc.RestaurantID]; !wanted {
				continue
			}
			if doc.Scope != evidence.ScopeEvidence || !doc.IsActive {
				continue
			}
			if _, wanted := types[doc.DocType]; len(types) > 0 && !wanted {
				continue
			}
			item := doc.ToEvidence(0)
			if req.Topic != "" && item.Topic != req.Topic {
				continue
			}
			out = append(out, item)
		}
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].RestaurantID != out[j].RestaurantID {
				return out[i].RestaurantID < out[j].RestaurantID
			}
			if out[i].DocType != out[j].DocType {
				return out[i].DocType < out[j].DocType
			}
			return out[i].EvidenceID < out[j].EvidenceID
		})
		if req.TopK > 0 && len(out) > req.TopK {
			out = out[:req.TopK]
		}
		return out, nil
	}

	docs, err := r.VectorSearch(context.Background(), store.VectorSearchRequest{
		Scope:         evidence.ScopeEvidence,
		Query:         req.Query,
		TopK:          req.TopK,
		RestaurantIDs: req.RestaurantIDs,
		DocTypes:      req.DocTypes,
		Topic:         req.Topic,
	})
	if err != nil {
		return nil, err
	}
	out := make([]evidence.Evidence, 0, len(docs))
	for _, doc := range docs {
		similarity := 1 - doc.Distance
		if similarity < 0 {
			similarity = 0
		}
		if similarity > 1 {
			similarity = 1
		}
		out = append(out, doc.ToEvidence(similarity))
	}
	return out, nil
}

// FindEvidenceByIDs returns the active evidence documents named by ids.
//
// Order follows the request rather than the stored document order: the caller
// received these ids in citation order and is putting them back on screen in
// that order, so re-sorting here would silently renumber the answer's
// footnotes.
func (r *KnowledgeRepository) FindEvidenceByIDs(
	_ context.Context, ids []int64,
) ([]evidence.Evidence, error) {
	byID := make(map[int64]evidence.KnowledgeDocument, len(r.docs))
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, doc := range r.docs {
		if doc.Scope != evidence.ScopeEvidence || !doc.IsActive {
			continue
		}
		byID[doc.DocumentID] = doc
	}
	out := make([]evidence.Evidence, 0, len(ids))
	for _, id := range ids {
		doc, ok := byID[id]
		if !ok {
			continue
		}
		out = append(out, doc.ToEvidence(0))
	}
	return out, nil
}

// boroughOf reads the denormalised borough a document was written with.
func boroughOf(doc evidence.KnowledgeDocument) string {
	if doc.Metadata == nil {
		return ""
	}
	borough, _ := doc.Metadata["borough"].(string)
	return borough
}

// topicOf reads the review topic a summary document was written for.
func topicOf(doc evidence.KnowledgeDocument) string {
	if doc.Metadata == nil {
		return ""
	}
	topic, _ := doc.Metadata["topic"].(string)
	return topic
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

var _ store.KnowledgeRepository = (*KnowledgeRepository)(nil)
