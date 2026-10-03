package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
	"github.com/zed1995/platepilot/chat-service/internal/retrieval"
	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/evidence"
	domainretrieval "github.com/zed1995/platepilot/shared/domain/retrieval"
	"github.com/zed1995/platepilot/shared/domain/search"
	domaintool "github.com/zed1995/platepilot/shared/domain/tool"
)

// ---- fakes ----------------------------------------------------------------

type fakeSearcher struct {
	lastReq domainretrieval.Request
	result  domainretrieval.SearchResult
	err     error
	called  int
}

func (f *fakeSearcher) Search(_ context.Context, req domainretrieval.Request) (domainretrieval.SearchResult, error) {
	f.called++
	f.lastReq = req
	return f.result, f.err
}

type fakeEvidenceReader struct {
	lastReq retrieval.EvidenceRequest
	result  retrieval.EvidenceResult
	err     error
	called  int
}

func (f *fakeEvidenceReader) Evidence(_ context.Context, req retrieval.EvidenceRequest) (retrieval.EvidenceResult, error) {
	f.called++
	f.lastReq = req
	return f.result, f.err
}

func invoke(t *testing.T, h toolreg.Handler, raw string) domaintool.ToolResult {
	t.Helper()
	out, err := h(context.Background(), json.RawMessage(raw))
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	return out
}

func ptrFloat(v float64) *float64 { return &v }
func ptrInt(v int) *int           { return &v }

// ---- search_restaurants ---------------------------------------------------

func TestSearchRestaurantsMapsArguments(t *testing.T) {
	svc := &fakeSearcher{}
	h := SearchRestaurantsEntry(svc).Handler

	out := invoke(t, h, `{"query":"安静的意餐约会","borough":"manhattan","cuisine":"italian","min_rating":4.2,"open_now":true,"top_k":5}`)
	if out.Status != domaintool.ToolStatusOK {
		t.Fatalf("status = %s err = %+v", out.Status, out.Error)
	}
	if svc.called != 1 {
		t.Fatalf("search called %d times", svc.called)
	}
	req := svc.lastReq
	if req.Query != "安静的意餐约会" {
		t.Fatalf("query = %q", req.Query)
	}
	if req.Filter.Borough != "manhattan" {
		t.Fatalf("borough = %q", req.Filter.Borough)
	}
	if len(req.Filter.Cuisines) != 1 || req.Filter.Cuisines[0] != "italian" {
		t.Fatalf("cuisines = %v", req.Filter.Cuisines)
	}
	if req.Filter.MinRating == nil || *req.Filter.MinRating != 4.2 {
		t.Fatalf("min_rating = %v", req.Filter.MinRating)
	}
	if req.Filter.OpenNow == nil || !*req.Filter.OpenNow {
		t.Fatalf("open_now = %v", req.Filter.OpenNow)
	}
	if req.TopK != 5 {
		t.Fatalf("top_k = %d, want 5", req.TopK)
	}
}

func TestSearchRestaurantsTopKClampAndDefault(t *testing.T) {
	svc := &fakeSearcher{}
	h := SearchRestaurantsEntry(svc).Handler

	invoke(t, h, `{"query":"q","top_k":0}`)
	if svc.lastReq.TopK != 10 {
		t.Fatalf("default top_k = %d, want 10", svc.lastReq.TopK)
	}
	invoke(t, h, `{"query":"q","top_k":99}`)
	if svc.lastReq.TopK != 10 {
		t.Fatalf("clamped top_k = %d, want 10", svc.lastReq.TopK)
	}
}

func TestSearchRestaurantsRejectsEmptyArguments(t *testing.T) {
	svc := &fakeSearcher{}
	h := SearchRestaurantsEntry(svc).Handler

	_, err := h(context.Background(), json.RawMessage(`{}`))
	if errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	if svc.called != 0 {
		t.Fatal("retrieval must not be called for empty arguments")
	}
}

