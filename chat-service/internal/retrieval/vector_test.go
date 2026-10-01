package retrieval

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/evidence"
	"github.com/zed/platepilot/shared/domain/retrieval"
	"github.com/zed/platepilot/shared/domain/search"
	"github.com/zed/platepilot/shared/port"
)

// stubKnowledge is a read-side knowledge repository a test can steer.
type stubKnowledge struct {
	docs       []port.ScoredDocument
	err        error
	got        port.VectorSearchRequest
	recallCall int
	recalled   []evidence.Evidence
	recallErr  error
	// onRecall observes the request the store received.
	onRecall func(port.EvidenceRequest)
}

func (s *stubKnowledge) FindEvidenceByRestaurant(_ context.Context, _ int64, _ string) ([]evidence.Evidence, error) {
	return nil, errs.New(errs.CodeRetrievalNoScope, "not used")
}

func (s *stubKnowledge) RecallEvidence(
	_ context.Context, req port.EvidenceRequest,
) ([]evidence.Evidence, error) {
	if len(req.RestaurantIDs) == 0 {
		return nil, errs.New(errs.CodeRetrievalNoScope, "evidence recall needs restaurants")
	}
	if s.onRecall != nil {
		s.onRecall(req)
	}
	return s.recalled, s.recallErr
}

func (s *stubKnowledge) VectorSearch(_ context.Context, req port.VectorSearchRequest) ([]port.ScoredDocument, error) {
	s.recallCall++
	s.got = req
	return s.docs, s.err
}

// stubEmbedding is an embedding provider a test can steer.
type stubEmbedding struct {
	dimensions int
	vector     []float32
	err        error
	calls      int
	query      string
}

func (s *stubEmbedding) ModelID() string { return "stub-embedding" }

func (s *stubEmbedding) Dimensions() int {
	if s.dimensions <= 0 {
		return len(s.vector)
	}
	return s.dimensions
}

func (s *stubEmbedding) EmbedDocuments(_ context.Context, docs []string) ([][]float32, error) {
	out := make([][]float32, 0, len(docs))
	for range docs {
		out = append(out, s.vector)
	}
	return out, s.err
}

func (s *stubEmbedding) EmbedQuery(_ context.Context, query string) ([]float32, error) {
	s.calls++
	s.query = query
	if s.err != nil {
		return nil, s.err
	}
	return s.vector, nil
}

var (
	_ port.KnowledgeRepository = (*stubKnowledge)(nil)
	_ port.EmbeddingProvider   = (*stubEmbedding)(nil)
)

func scoredDoc(documentID, restaurantID int64, title string, distance float64) port.ScoredDocument {
	return port.ScoredDocument{
		KnowledgeDocument: evidence.KnowledgeDocument{
			DocumentID:   documentID,
			RestaurantID: restaurantID,
			Scope:        evidence.ScopeRestaurant,
			DocType:      evidence.DocTypeRestaurantProfile,
			Title:        title,
			SnapshotAt:   testSnapshot,
		},
		Distance: distance,
	}
}

// vectorService builds a service with the vector channel and its dependencies
// wired in.
//
// The channel switch is an explicit argument rather than a field of the config
// because a zero value for it cannot say whether the caller asked for it off or
// did not care, and a case that is about the channel being switched off must not
// be overruled by the package default.
func vectorService(
	t *testing.T, repo *stubRestaurants, knowledge *stubKnowledge,
	embedding *stubEmbedding, enableVector bool,
) *Service {
	t.Helper()
	service := newService(t, repo, ServiceConfig{})
	service.knowledge = knowledge
	service.embedding = embedding
	service.cfg.EnableVector = enableVector
	if service.cfg.EmbeddingTimeout == 0 {
		service.cfg.EmbeddingTimeout = DefaultServiceConfig.EmbeddingTimeout
	}
	return service
}

