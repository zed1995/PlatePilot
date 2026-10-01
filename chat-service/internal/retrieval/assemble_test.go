package retrieval

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/evidence"
)

// longText builds a citation whose content costs roughly `words` tokens.
func longText(words int) string {
	return strings.Repeat("word ", words)
}

func citable(id, restaurantID int64, docType evidence.DocType, score float64, content string) evidence.Evidence {
	return evidence.Evidence{
		EvidenceID:   id,
		RestaurantID: restaurantID,
		DocType:      docType,
		Content:      content,
		Source:       "yelp_review",
		SnapshotAt:   testSnapshot,
		Score:        score,
		ContentHash:  "hash-" + strings.Repeat("x", int(id)),
	}
}

// A citation is a claim a reader can check. One missing its source cannot be, so
// it is dropped rather than rendered.
func TestAssemblyDropsWhatCannotBeCited(t *testing.T) {
	noSource := citable(1, 7, evidence.DocTypeRestaurantReviewSummary, 0.9, longText(20))
	noSource.Source = " "
	noSnapshot := citable(2, 7, evidence.DocTypeRestaurantReviewSummary, 0.9, longText(20))
	noSnapshot.SnapshotAt = time.Time{}
	emptyContent := citable(3, 7, evidence.DocTypeRestaurantReviewSummary, 0.9, "   ")
	noID := citable(4, 7, evidence.DocTypeRestaurantReviewSummary, 0.9, longText(20))
	noID.EvidenceID = 0

	// Nothing citable is a data problem, not a budget one, and the two send a
	// caller to different fixes.
	_, report, err := AssembleEvidence(
		[]evidence.Evidence{noSource, noSnapshot, emptyContent, noID}, AssembleOptions{})
	if err == nil {
		t.Fatal("an empty bundle must be reported, not returned")
	}
	if got := errs.CodeOf(err); got != errs.CodeValidationFailed {
		t.Fatalf("code = %q, want %q", got, errs.CodeValidationFailed)
	}
	for _, reason := range []string{"missing_source", "missing_snapshot", "empty_content", "missing_document_id"} {
		if report.DroppedByReason[reason] != 1 {
			t.Fatalf("reason %q = %d, want 1 (full report %v)",
				reason, report.DroppedByReason[reason], report.DroppedByReason)
		}
	}
}

// An observation time that is impossible or implausible is as uncitable as a
// missing one, and worse: a future date makes the data look newer than it is,
// which is the direction that slips past a freshness check.
func TestAssemblyDropsImplausibleSnapshotTimes(t *testing.T) {
	future := citable(1, 7, evidence.DocTypeRestaurantReviewSummary, 0.9, longText(20))
	future.SnapshotAt = time.Now().Add(48 * time.Hour)

	// Small but non-zero, so it passes the IsZero check and reaches the floor.
	// This is the shape a defaulted timestamp takes.
	ancient := citable(2, 7, evidence.DocTypeRestaurantReviewSummary, 0.9, longText(20))
	ancient.SnapshotAt = time.Date(1999, time.June, 1, 0, 0, 0, 0, time.UTC)

	_, report, err := AssembleEvidence(
		[]evidence.Evidence{future, ancient}, AssembleOptions{})
	if err == nil {
		t.Fatal("no citable item must be reported, not returned as an empty bundle")
	}
	if got := errs.CodeOf(err); got != errs.CodeValidationFailed {
		t.Fatalf("code = %q, want %q", got, errs.CodeValidationFailed)
	}
	for _, reason := range []string{"snapshot_in_future", "implausible_snapshot"} {
		if report.DroppedByReason[reason] != 1 {
			t.Fatalf("reason %q = %d, want 1 (full report %v)",
				reason, report.DroppedByReason[reason], report.DroppedByReason)
		}
	}
}

// The plausibility bounds must not reject a real snapshot. This corpus is a
// 2021 snapshot, so an over-eager floor would silently empty every bundle.
func TestAssemblyKeepsAPlausibleSnapshotAtTheFloor(t *testing.T) {
	item := citable(1, 7, evidence.DocTypeRestaurantReviewSummary, 0.9, longText(20))
	item.SnapshotAt = time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)

	kept, _, err := AssembleEvidence([]evidence.Evidence{item}, AssembleOptions{})
	if err != nil {
		t.Fatalf("a snapshot on the boundary year must be citable: %v", err)
	}
	if len(kept) != 1 {
		t.Fatalf("kept %d, want 1", len(kept))
	}
}

