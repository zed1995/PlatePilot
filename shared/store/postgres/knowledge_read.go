package postgres

import (
	"context"
	"strconv"
	"strings"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/store"
)

// KnowledgeReadRepository is the Postgres implementation of
// store.KnowledgeRepository.
//
// It is separate from KnowledgeStore because the two answer different questions.
// The store's methods move documents through their lifecycle — write, embed,
// activate, retire — and every one of them is shaped by the requirement that a
// re-run be idempotent. This one's methods are shaped by recall: one scoped,
// ordered, filtered read.
type KnowledgeReadRepository struct {
	client *Client
}

// NewKnowledgeReadRepository builds a read-side repository on an existing pool.
func NewKnowledgeReadRepository(client *Client) *KnowledgeReadRepository {
	return &KnowledgeReadRepository{client: client}
}

var _ store.KnowledgeRepository = (*KnowledgeReadRepository)(nil)

// FindEvidenceByRestaurant returns one restaurant's active evidence documents,
// optionally narrowed to a single review topic.
//
// Ordering is document id rather than similarity because there is no query: this
// is the "tell me everything about this place" read, and a stable order is more
// useful there than an arbitrary one.
func (r *KnowledgeReadRepository) FindEvidenceByRestaurant(
	ctx context.Context, restaurantID int64, topic string,
) ([]evidence.Evidence, error) {
	if restaurantID <= 0 {
		return nil, errs.New(errs.CodeRetrievalNoScope, "restaurant_id is required")
	}
	statement := `SELECT ` + knowledgeColumns + `
		FROM knowledge_documents
		WHERE is_active
		  AND retrieval_scope = 'evidence'
		  AND restaurant_id = $1`
	args := []any{restaurantID}
	if topic != "" {
		args = append(args, topic)
		statement += `
		  AND doc_type = 'restaurant_review_summary'
		  AND metadata->>'topic' = $2`
	}
	statement += `
		ORDER BY doc_type, document_id`

	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	rows, err := r.client.pool.Query(ctx, statement, args...)
	if err != nil {
		return nil, operationError("postgres: find evidence by restaurant", err)
	}
	documents, err := scanKnowledgeDocuments(rows)
	if err != nil {
		return nil, err
	}
	out := make([]evidence.Evidence, 0, len(documents))
	for _, doc := range documents {
		out = append(out, doc.ToEvidence(0))
	}
	return out, nil
}

// VectorSearch ranks active documents in one scope by cosine distance.
//
// The filters in req are pushed into the statement rather than applied to the
// returned rows, and that is the whole design. Measured on this corpus at 27,198
// active documents:
//
//	scope only, LIMIT 50   -> Index Scan using knowledge_documents_hnsw
//	                          Rows Removed by Filter: 38 of 40 scanned
//	scope + restaurant_id  -> Bitmap Heap Scan + top-N heapsort over 59 rows
//
// The second shape is the evidence path, and it is already the right plan: a
// bounded set of restaurants owns a few dozen documents, so ranking them exactly
// is cheaper than building an approximate structure to rank them. That is why
// this milestone adds no scope-partitioned index — measured, not assumed (see
// the plan document, E.11).
//
// The restaurant path is different. A query with a borough is answered by the
// borough-partitioned index, so the borough reaches the statement as a column
// test and the planner can pick knowledge_documents_hnsw_<borough>. Naming it in
// a metadata expression instead would turn it into a non-index predicate and
// restore the sequential scan M1-03 measured.
func (r *KnowledgeReadRepository) VectorSearch(
	ctx context.Context, req store.VectorSearchRequest,
) ([]store.ScoredDocument, error) {
	if err := validateVectorRequest(req); err != nil {
		return nil, err
	}
	statement, args := buildVectorSearch(req)

	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	rows, err := r.client.pool.Query(ctx, statement, args...)
	if err != nil {
		return nil, operationError("postgres: recall documents", err)
	}
	return scanScoredDocuments(rows)
}

