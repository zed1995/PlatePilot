package postgres

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/store"
)

// KnowledgeStore is the Postgres implementation of store.KnowledgeStore.
//
// Vector values are exchanged as text literals ("[0.1,0.2,...]"), which pgx
// passes to pgvector unchanged. There is no pgvector Go type registered on the
// pool, and adding one would mean a type that has to stay in sync with the
// table's declared width; formatting here keeps the constraint visible in the
// schema instead.
type KnowledgeStore struct {
	client *Client
}

// NewKnowledgeStore builds a store bound to an existing pool.
func NewKnowledgeStore(client *Client) *KnowledgeStore { return &KnowledgeStore{client: client} }

var _ store.KnowledgeStore = (*KnowledgeStore)(nil)

// knowledgeColumns is the projection shared by every read of a document. The
// borough is denormalised onto the row so the partial HNSW predicates can be
// local column tests.
// knowledgeColumns is the full document projection.
//
// The embedding is included because the contract says ListByRestaurant
// returns the document as stored, and callers rely on being able to see that a
// retired document kept its vector. VectorSearch appends its own distance
// column, so it selects from this list rather than repeating it.
const knowledgeColumns = `
	document_id, restaurant_id, retrieval_scope, doc_type, title, content,
	content_hash, embedding_model, embedding_dimensions, borough, metadata,
	source_record_ids, source_review_ids, snapshot_at, version, is_active, embedding`

// upsertDocumentsSQL inserts a batch of document versions.
//
// It is a package-level constant rather than an inline literal so the version
// assignment can be asserted structurally. The statement has three CTEs whose
// correctness depends on each other, and a mistake in any of them — most
// importantly the batch-blind version numbering this file used to have — is
// invisible to review and to the compiler alike.
// upsertDocumentsSQL inserts a batch of document versions.
//
// It is a package-level constant rather than an inline literal so the
// version assignment can be asserted structurally. The statement has
// three CTEs whose correctness depends on each other, and a mistake in
// any of them — most importantly the batch-blind version numbering this
// statement used to have — is invisible to review and to the compiler.
const upsertDocumentsSQL = `	WITH incoming AS (
		SELECT u.ord, u.restaurant_id, u.retrieval_scope, u.doc_type, u.title,
		       u.content, u.content_hash, u.borough, u.metadata,
		       u.source_record_ids, u.source_review_ids, u.snapshot_at, u.version, u.is_active
		FROM unnest(
		$1::bigint[], $2::text[], $3::text[], $4::text[], $5::text[],
		$6::text[], $7::text[], $8::jsonb[], $9::text[],
		$10::timestamptz[], $11::int[], $12::boolean[], $13::text[]
	) WITH ORDINALITY AS u(
		restaurant_id, retrieval_scope, doc_type, title,
		content, content_hash, borough, metadata, source_record_ids,
		snapshot_at, version, is_active, source_review_ids, ord)
	), next_version AS (
		SELECT g.ord,
		       coalesce(e.max_version, 0)
		           + row_number() OVER (
		               PARTITION BY g.restaurant_id, g.retrieval_scope, g.doc_type
		               ORDER BY g.ord
		           ) AS version
		FROM (
			SELECT i.ord, i.restaurant_id, i.retrieval_scope, i.doc_type
			FROM incoming i
		) g
		LEFT JOIN (
			SELECT d.restaurant_id, d.retrieval_scope, d.doc_type,
			       max(d.version) AS max_version
			FROM knowledge_documents d
			GROUP BY d.restaurant_id, d.retrieval_scope, d.doc_type
		) e
		       ON  e.restaurant_id  = g.restaurant_id
		   AND e.retrieval_scope = g.retrieval_scope
		   AND e.doc_type        = g.doc_type
	)
	INSERT INTO knowledge_documents (
		restaurant_id, retrieval_scope, doc_type, title, content,
		content_hash, borough, metadata, source_record_ids, snapshot_at,
		version, is_active, source_review_ids)
	SELECT i.restaurant_id, i.retrieval_scope, i.doc_type,
	       i.title, i.content, i.content_hash, i.borough, i.metadata,
	       coalesce(string_to_array(i.source_record_ids, chr(31)), '{}')::text[],
	       i.snapshot_at, nv.version, i.is_active,
	       coalesce(string_to_array(i.source_review_ids, ','), '{}')::bigint[]
	FROM incoming i
	JOIN next_version nv ON nv.ord = i.ord
	ON CONFLICT (restaurant_id, retrieval_scope, doc_type, content_hash)
	DO NOTHING`

