package retrieval_test

import (
	"testing"

	"github.com/zed1995/platepilot/shared/domain/retrieval"
)

// The rendered soft text is what the vector channel embeds, so its shape is a
// contract rather than formatting: the user's words and the topic keys the
// corpus is tagged with, in the order they were asked for, with nothing
// repeated.
func TestSoftQueryTextKeepsWordsAndTopicsInOrder(t *testing.T) {
	got := retrieval.SoftQueryText([]retrieval.SoftCondition{
		{Text: "安静", Topic: "ambience"},
		{Text: "适合约会", Topic: "ambience"},
	})
	if want := "安静 ambience 适合约会"; got != want {
		t.Fatalf("SoftQueryText = %q, want %q", got, want)
	}
}

// A repeated phrase contributes once. The conditions come from a model, and a
// model that says the same thing twice should not weight it twice.
func TestSoftQueryTextDropsRepeats(t *testing.T) {
	got := retrieval.SoftQueryText([]retrieval.SoftCondition{
		{Text: "安静", Topic: "ambience"},
		{Text: " 安静 ", Topic: "ambience"},
	})
	if want := "安静 ambience"; got != want {
		t.Fatalf("SoftQueryText = %q, want %q", got, want)
	}
}

// An unmapped condition still contributes its words: that is the whole reason a
// condition keeps its text when the topic lookup fails.
func TestSoftQueryTextKeepsAnUnmappedCondition(t *testing.T) {
	got := retrieval.SoftQueryText([]retrieval.SoftCondition{{Text: "有个好院子"}})
	if want := "有个好院子"; got != want {
		t.Fatalf("SoftQueryText = %q, want %q", got, want)
	}
}

// A condition that is only a topic — the user's wording was lost somewhere — is
// still renderable, and one that is only whitespace is not a condition at all.
func TestSoftQueryTextHandlesPartialConditions(t *testing.T) {
	cases := map[string]struct {
		conditions []retrieval.SoftCondition
		want       string
	}{
		"topic only":      {[]retrieval.SoftCondition{{Topic: "wait"}}, "wait"},
		"blank":           {[]retrieval.SoftCondition{{Text: "   "}}, ""},
		"nothing":         {nil, ""},
		"blank then real": {[]retrieval.SoftCondition{{Text: " "}, {Text: "安静"}}, "安静"},
		"words and topics combined": {
			[]retrieval.SoftCondition{{Text: "不排队", Topic: "wait"}},
			"不排队 wait",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := retrieval.SoftQueryText(tc.conditions); got != tc.want {
				t.Fatalf("SoftQueryText = %q, want %q", got, tc.want)
			}
		})
	}
}

// HasSoftConditions is what keeps a soft-only request from being refused as
// empty, so it has to be true for both halves of a condition and false for a
// list of blanks.
func TestHasSoftConditions(t *testing.T) {
	cases := map[string]struct {
		conditions []retrieval.SoftCondition
		want       bool
	}{
		"none":       {nil, false},
		"words":      {[]retrieval.SoftCondition{{Text: "安静"}}, true},
		"topic only": {[]retrieval.SoftCondition{{Topic: "ambience"}}, true},
		"blank":      {[]retrieval.SoftCondition{{Text: "  "}}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			req := retrieval.Request{SoftConditions: tc.conditions}
			if got := req.HasSoftConditions(); got != tc.want {
				t.Fatalf("HasSoftConditions = %t, want %t", got, tc.want)
			}
		})
	}
}