// A quote that stops mid-sentence is not a quote. The budget must drop whole
// documents, and what is kept must be exactly what was recalled.
func TestAssemblyNeverTruncatesACitation(t *testing.T) {
	// 60 words costs 75 tokens, so a 100-token budget holds exactly one.
	items := []evidence.Evidence{
		citable(1, 7, evidence.DocTypeRestaurantReviewSummary, 0.9, longText(60)),
		citable(2, 8, evidence.DocTypeRestaurantReviewSummary, 0.8, longText(60)),
	}
	kept, report, err := AssembleEvidence(items, AssembleOptions{TokenBudget: 100})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(kept) == 0 {
		t.Fatal("the budget should hold at least one of these")
	}
	if report.Tokens > report.TokenBudget {
		t.Fatalf("spent %d tokens against a budget of %d", report.Tokens, report.TokenBudget)
	}
	for _, item := range kept {
		if item.Content != longText(60) {
			t.Fatalf("a citation was altered: %q", item.Content)
		}
	}
	if report.DroppedByReason["token_budget"] != 2-len(kept) {
		t.Fatalf("budget drops = %d, want %d", report.DroppedByReason["token_budget"], 2-len(kept))
	}
}

// The same sentence quoted twice is one source, not two agreeing ones.
func TestAssemblyCollapsesOneSourceReachedTwice(t *testing.T) {
	standalone := citable(1, 7, evidence.DocTypeRestaurantReviewSummary, 0.60, longText(20))
	standalone.ContentHash = "same"
	quoted := citable(2, 7, evidence.DocTypeRestaurantReviewSummary, 0.91, longText(20))
	quoted.ContentHash = "same"

	kept, report, err := AssembleEvidence(
		[]evidence.Evidence{standalone, quoted}, AssembleOptions{})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(kept) != 1 {
		t.Fatalf("want 1 citation, got %d", len(kept))
	}
	if kept[0].EvidenceID != 2 {
		t.Fatalf("kept evidence %d, want the higher-scoring copy", kept[0].EvidenceID)
	}
	if report.Considered != 2 || report.Dropped != 1 {
		t.Fatalf("accounting: considered=%d dropped=%d", report.Considered, report.Dropped)
	}
}

// One well-documented restaurant must not fill the page. The cap is applied
// before the budget so diversity is decided independently of budget luck.
func TestAssemblyCapsWhatOneRestaurantContributes(t *testing.T) {
	items := make([]evidence.Evidence, 0, 9)
	for i := 1; i <= 9; i++ {
		items = append(items, citable(int64(i), 7,
			evidence.DocTypeRestaurantReviewSummary, 1.0-float64(i)/100, longText(10)))
	}
	items = append(items, citable(100, 8, evidence.DocTypeRestaurantReviewSummary, 0.1, longText(10)))

	kept, report, err := AssembleEvidence(items, AssembleOptions{})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	fromSeven := 0
	for _, item := range kept {
		if item.RestaurantID == 7 {
			fromSeven++
		}
	}
	if fromSeven != maxPerRestaurantDocType {
		t.Fatalf("restaurant 7 contributed %d citations, want %d",
			fromSeven, maxPerRestaurantDocType)
	}
	if report.DroppedByReason["per_source_diversity"] != 6 {
		t.Fatalf("diversity drops = %d, want 6", report.DroppedByReason["per_source_diversity"])
	}
	if len(kept) == 0 || kept[len(kept)-1].RestaurantID != 8 {
		t.Fatal("the other restaurant must survive the cap")
	}
}

// Two restaurants can carry identical text; merging them would attribute one
// restaurant's words to another.
func TestAssemblyKeepsIdenticalTextFromDifferentRestaurants(t *testing.T) {
	a := citable(1, 7, evidence.DocTypeRestaurantReviewSummary, 0.9, longText(20))
	a.ContentHash = "same"
	b := citable(2, 8, evidence.DocTypeRestaurantReviewSummary, 0.8, longText(20))
	b.ContentHash = "same"

	kept, _, err := AssembleEvidence([]evidence.Evidence{a, b}, AssembleOptions{})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(kept) != 2 {
		t.Fatalf("want 2 citations, got %d", len(kept))
	}
}

// An answer with no evidence is the failure this stage exists to prevent, so a
// budget too small for anything is reported rather than returned as empty.
func TestAssemblyRefusesABudgetTooSmallToHoldAnything(t *testing.T) {
	items := []evidence.Evidence{
		citable(1, 7, evidence.DocTypeRestaurantReviewSummary, 0.9, longText(60)),
	}
	_, _, err := AssembleEvidence(items, AssembleOptions{TokenBudget: 5})
	if err == nil {
		t.Fatal("a budget too small for one citation must be reported")
	}
	if got := errs.CodeOf(err); got != errs.CodeRetrievalBudgetExceeded {
		t.Fatalf("code = %q, want %q", got, errs.CodeRetrievalBudgetExceeded)
	}
}

