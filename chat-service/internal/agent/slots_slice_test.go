package agent_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	domainretrieval "github.com/zed1995/platepilot/shared/domain/retrieval"
	"github.com/zed1995/platepilot/shared/domain/review"
	"github.com/zed1995/platepilot/shared/domain/search"
	"github.com/zed1995/platepilot/shared/testkit"

	"github.com/zed1995/platepilot/chat-service/internal/agent"
	"github.com/zed1995/platepilot/chat-service/internal/agent/slots"
	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
	"github.com/zed1995/platepilot/chat-service/internal/agent/tools"
	"github.com/zed1995/platepilot/chat-service/internal/retrieval"
)

// recordingSearcher wraps the real retrieval service and keeps the request the
// tool actually issued.
//
// The request is the observable this slice is about: the injected conditions
// exist nowhere else. The turn's events describe that a tool ran, not what it
// was asked, and the plan itself lives on mutable state no test can reach.
type recordingSearcher struct {
	inner  tools.Searcher
	req    domainretrieval.Request
	result domainretrieval.SearchResult
	calls  int
}

func (s *recordingSearcher) Search(
	ctx context.Context, req domainretrieval.Request,
) (domainretrieval.SearchResult, error) {
	s.calls++
	s.req = req
	result, err := s.inner.Search(ctx, req)
	s.result = result
	return result, err
}

// seededSearchService builds a real retrieval service over an in-memory corpus.
//
// The vector channel is switched off rather than faked: this slice is about the
// hard conditions, and a semantic channel would let a restaurant that fails a
// hard filter back into the pool through a route that is not under test.
func seededSearchService(t *testing.T, restaurants ...search.RestaurantDetail) *retrieval.Service {
	t.Helper()
	repo := testkit.NewRestaurantRepository()
	for _, restaurant := range restaurants {
		if err := repo.Upsert(context.Background(), restaurant); err != nil {
			t.Fatalf("seed restaurant %d: %v", restaurant.RestaurantID, err)
		}
	}
	service, err := retrieval.NewService(retrieval.ServiceConfig{
		Weights:          retrieval.DefaultWeights,
		Oversample:       2,
		TopK:             10,
		EnableStructured: true,
		EnableKeyword:    true,
		EnableVector:     false,
	}, retrieval.Deps{
		Restaurants: repo,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("build retrieval service: %v", err)
	}
	return service
}

func rankedDetail(id int64, name string, borough string, cuisines []string, price int, rating float64) search.RestaurantDetail {
	priceLevel := price
	ratingValue := rating
	return search.RestaurantDetail{
		RestaurantID:    id,
		Name:            name,
		Address:         "1 Example St",
		Borough:         borough,
		Cuisines:        cuisines,
		PriceLevel:      &priceLevel,
		Rating:          &ratingValue,
		RatingCount:     120,
		IsActiveForDemo: true,
		SnapshotAt:      time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC),
	}
}

// factAnswerResponse scripts one grounded composer completion for a turn that
// has candidates but no review evidence: a fact-only (or review-gap) answer
// with no citation markers and a well-formed FOLLOWUPS trailer.
func factAnswerResponse(text string) domainchat.ChatResponse {
	return domainchat.ChatResponse{
		Message: domainchat.ChatMessage{
			Role:    domainchat.RoleAssistant,
			Content: text + "\nFOLLOWUPS: []",
		},
		FinishReason: domainchat.FinishReasonStop,
	}
}

// extractionResponse renders a scripted slot extraction.
func extractionResponse(t *testing.T, payload string) *scriptedProvider {
	t.Helper()
	return &scriptedProvider{
		supportTools: true,
		structuredResps: []domainchat.StructuredResponse{
			{Content: payload},
		},
		// The model asks for the search it was told to ask for and supplies no
		// arguments: the conditions have to come from the plan.
		toolResps: []domainchat.ToolCallResponse{
			toolCallResponse("c1", tools.SearchRestaurantsToolName, `{}`),
			assistantText("按你说的条件找到了几家餐厅。"),
		},
	}
}

