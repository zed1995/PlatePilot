package memory

import (
	"context"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/evidence"
	"github.com/zed/platepilot/shared/store"
)

// KnowledgeStore is an in-memory store.KnowledgeStore.
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

var _ store.KnowledgeStore = (*KnowledgeStore)(nil)

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

// group is the unit that exactly one version may be live for: the same
// restaurant, scope, and document type.
//
// It is deliberately not identity. The idempotency key includes the content
// hash, so two versions of the same fact are two identities in one group —
// which is exactly the distinction supersession has to make.
type group struct {
	restaurantID int64
	scope        evidence.RetrievalScope
	docType      evidence.DocType
}

func groupOf(doc evidence.KnowledgeDocument) group {
	return group{restaurantID: doc.RestaurantID, scope: doc.Scope, docType: doc.DocType}
}

// UpsertDocuments inserts new versions and skips documents already present.
func (s *KnowledgeStore) UpsertDocuments(_ context.Context, docs []evidence.KnowledgeDocument) (store.UpsertResult, error) {
	var result store.UpsertResult
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

// PendingDocuments returns documents with no vector, oldest id first.
//
// The live flag is deliberately not part of the predicate: documents are
// inserted inactive and only go live once vectored, so requiring is_active
// would select a state the table's CHECK constraint makes impossible.
func (s *KnowledgeStore) PendingDocuments(_ context.Context, limit int) ([]evidence.KnowledgeDocument, error) {
	if limit <= 0 {
		return []evidence.KnowledgeDocument{}, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]evidence.KnowledgeDocument, 0, len(s.byID))
	for _, doc := range s.byID {
		if len(doc.Embedding) == 0 {
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

// DistinctEmbeddingModels reports which models produced the documents that are
// currently live and vectored.
func (s *KnowledgeStore) DistinctEmbeddingModels(_ context.Context) ([]store.EmbeddingModelInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	type key struct {
		model      string
		dimensions int
	}
	// Only live documents count. A superseded model still has its rows, and
	// counting it would make every later run refuse to start.
	counts := make(map[key]int)
	for _, doc := range s.byID {
		if !doc.IsActive || len(doc.Embedding) == 0 || doc.EmbeddingModel == "" {
			continue
		}
		counts[key{doc.EmbeddingModel, doc.EmbeddingDimensions}]++
	}

	out := make([]store.EmbeddingModelInfo, 0, len(counts))
	for k, n := range counts {
		out = append(out, store.EmbeddingModelInfo{Model: k.model, Dimensions: k.dimensions, Documents: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Model != out[j].Model {
			return out[i].Model < out[j].Model
		}
		return out[i].Dimensions < out[j].Dimensions
	})
	return out, nil
}

// EmbeddedReviewCounts returns the per-restaurant review count reachable
// through a vector, mirroring the Postgres query's key handling.
func (s *KnowledgeStore) EmbeddedReviewCounts(_ context.Context) (map[int64]int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[int64]int, 16)
	for _, doc := range s.byID {
		if !doc.IsActive || len(doc.Embedding) == 0 {
			continue
		}
		if doc.DocType != evidence.DocTypeRestaurantRepresentativeReviews {
			continue
		}
		count, ok := doc.Metadata["representative_count"]
		if !ok {
			continue
		}
		number, ok := count.(int)
		if !ok {
			continue
		}
		out[doc.RestaurantID] = number
	}
	return out, nil
}

// VectorSearch ranks vectored documents in one scope by cosine distance.
//
// The distance is computed here rather than approximated so the mock and the
// database agree on ordering: a test that passes against memory and fails
// against Postgres is usually a ranking difference, and an approximate
// similarity would make that difference invisible until production.
func (s *KnowledgeStore) VectorSearch(
	_ context.Context,
	scope evidence.RetrievalScope,
	query []float32,
	topK int,
	filter store.VectorFilter,
) ([]store.ScoredDocument, error) {
	if scope != evidence.ScopeRestaurant && scope != evidence.ScopeEvidence {
		return nil, errs.Newf(errs.CodeInvalidArgument,
			"memory: vector search needs an explicit scope, got %q", scope)
	}
	if len(query) == 0 {
		return nil, errs.New(errs.CodeInvalidArgument, "memory: vector search needs a query vector")
	}
	if topK <= 0 {
		return nil, errs.Newf(errs.CodeInvalidArgument,
			"memory: vector search needs a positive top_k, got %d", topK)
	}
	queryNorm := norm(query)
	if queryNorm == 0 {
		return nil, errs.New(errs.CodeEmbeddingZeroVector,
			"memory: the query vector has zero magnitude, so every distance is undefined")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	scored := make([]store.ScoredDocument, 0, len(s.byID))
	for _, doc := range s.byID {
		if !doc.IsActive || len(doc.Embedding) == 0 || doc.Scope != scope {
			continue
		}
		if filter.Borough != "" {
			borough, _ := doc.Metadata["borough"].(string)
			if borough != filter.Borough {
				continue
			}
		}
		if filter.RestaurantID > 0 && doc.RestaurantID != filter.RestaurantID {
			continue
		}
		// A zero vector has an undefined direction, and pgvector excludes such
		// rows from distance results entirely. Skipping it here keeps the mock
		// from returning a hit the database would never produce.
		if norm(doc.Embedding) == 0 {
			continue
		}
		scored = append(scored, store.ScoredDocument{
			KnowledgeDocument: doc,
			Distance:          1 - dot(doc.Embedding, query)/(norm(doc.Embedding)*queryNorm),
		})
	}
	// Ties break on document id so the ordering is total and a re-run returns
	// the same list. Without it, two equally distant documents could swap
	// places between calls.
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].Distance != scored[j].Distance {
			return scored[i].Distance < scored[j].Distance
		}
		return scored[i].DocumentID < scored[j].DocumentID
	})
	if len(scored) > topK {
		scored = scored[:topK]
	}
	return scored, nil
}

// dot is the inner product of two equal-length vectors.
func dot(a, b []float32) float64 {
	length := len(a)
	if len(b) < length {
		length = len(b)
	}
	var sum float64
	for i := 0; i < length; i++ {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}

// norm is the Euclidean length of a vector.
func norm(v []float32) float64 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	return math.Sqrt(sum)
}

// SupersededDocumentIDs returns the live rows that the given documents displace.
func (s *KnowledgeStore) SupersededDocumentIDs(_ context.Context, docIDs []int64) ([]int64, error) {
	if len(docIDs) == 0 {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	groups := make(map[group]struct{}, len(docIDs))
	for _, id := range docIDs {
		doc, ok := s.byID[id]
		if !ok {
			continue
		}
		groups[groupOf(doc)] = struct{}{}
	}
	// The exclusion is membership in the caller's set rather than "a different
	// id": the caller passes a whole page, and a page routinely holds several
	// rows from one group. Excluding only self left those siblings live, so
	// the group ended up with every version active at once.
	member := make(map[int64]struct{}, len(docIDs))
	for _, id := range docIDs {
		member[id] = struct{}{}
	}
	out := make([]int64, 0, len(docIDs))
	for id, doc := range s.byID {
		if !doc.IsActive {
			continue
		}
		if _, replaced := groups[groupOf(doc)]; !replaced {
			continue
		}
		if _, listed := member[id]; listed {
			continue
		}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// VectoredDocumentIDs returns the subset of the given ids that carry a vector.
func (s *KnowledgeStore) VectoredDocumentIDs(_ context.Context, docIDs []int64) ([]int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]int64, 0, len(docIDs))
	for _, id := range docIDs {
		doc, ok := s.byID[id]
		if !ok || len(doc.Embedding) == 0 {
			continue
		}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// DeactivateStaleModels retires the live documents produced by another model.
func (s *KnowledgeStore) DeactivateStaleModels(_ context.Context, model string, dimensions int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	changed := 0
	for id, doc := range s.byID {
		if !doc.IsActive || len(doc.Embedding) == 0 {
			continue
		}
		if doc.EmbeddingModel == model && doc.EmbeddingDimensions == dimensions {
			continue
		}
		doc.IsActive = false
		s.byID[id] = doc
		changed++
	}
	return changed, nil
}
