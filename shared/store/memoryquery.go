package store

import "strings"

// MemoryQueryMinRunes is the shortest query term a memory search matches on.
//
// Three is the floor the trigram index can actually narrow on: below it there
// is no complete trigram to look up, so pg_trgm falls back to visiting every
// index entry and the index stops being a filter.
//
// For Chinese text there is a second and blunter reason — a single character
// like "的" appears in almost every phrase, so a one-character term would match
// the user's whole memory set and rank nothing. What the floor buys is a search
// that answers with the memories about what was asked; dropping the short terms
// and finding nothing is a better answer than keeping them and returning
// everything.
const MemoryQueryMinRunes = 3

// MemoryQueryTerms returns the terms a memory search matches on: the
// whitespace-delimited fields of query that are long enough to be worth
// matching, deduplicated case-insensitively, in the order they were written.
//
// It lives beside the MemoryRepository interface rather than inside one adapter
// because the matching rule is part of the contract. If the in-memory store and
// PostgreSQL tokenised differently, one user would get different memories
// depending on which adapter the service was built with, and the contract suite
// could not say which of the two was right.
//
// The rule is deliberately plain: split on whitespace, keep what is long
// enough, no stemming, no stop words, no CJK segmentation. Anything cleverer
// would have to be implemented twice — once in Go and once in SQL — and the two
// implementations would be the thing most likely to drift.
func MemoryQueryTerms(query string) []string {
	fields := strings.Fields(query)
	if len(fields) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(fields))
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if len([]rune(field)) < MemoryQueryMinRunes {
			continue
		}
		key := strings.ToLower(field)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, field)
	}
	return out
}