// TestHardConditionsSurviveAModelThatOmitsThem is the slice's acceptance test.
//
// It is written the way the failure it guards against actually happens: the
// model understands the sentence, decides to search, and passes nothing —
// because the sentence was already interpreted upstream and the model has no
// reason to repeat it. Without injection the search becomes unfiltered, and the
// user gets Brooklyn pizzerias for a Manhattan Italian question.
func TestHardConditionsSurviveAModelThatOmitsThem(t *testing.T) {
	service := seededSearchService(t,
		rankedDetail(1, "Trattoria Uno", "manhattan", []string{"italian"}, 2, 4.6),
		rankedDetail(2, "Trattoria Due", "manhattan", []string{"italian"}, 3, 4.4),
		rankedDetail(3, "Trattoria Tre", "manhattan", []string{"italian", "pizza"}, 2, 4.1),
		rankedDetail(4, "Pizza Only", "manhattan", []string{"pizza"}, 2, 4.8),
		rankedDetail(5, "Brooklyn Trattoria", "brooklyn", []string{"italian"}, 2, 4.9),
		rankedDetail(6, "Low Rated Trattoria", "manhattan", []string{"italian"}, 2, 3.4),
	)
	recorder := &recordingSearcher{inner: service}

	provider := extractionResponse(t, `{
		"intent": "discover",
		"query": "曼哈顿 4 星以上的意大利菜",
		"borough": "曼哈顿",
		"cuisines": ["意大利菜"],
		"min_rating": 4
	}`)
	// Candidate facts are answer material now: the turn reaches the grounded
	// composer even though no review evidence was recalled.
	provider.completeResps = append(provider.completeResps, factAnswerResponse(
		"为你找到 3 家曼哈顿意大利餐厅，评分都在 4 分以上（据 Google Local 2021 年快照）。"))

	registry := toolreg.New(0)
	if err := registry.Register(tools.SearchRestaurantsEntry(recorder)); err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 3}, agent.Deps{
		Chat:        provider,
		ToolCalling: provider,
		Registry:    registry,
		Extractor:   slots.New(slots.Deps{Structured: provider}),
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	result, events := runner.Run(context.Background(), agent.TurnInput{
		ThreadID:  "th-slice",
		UserInput: "曼哈顿 4 星以上的意大利菜",
	})
	drain(events)

	if result == nil || result.Answer == nil {
		t.Fatal("the turn must produce an answer")
	}
	if recorder.calls != 1 {
		t.Fatalf("search calls = %d, want 1", recorder.calls)
	}

	// The extraction ran once for the turn.
	if len(provider.structuredReqs) != 1 {
		t.Fatalf("structured calls = %d, want exactly 1 per turn", len(provider.structuredReqs))
	}

	filter := recorder.req.Filter
	if filter.Borough != "manhattan" {
		t.Fatalf("borough = %q, want the plan's manhattan to have been injected", filter.Borough)
	}
	if len(filter.Cuisines) != 1 || filter.Cuisines[0] != "italian" {
		t.Fatalf("cuisines = %v, want the plan's italian to have been injected", filter.Cuisines)
	}
	if filter.MinRating == nil || *filter.MinRating != 4 {
		t.Fatalf("min rating = %v, want 4", filter.MinRating)
	}

	candidates := recorder.result.Candidates
	if len(candidates) < 3 {
		t.Fatalf("candidates = %d, want at least 3 (got %+v)", len(candidates), candidates)
	}
	// The invariant the milestone states as "硬条件 100% 正确": every returned
	// candidate satisfies every hard condition.
	for _, candidate := range candidates {
		if !filter.Matches(candidate) {
			t.Errorf("candidate %d (%s, %s, rating %v) does not satisfy %+v",
				candidate.RestaurantID, candidate.Name, candidate.Borough,
				candidate.Rating, filter)
		}
	}
	// And the ones that must not appear are absent, by id.
	for _, id := range []int64{4, 5, 6} {
		for _, candidate := range candidates {
			if candidate.RestaurantID == id {
				t.Errorf("restaurant %d should have been filtered out", id)
			}
		}
	}
}

// TestExplicitModelArgumentsAreNotOverridden pins the direction of the merge.
//
// The plan fills omissions; it does not overrule. A model that reads a search
// coming back empty may legitimately widen it, and overriding that would make
// the widening impossible — the user would see "nothing found" forever.
func TestExplicitModelArgumentsAreNotOverridden(t *testing.T) {
	service := seededSearchService(t,
		rankedDetail(1, "Trattoria Uno", "manhattan", []string{"italian"}, 2, 4.6),
		rankedDetail(2, "Trattoria Due", "manhattan", []string{"italian"}, 2, 3.2),
	)
	recorder := &recordingSearcher{inner: service}

	provider := extractionResponse(t, `{
		"intent": "discover",
		"query": "曼哈顿 4 星以上的意大利菜",
		"borough": "manhattan",
		"cuisines": ["italian"],
		"min_rating": 4
	}`)
	// The model deliberately drops the rating floor on its own call.
	provider.toolResps[0] = toolCallResponse("c1", tools.SearchRestaurantsToolName,
		`{"borough":"manhattan","cuisine":"italian"}`)
	provider.completeResps = append(provider.completeResps, factAnswerResponse(
		"为你找到曼哈顿的意大利餐厅（据 Google Local 2021 年快照）。"))

	registry := toolreg.New(0)
	if err := registry.Register(tools.SearchRestaurantsEntry(recorder)); err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 3}, agent.Deps{
		Chat: provider, ToolCalling: provider, Registry: registry,
		Extractor: slots.New(slots.Deps{Structured: provider}),
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if _, events := runner.Run(context.Background(), agent.TurnInput{
		ThreadID: "th-widen", UserInput: "曼哈顿 4 星以上的意大利菜",
	}); events != nil {
		drain(events)
	}

	if recorder.req.Filter.Borough != "manhattan" {
		t.Fatalf("borough = %q", recorder.req.Filter.Borough)
	}
	// Fill-omissions semantics: the model omitted the floor, so the plan's
	// value is used even though the model's own call had none.
	if recorder.req.Filter.MinRating == nil || *recorder.req.Filter.MinRating != 4 {
		t.Fatalf("min rating = %v, want the plan's 4 to fill the omission",
			recorder.req.Filter.MinRating)
	}
	// The model asked for no neighbourhood; the plan had none either.
	if recorder.req.Filter.Neighborhood != "" {
		t.Fatalf("neighborhood = %q, want none", recorder.req.Filter.Neighborhood)
	}
}