// UpsertDocuments inserts document versions.
//
// Idempotency is enforced by (restaurant_id, retrieval_scope, doc_type,
// content_hash): an identical document is skipped, while changed content
// inserts a new version alongside the old one. Overwriting in place would
// break any citation already issued against the previous text.
//
// The write goes through unnest rather than a VALUES list because the extended
// query protocol caps bind parameters at 65535, which a full corpus build
// exceeds by orders of magnitude.
func (s *KnowledgeStore) UpsertDocuments(ctx context.Context, docs []evidence.KnowledgeDocument) (store.UpsertResult, error) {
	var result store.UpsertResult
	if len(docs) == 0 {
		return result, nil
	}

	restaurantIDs := make([]int64, len(docs))
	scopes := make([]string, len(docs))
	docTypes := make([]string, len(docs))
	titles := make([]*string, len(docs))
	contents := make([]string, len(docs))
	hashes := make([]string, len(docs))
	boroughs := make([]*string, len(docs))
	metadata := make([][]byte, len(docs))
	// source_record_ids is a one-dimensional text[]. pgx renders a Go
	// [][]string as a two-dimensional array, which the column rejects, so the
	// inner lists are flattened into one delimited text[] here and split apart
	// again in SQL. The delimiter cannot occur in a source record id.
	sourceIDs := make([]string, len(docs))
	// sourceReviewIDs mirrors the source_record_ids trick: each document's
	// bounded id list is flattened into one comma-delimited text value because
	// the per-document lists have different lengths and cannot be one
	// rectangular bigint[][] argument. It is split back into bigint[] in SQL.
	sourceReviewIDText := make([]string, len(docs))
	snapshotAt := make([]*time.Time, len(docs))
	versions := make([]int32, len(docs))
	active := make([]bool, len(docs))

	for i, doc := range docs {
		if doc.RestaurantID <= 0 {
			return result, errs.Newf(errs.CodeInvalidArgument,
				"postgres: document %d has no restaurant id", i)
		}
		if strings.TrimSpace(doc.Content) == "" {
			return result, errs.Newf(errs.CodeInvalidArgument,
				"postgres: document %d has empty content", i)
		}
		if strings.TrimSpace(doc.ContentHash) == "" {
			return result, errs.Newf(errs.CodeInvalidArgument,
				"postgres: document %d has no content hash", i)
		}

		restaurantIDs[i] = doc.RestaurantID
		scopes[i] = string(doc.Scope)
		docTypes[i] = string(doc.DocType)
		titles[i] = optionalString(doc.Title)
		contents[i] = doc.Content
		hashes[i] = doc.ContentHash
		boroughs[i] = optionalString(boroughOf(doc))
		encoded, err := json.Marshal(orEmptyMap(doc.Metadata))
		if err != nil {
			return result, operationError("postgres: encode document metadata", err)
		}
		metadata[i] = encoded
		sourceIDs[i] = strings.Join(doc.SourceRecordIDs, sourceIDDelimiter)
		reviewIDText := make([]string, 0, len(doc.SourceReviewIDs))
		for _, id := range doc.SourceReviewIDs {
			reviewIDText = append(reviewIDText, strconv.FormatInt(id, 10))
		}
		sourceReviewIDText[i] = strings.Join(reviewIDText, ",")
		if !doc.SnapshotAt.IsZero() {
			snapshot := doc.SnapshotAt
			snapshotAt[i] = &snapshot
		}
		version := doc.Version
		if version <= 0 {
			version = 1
		}
		versions[i] = int32(version)
		active[i] = doc.IsActive
	}

	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	// The version number is assigned per (restaurant_id, scope, doc_type) group
	// from the maximum over *all* history, not just active rows. Considering
	// only live rows would hand the same version number to two different
	// documents whenever every version of a group was inactive.
	//
	// It also has to account for the other rows of the *same batch*. A group is
	// 1:N — one restaurant can produce several evidence chunks of the same doc
	// type in a single build — and a plain correlated subquery only sees rows
	// that are already in the table. Every document of a fresh group would
	// then be handed the same number, and nothing rejects that: the version
	// column is not unique, and the documents differ in content hash so the
	// idempotency key does not catch it either. The window functions below are
	// what let each row see the rows ahead of it in its own batch.
	//
	// document_id is left to the identity column: the callers build documents
	// before they know their ids, and supplying 0 for every row would collide
	// on the primary key instead of on the idempotency key.
	tag, err := s.client.pool.Exec(ctx, upsertDocumentsSQL,
		restaurantIDs, scopes, docTypes, titles, contents, hashes,
		boroughs, metadata, sourceIDs, snapshotAt, versions, active,
		sourceReviewIDText)
	if err != nil {
		return result, operationError("postgres: upsert knowledge documents", err)
	}

	inserted := int(tag.RowsAffected())
	result.Inserted = inserted
	result.Skipped = len(docs) - inserted
	return result, nil
}