// The whole point of the vector channel: a soft condition the hard filters could
// not express still moves restaurants up the list.
func TestVectorChannelRanksOnASoftCondition(t *testing.T) {
	repo := &stubRestaurants{
		// A restaurant matching nothing on text but highly rated.
		searchRows: []search.RestaurantCandidate{row(1, "Quiet Corner", 0)},
		details: map[int64]search.RestaurantDetail{
			1: {Name: "Quiet Corner", KnowledgeScore: 4.8, RatingCount: 400},
			// The semantic match has to be a real row the filters accept, or
			// fusion is right to reject it and this test is asserting that the
			// hard filter does not apply to recalled candidates -- which is the
			// opposite of what it is for.
			2: {Name: "Rooftop Garden", Borough: "manhattan", RatingCount: 10},
		},
	}
	knowledge := &stubKnowledge{docs: []port.ScoredDocument{
		scoredDoc(1, 2, "Rooftop Garden", 0.10),
		scoredDoc(2, 1, "Quiet Corner", 0.90),
	}}
	embedding := &stubEmbedding{vector: []float32{1, 0, 0}}

	service := vectorService(t, repo, knowledge, embedding, true)
	got, err := service.Search(context.Background(), reqWithQuery("a quiet place for a date"))
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got.Candidates) == 0 {
		t.Fatal("want candidates")
	}
	if got.Candidates[0].RestaurantID != 2 {
		t.Fatalf("the semantic match must win, got %v", ids(got.Candidates))
	}
	var vectorRan bool
	for _, summary := range got.Trace.Channels {
		if summary.Channel != retrieval.ChannelVector {
			continue
		}
		vectorRan = summary.Ran
	}
	if !vectorRan {
		t.Fatal("the vector channel must be recorded as run")
	}
	if got.Trace.EmbeddingModelID != "stub-embedding" {
		t.Fatalf("the trace must name the model, got %q", got.Trace.EmbeddingModelID)
	}
}

// The dimension is checked before the store is touched. A mismatched vector is
// rejected by the column, but the error names a type rather than the setting
// that caused it — and a needless round trip is wasted work.
//
// It is also raised rather than degraded. A mismatch means the corpus was
// embedded with a different model than the one now configured, so every recall
// fails the same way; serving a ranking that never had semantic recall in it
// would look like a correct answer.
func TestVectorChannelValidatesTheDimensionBeforeRecalling(t *testing.T) {
	repo := &stubRestaurants{searchRows: []search.RestaurantCandidate{row(1, "A", 0)}}
	knowledge := &stubKnowledge{}
	embedding := &stubEmbedding{vector: []float32{1, 0, 0}, dimensions: 1024}

	service := vectorService(t, repo, knowledge, embedding, true)
	_, err := service.Search(context.Background(), reqWithQuery("anything"))
	if err == nil {
		t.Fatal("a dimension mismatch must be reported, not absorbed")
	}
	if got := errs.CodeOf(err); got != errs.CodeEmbeddingDimensionMismatch {
		t.Fatalf("code = %q, want %q", got, errs.CodeEmbeddingDimensionMismatch)
	}
	if knowledge.recallCall != 0 {
		t.Fatal("a mismatched vector must not reach the store")
	}
}

// A provider that answers with no vector is the same class of defect for the
// same reason: it is a configuration fault, not a momentary outage.
func TestVectorChannelReportsAnEmptyVector(t *testing.T) {
	repo := &stubRestaurants{searchRows: []search.RestaurantCandidate{row(1, "A", 0)}}
	knowledge := &stubKnowledge{}
	embedding := &stubEmbedding{vector: nil, dimensions: 0}

	service := vectorService(t, repo, knowledge, embedding, true)
	_, err := service.Search(context.Background(), reqWithQuery("anything"))
	if err == nil {
		t.Fatal("an empty vector must be reported, not absorbed")
	}
	if got := errs.CodeOf(err); got != errs.CodeEmbeddingEmpty {
		t.Fatalf("code = %q, want %q", got, errs.CodeEmbeddingEmpty)
	}
	if knowledge.recallCall != 0 {
		t.Fatal("an empty vector must not reach the store")
	}
}