// validateVectorRequest rejects a request the store cannot execute.
//
// An evidence recall without restaurants is refused rather than widened to a
// global search. That distinction is the difference between a citation and a
// confidently wrong one: a caller that forgot to scope its question would
// otherwise receive the nearest review in the corpus and cite it for a
// restaurant it is not about.
func validateVectorRequest(req store.VectorSearchRequest) error {
	if req.Scope != evidence.ScopeRestaurant && req.Scope != evidence.ScopeEvidence {
		return errs.Newf(errs.CodeRetrievalNoScope,
			"recall needs an explicit retrieval scope, got %q", req.Scope)
	}
	if len(req.Query) == 0 {
		return errs.New(errs.CodeInvalidArgument, "recall needs a query vector")
	}
	if req.TopK <= 0 {
		return errs.Newf(errs.CodeInvalidArgument, "recall needs a positive top_k, got %d", req.TopK)
	}
	if req.Scope == evidence.ScopeEvidence && len(req.RestaurantIDs) == 0 {
		return errs.New(errs.CodeRetrievalNoScope,
			"evidence recall must name the restaurants it is asking about")
	}
	return nil
}

// buildVectorSearch renders the recall statement and its bound arguments.
//
// Placeholders are numbered as clauses are appended, so every number the
// statement mentions is one that is actually sent. Numbering optional filters up
// front and skipping one leaves a hole, and PostgreSQL rejects the statement
// with "could not determine data type of parameter $2" — an error that names a
// type rather than the clause that should have used it.
func buildVectorSearch(req store.VectorSearchRequest) (string, []any) {
	args := make([]any, 0, 6)
	bind := func(value any) string {
		args = append(args, value)
		return "$" + strconv.Itoa(len(args))
	}

	clauses := []string{"is_active", "retrieval_scope = " + bind(string(req.Scope))}

	if req.Borough != "" {
		// The column, not the metadata: this predicate selects the index.
		clauses = append(clauses, "borough = "+bind(req.Borough))
	}
	if len(req.RestaurantIDs) > 0 {
		clauses = append(clauses, "restaurant_id = ANY("+bind(req.RestaurantIDs)+")")
	}
	if len(req.DocTypes) > 0 {
		types := make([]string, 0, len(req.DocTypes))
		for _, docType := range req.DocTypes {
			types = append(types, string(docType))
		}
		clauses = append(clauses, "doc_type = ANY("+bind(types)+")")
	}
	if req.Topic != "" {
		clauses = append(clauses,
			"doc_type = "+bind(string(evidence.DocTypeRestaurantReviewSummary))+
				" AND metadata->>'topic' = "+bind(req.Topic))
	}

	// The vector is bound once and referenced twice: once in the projection and
	// once in the ORDER BY. Two placeholders would work too, but then a query
	// has two copies of a 1024-dimension literal on the wire.
	vectorParam := bind(vectorLiteral(req.Query))

	statement := `SELECT ` + knowledgeColumns + `, embedding <=> ` + vectorParam + ` AS distance
		FROM knowledge_documents
		WHERE ` + strings.Join(clauses, `
		  AND `) + `
		ORDER BY embedding <=> ` + vectorParam + `
		LIMIT ` + strconv.Itoa(req.TopK)
	return statement, args
}