func TestSearchRestaurantsRendersCandidatesAndDataRoundtrip(t *testing.T) {
	svc := &fakeSearcher{result: domainretrieval.SearchResult{
		Candidates: []search.RestaurantCandidate{
			{
				RestaurantID: 42, Name: "Trattoria Bella", Borough: "manhattan",
				Cuisines: []string{"italian"}, Rating: ptrFloat(4.6),
				PriceLevel: ptrInt(2), Address: "12 Spring St",
			},
		},
		Trace: &domainretrieval.Trace{Warnings: []string{"rerank unavailable"}},
	}}
	h := SearchRestaurantsEntry(svc).Handler

	out := invoke(t, h, `{"query":"italian"}`)
	for _, want := range []string{"id=42", "Trattoria Bella", "manhattan", "italian", "评分4.6", "价格2", "12 Spring St", "降级提示：rerank unavailable"} {
		if !strings.Contains(out.Content, want) {
			t.Fatalf("content missing %q:\n%s", want, out.Content)
		}
	}

	var payload domainretrieval.SearchResult
	if err := json.Unmarshal(out.Data, &payload); err != nil {
		t.Fatalf("Data is not a SearchResult: %v", err)
	}
	if len(payload.Candidates) != 1 || payload.Candidates[0].RestaurantID != 42 {
		t.Fatalf("payload candidates = %+v", payload.Candidates)
	}
	if len(payload.Trace.Warnings) != 1 {
		t.Fatalf("trace warnings lost: %+v", payload.Trace)
	}
}

func TestSearchRestaurantsEmptyResultIsNotAnError(t *testing.T) {
	svc := &fakeSearcher{result: domainretrieval.SearchResult{}}
	h := SearchRestaurantsEntry(svc).Handler

	out := invoke(t, h, `{"query":"nothing"}`)
	if out.Status != domaintool.ToolStatusOK {
		t.Fatalf("status = %s", out.Status)
	}
	if !strings.Contains(out.Content, "未找到") {
		t.Fatalf("content = %q", out.Content)
	}
}

func TestSearchRestaurantsPropagatesRetrievalError(t *testing.T) {
	svc := &fakeSearcher{err: errs.New(errs.CodeRetrievalInvalidFilter, "bad borough")}
	h := SearchRestaurantsEntry(svc).Handler

	_, err := h(context.Background(), json.RawMessage(`{"borough":"nowhere"}`))
	if errs.CodeOf(err) != errs.CodeRetrievalInvalidFilter {
		t.Fatalf("err = %v, want retrieval_invalid_filter", err)
	}
}

// ---- get_restaurant_evidence ----------------------------------------------

