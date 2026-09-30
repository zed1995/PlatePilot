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
	cases := map[string]string{
		"crlf":         "Mon-Fri 11:00-22:00\r\nSat 11:00-23:00",
		"extra blank":  "Mon-Fri 11:00-22:00\n\n\n\nSat 11:00-23:00",
		"trailing":     "Mon-Fri 11:00-22:00\nSat 11:00-23:00   ",
		"leading":      "   Mon-Fri 11:00-22:00\nSat 11:00-23:00",
		"double space": "Mon-Fri  11:00-22:00\nSat 11:00-23:00",
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
