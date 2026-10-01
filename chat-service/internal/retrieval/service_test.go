package retrieval

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/retrieval"
	"github.com/zed/platepilot/shared/domain/search"
)

// stubRestaurants is a read-side repository a test can steer.
type stubRestaurants struct {
	searchRows  []search.RestaurantCandidate
	searchErr   error
	matchRows   []search.RestaurantCandidate
	matchErr    error
	details     map[int64]search.RestaurantDetail
	searchCalls []search.SearchQuery
	matchCalls  []string
}

func (s *stubRestaurants) GetByID(_ context.Context, id int64) (search.RestaurantDetail, error) {
	if detail, ok := s.details[id]; ok {
		return detail, nil
	}
	return search.RestaurantDetail{}, errs.Newf(errs.CodeNotFound, "restaurant %d not found", id)
}

func (s *stubRestaurants) Search(_ context.Context, query search.SearchQuery) ([]search.RestaurantCandidate, error) {
	s.searchCalls = append(s.searchCalls, query)
	if s.searchErr != nil {
		return nil, s.searchErr
	}
	return s.searchRows, nil
}

func (s *stubRestaurants) MatchByText(_ context.Context, text string, _ int) ([]search.RestaurantCandidate, error) {
	s.matchCalls = append(s.matchCalls, text)
	if s.matchErr != nil {
		return nil, s.matchErr
	}
	return s.matchRows, nil
}

func newService(t *testing.T, repo *stubRestaurants, cfg ServiceConfig) *Service {
	t.Helper()
	if cfg.Oversample == 0 {
		cfg.Oversample = 2
	}
	if cfg.TopK == 0 {
		cfg.TopK = 5
	}
	if cfg.Weights == (Weights{}) {
		cfg.Weights = DefaultWeights
	}
	if !cfg.EnableStructured && !cfg.EnableKeyword {
		cfg.EnableStructured = true
		cfg.EnableKeyword = true
	}
	service, err := NewService(cfg, Deps{Restaurants: repo})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return service
}

// testSnapshot matches the corpus's observation date.
var testSnapshot = time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC)

// row builds a candidate the way the real repository returns one.
//
// The borough is set because fusion now re-checks every merged candidate against
// the hard filter. A stub that omits it is not a smaller fake but a different
// one: the repository always returns the borough for a row it matched on one,
// and a candidate without it would be correctly rejected by the filter check,
// so the stub would be testing a path the real system never takes.
func row(id int64, name string, prior float64) search.RestaurantCandidate {
	return search.RestaurantCandidate{
		RestaurantID: id,
		Name:         name,
		Score:        0.5,
		SnapshotAt:   testSnapshot,
		Borough:      "manhattan",
	}
}

// A request with neither text nor filters has no answer, and listing the corpus
// in prior order would look like one.
func TestSearchRefusesAnEmptyRequest(t *testing.T) {
	service := newService(t, &stubRestaurants{}, ServiceConfig{})
	_, err := service.Search(context.Background(), retrieval.Request{})
	if errs.CodeOf(err) != errs.CodeRetrievalEmptyQuery {
		t.Fatalf("want retrieval_empty_query, got %v", err)
	}
}

// An empty result is a successful search, not a failure.
func TestSearchReturnsAnEmptyListRatherThanAnError(t *testing.T) {
	repo := &stubRestaurants{}
	service := newService(t, repo, ServiceConfig{})
	got, err := service.Search(context.Background(), retrieval.Request{
		Filter: search.RestaurantFilter{Borough: "manhattan"},
	})
	if err != nil {
		t.Fatalf("an empty result is not an error: %v", err)
	}
	if len(got.Candidates) != 0 {
		t.Fatalf("want no candidates, got %v", ids(got.Candidates))
	}
	if got.Trace == nil {
		t.Fatal("an empty result still carries a trace")
	}
}

// A misspelled borough and an empty result set look identical if a bad borough
// is reported as "no matches", so it is refused. Casing is not a misspelling:
// a caller may send "Manhattan" and get the canonical row back.
func TestSearchRefusesAnUnknownBoroughButAcceptsAnyCasing(t *testing.T) {
	service := newService(t, &stubRestaurants{}, ServiceConfig{})
	if _, err := service.Search(context.Background(), retrieval.Request{
		Filter: search.RestaurantFilter{Borough: "New Jersey"},
	}); errs.CodeOf(err) != errs.CodeRetrievalInvalidFilter {
		t.Fatalf("want retrieval_invalid_filter for an unknown borough, got %v", err)
	}
	if _, err := service.Search(context.Background(), retrieval.Request{
		Filter: search.RestaurantFilter{Borough: "Manhattan"},
	}); err != nil {
		t.Fatalf("casing must not be a validation error: %v", err)
	}
}

