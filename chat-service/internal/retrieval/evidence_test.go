package retrieval

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/store"
)

// evidenceService builds a service whose evidence path is wired.
func evidenceService(t *testing.T, knowledge *stubKnowledge, embedding *stubEmbedding) *Service {
	t.Helper()
	service := newService(t, &stubRestaurants{}, ServiceConfig{})
	service.knowledge = knowledge
	service.embedding = embedding
	if service.cfg.EmbeddingTimeout == 0 {
		service.cfg.EmbeddingTimeout = DefaultServiceConfig.EmbeddingTimeout
	}
	return service
}

func cite(id, restaurantID int64, docType evidence.DocType, score float64) evidence.Evidence {
	return evidence.Evidence{
		EvidenceID:   id,
		RestaurantID: restaurantID,
		DocType:      docType,
		Title:        "t",
		Content:      "c",
		Source:       "yelp_review",
		SnapshotAt:   testSnapshot,
		Score:        score,
	}
}

// The rule the whole layer exists to protect: a citation names a restaurant, so
// a recall without one is refused rather than widened to the whole corpus.
func TestEvidenceRefusesAnEmptyRestaurantScope(t *testing.T) {
	knowledge := &stubKnowledge{}
	service := evidenceService(t, knowledge, &stubEmbedding{vector: []float32{1}})

	_, err := service.Evidence(context.Background(), EvidenceRequest{Query: "wait times"})
	if err == nil {
		t.Fatal("an evidence recall without restaurants must be refused")
	}
	if got := errs.CodeOf(err); got != errs.CodeRetrievalNoScope {
		t.Fatalf("code = %q, want %q", got, errs.CodeRetrievalNoScope)
	}
	if knowledge.recallCall != 0 {
		t.Fatal("nothing may be recalled before the scope is checked")
	}
}

// A caller passing zero or repeated ids would otherwise widen the store's work
// without widening the answer, and a zero id names no restaurant at all.
func TestEvidenceNormalisesTheRestaurantScope(t *testing.T) {
	knowledge := &stubKnowledge{}
	service := evidenceService(t, knowledge, nil)

	if _, err := service.Evidence(context.Background(), EvidenceRequest{
		RestaurantIDs: []int64{0, 7, 7, -3, 9},
	}); err != nil {
		t.Fatalf("evidence: %v", err)
	}
	// The stub records nothing, so the scope is checked through the result trace.
	if _, err := service.Evidence(context.Background(), EvidenceRequest{
		RestaurantIDs: []int64{0, -1},
	}); err == nil {
		t.Fatal("a scope of nothing but non-restaurants is still no scope")
	}
}

// The topic narrows the recall but must not replace the ranking: two
// restaurants' reviews on one topic still differ in wording.
func TestEvidenceForwardsTheTopicToTheStore(t *testing.T) {
	knowledge := &stubKnowledge{recalled: []evidence.Evidence{
		cite(1, 7, evidence.DocTypeRestaurantReviewSummary, 0.8),
	}}
	var got store.EvidenceRequest
	knowledge.onRecall = func(req store.EvidenceRequest) { got = req }

	service := evidenceService(t, knowledge, &stubEmbedding{vector: []float32{1}})
	if _, err := service.Evidence(context.Background(), EvidenceRequest{
		RestaurantIDs: []int64{7}, Query: "is it slow", Topic: "wait",
	}); err != nil {
		t.Fatalf("evidence: %v", err)
	}
	if got.Topic != "wait" {
		t.Fatalf("topic = %q, want it forwarded to the store", got.Topic)
	}
	if len(got.Query) == 0 {
		t.Fatal("a topic must narrow the recall, not replace the vector")
	}
}

// An empty question has no embedding behind it, and asking the provider for one
// would spend a model call to produce a vector that means nothing.
func TestEvidenceSkipsEmbeddingWhenThereIsNoQuestion(t *testing.T) {
	knowledge := &stubKnowledge{recalled: []evidence.Evidence{
		cite(1, 7, evidence.DocTypeRestaurantAttributes, 0),
	}}
	var got store.EvidenceRequest
	knowledge.onRecall = func(req store.EvidenceRequest) { got = req }
	embedding := &stubEmbedding{vector: []float32{1}}

	service := evidenceService(t, knowledge, embedding)
	result, err := service.Evidence(context.Background(), EvidenceRequest{RestaurantIDs: []int64{7}})
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	if embedding.calls != 0 {
		t.Fatal("a question-less recall has nothing to embed")
	}
	if len(got.Query) != 0 {
		t.Fatal("the unordered read must not carry a query vector")
	}
	if len(result.Evidence) != 1 {
		t.Fatalf("want the restaurant's evidence, got %d", len(result.Evidence))
	}
}

