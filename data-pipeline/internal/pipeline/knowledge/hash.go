// Package knowledge builds the retrieval documents that the embedding stage
// turns into vectors.
//
// Nothing in this package talks to a model or a database: its job is to decide
// what text belongs in a document and how to identify it, so that the same
// input always produces the same document and any change produces a new one.
package knowledge

import (
	"strconv"
	"strings"

	"github.com/zed/platepilot/data-pipeline/internal/pipeline/curate"
	"github.com/zed/platepilot/shared/domain/evidence"
)

// ContentHash is the identity of a document's text.
//
// The scope, doc type, and restaurant id are part of the digest because short
// chunks genuinely repeat across restaurants: two places that both serve
// "本店提供 Wi-Fi" would otherwise collide and one document would overwrite
// the other. Including them keeps the hash a full identity rather than a
// content fingerprint that happens to be unique.
//
// The digest is taken over normalised text, so a document that only differs in
// line endings or trailing spaces does not churn a new version. The
// normalisation rule is curate.NormalizeText, the same one reviews use: a
// second rule here would make the same text hash differently in two stages.
func ContentHash(scope evidence.RetrievalScope, docType evidence.DocType, restaurantID int64, content string) string {
	h := curate.HashNormalized(normalizeForHash(content))

	// The identity fields are prefixed onto the content digest and re-hashed so
	// that the result is still a single fixed-width value with the same
	// "sha256:" format the pipeline uses everywhere else.
	var b strings.Builder
	b.WriteString(string(scope))
	b.WriteByte(0)
	b.WriteString(string(docType))
	b.WriteByte(0)
	b.WriteString(strconv.FormatInt(restaurantID, 10))
	b.WriteByte(0)
	b.WriteString(h)
	return curate.HashNormalized(b.String())
}

// normalizeForHash applies the narrow normalisation described on
// ContentHash. It delegates to the shared rule rather than reimplementing it.
func normalizeForHash(content string) string {
	return curate.NormalizeText(content)
}

// DocumentKey is the idempotency key for one document row: the same
// restaurant, scope, doc type, and text is the same document, so a re-run
// writes nothing.
func DocumentKey(scope evidence.RetrievalScope, docType evidence.DocType, restaurantID int64, content string) string {
	return ContentHash(scope, docType, restaurantID, content)
}