// The structured channel is the only one whose results are guaranteed to
// satisfy the stated conditions. If it fails, a fuzzy list is an answer to a
// different question.
func TestSearchFailsWhenTheStructuredChannelFails(t *testing.T) {
	repo := &stubRestaurants{
		searchErr: errors.New("database down"),
		matchRows: []search.RestaurantCandidate{row(1, "A", 0)},
	}
	service := newService(t, repo, ServiceConfig{})
	if _, err := service.Search(context.Background(), retrieval.Request{
		Text:   "pizza",
		Filter: search.RestaurantFilter{Borough: "manhattan"},
	}); err == nil {
		t.Fatal("a structured failure must fail the search")
	}
}

// A keyword failure is survivable: the hard filters still hold, and fusion is
// built for a channel to come back empty. The caller learns the ranking is
// weaker without also learning the search failed.
func TestSearchDegradesWhenTheKeywordChannelFails(t *testing.T) {
	repo := &stubRestaurants{
		searchRows: []search.RestaurantCandidate{row(1, "A", 0), row(2, "B", 0)},
		matchErr:   errors.New("timeout"),
		details:    map[int64]search.RestaurantDetail{1: {}, 2: {}},
	}
	service := newService(t, repo, ServiceConfig{})
	got, err := service.Search(context.Background(), retrieval.Request{
		Text:   "pizza",
		Filter: search.RestaurantFilter{Borough: "manhattan"},
	})
	if err != nil {
		t.Fatalf("a keyword failure must not fail the search: %v", err)
	}
	if len(got.Candidates) != 2 {
		t.Fatalf("the structured results must survive, got %v", ids(got.Candidates))
	}

	var keyword retrieval.ChannelSummary
	for _, summary := range got.Trace.Channels {
		if summary.Channel == retrieval.ChannelKeyword {
			keyword = summary
		}
	}
	if keyword.Ran {
		t.Fatal("the failed keyword channel must not be recorded as run")
	}
	if keyword.Note == "" {
		t.Fatal("the degradation must be explained in the trace")
	}
	if len(got.Trace.Warnings) == 0 {
		t.Fatal("a degraded channel must surface as a warning")
	}
}

