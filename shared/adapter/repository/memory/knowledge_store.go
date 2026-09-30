package memory

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/evidence"
	"github.com/zed/platepilot/shared/port"
)

// KnowledgeStore is an in-memory port.KnowledgeStore.
//
// It mirrors the Postgres adapter's version semantics so the same contract
// suite can drive both: an unchanged document is skipped, changed content
// becomes a new version, and version numbers are assigned per group from the
// maximum over all history rather than over live rows only.
type KnowledgeStore struct {
	mu         sync.RWMutex
	byID       map[int64]evidence.KnowledgeDocument
	byIdentity map[identity]int64
	nextID     int64
}

// NewKnowledgeStore returns an empty in-memory knowledge store.
func NewKnowledgeStore() *KnowledgeStore {
	return &KnowledgeStore{
		byID:       make(map[int64]evidence.KnowledgeDocument),
		byIdentity: make(map[identity]int64),
	}
}

var _ port.KnowledgeStore = (*KnowledgeStore)(nil)

// identity is the idempotency key: the same restaurant, scope, doc type, and
// text is the same document.
type identity struct {
	restaurantID int64
	scope        evidence.RetrievalScope
	docType      evidence.DocType
	contentHash  string
}

func identityOf(doc evidence.KnowledgeDocument) identity {
	return identity{
		restaurantID: doc.RestaurantID,
		scope:        doc.Scope,
		docType:      doc.DocType,
		contentHash:  doc.ContentHash,
	}
}

