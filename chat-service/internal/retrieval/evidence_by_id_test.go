package retrieval

import (
	"context"
	"strings"
	"testing"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/evidence"
)

// ---------------------------------------------------------------------------
// Recall by cited document id
//
// A citation event carries document ids, and the client that received them has
// no way to learn which restaurant each belongs to. The read that answers "show
// me this footnote" therefore has to take the ids themselves.
// ---------------------------------------------------------------------------

// The ids go to the id-directed read rather than through the restaurant
// recall, because guessing each document's restaurant is exactly how a footnote
// gets attributed to the wrong place.
func TestEvidenceRoutesAnIdOnlyRequestToTheIdRead(t *testing.T) {
	knowledge := &stubKnowledge{byIDs: []evidence.Evidence{cite(101, 7, evidence.DocTypeRestaurantRepresentativeReviews, 0.5)}}
	service := evidenceService(t, knowledge, nil)

	result, err := service.Evidence(context.Background(), EvidenceRequest{EvidenceIDs: []int64{101}})
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].EvidenceID != 101 {
		t.Fatalf("evidence = %+v, want the named document", result.Evidence)
	}
	if len(knowledge.lastIDs) != 1 || knowledge.lastIDs[0] != 101 {
		t.Fatalf("store saw ids %v", knowledge.lastIDs)
	}
}

// A restaurant scope wins when both are named. The transport's own rule is that
// an explicit path beats anything derived from the body, and the alternative —
// letting a stale id narrow a fresh recall — would silently drop evidence the
// caller asked for.
func TestARestaurantScopeBeatsAnIdList(t *testing.T) {
	knowledge := &stubKnowledge{
		recalled: []evidence.Evidence{cite(7, 7, evidence.DocTypeRestaurantReviewSummary, 0.9)},
		byIDs:    []evidence.Evidence{cite(101, 7, evidence.DocTypeRestaurantRepresentativeReviews, 0.5)},
	}
	service := evidenceService(t, knowledge, nil)

	result, err := service.Evidence(context.Background(), EvidenceRequest{
		RestaurantIDs: []int64{7},
		EvidenceIDs:   []int64{101},
	})
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].EvidenceID != 7 {
		t.Fatalf("evidence = %+v, want the restaurant recall", result.Evidence)
	}
	if knowledge.lastIDs != nil {
		t.Fatalf("the id read ran despite a restaurant scope: %v", knowledge.lastIDs)
	}
}

// An id list of nothing is no scope. Returning "no evidence" instead would read
// as a retired citation rather than as a malformed request.
func TestAnEmptyIdListIsStillNoScope(t *testing.T) {
	knowledge := &stubKnowledge{byIDs: []evidence.Evidence{cite(101, 7, evidence.DocTypeRestaurantReviewSummary, 0.5)}}
	service := evidenceService(t, knowledge, nil)

	_, err := service.Evidence(context.Background(), EvidenceRequest{EvidenceIDs: []int64{0, -1}})
	if err == nil {
		t.Fatal("an id list with nothing positive in it must still be refused")
	}
	if got := errs.CodeOf(err); got != errs.CodeRetrievalNoScope {
		t.Fatalf("code = %q, want %q", got, errs.CodeRetrievalNoScope)
	}
}

// The id-directed read still runs the citation hygiene passes. A document with
// no source has to become visibly "unknown" rather than render as a quote with a
// blank attribution, which looks like a UI bug rather than a provenance gap.
func TestTheIdReadStillResolvesCitations(t *testing.T) {
	orphan := cite(101, 7, evidence.DocTypeRestaurantReviewSummary, 0.9)
	orphan.Source = ""
	knowledge := &stubKnowledge{byIDs: []evidence.Evidence{orphan}}
	service := evidenceService(t, knowledge, nil)

	result, err := service.EvidenceByIDs(context.Background(), []int64{101})
	if err != nil {
		t.Fatalf("evidence by ids: %v", err)
	}
	if len(result.Evidence) != 1 {
		t.Fatalf("evidence = %+v", result.Evidence)
	}
	if got := result.Evidence[0].Source; got != unknownSource {
		t.Fatalf("source = %q, want %q", got, unknownSource)
	}
	if !strings.Contains(strings.Join(result.Trace.Warnings, " "), "unknown") {
		t.Fatalf("the missing provenance is not in the trace: %v", result.Trace.Warnings)
	}
}

// An id that is absent or retired is skipped rather than answered with an
// error: one dead footnote must not invalidate the ones beside it, and the
// caller can tell which came back by comparing lengths. It is warned about
// because silently returning fewer documents than asked for reads as a corpus
// gap.
func TestTheIdReadSkipsDocumentsItCannotCite(t *testing.T) {
	knowledge := &stubKnowledge{byIDs: []evidence.Evidence{
		cite(101, 7, evidence.DocTypeRestaurantReviewSummary, 0.9),
	}}
	service := evidenceService(t, knowledge, nil)

	result, err := service.EvidenceByIDs(context.Background(), []int64{101, 404})
	if err != nil {
		t.Fatalf("evidence by ids: %v", err)
	}
	if len(result.Evidence) != 1 {
		t.Fatalf("evidence = %+v, want the one citable document", result.Evidence)
	}
	if len(result.Trace.Warnings) == 0 {
		t.Fatal("the missing id left no trace")
	}
	if result.Trace.Recalled != 1 || result.Trace.ScopeSize != 2 {
		t.Fatalf("trace = %+v, want scope 2 / recalled 1", result.Trace)
	}
}

// Order follows the request because the caller received these ids in footnote
// order; re-sorting here would renumber the answer's citations.
func TestTheIdReadKeepsTheRequestedOrder(t *testing.T) {
	second := cite(202, 8, evidence.DocTypeRestaurantHours, 0.1)
	first := cite(101, 7, evidence.DocTypeRestaurantReviewSummary, 0.9)
	knowledge := &stubKnowledge{byIDs: []evidence.Evidence{first, second}}
	service := evidenceService(t, knowledge, nil)

	result, err := service.EvidenceByIDs(context.Background(), []int64{202, 101})
	if err != nil {
		t.Fatalf("evidence by ids: %v", err)
	}
	if len(result.Evidence) != 2 || result.Evidence[0].EvidenceID != 202 {
		t.Fatalf("order = %+v, want the requested order", result.Evidence)
	}
}