// PendingDocuments returns active documents that still need a vector.
//
// Only inactive-until-embedded rows qualify, which is why the build stage
// PendingDocuments returns documents that have no vector yet, regardless of the
// live flag.
//
// The flag cannot be part of this predicate. Documents are inserted with
// is_active = false and only become live after their vector exists, so
// filtering on is_active here would select a row the table's own CHECK
// (is_active = false OR embedding IS NOT NULL) makes impossible — the query
// would match nothing and the whole embedding stage would be a silent no-op.
// Newly built documents are exactly the ones that need a vector, so the
// predicate is "no vector", full stop.
func (s *KnowledgeStore) PendingDocuments(ctx context.Context, limit int) ([]evidence.KnowledgeDocument, error) {
	if limit <= 0 {
		return []evidence.KnowledgeDocument{}, nil
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	rows, err := s.client.pool.Query(ctx,
		`SELECT `+knowledgeColumns+`
		 FROM knowledge_documents
		 WHERE embedding IS NULL
		 ORDER BY document_id
		 LIMIT $1`, limit)
	if err != nil {
		return nil, operationError("postgres: list pending documents", err)
	}
	defer rows.Close()
	return scanKnowledgeDocuments(rows)
}

// SetEmbedding writes vectors and the model identity that produced them.
//
// The ids and vectors are zipped here rather than trusted to line up: a
// mismatch would attach a vector to the wrong document, which is worse than a
// failed write because nothing would report it.
func (s *KnowledgeStore) SetEmbedding(
	ctx context.Context,
	docIDs []int64,
	vectors [][]float32,
	model string,
	dimensions int,
) (int, error) {
	if len(docIDs) != len(vectors) {
		return 0, errs.Newf(errs.CodeInvalidArgument,
			"postgres: %d document ids but %d vectors", len(docIDs), len(vectors))
	}
	if len(docIDs) == 0 {
		return 0, nil
	}
	if dimensions <= 0 {
		return 0, errs.Newf(errs.CodeInvalidArgument,
			"postgres: embedding dimensions must be > 0 (got %d)", dimensions)
	}
	if strings.TrimSpace(model) == "" {
		return 0, errs.New(errs.CodeInvalidArgument, "postgres: embedding model is required")
	}

	ids := make([]int64, len(docIDs))
	literals := make([]string, len(vectors))
	for i, vec := range vectors {
		if len(vec) != dimensions {
			return 0, errs.Newf(errs.CodeEmbeddingDimensionMismatch,
				"postgres: vector %d has %d dimensions, want %d", i, len(vec), dimensions)
		}
		ids[i] = docIDs[i]
		literals[i] = vectorLiteral(vec)
	}

	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	tag, err := s.client.pool.Exec(ctx, `
		UPDATE knowledge_documents d
		SET embedding = v.embedding::vector,
		    embedding_model = $2,
		    embedding_dimensions = $3
		FROM unnest($1::bigint[], $4::text[]) AS v(document_id, embedding)
		WHERE d.document_id = v.document_id`,
		ids, model, dimensions, literals)
	if err != nil {
		return 0, operationError("postgres: set document embeddings", err)
	}
	return int(tag.RowsAffected()), nil
}

// ActivateDocuments flips the live flag for a set of documents.
//
// Both halves of a version switch go through here, inside one transaction, so
// a group can never be observed with zero active rows or two.
func (s *KnowledgeStore) ActivateDocuments(ctx context.Context, docIDs []int64, active bool) (int, error) {
	if len(docIDs) == 0 {
		return 0, nil
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	tag, err := s.client.pool.Exec(ctx,
		`UPDATE knowledge_documents SET is_active = $2 WHERE document_id = ANY($1)`,
		docIDs, active)
	if err != nil {
		return 0, operationError("postgres: activate knowledge documents", err)
	}
	return int(tag.RowsAffected()), nil
}

// ListByRestaurant returns one restaurant's documents in one scope, newest
// version first.
func (s *KnowledgeStore) ListByRestaurant(
	ctx context.Context,
	restaurantID int64,
	scope evidence.RetrievalScope,
) ([]evidence.KnowledgeDocument, error) {
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	rows, err := s.client.pool.Query(ctx,
		`SELECT `+knowledgeColumns+`
		 FROM knowledge_documents
		 WHERE restaurant_id = $1 AND retrieval_scope = $2
		 ORDER BY doc_type, version DESC, document_id`,
		restaurantID, string(scope))
	if err != nil {
		return nil, operationError("postgres: list documents by restaurant", err)
	}
	defer rows.Close()
	return scanKnowledgeDocuments(rows)
}

// DistinctEmbeddingModels reports which models produced the documents that are
// currently live and vectored.
//
// The embedding stage calls this before writing: two models sharing one live
// set produce similarity scores that cannot be interpreted, so the stage
// refuses to continue unless the operator forces the change.
func (s *KnowledgeStore) DistinctEmbeddingModels(ctx context.Context) ([]store.EmbeddingModelInfo, error) {
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	// Only live documents count. A superseded model still has its rows — that
	// history is deliberate — and counting it would make every later run refuse
	// to start, long after the conflict it reported was resolved.
	rows, err := s.client.pool.Query(ctx, `
		SELECT embedding_model, embedding_dimensions, count(*)
		FROM knowledge_documents
		WHERE is_active AND embedding IS NOT NULL AND embedding_model IS NOT NULL
		GROUP BY embedding_model, embedding_dimensions
		ORDER BY embedding_model`)
	if err != nil {
		return nil, operationError("postgres: list embedding models", err)
	}
	defer rows.Close()

	out := make([]store.EmbeddingModelInfo, 0, 4)
	for rows.Next() {
		var info store.EmbeddingModelInfo
		var dimensions *int32
		if err := rows.Scan(&info.Model, &dimensions, &info.Documents); err != nil {
			return nil, operationError("postgres: scan embedding model", err)
		}
		if dimensions != nil {
			info.Dimensions = int(*dimensions)
		}
		out = append(out, info)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate embedding models", err)
	}
	return out, nil
}

// scanKnowledgeDocuments materialises the shared projection.
func scanKnowledgeDocuments(rows pgx.Rows) ([]evidence.KnowledgeDocument, error) {
	out := make([]evidence.KnowledgeDocument, 0, 64)
	for rows.Next() {
		var (
			doc            evidence.KnowledgeDocument
			scope          string
			docType        string
			title          *string
			embeddingModel *string
			dimensions     *int32
			borough        *string
			metadataRaw    []byte
			snapshotAt     *time.Time
			embeddingText  *string
		)
		// embedding_model and embedding_dimensions are nullable because a
		// document exists before its vector does, so they are scanned through
		// pointers rather than straight into the domain fields.
		if err := rows.Scan(
			&doc.DocumentID, &doc.RestaurantID, &scope, &docType, &title, &doc.Content,
			&doc.ContentHash, &embeddingModel, &dimensions, &borough,
			&metadataRaw, &doc.SourceRecordIDs, &doc.SourceReviewIDs, &snapshotAt,
			&doc.Version, &doc.IsActive, &embeddingText,
		); err != nil {
			return nil, operationError("postgres: scan knowledge document", err)
		}
		vector, err := parseVectorLiteral(embeddingText)
		if err != nil {
			return nil, err
		}
		doc.Embedding = vector
		if err := applyScannedDocument(&doc, scope, docType, title, embeddingModel,
			dimensions, borough, metadataRaw, snapshotAt); err != nil {
			return nil, err
		}
		out = append(out, doc)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate knowledge documents", err)
	}
	return out, nil
}

// EmbeddedReviewCounts returns the per-restaurant review count that is now
// reachable through a vector.
//
// The number comes from the representative-review document's metadata rather
// than from re-reading reviews: that document is the one artefact that lists
// which reviews were quoted, so counting it answers "how much of the corpus is
// searchable" without a second aggregation that could disagree with the first.
//
// Documents without the metadata key are skipped rather than counted as one.
// A missing key means the document was not built by the representative
// builder, and counting it would attribute reviews it never quoted.
func (s *KnowledgeStore) EmbeddedReviewCounts(ctx context.Context) (map[int64]int, error) {
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	rows, err := s.client.pool.Query(ctx, `
		SELECT restaurant_id, (metadata->>'representative_count')::bigint
		FROM knowledge_documents
		WHERE is_active
		  AND embedding IS NOT NULL
		  AND doc_type = $1
		  AND metadata ? 'representative_count'`,
		evidence.DocTypeRestaurantRepresentativeReviews)
	if err != nil {
		return nil, operationError("postgres: count embedded reviews", err)
	}
	defer rows.Close()

	out := make(map[int64]int, 16)
	for rows.Next() {
		var restaurantID, count int64
		if err := rows.Scan(&restaurantID, &count); err != nil {
			return nil, operationError("postgres: scan embedded review count", err)
		}
		out[restaurantID] = int(count)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate embedded review counts", err)
	}
	return out, nil
}

// VectorSearch ranks vectored documents in one retrieval scope by cosine
// distance.
//
// The scope is a required filter rather than an optional one. A search that
// could return both restaurant profiles and evidence chunks would let a
// profile outrank the quote a user is meant to see, and nothing downstream could
// tell the two apart. Callers pick the scope; this method never guesses.
//
// The borough filter exists to pick a partial HNSW index, not to trim results.
// M1-03 measured that filtering the global index by borough in a WHERE clause
// degrades to a sequential scan with tens of thousands of rows discarded after
// the fact, because the planner cannot assume a filtered stream is still in
// vector order. Naming the borough lets it choose knowledge_documents_hnsw_<b>.
//
// Distance is computed with the <=> operator, which matches the vector_cosine_ops
// opclass the indexes were built with. Using a different operator class on the
// same column would make the index unusable, and the query would still return
// correct results — just slowly, and without saying why.
func (s *KnowledgeStore) VectorSearch(
	ctx context.Context,
	scope evidence.RetrievalScope,
	query []float32,
	topK int,
	filter store.VectorFilter,
) ([]store.ScoredDocument, error) {
	if scope != evidence.ScopeRestaurant && scope != evidence.ScopeEvidence {
		return nil, errs.Newf(errs.CodeInvalidArgument,
			"postgres: vector search needs an explicit scope, got %q", scope)
	}
	if len(query) == 0 {
		return nil, errs.New(errs.CodeInvalidArgument, "postgres: vector search needs a query vector")
	}
	if topK <= 0 {
		return nil, errs.Newf(errs.CodeInvalidArgument,
			"postgres: vector search needs a positive top_k, got %d", topK)
	}

	statement, args := vectorSearchStatement(scope, query, topK, filter)

	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	rows, err := s.client.pool.Query(ctx, statement, args...)
	if err != nil {
		return nil, operationError("postgres: vector search", err)
	}
	defer rows.Close()

	return scanScoredDocuments(rows)
}

// vectorSearchStatement builds the search SQL and its bound arguments.
//
// It is a separate function so the placeholder numbering can be tested without
// a database. That numbering is the easy thing to get wrong and the expensive
// thing to get wrong: PostgreSQL rejects the statement outright when a bound
// parameter is never referenced, and the error names a type it could not
// determine rather than the clause that was supposed to use it.
//
// Placeholders are therefore numbered as the clauses are appended, so every
// number the statement mentions is one that is actually sent. Numbering the
// optional filters up front and skipping one leaves a hole, and the query fails
// with "could not determine data type of parameter $2".
//
// topK is interpolated rather than bound because a LIMIT placeholder would
// force PostgreSQL to infer its type from context, and an integer literal is
// unambiguous. It is an int that has already been range-checked by the caller,
// so it carries no injection surface.
func vectorSearchStatement(
	scope evidence.RetrievalScope,
	query []float32,
	topK int,
	filter store.VectorFilter,
) (string, []any) {
	args := make([]any, 0, 4)
	bind := func(value any) string {
		args = append(args, value)
		return "$" + strconv.Itoa(len(args))
	}
	scopeParam := bind(string(scope))
	boroughClause := ""
	if filter.Borough != "" {
		boroughClause = "\n\t\t  AND borough = " + bind(filter.Borough)
	}
	restaurantClause := ""
	if filter.RestaurantID > 0 {
		restaurantClause = "\n\t\t  AND restaurant_id = " + bind(filter.RestaurantID)
	}
	// The distance operator appears in both the projection and the ORDER BY, so
	// the vector is bound once and referenced by the same number twice.
	vectorParam := bind(vectorLiteral(query))

	statement := `
		SELECT ` + knowledgeColumns + `, embedding <=> ` + vectorParam + ` AS distance
		FROM knowledge_documents
		WHERE is_active
		  AND embedding IS NOT NULL
		  AND retrieval_scope = ` + scopeParam + boroughClause + restaurantClause + `
		ORDER BY embedding <=> ` + vectorParam + `
		LIMIT ` + strconv.Itoa(topK)
	return statement, args
}

// SupersededDocumentIDs returns the live rows that the given documents displace.
//
// The comparison is per (restaurant_id, retrieval_scope, doc_type) group, which
// is the granularity the version number is allocated at. A live row in one of
// those groups that the caller did *not* list is an older version of the same
// fact, and two live rows in one group would make retrieval ambiguous.
//
// The exclusion is membership in the caller's set, not merely "a different
// document_id". The caller passes a whole page, and a page routinely holds
// several rows from one group — a restaurant with three opening-hours chunks
// lands in the same page. Excluding only self left those siblings live, so the
// group ended up with every version active at once and the index returned
// whichever the planner happened to order first.
func (s *KnowledgeStore) SupersededDocumentIDs(ctx context.Context, docIDs []int64) ([]int64, error) {
	if len(docIDs) == 0 {
		return nil, nil
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	// The join must be pinned to the rows the caller passed in. Without the
	// `n.document_id = incoming.document_id` conjunct the self-join pairs every
	// active document with every other document in *its own* group, so an
	// unrelated live profile is reported as displaced by whatever page the
	// caller asked about -- and the caller deactivates everything that comes
	// back.
	//
	// The NOT EXISTS excludes the whole listed page, not just the row paired
	// with each candidate. The embedding stage hands over a whole page, and a
	// page routinely holds several rows of one group (the opening-hours chunks
	// of a restaurant); those siblings are being switched on, not displaced.
	rows, err := s.client.pool.Query(ctx, `
		SELECT DISTINCT d.document_id
		FROM unnest($1::bigint[]) AS incoming(document_id)
		JOIN knowledge_documents n
		  ON n.document_id = incoming.document_id
		JOIN knowledge_documents d
		  ON d.restaurant_id  = n.restaurant_id
		 AND d.retrieval_scope = n.retrieval_scope
		 AND d.doc_type        = n.doc_type
		WHERE d.is_active
		  AND NOT EXISTS (
		      SELECT 1 FROM unnest($1::bigint[]) AS listed(document_id)
		      WHERE listed.document_id = d.document_id
		  )`, docIDs)
	if err != nil {
		return nil, operationError("postgres: find superseded documents", err)
	}
	defer rows.Close()

	out := make([]int64, 0, len(docIDs))
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, operationError("postgres: scan superseded document", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate superseded documents", err)
	}
	return out, nil
}

// VectoredDocumentIDs returns the subset of the given ids that carry a vector.
func (s *KnowledgeStore) VectoredDocumentIDs(ctx context.Context, docIDs []int64) ([]int64, error) {
	if len(docIDs) == 0 {
		return nil, nil
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	rows, err := s.client.pool.Query(ctx,
		`SELECT document_id FROM knowledge_documents
		 WHERE document_id = ANY($1) AND embedding IS NOT NULL
		 ORDER BY document_id`, docIDs)
	if err != nil {
		return nil, operationError("postgres: find vectored documents", err)
	}
	defer rows.Close()

	out := make([]int64, 0, len(docIDs))
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, operationError("postgres: scan vectored document", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate vectored documents", err)
	}
	return out, nil
}

// DeactivateStaleModels retires the live documents produced by another model.
func (s *KnowledgeStore) DeactivateStaleModels(ctx context.Context, model string, dimensions int) (int, error) {
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	tag, err := s.client.pool.Exec(ctx, `
		UPDATE knowledge_documents SET is_active = false
		WHERE is_active
		  AND embedding IS NOT NULL
		  AND (embedding_model IS DISTINCT FROM $1 OR embedding_dimensions IS DISTINCT FROM $2)`,
		model, dimensions)
	if err != nil {
		return 0, operationError("postgres: deactivate stale models", err)
	}
	return int(tag.RowsAffected()), nil
}
