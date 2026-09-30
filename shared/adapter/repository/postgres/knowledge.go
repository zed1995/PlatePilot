package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/evidence"
	"github.com/zed/platepilot/shared/port"
)

// KnowledgeStore is the Postgres implementation of port.KnowledgeStore.
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

var _ port.KnowledgeStore = (*KnowledgeStore)(nil)

// knowledgeColumns is the projection shared by every read of a document. The
// borough is denormalised onto the row so the partial HNSW predicates can be
// local column tests.
const knowledgeColumns = `
	document_id, restaurant_id, retrieval_scope, doc_type, title, content,
	content_hash, embedding_model, embedding_dimensions, borough, metadata,
	source_record_ids, snapshot_at, version, is_active`

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
func (s *KnowledgeStore) UpsertDocuments(ctx context.Context, docs []evidence.KnowledgeDocument) (port.UpsertResult, error) {
	var result port.UpsertResult
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

	// The version number is assigned per (restaurant, scope, doc_type) group
	// from the maximum over *all* history, not just active rows. Considering
	// only live rows would hand the same version number to two different
	// documents whenever every version of a group was inactive.
	// document_id is left to the identity column: the callers build documents
	// before they know their ids, and supplying 0 for every row would collide
	// on the primary key instead of on the idempotency key.
	//
	// The version number is assigned per (restaurant, scope, doc_type) group
	// from the maximum over *all* history, not just active rows. Considering
	// only live rows would hand the same version number to two documents
	// whenever every version of a group was inactive.
	tag, err := s.client.pool.Exec(ctx, `
		WITH incoming AS (
			SELECT u.ord, u.restaurant_id, u.retrieval_scope, u.doc_type, u.title,
			       u.content, u.content_hash, u.borough, u.metadata,
			       u.source_record_ids, u.snapshot_at, u.version, u.is_active
			FROM unnest(
				$1::bigint[], $2::text[], $3::text[], $4::text[], $5::text[],
				$6::text[], $7::text[], $8::jsonb[], $9::text[],
				$10::timestamptz[], $11::int[], $12::boolean[]
			) WITH ORDINALITY AS u(
				restaurant_id, retrieval_scope, doc_type, title,
				content, content_hash, borough, metadata, source_record_ids,
				snapshot_at, version, is_active, ord)
		), next_version AS (
			SELECT i.ord,
			       coalesce((
			           SELECT max(d.version) + 1
			           FROM knowledge_documents d
			           WHERE d.restaurant_id = i.restaurant_id
			             AND d.retrieval_scope = i.retrieval_scope
			             AND d.doc_type = i.doc_type
			       ), 1) AS version
			FROM incoming i
		)
		INSERT INTO knowledge_documents (
			restaurant_id, retrieval_scope, doc_type, title, content,
			content_hash, borough, metadata, source_record_ids, snapshot_at,
			version, is_active)
		SELECT i.restaurant_id, i.retrieval_scope, i.doc_type,
		       i.title, i.content, i.content_hash, i.borough, i.metadata,
		       coalesce(string_to_array(i.source_record_ids, chr(31)), '{}')::text[],
		       i.snapshot_at, nv.version, i.is_active
		FROM incoming i
		JOIN next_version nv ON nv.ord = i.ord
		ON CONFLICT (restaurant_id, retrieval_scope, doc_type, content_hash)
		DO NOTHING`,
		restaurantIDs, scopes, docTypes, titles, contents, hashes,
		boroughs, metadata, sourceIDs, snapshotAt, versions, active)
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
// inserts documents with is_active = false: a document becomes recallable in
// one step, after its vector exists.
func (s *KnowledgeStore) PendingDocuments(ctx context.Context, limit int) ([]evidence.KnowledgeDocument, error) {
	if limit <= 0 {
		return []evidence.KnowledgeDocument{}, nil
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	rows, err := s.client.pool.Query(ctx,
		`SELECT `+knowledgeColumns+`
		 FROM knowledge_documents
		 WHERE is_active AND embedding IS NULL
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

// DistinctEmbeddingModels reports which models are present among documents
// that already have a vector.
//
// The embedding stage calls this before writing: two models sharing one live
// set produce similarity scores that cannot be interpreted, so the stage
// refuses to continue unless the operator forces the change.
func (s *KnowledgeStore) DistinctEmbeddingModels(ctx context.Context) ([]port.EmbeddingModelInfo, error) {
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()

	rows, err := s.client.pool.Query(ctx, `
		SELECT embedding_model, embedding_dimensions, count(*)
		FROM knowledge_documents
		WHERE embedding IS NOT NULL AND embedding_model IS NOT NULL
		GROUP BY embedding_model, embedding_dimensions
		ORDER BY embedding_model`)
	if err != nil {
		return nil, operationError("postgres: list embedding models", err)
	}
	defer rows.Close()

	out := make([]port.EmbeddingModelInfo, 0, 4)
	for rows.Next() {
		var info port.EmbeddingModelInfo
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
		)
		// embedding_model and embedding_dimensions are nullable because a
		// document exists before its vector does, so they are scanned through
		// pointers rather than straight into the domain fields.
		if err := rows.Scan(
			&doc.DocumentID, &doc.RestaurantID, &scope, &docType, &title, &doc.Content,
			&doc.ContentHash, &embeddingModel, &dimensions, &borough,
			&metadataRaw, &doc.SourceRecordIDs, &snapshotAt, &doc.Version, &doc.IsActive,
		); err != nil {
			return nil, operationError("postgres: scan knowledge document", err)
		}
		doc.Scope = evidence.RetrievalScope(scope)
		doc.DocType = evidence.DocType(docType)
		if title != nil {
			doc.Title = *title
		}
		if embeddingModel != nil {
			doc.EmbeddingModel = *embeddingModel
		}
		if dimensions != nil {
			doc.EmbeddingDimensions = int(*dimensions)
		}
		if snapshotAt != nil {
			doc.SnapshotAt = *snapshotAt
		}
		// The stored metadata is authoritative; the borough column is only a
		// denormalised copy for the index predicate.
		doc.Metadata = map[string]any{}
		if len(metadataRaw) > 0 {
			if err := json.Unmarshal(metadataRaw, &doc.Metadata); err != nil {
				return nil, operationError("postgres: decode document metadata", err)
			}
		}
		if doc.Metadata == nil {
			doc.Metadata = map[string]any{}
		}
		if borough != nil && *borough != "" {
			doc.Metadata["borough"] = *borough
		}
		out = append(out, doc)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate knowledge documents", err)
	}
	return out, nil
}