// A text-only search is legitimate, and the structured channel must not turn it
// into a corpus listing.
func TestSearchWithoutFiltersRunsOnlyTheKeywordChannel(t *testing.T) {
	repo := &stubRestaurants{
		matchRows: []search.RestaurantCandidate{row(1, "A", 0)},
		details:   map[int64]search.RestaurantDetail{1: {}},
	}
	service := newService(t, repo, ServiceConfig{})
	got, err := service.Search(context.Background(), retrieval.Request{Text: "pizza"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(repo.searchCalls) != 0 {
		t.Fatalf("the structured channel must not run without filters, calls = %v", repo.searchCalls)
	}
	if len(got.Candidates) != 1 {
		t.Fatalf("want the keyword hit, got %v", ids(got.Candidates))
	}
	for _, summary := range got.Trace.Channels {
		if summary.Channel == retrieval.ChannelStructured && summary.Ran {
			t.Fatal("the structured channel must be recorded as not run")
		}
	}
}

// The vector channel is not built yet. Reporting it as "not run" rather than
// omitting it keeps the trace honest about what is missing.
func TestSearchRecordsTheVectorChannelAsNotRun(t *testing.T) {
	repo := &stubRestaurants{
		searchRows: []search.RestaurantCandidate{row(1, "A", 0)},
		details:    map[int64]search.RestaurantDetail{1: {}},
	}
	service := newService(t, repo, ServiceConfig{})
	got, err := service.Search(context.Background(), retrieval.Request{
		Filter: search.RestaurantFilter{Borough: "manhattan"},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	for _, summary := range got.Trace.Channels {
		if summary.Channel != retrieval.ChannelVector {
			continue
		}
		if summary.Ran {
			t.Fatal("the vector channel is not built yet")
		}
		if summary.Note == "" {
			t.Fatal("the unimplemented channel must say so")
		}
	}
}

// Every channel reads deeper than the page it fills, or a channel that fills its
// own page can hide a restaurant another channel ranked first.
func TestSearchReadsDeeperThanItReturns(t *testing.T) {
	repo := &stubRestaurants{
		searchRows: []search.RestaurantCandidate{row(1, "A", 0)},
		details:    map[int64]search.RestaurantDetail{1: {}},
	}
	service := newService(t, repo, ServiceConfig{Oversample: 3})
	if _, err := service.Search(context.Background(), retrieval.Request{
		Filter: search.RestaurantFilter{Borough: "manhattan"},
		TopK:   4,
	}); err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(repo.searchCalls) != 1 {
		t.Fatalf("want one structured call, got %d", len(repo.searchCalls))
	}
	// The structured channel overreads on top of the oversample. Its rows are not
	// a ranking, so its page size only decides how many correct answers fusion
	// ever learns about -- and a restaurant excluded by where an unordered list
	// stopped is excluded for a reason the user never stated.
	if got, want := repo.searchCalls[0].TopK, 4*3*structuredOverread; got != want {
		t.Fatalf("structured depth = %d, want top_k*oversample*overread = %d", got, want)
	}
}

// The overread is bounded. A filter matching the whole corpus must not turn one
// request into a table scan.
func TestTheStructuredOverreadIsBounded(t *testing.T) {
	repo := &stubRestaurants{
		searchRows: []search.RestaurantCandidate{row(1, "A", 0)},
		details:    map[int64]search.RestaurantDetail{1: {}},
	}
	service := newService(t, repo, ServiceConfig{Oversample: 50, TopK: maxTopK})
	if _, err := service.Search(context.Background(), retrieval.Request{
		Filter: search.RestaurantFilter{Borough: "manhattan"},
		TopK:   maxTopK,
	}); err != nil {
		t.Fatalf("search: %v", err)
	}
	if got := repo.searchCalls[0].TopK; got != maxStructuredDepth {
		t.Fatalf("structured depth = %d, want it clamped to %d", got, maxStructuredDepth)
	}
}

func TestSearchClampsAnAbsurdTopK(t *testing.T) {
	repo := &stubRestaurants{
		searchRows: []search.RestaurantCandidate{row(1, "A", 0)},
		details:    map[int64]search.RestaurantDetail{1: {}},
	}
	service := newService(t, repo, ServiceConfig{})
	got, err := service.Search(context.Background(), retrieval.Request{
		Filter: search.RestaurantFilter{Borough: "manhattan"},
		TopK:   100000,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got.Trace.TopK != maxTopK {
		t.Fatalf("top_k = %d, want it clamped to %d", got.Trace.TopK, maxTopK)
	}
}

// The same question twice must produce the same list, or a replayed run cannot
// be compared with the original.
func TestSearchIsReproducible(t *testing.T) {
	repo := &stubRestaurants{
		searchRows: []search.RestaurantCandidate{row(3, "C", 0), row(1, "A", 0), row(2, "B", 0)},
		matchRows:  []search.RestaurantCandidate{row(2, "B", 0)},
		details: map[int64]search.RestaurantDetail{
			1: {KnowledgeScore: 0.9}, 2: {KnowledgeScore: 0.1}, 3: {KnowledgeScore: 0.5},
		},
	}
	service := newService(t, repo, ServiceConfig{})
	req := retrieval.Request{
		Text:   "pizza",
		Filter: search.RestaurantFilter{Borough: "manhattan"},
	}
	first, err := service.Search(context.Background(), req)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	second, err := service.Search(context.Background(), req)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	for i := range first.Candidates {
		if first.Candidates[i].RestaurantID != second.Candidates[i].RestaurantID {
			t.Fatalf("order differs: %v vs %v", ids(first.Candidates), ids(second.Candidates))
		}
		if first.Candidates[i].Score != second.Candidates[i].Score {
			t.Fatalf("score differs for %d: %v vs %v", first.Candidates[i].RestaurantID,
				first.Candidates[i].Score, second.Candidates[i].Score)
		}
	}
}

// A hard-filtered restaurant and a keyword-matched one must both appear, and the
// reason each was surfaced must name its channel.
func TestSearchExplainsWhyEachCandidateAppeared(t *testing.T) {
	repo := &stubRestaurants{
		searchRows: []search.RestaurantCandidate{row(1, "A", 0)},
		matchRows:  []search.RestaurantCandidate{row(2, "B", 0)},
		details:    map[int64]search.RestaurantDetail{1: {}, 2: {}},
	}
	service := newService(t, repo, ServiceConfig{})
	got, err := service.Search(context.Background(), retrieval.Request{
		Text:   "pizza",
		Filter: search.RestaurantFilter{Borough: "manhattan"},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got.Candidates) != 2 {
		t.Fatalf("want both channels' candidates, got %v", ids(got.Candidates))
	}
	for _, candidate := range got.Candidates {
		if len(candidate.Reasons) == 0 {
			t.Fatalf("candidate %d must explain itself: %+v", candidate.RestaurantID, candidate)
		}
	}
	if len(got.Trace.Candidates) != len(got.Candidates) {
		t.Fatalf("the trace must be aligned with the results: %d vs %d",
			len(got.Trace.Candidates), len(got.Candidates))
	}
}

func TestNewServiceRequiresARepository(t *testing.T) {
	if _, err := NewService(ServiceConfig{}, Deps{}); err == nil {
		t.Fatal("a service without a repository cannot answer anything")
	}
}

func TestChannelSwitchesAreHonoured(t *testing.T) {
	repo := &stubRestaurants{
		searchRows: []search.RestaurantCandidate{row(1, "A", 0)},
		matchRows:  []search.RestaurantCandidate{row(2, "B", 0)},
		details:    map[int64]search.RestaurantDetail{1: {}, 2: {}},
	}
	service := newService(t, repo, ServiceConfig{EnableStructured: true, EnableKeyword: false})
	got, err := service.Search(context.Background(), retrieval.Request{
		Text:   "pizza",
		Filter: search.RestaurantFilter{Borough: "manhattan"},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(repo.matchCalls) != 0 {
		t.Fatal("a disabled keyword channel must not be called")
	}
	for _, summary := range got.Trace.Channels {
		if summary.Channel == retrieval.ChannelKeyword && summary.Ran {
			t.Fatal("a disabled channel must be recorded as not run")
		}
	}
}

// A query too short to match is a request defect, not a channel failure.
// Degrading it would answer with an empty list that reads as "no such
// restaurant" rather than "your query was unusable".
func TestSearchSurfacesARequestDefectFromTheKeywordChannel(t *testing.T) {
	repo := &stubRestaurants{
		searchRows: []search.RestaurantCandidate{row(1, "A", 0)},
		matchErr:   errs.New(errs.CodeRetrievalQueryTooShort, "too short"),
		details:    map[int64]search.RestaurantDetail{1: {}},
	}
	service := newService(t, repo, ServiceConfig{})
	_, err := service.Search(context.Background(), retrieval.Request{
		Text:   "ab",
		Filter: search.RestaurantFilter{Borough: "manhattan"},
	})
	if errs.CodeOf(err) != errs.CodeRetrievalQueryTooShort {
		t.Fatalf("want retrieval_query_too_short, got %v", err)
	}
}

// A repository that is merely unavailable is still survivable: the degradation
// path must not swallow every failure the keyword channel can produce.
func TestSearchStillDegradesOnAnInfrastructureFailure(t *testing.T) {
	repo := &stubRestaurants{
		searchRows: []search.RestaurantCandidate{row(1, "A", 0)},
		matchErr:   errs.New(errs.CodeProviderTimeout, "timeout"),
		details:    map[int64]search.RestaurantDetail{1: {}},
	}
	service := newService(t, repo, ServiceConfig{})
	got, err := service.Search(context.Background(), retrieval.Request{
		Text:   "pizza",
		Filter: search.RestaurantFilter{Borough: "manhattan"},
	})
	if err != nil {
		t.Fatalf("an infrastructure failure must degrade, not fail: %v", err)
	}
	if len(got.Candidates) != 1 {
		t.Fatalf("the structured result must survive, got %v", ids(got.Candidates))
	}
}

// The prior is rating shrunk toward the corpus mean by its sample size. A 4.9
// from three reviews is not the same claim as a 4.5 from three hundred.
func TestPriorShrinksThinSamplesTowardTheMean(t *testing.T) {
	thick := priorFrom(search.RestaurantDetail{Rating: floatPtr(4.5), RatingCount: 300})
	thin := priorFrom(search.RestaurantDetail{Rating: floatPtr(4.9), RatingCount: 3})
	thickInflated := priorFrom(search.RestaurantDetail{Rating: floatPtr(4.5), RatingCount: 3})

	if thick <= priorRatingMean {
		t.Fatalf("a well-sampled rating should sit near its value, got %v", thick)
	}
	if thin <= priorRatingMean {
		t.Fatalf("a thin high rating should be pulled toward the mean, got %v", thin)
	}
	// The inflated-but-thin rating must not beat the well-sampled one.
	if thickInflated >= thick {
		t.Fatalf("a thin 4.9 must not outrank a solid 4.5: %v vs %v", thickInflated, thick)
	}
	if got := priorFrom(search.RestaurantDetail{}); got != 0 {
		t.Fatalf("a restaurant with no rating has no prior, got %v", got)
	}
}

func floatPtr(v float64) *float64 { return &v }
