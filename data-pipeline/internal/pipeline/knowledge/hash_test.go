package knowledge

import (
	"testing"

	"github.com/zed/platepilot/shared/domain/evidence"
)

func TestContentHashIsStable(t *testing.T) {
	a := ContentHash(evidence.ScopeRestaurant, evidence.DocTypeRestaurantProfile, 7, "Pizza place")
	b := ContentHash(evidence.ScopeRestaurant, evidence.DocTypeRestaurantProfile, 7, "Pizza place")
	if a != b {
		t.Errorf("same input hashed differently:\n%s\n%s", a, b)
	}
	if a == "" {
		t.Fatal("hash must not be empty")
	}
}

func TestContentHashIgnoresWhitespaceOnlyChurn(t *testing.T) {
	base := ContentHash(evidence.ScopeEvidence, evidence.DocTypeRestaurantHours, 1, "Mon-Fri 11:00-22:00\nSat 11:00-23:00")
	// Only the four steps the plan lists are forgiven. A run of *two* spaces
	// inside a line is not one of them, so it is content and has to change the
	// hash: the rule tidsies line endings, trailing whitespace, long runs of
	// blank lines, and the edges, and leaves the inside of a line alone.
	cases := map[string]string{
		"crlf":         "Mon-Fri 11:00-22:00\r\nSat 11:00-23:00",
		"trailing":     "Mon-Fri 11:00-22:00\nSat 11:00-23:00   ",
		"leading":      "   Mon-Fri 11:00-22:00\nSat 11:00-23:00",
		"lone cr":      "Mon-Fri 11:00-22:00\rSat 11:00-23:00",
		"tab trailing": "Mon-Fri 11:00-22:00\t\nSat 11:00-23:00",
	}
	for name, content := range cases {
		if got := ContentHash(evidence.ScopeEvidence, evidence.DocTypeRestaurantHours, 1, content); got != base {
			t.Errorf("%s: whitespace changed the hash", name)
		}
	}
}

func TestContentHashChangesOnWordingChange(t *testing.T) {
	base := ContentHash(evidence.ScopeEvidence, evidence.DocTypeRestaurantAttributes, 1, "Outdoor seating")
	for _, content := range []string{
		"Outdoor seating!", // punctuation
		"outdoor seating",  // case
		"Indoor seating",   // different wording
		"Outdoor seatings", // typo
		"Outdoor seating ", // trailing space only: must NOT change
	} {
		got := ContentHash(evidence.ScopeEvidence, evidence.DocTypeRestaurantAttributes, 1, content)
		sameAsBase := got == base
		wantSame := content == "Outdoor seating "
		if sameAsBase != wantSame {
			t.Errorf("content %q: changed=%v, want changed=%v", content, sameAsBase, !wantSame)
		}
	}
}

func TestContentHashSeparatesRestaurants(t *testing.T) {
	// Two restaurants really can share identical short text.
	const text = "Wi-Fi available"
	a := ContentHash(evidence.ScopeEvidence, evidence.DocTypeRestaurantAttributes, 1, text)
	b := ContentHash(evidence.ScopeEvidence, evidence.DocTypeRestaurantAttributes, 2, text)
	if a == b {
		t.Error("identical text at two restaurants collided")
	}
}

func TestContentHashSeparatesDocTypes(t *testing.T) {
	const text = "11:00-22:00"
	a := ContentHash(evidence.ScopeEvidence, evidence.DocTypeRestaurantHours, 1, text)
	b := ContentHash(evidence.ScopeEvidence, evidence.DocTypeRestaurantAttributes, 1, text)
	if a == b {
		t.Error("identical text under two doc types collided")
	}
}

func TestContentHashSeparatesScopes(t *testing.T) {
	const text = "some content"
	a := ContentHash(evidence.ScopeRestaurant, evidence.DocTypeRestaurantProfile, 1, text)
	b := ContentHash(evidence.ScopeEvidence, evidence.DocTypeRestaurantReviewSummary, 1, text)
	if a == b {
		t.Error("identical text in two scopes collided")
	}
}

func TestDocumentKeyMatchesContentHash(t *testing.T) {
	got := DocumentKey(evidence.ScopeEvidence, evidence.DocTypeRestaurantHours, 3, "11:00-22:00")
	want := ContentHash(evidence.ScopeEvidence, evidence.DocTypeRestaurantHours, 3, "11:00-22:00")
	if got != want {
		t.Errorf("DocumentKey = %s, want %s", got, want)
	}
}

// The normalisation rule is deliberately narrow, and "narrow" has a specific
// meaning here: it may tidy line endings and runs of blank lines, and it may
// trim the edges, but it must not touch wording and it must not destroy the
// document's structure.
//
// Collapsing every whitespace run into a single space satisfies the tests
// above while quietly deleting the paragraph structure, which is the part of a
// document that says what the lines are *for*. A hours document that lists
// "Mon-Fri 11:00-22:00" and "Sat 11:00-23:00" as separate lines becomes one
// line, and two documents whose wording is identical but whose line structure
// differs then hash the same. Since content_hash is half the idempotency key,
// that is a document being silently treated as one that was already stored.
func TestContentHashPreservesLineStructure(t *testing.T) {
	hours := ContentHash(evidence.ScopeEvidence, evidence.DocTypeRestaurantHours, 1,
		"Mon-Fri 11:00-22:00\nSat 11:00-23:00")
	oneLine := ContentHash(evidence.ScopeEvidence, evidence.DocTypeRestaurantHours, 1,
		"Mon-Fri 11:00-22:00 Sat 11:00-23:00")
	if hours == oneLine {
		t.Error("line structure was collapsed: a two-line document and the same words " +
			"on one line hash identically, so the second one is treated as already stored")
	}
}

func TestContentHashCollapsesRunsOfBlankLines(t *testing.T) {
	// Two blank lines is the canonical form: three or more collapse to it, and
	// one blank line stays one because it is different text.
	want := ContentHash(evidence.ScopeEvidence, evidence.DocTypeRestaurantReviewSummary, 1,
		"Great pizza.\n\n\nSlow service.\n\n\nWould return.")
	for name, content := range map[string]string{
		"three blanks":            "Great pizza.\n\n\n\nSlow service.\n\n\n\nWould return.",
		"many blanks":             "Great pizza.\n\n\n\n\n\nSlow service.\n\n\n\n\n\nWould return.",
		"crlf":                    "Great pizza.\r\n\r\n\r\nSlow service.\r\n\r\n\r\nWould return.",
		"trailing ws":             "Great pizza.   \n\n\nSlow service.\t\n\n\nWould return.  ",
		"space on the blank line": "Great pizza.\n   \n\nSlow service.\n \n\n\nWould return.",
	} {
		if got := ContentHash(evidence.ScopeEvidence, evidence.DocTypeRestaurantReviewSummary, 1, content); got != want {
			t.Errorf("%s: hash differs; the blank-line rule is not the one specified", name)
		}
	}
}

// Line structure is content. Two documents that list the same facts in a
// different order are different documents, and the hours builder produces
// exactly this kind of pair.
func TestContentHashDistinguishesReorderedLines(t *testing.T) {
	a := ContentHash(evidence.ScopeEvidence, evidence.DocTypeRestaurantHours, 1,
		"Mon-Fri 11:00-22:00\nSat 11:00-23:00")
	b := ContentHash(evidence.ScopeEvidence, evidence.DocTypeRestaurantHours, 1,
		"Sat 11:00-23:00\nMon-Fri 11:00-22:00")
	if a == b {
		t.Error("reordering two lines did not change the hash")
	}
}