// UpsertDocuments inserts new versions and skips documents already present.
func (s *KnowledgeStore) UpsertDocuments(_ context.Context, docs []evidence.KnowledgeDocument) (port.UpsertResult, error) {
	var result port.UpsertResult
	if len(docs) == 0 {
		return result, nil
	}
	// The whole batch is validated before anything is written so a bad
	// document cannot leave the store half-updated.
	for i, doc := range docs {
		if doc.RestaurantID <= 0 {
			return result, errs.Newf(errs.CodeInvalidArgument,
				"memory: document %d has no restaurant id", i)
		}
		if strings.TrimSpace(doc.Content) == "" {
			return result, errs.Newf(errs.CodeInvalidArgument,
				"memory: document %d has empty content", i)
		}
		if strings.TrimSpace(doc.ContentHash) == "" {
			return result, errs.Newf(errs.CodeInvalidArgument,
				"memory: document %d has no content hash", i)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, doc := range docs {
		key := identityOf(doc)
		if _, exists := s.byIdentity[key]; exists {
			result.Skipped++
			continue
		}
		doc.Version = s.nextVersionFor(key)
		if doc.DocumentID <= 0 {
			s.nextID++
			doc.DocumentID = s.nextID
		} else if doc.DocumentID > s.nextID {
			s.nextID = doc.DocumentID
		}
		s.byID[doc.DocumentID] = doc
		s.byIdentity[key] = doc.DocumentID
		result.Inserted++
	}
	return result, nil
}

// nextVersionFor returns max(version) + 1 across every version of the group,
// live or not. Considering only live rows would reuse a version number once a
// group had been fully deactivated.
func (s *KnowledgeStore) nextVersionFor(key identity) int {
	max := 0
	for _, doc := range s.byID {
		if doc.RestaurantID != key.restaurantID || doc.Scope != key.scope || doc.DocType != key.docType {
			continue
		}
		if doc.Version > max {
			max = doc.Version
		}
	}
	return max + 1
}

// PendingDocuments returns active documents with no vector, oldest id first.
func (s *KnowledgeStore) PendingDocuments(_ context.Context, limit int) ([]evidence.KnowledgeDocument, error) {
	if limit <= 0 {
		return []evidence.KnowledgeDocument{}, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]evidence.KnowledgeDocument, 0, len(s.byID))
	for _, doc := range s.byID {
		if doc.IsActive && len(doc.Embedding) == 0 {
			out = append(out, doc)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DocumentID < out[j].DocumentID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// SetEmbedding writes vectors and the model identity that produced them.
func (s *KnowledgeStore) SetEmbedding(
	_ context.Context,
	docIDs []int64,
	vectors [][]float32,
	model string,
	dimensions int,
) (int, error) {
	if len(docIDs) != len(vectors) {
		return 0, errs.Newf(errs.CodeInvalidArgument,
			"memory: %d document ids but %d vectors", len(docIDs), len(vectors))
	}
	if len(docIDs) == 0 {
		return 0, nil
	}
	if dimensions <= 0 {
		return 0, errs.Newf(errs.CodeInvalidArgument,
			"memory: embedding dimensions must be > 0 (got %d)", dimensions)
	}
	if strings.TrimSpace(model) == "" {
		return 0, errs.New(errs.CodeInvalidArgument, "memory: embedding model is required")
	}
	for i, vec := range vectors {
		if len(vec) != dimensions {
			return 0, errs.Newf(errs.CodeEmbeddingDimensionMismatch,
				"memory: vector %d has %d dimensions, want %d", i, len(vec), dimensions)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	written := 0
	for i, id := range docIDs {
		doc, ok := s.byID[id]
		if !ok {
			continue
		}
		doc.Embedding = vectors[i]
		doc.EmbeddingModel = model
		doc.EmbeddingDimensions = dimensions
		s.byID[id] = doc
		written++
	}
	return written, nil
}

// ActivateDocuments flips the live flag.
//
// A document may not be activated before it has a vector. The database enforces
// the same rule with CHECK (is_active = false OR embedding IS NOT NULL), and the
// in-memory store has to enforce it too: a mock that let an impossible state
// through would let the ordering bug it is meant to catch ship.
func (s *KnowledgeStore) ActivateDocuments(_ context.Context, docIDs []int64, active bool) (int, error) {
	if len(docIDs) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if active {
		for _, id := range docIDs {
			doc, ok := s.byID[id]
			if !ok {
				continue
			}
			if len(doc.Embedding) == 0 {
				return 0, errs.Newf(errs.CodeConflict,
					"memory: cannot activate document %d before it has a vector", id)
			}
		}
	}

	changed := 0
	for _, id := range docIDs {
		doc, ok := s.byID[id]
		if !ok {
			continue
		}
		doc.IsActive = active
		s.byID[id] = doc
		changed++
	}
	return changed, nil
}

// ListByRestaurant returns a restaurant's documents in one scope, newest
// version first.
func (s *KnowledgeStore) ListByRestaurant(
	_ context.Context,
	restaurantID int64,
	scope evidence.RetrievalScope,
) ([]evidence.KnowledgeDocument, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]evidence.KnowledgeDocument, 0, 8)
	for _, doc := range s.byID {
		if doc.RestaurantID == restaurantID && doc.Scope == scope {
			out = append(out, doc)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DocType != out[j].DocType {
			return out[i].DocType < out[j].DocType
		}
		if out[i].Version != out[j].Version {
			return out[i].Version > out[j].Version
		}
		return out[i].DocumentID < out[j].DocumentID
	})
	return out, nil
}

// DistinctEmbeddingModels reports which models are present among documents
// that already have a vector.
func (s *KnowledgeStore) DistinctEmbeddingModels(_ context.Context) ([]port.EmbeddingModelInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	type key struct {
		model      string
		dimensions int
	}
	counts := make(map[key]int)
	for _, doc := range s.byID {
		if len(doc.Embedding) == 0 || doc.EmbeddingModel == "" {
			continue
		}
		counts[key{doc.EmbeddingModel, doc.EmbeddingDimensions}]++
	}

	out := make([]port.EmbeddingModelInfo, 0, len(counts))
	for k, n := range counts {
		out = append(out, port.EmbeddingModelInfo{Model: k.model, Dimensions: k.dimensions, Documents: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Model != out[j].Model {
			return out[i].Model < out[j].Model
		}
		return out[i].Dimensions < out[j].Dimensions
	})
	return out, nil
}