// RecallEvidence returns a restaurant's citable evidence for one question.
//
// The restaurant set is not a filter it applies; it is a precondition. An
// evidence recall without one would return the nearest review in the corpus and
// hand the caller a citation for a restaurant it is not about, so the request is
// refused instead.
//
// An empty Query with restaurants named is answered rather than refused. "What
// is this place like" has no single embedding behind it, and returning the
// restaurant's active documents in document order is the honest answer to it.
func (r *KnowledgeReadRepository) RecallEvidence(
	ctx context.Context, req store.EvidenceRequest,
) ([]evidence.Evidence, error) {
	if len(req.RestaurantIDs) == 0 {
		return nil, errs.New(errs.CodeRetrievalNoScope,
			"evidence recall must name the restaurants it is asking about")
	}

	// The unordered read exists, but it is a different question from the ranked
	// one, so it goes through the document read rather than being bolted onto a
	// vector search with a nil vector.
	if len(req.Query) == 0 {
		if len(req.RestaurantIDs) == 1 {
			return r.FindEvidenceByRestaurant(ctx, req.RestaurantIDs[0], req.Topic)
		}
		return r.findEvidenceForRestaurants(ctx, req.RestaurantIDs, req.Topic, req.DocTypes, req.TopK)
	}

	docs, err := r.VectorSearch(ctx, store.VectorSearchRequest{
		Scope:         evidence.ScopeEvidence,
		Query:         req.Query,
		TopK:          req.TopK,
		RestaurantIDs: req.RestaurantIDs,
		Topic:         req.Topic,
		DocTypes:      req.DocTypes,
	})
	if err != nil {
		return nil, err
	}
	return scanEvidenceProjects(docs), nil
}

// findEvidenceForRestaurants is the unordered read across several restaurants.
//
// Ordering is by restaurant, then document type, then document id. The stable
// ordering matters because this is the fallback path for a question with no
// vector behind it: two identical requests have to produce identical output,
// and document id is the only tie-breaker that is both stable and meaningful.
func (r *KnowledgeReadRepository) findEvidenceForRestaurants(
	ctx context.Context, restaurantIDs []int64, topic string,
	docTypes []evidence.DocType, topK int,
) ([]evidence.Evidence, error) {
	statement := `SELECT ` + knowledgeColumns + `
		FROM knowledge_documents
		WHERE is_active
		  AND retrieval_scope = 'evidence'
		  AND restaurant_id = ANY($1)`
	args := []any{restaurantIDs}
	if topic != "" {
		args = append(args, topic)
		statement += `
		  AND doc_type = 'restaurant_review_summary'
		  AND metadata->>'topic' = $2`
	}
	if len(docTypes) > 0 {
		types := make([]string, 0, len(docTypes))
		for _, docType := range docTypes {
			types = append(types, string(docType))
		}
		args = append(args, types)
		statement += `
		  AND doc_type = ANY($` + strconv.Itoa(len(args)) + `)`
	}
	statement += `
		ORDER BY restaurant_id, doc_type, document_id`
	if topK > 0 {
		statement += `
		LIMIT ` + strconv.Itoa(topK)
	}

	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	rows, err := r.client.pool.Query(ctx, statement, args...)
	if err != nil {
		return nil, operationError("postgres: find evidence for restaurants", err)
	}
	documents, err := scanKnowledgeDocuments(rows)
	if err != nil {
		return nil, err
	}
	out := make([]evidence.Evidence, 0, len(documents))
	for _, doc := range documents {
		out = append(out, doc.ToEvidence(0))
	}
	return out, nil
}

// scanEvidenceProjects turns scored documents into citable evidence.
func scanEvidenceProjects(docs []store.ScoredDocument) []evidence.Evidence {
	out := make([]evidence.Evidence, 0, len(docs))
	for _, doc := range docs {
		out = append(out, doc.ToEvidence(similarityOf(doc.Distance)))
	}
	return out
}

// similarityOf converts a cosine distance into a similarity in [0,1].
//
// The clamp is two-sided on purpose. pgvector can report a distance a little
// above 2 for vectors that are not perfectly normalised, which makes the
// similarity negative and ranks a candidate below one that matched nothing at
// all. A negative distance should not be reachable — cosine distance is a
// non-negative quantity — but an unclamped mapping would turn any such value
// into a similarity above 1, which is worse than the original problem: it would
// silently outrank a perfect match. Clamping both ends keeps the output a
// similarity whatever the input.
func similarityOf(distance float64) float64 {
	similarity := 1 - distance
	if similarity < 0 {
		return 0
	}
	if similarity > 1 {
		return 1
	}
	return similarity
}
