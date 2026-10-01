package retrieval

import (
	"context"
	"errors"
	"testing"

	"github.com/zed/platepilot/shared/domain/search"
	"github.com/zed/platepilot/shared/port"
)

// stubRerank returns whatever the test tells it to.
type stubRerank struct {
	modelID    string
	out        []search.RestaurantCandidate
	err        error
	calledWith []search.RestaurantCandidate
}

func (s *stubRerank) ModelID() string { return s.modelID }

func (s *stubRerank) Rerank(_ context.Context, _ string, candidates []search.RestaurantCandidate) ([]search.RestaurantCandidate, error) {
	s.calledWith = candidates
	if s.err != nil {
		return nil, s.err
	}
	return s.out, nil
}

var _ port.RerankProvider = (*stubRerank)(nil)

func sampleCandidates() []search.RestaurantCandidate {
	return []search.RestaurantCandidate{
		{RestaurantID: 1, Name: "A", Score: 0.9},
		{RestaurantID: 2, Name: "B", Score: 0.5},
		{RestaurantID: 3, Name: "C", Score: 0.1},
	}
}

// The system has to run with no reranker at all. That is the default state, so
// it is the case most worth pinning.
func TestApplyRerankWithoutAProviderKeepsTheFusedOrder(t *testing.T) {
	in := sampleCandidates()
	got := ApplyRerank(context.Background(), "q", in, nil)
	if got.Applied {
		t.Fatal("no provider means rerank was not applied")
	}
	if got.ModelID != "" {
		t.Fatalf("no provider means no model id, got %q", got.ModelID)
	}
	for i := range in {
		if got.Candidates[i].RestaurantID != in[i].RestaurantID {
			t.Fatalf("order changed without a provider: %v", ids(got.Candidates))
		}
	}
}

func TestApplyRerankReordersWhenValid(t *testing.T) {
	in := sampleCandidates()
	reversed := []search.RestaurantCandidate{in[2], in[1], in[0]}
	got := ApplyRerank(context.Background(), "q", in, &stubRerank{modelID: "stub", out: reversed})
	if !got.Applied {
		t.Fatalf("a valid permutation must be applied, note = %q", got.Note)
	}
	if got.ModelID != "stub" {
		t.Fatalf("model id = %q", got.ModelID)
	}
	for i, id := range []int64{3, 2, 1} {
		if got.Candidates[i].RestaurantID != id {
			t.Fatalf("want reversed order, got %v", ids(got.Candidates))
		}
	}
}

// A reranker that drops a candidate turns a page of three into a page of one,
// and nothing else can tell that from a deliberate rerank: the request succeeds
// and the response still looks complete. This is the check that catches it.
func TestApplyRerankDiscardsAResultThatIsNotAPermutation(t *testing.T) {
	cases := map[string][]search.RestaurantCandidate{
		"dropped one":  {{RestaurantID: 2}, {RestaurantID: 3}},
		"added one":    {{RestaurantID: 1}, {RestaurantID: 2}, {RestaurantID: 3}, {RestaurantID: 4}},
		"invented one": {{RestaurantID: 1}, {RestaurantID: 2}, {RestaurantID: 4}},
		"duplicated":   {{RestaurantID: 1}, {RestaurantID: 1}, {RestaurantID: 2}},
		"replaced one": {{RestaurantID: 1}, {RestaurantID: 2}, {RestaurantID: 99}},
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			in := sampleCandidates()
			got := ApplyRerank(context.Background(), "q", in, &stubRerank{modelID: "stub", out: out})
			if got.Applied {
				t.Fatal("a non-permutation must not be applied")
			}
			if got.Note == "" {
				t.Fatal("discarding a rerank result must be explained")
			}
			if got.ModelID != "stub" {
				t.Fatal("a configured-but-rejected rerank still reports its model")
			}
			for i := range in {
				if got.Candidates[i].RestaurantID != in[i].RestaurantID {
					t.Fatalf("the fused order must survive: %v", ids(got.Candidates))
				}
			}
		})
	}
}

// A failing reranker degrades the ranking; it does not fail the search.
func TestApplyRerankDegradesOnProviderError(t *testing.T) {
	in := sampleCandidates()
	got := ApplyRerank(context.Background(), "q", in,
		&stubRerank{modelID: "stub", err: errors.New("boom")})
	if got.Applied {
		t.Fatal("a failed rerank must not be reported as applied")
	}
	if got.Note == "" {
		t.Fatal("a failed rerank must be explained")
	}
	if got.ModelID != "stub" {
		t.Fatal("the model id must survive a failure so the trace can show it")
	}
	for i := range in {
		if got.Candidates[i].RestaurantID != in[i].RestaurantID {
			t.Fatalf("the fused order must survive a failure: %v", ids(got.Candidates))
		}
	}
}

// Reordering is the point of a reranker, so scores it rewrites are legitimate.
func TestApplyRerankKeepsRewrittenScores(t *testing.T) {
	in := sampleCandidates()
	out := []search.RestaurantCandidate{
		{RestaurantID: 3, Score: 0.99},
		{RestaurantID: 2, Score: 0.50},
		{RestaurantID: 1, Score: 0.01},
	}
	got := ApplyRerank(context.Background(), "q", in, &stubRerank{modelID: "stub", out: out})
	if !got.Applied {
		t.Fatalf("rewriting scores is allowed: %q", got.Note)
	}
	if got.Candidates[0].Score != 0.99 {
		t.Fatalf("score = %v, want 0.99", got.Candidates[0].Score)
	}
}
