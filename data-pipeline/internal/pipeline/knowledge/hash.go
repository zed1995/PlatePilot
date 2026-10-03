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

	"github.com/zed1995/platepilot/data-pipeline/internal/pipeline/curate"
	"github.com/zed1995/platepilot/shared/domain/evidence"
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
// line endings or trailing spaces does not churn a new version. The rule is
// deliberately narrower than the one reviews use — see normalizeForHash for
// why sharing that rule would be wrong here.
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

// normalizeForHash applies the narrow normalisation the plan specifies, and
// nothing else.
//
// It is deliberately not curate.NormalizeText, which is the reviews rule and
// collapses every whitespace run into a single space. That is the right rule
// for de-duplicating free-form review text, where "the same words" is exactly
// what makes two reviews duplicates. It is the wrong rule for a document: a
// knowledge document's line structure carries its meaning, so flattening it
// makes two documents whose wording is identical but whose layout differs hash
// the same. Since content_hash is half the idempotency key, that is a
// document being silently swallowed as one that was already stored.
//
// The four steps below are the whole rule:
//
//  1. CRLF and lone CR become LF;
//  2. trailing whitespace is dropped from each line;
//  3. three or more consecutive newlines become two, so paragraphs survive
//     while a stray extra blank line does not churn a version;
//  4. leading and trailing whitespace is trimmed.
//
// Case, punctuation, and full-width characters are left alone on purpose:
// normalising them would mask a real change in the text.
func normalizeForHash(content string) string {
	text := strings.ReplaceAll(content, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t\v\f")
	}
	text = strings.Join(lines, "\n")

	// A run of n+1 newlines is n blank lines. Three blank lines is four
	// newlines, which is what the rule collapses to two blank lines.
	for strings.Contains(text, "\n\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n\n", "\n\n\n")
	}
	return strings.TrimSpace(text)
}

// DocumentKey is the idempotency key for one document row: the same
// restaurant, scope, doc type, and text is the same document, so a re-run
// writes nothing.
func DocumentKey(scope evidence.RetrievalScope, docType evidence.DocType, restaurantID int64, content string) string {
	return ContentHash(scope, docType, restaurantID, content)
}
