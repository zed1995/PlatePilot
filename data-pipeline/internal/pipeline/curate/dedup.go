package curate

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// normalizeText collapses runs of whitespace so trivially different copies of
// the same review hash identically.
func normalizeText(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// TextHash returns the canonical hash of a review's normalised text.
func TextHash(text string) string {
	sum := sha256.Sum256([]byte(normalizeText(text)))
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
