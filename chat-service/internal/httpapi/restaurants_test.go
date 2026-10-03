package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/retrieval"
	"github.com/zed1995/platepilot/shared/domain/search"
)

// stubSearch records what the handler passed down and returns a fixed answer.
type stubSearch struct {
	got   retrieval.Request
	out   retrieval.SearchResult
	err   error
	calls int
}

func (s *stubSearch) Search(_ context.Context, req retrieval.Request) (retrieval.SearchResult, error) {
	s.calls++
	s.got = req
	return s.out, s.err
}

func searchRouter(service SearchService) *server.Hertz {
	return NewRouter(Config{Addr: ":0", Version: "test", Logger: quietLogger(), Search: service})
}

func jsonBody(payload string) *ut.Body {
	return &ut.Body{Body: strings.NewReader(payload), Len: len(payload)}
}

const jsonContentType = "Content-Type"

func postSearch(t *testing.T, service SearchService, body string) *ut.ResponseRecorder {
	t.Helper()
	h := searchRouter(service)
	return ut.PerformRequest(h.Engine, http.MethodPost, "/v1/restaurants/search",
		jsonBody(body),
		ut.Header{Key: jsonContentType, Value: "application/json"},
	)
}

func TestSearchEndpointReturnsCandidatesAndTrace(t *testing.T) {
	service := &stubSearch{out: retrieval.SearchResult{
		Candidates: []search.RestaurantCandidate{{RestaurantID: 7, Name: "Joe's Pizza", Score: 1.5}},
		Trace:      &retrieval.Trace{CandidatePool: 1, Returned: 1},
	}}
	w := postSearch(t, service, `{"filter":{"borough":"manhattan"},"top_k":3}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var body SearchResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Candidates) != 1 || body.Candidates[0].RestaurantID != 7 {
		t.Fatalf("unexpected candidates: %+v", body.Candidates)
	}
	if body.Trace == nil {
		t.Fatal("the trace must travel with the candidates")
	}
	if service.got.Filter.Borough != "manhattan" || service.got.TopK != 3 {
		t.Fatalf("the request did not reach the service: %+v", service.got)
	}
}

// "No matches" is a successful search. Returning an empty array keeps a client
// from retrying a question that has no answer.
func TestSearchEndpointReturnsAnEmptyArrayRatherThanNull(t *testing.T) {
	w := postSearch(t, &stubSearch{}, `{"text":"pizza"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var body SearchResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Candidates == nil {
		t.Fatal("candidates must be an empty array, not null")
	}
	if !strings.Contains(w.Body.String(), `"candidates":[]`) {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestSearchEndpointRejectsAnEmptyRequest(t *testing.T) {
	service := &stubSearch{}
	w := postSearch(t, service, `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
	if got := decodeError(t, w.Body.Bytes()).Error.Code; got != string(errs.CodeRetrievalEmptyQuery) {
		t.Fatalf("code = %q, want %q", got, errs.CodeRetrievalEmptyQuery)
	}
	if service.calls != 0 {
		t.Fatal("an invalid request must not reach the service")
	}
}

// An unknown borough is a request defect. Reporting it as 500 would send an
// operator looking for a service fault that does not exist.
func TestSearchEndpointRejectsAnUnknownBoroughWithA4xx(t *testing.T) {
	service := &stubSearch{}
	w := postSearch(t, service, `{"filter":{"borough":"New Jersey"}}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
	if got := decodeError(t, w.Body.Bytes()).Error.Code; got != string(errs.CodeRetrievalInvalidFilter) {
		t.Fatalf("code = %q, want %q", got, errs.CodeRetrievalInvalidFilter)
	}
	if service.calls != 0 {
		t.Fatal("an invalid filter must not reach the service")
	}
}

func TestSearchEndpointRejectsAnImpossiblePriceLevel(t *testing.T) {
	w := postSearch(t, &stubSearch{}, `{"filter":{"price_levels":[9]}}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
	if got := decodeError(t, w.Body.Bytes()).Error.Code; got != string(errs.CodeRetrievalInvalidFilter) {
		t.Fatalf("code = %q, want %q", got, errs.CodeRetrievalInvalidFilter)
	}
}

func TestSearchEndpointSurfacesAServiceFailure(t *testing.T) {
	w := postSearch(t, &stubSearch{err: errs.New(errs.CodeNotFound, "no restaurant")},
		`{"text":"nonexistent"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", w.Code, w.Body.String())
	}
	if got := decodeError(t, w.Body.Bytes()).Error.Code; got != string(errs.CodeNotFound) {
		t.Fatalf("code = %q", got)
	}
}

func TestSearchEndpointRejectsAMalformedBody(t *testing.T) {
	w := postSearch(t, &stubSearch{}, `{"top_k":"many"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
}

// The route is versioned because the response shape is a client contract: the
// candidate explanations are rendered by a UI.
func TestSearchRouteIsRegisteredUnderV1(t *testing.T) {
	w := postSearch(t, &stubSearch{}, `{"text":"pizza"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("the /v1 route is not registered: %d (body %s)", w.Code, w.Body.String())
	}
}

func TestSearchRouteIsAbsentWithoutAService(t *testing.T) {
	h := testRouter()
	w := ut.PerformRequest(h.Engine, http.MethodPost, "/v1/restaurants/search",
		jsonBody(`{"text":"x"}`),
		ut.Header{Key: jsonContentType, Value: "application/json"})
	if w.Code == http.StatusOK {
		t.Fatal("the read route must not be served when no service is wired")
	}
}

func TestUnknownRouteIsStillNotFound(t *testing.T) {
	h := searchRouter(&stubSearch{})
	w := ut.PerformRequest(h.Engine, http.MethodPost, "/v1/unknown",
		jsonBody(`{}`),
		ut.Header{Key: jsonContentType, Value: "application/json"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}