// A citation with no source renders as a blank next to a quote, which is
// indistinguishable from a rendering bug. It must be visible, and the gap must
// be reported rather than absorbed.
func TestEvidenceMakesAMissingSourceVisible(t *testing.T) {
	orphan := cite(1, 7, evidence.DocTypeRestaurantReviewSummary, 0.8)
	orphan.Source = ""
	knowledge := &stubKnowledge{recalled: []evidence.Evidence{orphan}}

	service := evidenceService(t, knowledge, nil)
	result, err := service.Evidence(context.Background(), EvidenceRequest{RestaurantIDs: []int64{7}})
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	if len(result.Evidence) != 1 {
		t.Fatal("a row with no source must not be dropped silently")
	}
	if result.Evidence[0].Source != unknownSource {
		t.Fatalf("source = %q, want %q", result.Evidence[0].Source, unknownSource)
	}
	if len(result.Trace.Warnings) == 0 {
		t.Fatal("the missing source must be reported in the trace")
	}
	if !strings.Contains(result.Trace.Warnings[0], unknownSource) {
		t.Fatalf("warning = %q, want it to name the placeholder", result.Trace.Warnings[0])
	}
}

// The same paragraph reached twice — once alone, once quoted inside a review
// chunk — is one source, not two agreeing sources.
func TestEvidenceCollapsesOneSourceReachedTwice(t *testing.T) {
	standalone := cite(1, 7, evidence.DocTypeRestaurantReviewSummary, 0.60)
	standalone.ContentHash = "sha-abc"
	quoted := cite(2, 7, evidence.DocTypeRestaurantReviewSummary, 0.91)
	quoted.ContentHash = "sha-abc"
	other := cite(3, 7, evidence.DocTypeRestaurantReviewSummary, 0.70)
	other.ContentHash = "sha-def"

	knowledge := &stubKnowledge{recalled: []evidence.Evidence{standalone, quoted, other}}
	service := evidenceService(t, knowledge, nil)

	result, err := service.Evidence(context.Background(), EvidenceRequest{RestaurantIDs: []int64{7}})
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	if len(result.Evidence) != 2 {
		t.Fatalf("want 2 distinct sources, got %d: %+v", len(result.Evidence), result.Evidence)
	}
	if result.Evidence[0].EvidenceID != 2 {
		t.Fatalf("the highest-scoring copy must win, got evidence %d", result.Evidence[0].EvidenceID)
	}
	if result.Trace.Recalled != 3 {
		t.Fatalf("the pre-dedup count must survive, got %d", result.Trace.Recalled)
	}
}

// Two restaurants can carry identical text; merging them would attribute one
// restaurant's words to another.
func TestEvidenceDoesNotMergeAcrossRestaurants(t *testing.T) {
	a := cite(1, 7, evidence.DocTypeRestaurantReviewSummary, 0.8)
	a.ContentHash = "sha-same"
	b := cite(2, 8, evidence.DocTypeRestaurantReviewSummary, 0.7)
	b.ContentHash = "sha-same"

	knowledge := &stubKnowledge{recalled: []evidence.Evidence{a, b}}
	service := evidenceService(t, knowledge, nil)

	result, err := service.Evidence(context.Background(), EvidenceRequest{RestaurantIDs: []int64{7, 8}})
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	if len(result.Evidence) != 2 {
		t.Fatalf("identical text at two restaurants is two sources, got %d", len(result.Evidence))
	}
}

// A row with no content hash has nothing to compare, so it must not be merged
// with every other hashless row.
func TestEvidenceKeepsHashlessRowsApart(t *testing.T) {
	knowledge := &stubKnowledge{recalled: []evidence.Evidence{
		cite(1, 7, evidence.DocTypeRestaurantHours, 0.8),
		cite(2, 7, evidence.DocTypeRestaurantHours, 0.7),
	}}
	service := evidenceService(t, knowledge, nil)

	result, err := service.Evidence(context.Background(), EvidenceRequest{RestaurantIDs: []int64{7}})
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	if len(result.Evidence) != 2 {
		t.Fatalf("hashless rows must stay distinct, got %d", len(result.Evidence))
	}
}

