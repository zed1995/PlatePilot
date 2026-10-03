package postgres

import (
	"testing"

	"github.com/zed1995/platepilot/shared/domain/evidence"
)

// The borough is not a label. It is the predicate that decides which of the six
// partial HNSW indexes holds a document, so a value that is read wrong here is
// a document that no index can return. These three helpers are the whole of
// that translation, and they are reachable without a database.
func TestBoroughOf(t *testing.T) {
	cases := []struct {
		name string
		doc  evidence.KnowledgeDocument
		want string
	}{
		{"absent", evidence.KnowledgeDocument{}, ""},
		{"nil metadata", evidence.KnowledgeDocument{Metadata: nil}, ""},
		{"present", evidence.KnowledgeDocument{
			Metadata: map[string]any{"borough": "manhattan"},
		}, "manhattan"},
		{"wrong type", evidence.KnowledgeDocument{
			// A number where a string belongs is a builder bug, not something
			// to panic on: the document still has to be storable, it just does
			// not belong to a borough index.
			Metadata: map[string]any{"borough": 42},
		}, ""},
		{"empty value", evidence.KnowledgeDocument{
			Metadata: map[string]any{"borough": ""},
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := boroughOf(tc.doc); got != tc.want {
				t.Errorf("boroughOf = %q, want %q", got, tc.want)
			}
		})
	}
}

// An empty string in a text column and a NULL are different claims: "" is an
// empty title, NULL is "no title was recorded". The distinction is what lets
// the report tell a missing field from a blank one.
func TestOptionalString(t *testing.T) {
	if got := optionalString(""); got != nil {
		t.Errorf("optionalString(\"\") = %q, want nil so the column stores NULL", *got)
	}
	if got := optionalString("   "); got != nil {
		t.Errorf("optionalString(%q) = %q, want nil; whitespace is not a value", "   ", *got)
	}
	got := optionalString("  Pizza Palace  ")
	if got == nil {
		t.Fatal("optionalString returned nil for a non-empty value")
	}
	if *got != "Pizza Palace" {
		t.Errorf("optionalString did not trim: %q", *got)
	}
}

// The metadata column defaults to '{}', so a nil map has to become an empty
// object on the way in. Writing JSON null instead would violate that default
// and make "has no metadata" indistinguishable from "metadata was not captured".
func TestOrEmptyMap(t *testing.T) {
	if got := orEmptyMap(nil); got == nil {
		t.Error("orEmptyMap(nil) = nil, want an empty map so the column gets {} not null")
	}
	if got := orEmptyMap(nil); len(got) != 0 {
		t.Errorf("orEmptyMap(nil) = %v, want an empty map", got)
	}
	original := map[string]any{"borough": "queens"}
	got := orEmptyMap(original)
	if got["borough"] != "queens" {
		t.Errorf("orEmptyMap dropped its input: %v", got)
	}
}