func TestEvidenceValidatesRequiredArguments(t *testing.T) {
	svc := &fakeEvidenceReader{}
	h := RestaurantEvidenceEntry(svc).Handler

	cases := map[string]string{
		"missing query":      `{"restaurant_ids":[1]}`,
		"missing ids":        `{"query":"hours"}`,
		"both missing":       `{}`,
		"more than five ids": `{"restaurant_ids":[1,2,3,4,5,6],"query":"q"}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := h(context.Background(), json.RawMessage(raw))
			if errs.CodeOf(err) != errs.CodeInvalidArgument {
				t.Fatalf("err = %v, want invalid_argument", err)
			}
		})
	}
	if svc.called != 0 {
		t.Fatal("retrieval must not be called for invalid arguments")
	}
}

func TestEvidenceMapsArgumentsAndTopK(t *testing.T) {
	svc := &fakeEvidenceReader{}
	h := RestaurantEvidenceEntry(svc).Handler

	invoke(t, h, `{"restaurant_ids":[7,8],"query":"周末几点关门","topic":"wait time","doc_types":["restaurant_hours","restaurant_profile"],"top_k":30}`)
	if svc.called != 1 {
		t.Fatalf("evidence called %d times", svc.called)
	}
	req := svc.lastReq
	if req.Query != "周末几点关门" || req.Topic != "wait time" {
		t.Fatalf("request = %+v", req)
	}
	if len(req.RestaurantIDs) != 2 || req.RestaurantIDs[0] != 7 || req.RestaurantIDs[1] != 8 {
		t.Fatalf("restaurant_ids = %v", req.RestaurantIDs)
	}
	if len(req.DocTypes) != 2 ||
		req.DocTypes[0] != evidence.DocTypeRestaurantHours ||
		req.DocTypes[1] != evidence.DocTypeRestaurantProfile {
		t.Fatalf("doc_types = %v", req.DocTypes)
	}
	if req.TopK != 30 {
		t.Fatalf("top_k = %d", req.TopK)
	}

	invoke(t, h, `{"restaurant_ids":[1],"query":"q","top_k":0}`)
	if svc.lastReq.TopK != 20 {
		t.Fatalf("default top_k = %d, want 20", svc.lastReq.TopK)
	}
	invoke(t, h, `{"restaurant_ids":[1],"query":"q","top_k":500}`)
	if svc.lastReq.TopK != 100 {
		t.Fatalf("clamped top_k = %d, want 100", svc.lastReq.TopK)
	}
}

func TestEvidenceRejectsUnknownDocType(t *testing.T) {
	svc := &fakeEvidenceReader{}
	h := RestaurantEvidenceEntry(svc).Handler

	_, err := h(context.Background(), json.RawMessage(`{"restaurant_ids":[1],"query":"q","doc_types":["menu"]}`))
	if errs.CodeOf(err) != errs.CodeValidationFailed {
		t.Fatalf("err = %v, want validation_failed", err)
	}
}

func TestEvidenceRendersWithIDsAndDataRoundtrip(t *testing.T) {
	svc := &fakeEvidenceReader{result: retrieval.EvidenceResult{
		Evidence: []evidence.Evidence{
			{EvidenceID: 901, RestaurantID: 42, RestaurantName: "Trattoria Bella",
				DocType: evidence.DocTypeRestaurantHours, Title: "营业时间", Content: "周一至周日 11:00-23:00"},
		},
		Trace: &domainretrieval.EvidenceTrace{Warnings: []string{"token budget trimmed 2 docs"}},
	}}
	h := RestaurantEvidenceEntry(svc).Handler

	out := invoke(t, h, `{"restaurant_ids":[42],"query":"几点关门"}`)
	if out.Status != domaintool.ToolStatusOK {
		t.Fatalf("status = %s err = %+v", out.Status, out.Error)
	}
	for _, want := range []string{"[证据 901 |", "restaurant_id=42", "restaurant_hours", "tokens≈", "周一至周日 11:00-23:00", "降级提示"} {
		if !strings.Contains(out.Content, want) {
			t.Fatalf("content missing %q:\n%s", want, out.Content)
		}
	}

	var payload retrieval.EvidenceResult
	if err := json.Unmarshal(out.Data, &payload); err != nil {
		t.Fatalf("Data is not an EvidenceResult: %v", err)
	}
	if len(payload.Evidence) != 1 || payload.Evidence[0].EvidenceID != 901 {
		t.Fatalf("payload evidence = %+v", payload.Evidence)
	}
}

func TestEvidenceEmptyResultIsNotAnError(t *testing.T) {
	svc := &fakeEvidenceReader{result: retrieval.EvidenceResult{}}
	h := RestaurantEvidenceEntry(svc).Handler

	out := invoke(t, h, `{"restaurant_ids":[42],"query":"unknown thing"}`)
	if out.Status != domaintool.ToolStatusOK {
		t.Fatalf("status = %s", out.Status)
	}
	if !strings.Contains(out.Content, "未找到相关证据") {
		t.Fatalf("content = %q", out.Content)
	}
}

// ---- end to end through the registry --------------------------------------

func TestTwoToolsChainedThroughRegistry(t *testing.T) {
	searcher := &fakeSearcher{result: domainretrieval.SearchResult{
		Candidates: []search.RestaurantCandidate{{RestaurantID: 77, Name: "Noodle House"}},
	}}
	reader := &fakeEvidenceReader{result: retrieval.EvidenceResult{
		Evidence: []evidence.Evidence{{EvidenceID: 501, RestaurantID: 77, Content: "招牌是拉面。"}},
	}}

	reg := toolreg.New(0)
	if err := reg.Register(SearchRestaurantsEntry(searcher)); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(RestaurantEvidenceEntry(reader)); err != nil {
		t.Fatal(err)
	}

	searchOut := reg.Invoke(context.Background(), domaintool.ToolCall{
		ID: "c1", Name: SearchRestaurantsToolName, Arguments: json.RawMessage(`{"query":"ramen"}`),
	})
	if searchOut.Status != domaintool.ToolStatusOK || searchOut.CallID != "c1" {
		t.Fatalf("search result = %+v", searchOut)
	}
	var found domainretrieval.SearchResult
	if err := json.Unmarshal(searchOut.Data, &found); err != nil || len(found.Candidates) != 1 {
		t.Fatalf("search data broken: %v %+v", err, found)
	}

	evidenceOut := reg.Invoke(context.Background(), domaintool.ToolCall{
		ID: "c2", Name: RestaurantEvidenceToolName,
		Arguments: json.RawMessage(mustJSON(map[string]any{
			"restaurant_ids": []int64{found.Candidates[0].RestaurantID},
			"query":          "招牌是什么",
		})),
	})
	if evidenceOut.Status != domaintool.ToolStatusOK {
		t.Fatalf("evidence result = %+v", evidenceOut)
	}
	if reader.lastReq.RestaurantIDs[0] != 77 {
		t.Fatalf("chained id = %v", reader.lastReq.RestaurantIDs)
	}
	var gathered retrieval.EvidenceResult
	if err := json.Unmarshal(evidenceOut.Data, &gathered); err != nil {
		t.Fatal(err)
	}
	if gathered.Evidence[0].EvidenceID != 501 {
		t.Fatalf("evidence id not carried through: %+v", gathered.Evidence)
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