// The same failure has to be reported when the documents are too large rather
// than the budget too small: the caller's fix differs.
func TestAssemblyRefusesWhenEveryDocumentIsTooLarge(t *testing.T) {
	items := []evidence.Evidence{
		citable(1, 7, evidence.DocTypeRestaurantReviewSummary, 0.9, longText(200)),
		citable(2, 7, evidence.DocTypeRestaurantReviewSummary, 0.8, longText(200)),
	}
	_, _, err := AssembleEvidence(items, AssembleOptions{TokenBudget: 50})
	if err == nil {
		t.Fatal("documents that cannot fit must be reported, not silently dropped")
	}
	if got := errs.CodeOf(err); got != errs.CodeRetrievalBudgetExceeded {
		t.Fatalf("code = %q, want %q", got, errs.CodeRetrievalBudgetExceeded)
	}
}

// The kept citations must come back strongest-first, or the budget is admitting
// documents in an order the reader did not ask for.
func TestAssemblyKeepsTheStrongestCitations(t *testing.T) {
	items := []evidence.Evidence{
		citable(1, 7, evidence.DocTypeRestaurantReviewSummary, 0.10, longText(20)),
		citable(2, 8, evidence.DocTypeRestaurantReviewSummary, 0.90, longText(20)),
		citable(3, 9, evidence.DocTypeRestaurantReviewSummary, 0.50, longText(20)),
	}
	kept, _, err := AssembleEvidence(items, AssembleOptions{TokenBudget: 45})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(kept) != 1 {
		t.Fatalf("a 45-token budget holds one 25-token citation, got %d", len(kept))
	}
	if kept[0].Score != 0.90 {
		t.Fatalf("kept the citation scoring %v, want the strongest", kept[0].Score)
	}
}

// Equal scores must not reorder between two identical runs.
func TestAssemblyIsReproducible(t *testing.T) {
	items := make([]evidence.Evidence, 0, 5)
	for i := 1; i <= 5; i++ {
		items = append(items, citable(int64(i), int64(i), evidence.DocTypeRestaurantReviewSummary, 0.5, longText(10)))
	}
	first, _, err := AssembleEvidence(items, AssembleOptions{})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	for run := 0; run < 5; run++ {
		again, _, err := AssembleEvidence(items, AssembleOptions{})
		if err != nil {
			t.Fatalf("assemble: %v", err)
		}
		if len(again) != len(first) {
			t.Fatalf("run %d kept %d, first kept %d", run, len(again), len(first))
		}
		for i := range first {
			if again[i].EvidenceID != first[i].EvidenceID {
				t.Fatalf("run %d position %d: evidence %d, want %d",
					run, i, again[i].EvidenceID, first[i].EvidenceID)
			}
		}
	}
}

// The estimate has to count CJK per character; dividing a Chinese review by four
// would undercount it several times over and blow the budget.
func TestTokenEstimateHandlesMixedScripts(t *testing.T) {
	cjk := EstimateTokens(strings.Repeat("好", 100))
	if cjk < 90 || cjk > 110 {
		t.Fatalf("a 100-character Chinese text cost %d tokens, want about 100", cjk)
	}
	latin := EstimateTokens(strings.Repeat("word ", 40))
	if latin > 60 || latin < 40 {
		t.Fatalf("a 40-word English text cost %d tokens, want about 50", latin)
	}
}

// The composed path must report both stages' accounting in one trace.
func TestAssembleAndReturnReportsBothStages(t *testing.T) {
	items := []evidence.Evidence{
		citable(1, 7, evidence.DocTypeRestaurantReviewSummary, 0.9, longText(20)),
		citable(2, 7, evidence.DocTypeRestaurantReviewSummary, 0.8, longText(20)),
		citable(3, 8, evidence.DocTypeRestaurantReviewSummary, 0.7, longText(20)),
	}
	knowledge := &stubKnowledge{recalled: items}
	service := evidenceService(t, knowledge, nil)

	kept, trace, err := service.AssembleAndReturn(context.Background(), EvidenceRequest{
		RestaurantIDs: []int64{7, 8},
	}, AssembleOptions{TokenBudget: 40})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(kept) != 1 {
		t.Fatalf("a 40-token budget holds one 25-token citation, got %d", len(kept))
	}
	if trace.Recalled != 3 || trace.Kept != 1 || trace.Dropped != 2 {
		t.Fatalf("accounting: recalled=%d kept=%d dropped=%d",
			trace.Recalled, trace.Kept, trace.Dropped)
	}
	if trace.TokenBudget != 40 || trace.Tokens <= 0 || trace.Tokens > 40 {
		t.Fatalf("budget accounting: budget=%d spent=%d", trace.TokenBudget, trace.Tokens)
	}
	if trace.ScopeSize != 2 {
		t.Fatalf("scope_size = %d, want 2", trace.ScopeSize)
	}
}
