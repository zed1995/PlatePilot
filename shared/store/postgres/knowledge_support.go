package postgres

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/store"
)

// optionalString returns nil for an empty value so the column stores SQL NULL
// rather than an empty string. An empty title is "unknown"; "" is not the same
// statement, and a default-"" column would erase that distinction.
func optionalString(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// boroughOf reads the denormalised borough. It lives in metadata because that
// is where the builders put it, and the column exists only to make the partial
// index predicate a local test.
func boroughOf(doc evidence.KnowledgeDocument) string {
	if doc.Metadata == nil {
		return ""
	}
	value, _ := doc.Metadata["borough"].(string)
	return value
}

// orEmptyMap returns an empty map for nil metadata so json.Marshal writes {}
// rather than null, matching the column default.
func orEmptyMap(metadata map[string]any) map[string]any {
	if metadata == nil {
		return map[string]any{}
	}
	return metadata
}

// vectorLiteral renders a vector as the pgvector text input form.
//
// float32 is formatted through strconv with full precision rather than being
// cast from float64, so the stored value is exactly what the model produced and
// a later recomputation agrees with it.
func vectorLiteral(vec []float32) string {
	var b strings.Builder
	// "[1,0.5]" is 2 + 3 + 10*n characters; sizing avoids a mid-loop grow for
	// the 1024-dimension vectors this pipeline uses.
	b.Grow(2 + len(vec)*11)
	b.WriteByte('[')
	for i, v := range vec {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(v), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// parseVectorLiteral is the inverse of vectorLiteral.
//
// pgvector renders a vector as the same bracketed, comma-separated text the
// store writes, so reading one back is a parse rather than a second codec. The
// scan target is a string, not []float32, because no pgvector type is
// registered on the pool: handing the raw text to strconv is what keeps the
// stored width a property of the schema instead of of a Go type.
//
// A nil or empty input yields a nil vector, which is how "this document has no
// vector yet" stays distinguishable from "this document has an empty vector".
func parseVectorLiteral(text *string) ([]float32, error) {
	if text == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*text)
	if trimmed == "" || trimmed == "[]" {
		return nil, nil
	}
	trimmed = strings.TrimPrefix(trimmed, "[")
	trimmed = strings.TrimSuffix(trimmed, "]")
	if trimmed == "" {
		return nil, nil
	}
	parts := strings.Split(trimmed, ",")
	out := make([]float32, 0, len(parts))
	for _, part := range parts {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 32)
		if err != nil {
			return nil, errs.Newf(errs.CodeInternal,
				"postgres: parse stored vector component %q", part)
		}
		out = append(out, float32(value))
	}
	return out, nil
}

// sourceIDDelimiter joins the source record ids of one document so they can
// travel through a flat text[] parameter. It is the ASCII unit separator, which
// cannot occur in an identifier from the source dataset; an empty string yields
// an empty array rather than a one-element array holding "".
const sourceIDDelimiter = "\x1f"

// scanScoredDocuments materialises search results: the shared document
// projection followed by the distance column.
//
// The scan is written out rather than delegating to scanKnowledgeDocuments
// because that helper takes a pgx.Rows of the bare projection, and wrapping the
// rows to hide the extra column would be more machinery than one scan. The two
// must stay in step: knowledgeColumns is referenced in both.
func scanScoredDocuments(rows pgx.Rows) ([]store.ScoredDocument, error) {
	out := make([]store.ScoredDocument, 0, 16)
	for rows.Next() {
		var (
			item           store.ScoredDocument
			doc            = &item.KnowledgeDocument
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
		if err := rows.Scan(
			&doc.DocumentID, &doc.RestaurantID, &scope, &docType, &title, &doc.Content,
			&doc.ContentHash, &embeddingModel, &dimensions, &borough,
			&metadataRaw, &doc.SourceRecordIDs, &snapshotAt, &doc.Version, &doc.IsActive,
			&embeddingText, &item.Distance,
		); err != nil {
			return nil, operationError("postgres: scan scored document", err)
		}
		vector, err := parseVectorLiteral(embeddingText)
		if err != nil {
			return nil, err
		}
		doc.Embedding = vector
		if err := applyScannedDocument(doc, scope, docType, title, embeddingModel, dimensions,
			borough, metadataRaw, snapshotAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate scored documents", err)
	}
	return out, nil
}

// applyScannedDocument copies the nullable columns of a scanned row onto the
// domain type and decodes its metadata.
//
// It is shared by the document and scored-document scanners so the two cannot
// drift: a column added to knowledgeColumns has to be handled once, and a
// search result must carry exactly the same document state as a direct read.
func applyScannedDocument(
	doc *evidence.KnowledgeDocument,
	scope, docType string,
	title, embeddingModel *string,
	dimensions *int32,
	borough *string,
	metadataRaw []byte,
	snapshotAt *time.Time,
) error {
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
	// denormalised copy that exists for the partial index predicate.
	doc.Metadata = map[string]any{}
	if len(metadataRaw) > 0 {
		if err := json.Unmarshal(metadataRaw, &doc.Metadata); err != nil {
			return operationError("postgres: decode document metadata", err)
		}
	}
	if doc.Metadata == nil {
		doc.Metadata = map[string]any{}
	}
	if borough != nil && *borough != "" {
		doc.Metadata["borough"] = *borough
	}
	return nil
}