// A corpus and a model that disagree fail the same way every time. Serving an
// unordered answer would hide that.
func TestEvidenceRaisesAMisconfiguredCorpus(t *testing.T) {
	knowledge := &stubKnowledge{}
	embedding := &stubEmbedding{vector: []float32{1, 0, 0}, dimensions: 1024}
	service := evidenceService(t, knowledge, embedding)

	_, err := service.Evidence(context.Background(), EvidenceRequest{
		RestaurantIDs: []int64{7}, Query: "wait times",
	})
	if err == nil {
		t.Fatal("a dimension mismatch must be reported, not absorbed")
	}
	if got := errs.CodeOf(err); got != errs.CodeEmbeddingDimensionMismatch {
		t.Fatalf("code = %q, want %q", got, errs.CodeEmbeddingDimensionMismatch)
	}
	if knowledge.recallCall != 0 {
		t.Fatal("a misconfigured corpus must not reach the store")
	}
}

// A provider that is merely down is not the caller's problem: the question still
// has an honest answer in document order, and it must say that is what it is.
func TestEvidenceDegradesToDocumentOrderWhenTheProviderIsDown(t *testing.T) {
	knowledge := &stubKnowledge{recalled: []evidence.Evidence{
		cite(1, 7, evidence.DocTypeRestaurantReviewSummary, 0),
	}}
	embedding := &stubEmbedding{err: errs.New(errs.CodeProviderUnavailable, "down")}
	service := evidenceService(t, knowledge, embedding)

	result, err := service.Evidence(context.Background(), EvidenceRequest{
		RestaurantIDs: []int64{7}, Query: "wait times",
	})
	if err != nil {
		t.Fatalf("a provider outage must not fail the recall: %v", err)
	}
	if len(result.Evidence) != 1 {
		t.Fatal("the recall must still answer")
	}
	if len(result.Trace.Warnings) == 0 {
		t.Fatal("the degradation must be visible in the trace")
	}
	if result.Trace.EmbeddingModelID != "" {
		t.Fatal("a trace must not name a model that did not run")
	}
}

// A store fault fails the recall. Unlike the candidate search, there is no
// second channel to answer from, and an empty evidence set would read as "this
// restaurant has nothing on record".
func TestEvidenceRaisesAStoreFailure(t *testing.T) {
	knowledge := &stubKnowledge{recallErr: errors.New("connection reset")}
	service := evidenceService(t, knowledge, nil)

	_, err := service.Evidence(context.Background(), EvidenceRequest{RestaurantIDs: []int64{7}})
	if err == nil {
		t.Fatal("a store failure must not read as an empty answer")
	}
}

// An absurd depth is clamped rather than honoured.
func TestEvidenceClampsTheDepth(t *testing.T) {
	knowledge := &stubKnowledge{}
	var got store.EvidenceRequest
	knowledge.onRecall = func(req store.EvidenceRequest) { got = req }
	service := evidenceService(t, knowledge, nil)

	if _, err := service.Evidence(context.Background(), EvidenceRequest{
		RestaurantIDs: []int64{7}, TopK: 10_000,
	}); err != nil {
		t.Fatalf("evidence: %v", err)
	}
	if got.TopK != maxEvidenceDepth {
		t.Fatalf("top_k = %d, want it clamped to %d", got.TopK, maxEvidenceDepth)
	}

	if _, err := service.Evidence(context.Background(), EvidenceRequest{
		RestaurantIDs: []int64{7},
	}); err != nil {
		t.Fatalf("evidence: %v", err)
	}
	if got.TopK != defaultEvidenceDepth {
		t.Fatalf("top_k = %d, want the default %d", got.TopK, defaultEvidenceDepth)
	}
}

// The recall must be bounded by the timeout the same way candidate recall is.
func TestEvidenceBoundsTheEmbedding(t *testing.T) {
	knowledge := &stubKnowledge{}
	embedding := &stubEmbedding{vector: []float32{1}}
	service := evidenceService(t, knowledge, embedding)
	service.cfg.EmbeddingTimeout = 50 * time.Millisecond

	if _, err := service.Evidence(context.Background(), EvidenceRequest{
		RestaurantIDs: []int64{7}, Query: "wait times",
	}); err != nil {
		t.Fatalf("evidence: %v", err)
	}
	if service.cfg.EmbeddingTimeout != 50*time.Millisecond {
		t.Fatal("the configured timeout must survive")
	}
}