// An empty filter plus no text is a different question from a soft condition, so
// nothing is embedded for it.
func TestVectorChannelSkipsWhenThereIsNoNaturalLanguageQuery(t *testing.T) {
	repo := &stubRestaurants{
		searchRows: []search.RestaurantCandidate{row(1, "A", 0)},
		details:    map[int64]search.RestaurantDetail{1: {}},
	}
	knowledge := &stubKnowledge{}
	embedding := &stubEmbedding{vector: []float32{1, 0, 0}}

	service := vectorService(t, repo, knowledge, embedding, true)
	got, err := service.Search(context.Background(), reqWithFilter())
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if embedding.calls != 0 {
		t.Fatal("a filter-only search has nothing to embed")
	}
	for _, summary := range got.Trace.Channels {
		if summary.Channel == retrieval.ChannelVector && summary.Ran {
			t.Fatal("the vector channel must not run without a query")
		}
	}
}

// Every way the channel can fail must degrade rather than fail the search, and
// each has to say so: the ranking is weaker and the trace has to show why.
func TestVectorChannelDegradesWithoutFailingTheSearch(t *testing.T) {
	cases := map[string]struct {
		knowledge *stubKnowledge
		embedding *stubEmbedding
		wantNote  string
	}{
		"provider unavailable": {
			knowledge: &stubKnowledge{},
			embedding: &stubEmbedding{err: errs.New(errs.CodeProviderUnavailable, "down")},
			wantNote:  "provider_unavailable",
		},
		"provider timeout": {
			knowledge: &stubKnowledge{},
			embedding: &stubEmbedding{err: errs.New(errs.CodeProviderTimeout, "slow")},
			wantNote:  "provider_timeout",
		},
		// Classified the way the adapter classifies it. A bare error here would
		// assert a contract the store does not have: it returns a typed code for
		// every fault, so the note has to be read against that contract.
		"store unreachable": {
			knowledge: &stubKnowledge{err: errs.New(errs.CodeProviderUnavailable, "database unavailable")},
			embedding: &stubEmbedding{vector: []float32{1}},
			wantNote:  "provider_unavailable",
		},
		"store timed out": {
			knowledge: &stubKnowledge{err: errs.New(errs.CodeProviderTimeout, "query canceled")},
			embedding: &stubEmbedding{vector: []float32{1}},
			wantNote:  "provider_timeout",
		},
		"unclassified store fault": {
			knowledge: &stubKnowledge{err: errors.New("boom")},
			embedding: &stubEmbedding{vector: []float32{1}},
			wantNote:  "embedding 失败",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			repo := &stubRestaurants{
				searchRows: []search.RestaurantCandidate{row(1, "A", 0)},
				details:    map[int64]search.RestaurantDetail{1: {}},
			}
			service := vectorService(t, repo, tc.knowledge, tc.embedding, true)
			got, err := service.Search(context.Background(), reqWithQuery("a quiet place"))
			if err != nil {
				t.Fatalf("a vector failure must degrade, not fail: %v", err)
			}
			if len(got.Candidates) != 1 {
				t.Fatalf("the structured result must survive, got %v", ids(got.Candidates))
			}
			var found bool
			for _, summary := range got.Trace.Channels {
				if summary.Channel != retrieval.ChannelVector {
					continue
				}
				found = true
				if summary.Ran {
					t.Fatal("the channel must not report a run it did not make")
				}
				if !strings.Contains(summary.Note, tc.wantNote) {
					t.Fatalf("note = %q, want it to mention %q", summary.Note, tc.wantNote)
				}
			}
			if !found {
				t.Fatal("the vector channel is missing from the trace")
			}
		})
	}
}

