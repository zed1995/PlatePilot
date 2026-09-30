package curate

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// NormalizeText collapses runs of whitespace so trivially different copies of
// the same text hash identically.
//
// It deliberately normalises whitespace and nothing else: no case folding, no
// punctuation rewriting, no full-width conversion. Those would hide real
// wording changes, which is the opposite of what a content hash is for.
//
// It is exported because the M2 knowledge-document hash must agree with this
// rule; a second, subtly different normaliser would mean the same review
// hashed differently in the two stages.
func NormalizeText(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// normalizeText is the internal alias kept for the existing call sites.
func normalizeText(text string) string { return NormalizeText(text) }

// TextHash returns the canonical hash of a review's normalised text.
func TextHash(text string) string {
	return HashNormalized(NormalizeText(text))
}

// HashNormalized hashes already-normalised text. Callers that have their own
// normalisation step use this so the digest format stays identical.
func HashNormalized(normalized string) string {
	sum := sha256.Sum256([]byte(normalized))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ReviewDedupKey is the deterministic key used to collapse duplicate reviews
// within a single import run:
//
//	sha256(gmap_id + user_id + time + text_hash)
//
// The reviewer's raw user_id only ever participates here; it is never stored in
// a curated collection. It is *not* the review's primary key: reviews.id is an
// identity column assigned by the database, and re-import idempotency is
// enforced by the unique index on (restaurant_id, text_hash, rating, reviewed_at).
func ReviewDedupKey(gmapID, userID string, unixMilli int64, textHash string) string {
	h := sha256.New()
	h.Write([]byte(gmapID))
	h.Write([]byte{0})
	h.Write([]byte(userID))
	h.Write([]byte{0})
	h.Write([]byte(strconv.FormatInt(unixMilli, 10)))
	h.Write([]byte{0})
	h.Write([]byte(textHash))
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}