// TestSoftConditionsReachTheRequestButNeverTheFilter is the M5-01 half of the
// boundary M5-02 completes: the condition has to survive into retrieval, and it
// has to stay out of the hard filter while doing so.
func TestSoftConditionsReachTheRequestButNeverTheFilter(t *testing.T) {
	service := seededSearchService(t,
		rankedDetail(1, "Quiet Trattoria", "manhattan", []string{"italian"}, 2, 4.6),
		rankedDetail(2, "Trattoria Due", "manhattan", []string{"italian"}, 2, 4.4),
	)
	recorder := &recordingSearcher{inner: service}

	provider := extractionResponse(t, `{
		"intent": "recommend",
		"query": "安静的意大利餐厅",
		"cuisines": ["italian"],
		"soft_conditions": [{"text":"安静","topic":"ambience"}]
	}`)
	// No evidence tool exists in this fixture, so the opinion half is a review
	// gap: the composer still runs on the candidate facts and has to flag that
	// 安静 cannot be confirmed.
	provider.completeResps = append(provider.completeResps, factAnswerResponse(
		"是否安静缺少评论资料，无法确认；餐厅的客观信息见上。"))

	registry := toolreg.New(0)
	if err := registry.Register(tools.SearchRestaurantsEntry(recorder)); err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 3}, agent.Deps{
		Chat: provider, ToolCalling: provider, Registry: registry,
		Extractor: slots.New(slots.Deps{Structured: provider}),
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if _, events := runner.Run(context.Background(), agent.TurnInput{
		ThreadID: "th-soft", UserInput: "安静的意大利餐厅",
	}); events != nil {
		drain(events)
	}

	if len(recorder.req.SoftConditions) != 1 || recorder.req.SoftConditions[0].Text != "安静" {
		t.Fatalf("soft conditions = %v, want the user's own phrase", recorder.req.SoftConditions)
	}
	if got := recorder.req.SoftConditions[0].Topic; got != review.TopicAmbience {
		t.Fatalf("soft condition topic = %q, want %q", got, review.TopicAmbience)
	}
	filter := recorder.req.Filter
	for _, value := range append([]string{filter.Borough, filter.Neighborhood}, filter.Cuisines...) {
		if value == "安静" || value == "ambience" {
			t.Fatalf("soft condition leaked into the hard filter: %+v", filter)
		}
	}
}

// TestTurnWithoutAnExtractorStillRuns keeps the plan optional. A deployment that
// wires no extractor must not have a broken chat path; it simply loses the
// defaulting, and the model's own arguments are all there is.
func TestTurnWithoutAnExtractorStillRuns(t *testing.T) {
	service := seededSearchService(t,
		rankedDetail(1, "Trattoria Uno", "manhattan", []string{"italian"}, 2, 4.6),
	)
	recorder := &recordingSearcher{inner: service}
	provider := &scriptedProvider{
		supportTools: true,
		toolResps: []domainchat.ToolCallResponse{
			toolCallResponse("c1", tools.SearchRestaurantsToolName, `{"cuisine":"italian"}`),
			assistantText("找到一家。"),
		},
		completeResps: []domainchat.ChatResponse{factAnswerResponse(
			"找到一家意大利餐厅（据 Google Local 2021 年快照）。")},
	}
	registry := toolreg.New(0)
	if err := registry.Register(tools.SearchRestaurantsEntry(recorder)); err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 3}, agent.Deps{
		Chat: provider, ToolCalling: provider, Registry: registry,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	result, events := runner.Run(context.Background(), agent.TurnInput{
		ThreadID: "th-bare", UserInput: "意大利菜",
	})
	drain(events)
	if result == nil || result.Answer == nil {
		t.Fatal("a turn without an extractor must still answer")
	}
	if len(provider.structuredReqs) != 0 {
		t.Fatal("no extractor means no structured call")
	}
	if recorder.calls != 1 {
		t.Fatalf("search calls = %d, want 1", recorder.calls)
	}
}