// A disabled channel is recorded as not run, so a weakened ranking is visible.
func TestDisabledVectorChannelIsRecordedNotRun(t *testing.T) {
	repo := &stubRestaurants{
		searchRows: []search.RestaurantCandidate{row(1, "A", 0)},
		details:    map[int64]search.RestaurantDetail{1: {}},
	}
	knowledge := &stubKnowledge{}
	embedding := &stubEmbedding{vector: []float32{1}}

	service := vectorService(t, repo, knowledge, embedding, false)
	if service.cfg.EnableVector {
		t.Fatal("the channel must stay off when the caller asked for that")
	}
	got, err := service.Search(context.Background(), reqWithQuery("quiet"))
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if embedding.calls != 0 {
		t.Fatal("a disabled channel must not embed")
	}
	for _, summary := range got.Trace.Channels {
		if summary.Channel == retrieval.ChannelVector {
			if summary.Ran {
				t.Fatal("a disabled channel must be recorded as not run")
			}
			if !strings.Contains(summary.Note, "已关闭") {
				t.Fatalf("note = %q, want it to say the channel is off", summary.Note)
			}
		}
	}
}

// The hard filters must reach the recall statement. Ranking first and filtering
// afterwards would let a soft condition promote a restaurant the user excluded.
func TestVectorChannelPushesTheBoroughIntoTheRecall(t *testing.T) {
	repo := &stubRestaurants{}
	knowledge := &stubKnowledge{}
	embedding := &stubEmbedding{vector: []float32{1, 0, 0}}

	service := vectorService(t, repo, knowledge, embedding, true)
	req := reqWithQuery("quiet")
	req.Filter.Borough = "Manhattan"
	if _, err := service.Search(context.Background(), req); err != nil {
		t.Fatalf("search: %v", err)
	}
	if knowledge.got.Borough != "manhattan" {
		t.Fatalf("borough = %q, want the canonical form", knowledge.got.Borough)
	}
	if knowledge.got.Scope != evidence.ScopeRestaurant {
		t.Fatalf("scope = %q, want the restaurant scope", knowledge.got.Scope)
	}
}

// A recalled profile carries the name but not the rating, and a candidate the UI
// cannot display is not a result.
func TestVectorChannelEnrichesRecalledCandidates(t *testing.T) {
	repo := &stubRestaurants{details: map[int64]search.RestaurantDetail{
		2: {Name: "Rooftop Garden", Address: "9 Rooftop Way", Borough: "manhattan",
			Cuisines: []string{"italian"}, Rating: floatPtr(4.6), RatingCount: 220},
	}}
	knowledge := &stubKnowledge{docs: []port.ScoredDocument{
		scoredDoc(1, 2, "Rooftop Garden", 0.10),
	}}
	embedding := &stubEmbedding{vector: []float32{1, 0, 0}}

	service := vectorService(t, repo, knowledge, embedding, true)
	got, err := service.Search(context.Background(), reqWithQuery("romantic dinner"))
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got.Candidates) != 1 {
		t.Fatalf("want one candidate, got %v", ids(got.Candidates))
	}
	c := got.Candidates[0]
	if c.Name != "Rooftop Garden" || c.Address != "9 Rooftop Way" {
		t.Fatalf("the recalled candidate was not filled in: %+v", c)
	}
	if c.Rating == nil || *c.Rating != 4.6 || c.RatingCount != 220 {
		t.Fatalf("rating and its sample size must travel with the candidate: %v / %d",
			c.Rating, c.RatingCount)
	}
}

// reqWithQuery is the common case for the vector tests: a natural language
// question that also carries a hard filter.
func reqWithQuery(query string) retrieval.Request {
	return retrieval.Request{
		Query:  query,
		Filter: search.RestaurantFilter{Borough: "manhattan"},
	}
}

// reqWithFilter is a filter-only search. There is nothing to embed, which is the
// point of the test that uses it.
func reqWithFilter() retrieval.Request {
	return retrieval.Request{Filter: search.RestaurantFilter{Borough: "manhattan"}}
}
