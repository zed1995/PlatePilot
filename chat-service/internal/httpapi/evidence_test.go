package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/evidence"
)

// stubEvidence records what the handler passed down.
type stubEvidence struct {
	got   EvidenceQuery
	out   EvidenceResult
	err   error
	calls int
}

func (s *stubEvidence) Evidence(_ context.Context, req EvidenceQuery) (EvidenceResult, error) {
	s.calls++
	s.got = req
	return s.out, s.err
}

func evidenceRouter(service EvidenceService) *server.Hertz {
	return NewRouter(Config{Addr: ":0", Version: "test", Logger: quietLogger(), Evidence: service})
}

func postEvidence(t *testing.T, service EvidenceService, path, body string) *ut.ResponseRecorder {
	t.Helper()
	h := evidenceRouter(service)
	return ut.PerformRequest(h.Engine, http.MethodPost, path,
		jsonBody(body),
		ut.Header{Key: jsonContentType, Value: "application/json"},
	)
}

func citation(id, restaurantID int64, content string) evidence.Evidence {
	return evidence.Evidence{
		EvidenceID:   id,
		RestaurantID: restaurantID,
		DocType:      evidence.DocTypeRestaurantReviewSummary,
		Title:        "reviews",
		Content:      content,
		Source:       "yelp_review",
		SnapshotAt:   time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC),
		Score:        0.9,
		Topic:        "wait",
	}
}

func decodeEvidence(t *testing.T, w *ut.ResponseRecorder) EvidenceBundle {
	t.Helper()
	var bundle EvidenceBundle
	if err := json.Unmarshal(w.Body.Bytes(), &bundle); err != nil {
		t.Fatalf("decode: %v (body %s)", err, w.Body.String())
	}
	return bundle
}

// The response has to carry the trace: a citation a reader cannot check is an
// assertion, and the trace is what makes it checkable.
func TestEvidenceEndpointReturnsCitationsAndTrace(t *testing.T) {
	service := &stubEvidence{out: EvidenceResult{
		Evidence: []evidence.Evidence{citation(1, 7, "the wait was long")},
		Trace: &EvidenceTrace{
			Query: "how long is the wait", ScopeSize: 1,
			Recalled: 5, Kept: 1, Dropped: 4, Tokens: 120, TokenBudget: 2000,
			DroppedByReason:  map[string]int{"token_budget": 4},
			EmbeddingModelID: "qwen3-embedding:0.6b", QueryEmbeddingDim: 1024,
		},
	}}
	w := postEvidence(t, service, "/v1/restaurants/evidence",
		`{"restaurant_ids":[7],"query":"how long is the wait"}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	bundle := decodeEvidence(t, w)
	if len(bundle.Evidence) != 1 || bundle.Evidence[0].EvidenceID != 1 {
		t.Fatalf("citations = %+v", bundle.Evidence)
	}
	if bundle.Trace == nil {
		t.Fatal("the trace must travel with the citations")
	}
	if bundle.Trace.ScopeSize != 1 || bundle.Trace.Kept != 1 || bundle.Trace.Dropped != 4 {
		t.Fatalf("trace = %+v", bundle.Trace)
	}
	if bundle.Trace.DroppedByReason["token_budget"] != 4 {
		t.Fatalf("drop reasons = %v", bundle.Trace.DroppedByReason)
	}
	if bundle.Trace.EmbeddingModelID != "qwen3-embedding:0.6b" {
		t.Fatalf("the trace must name the model, got %q", bundle.Trace.EmbeddingModelID)
	}
}

// An unscoped recall is the failure the whole feature guards against, and it
// must be refused with a code the caller can act on.
func TestEvidenceEndpointRefusesAnUnscopedRequest(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"restaurant_ids":[]}`,
		`{"restaurant_ids":[0]}`,
		`{"restaurant_ids":[-4]}`,
		`{"query":"wait times"}`,
	} {
		service := &stubEvidence{}
		w := postEvidence(t, service, "/v1/restaurants/evidence", body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d, want 400", body, w.Code)
		}
		if service.calls != 0 {
			t.Fatalf("body %s: an unscoped request must not reach the service", body)
		}
		var errBody struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &errBody); err != nil {
			t.Fatalf("body %s: decode: %v", body, err)
		}
		if errBody.Error.Code != string(errs.CodeRetrievalNoScope) {
			t.Fatalf("body %s: code = %q, want %q",
				body, errBody.Error.Code, errs.CodeRetrievalNoScope)
		}
	}
}

// The path is authoritative. A client that sends a different id in the body must
// still be answered about the restaurant the route named.
func TestRestaurantEvidenceRouteTakesTheIdFromThePath(t *testing.T) {
	service := &stubEvidence{}
	w := postEvidence(t, service, "/v1/restaurants/7/evidence", `{"query":"wait"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if len(service.got.RestaurantIDs) != 1 || service.got.RestaurantIDs[0] != 7 {
		t.Fatalf("restaurant_ids = %v, want [7]", service.got.RestaurantIDs)
	}
	if service.got.Query != "wait" {
		t.Fatalf("query = %q, want it forwarded", service.got.Query)
	}
}

// The path id wins over a conflicting body id, and the route's own id is not
// duplicated when the body repeats it.
func TestRestaurantEvidenceRoutePrefersThePathOverTheBody(t *testing.T) {
	for _, body := range []string{
		`{"restaurant_ids":[9]}`,
		`{"restaurant_ids":[7]}`,
		`{"restaurant_ids":[7,9]}`,
		`{}`,
	} {
		service := &stubEvidence{}
		w := postEvidence(t, service, "/v1/restaurants/7/evidence", body)
		if w.Code != http.StatusOK {
			t.Fatalf("body %s: status = %d, want 200", body, w.Code)
		}
		if service.got.RestaurantIDs[0] != 7 {
			t.Fatalf("body %s: the path id must come first, got %v", body, service.got.RestaurantIDs)
		}
		for _, id := range service.got.RestaurantIDs {
			if id == 7 && len(service.got.RestaurantIDs) > 1 {
				count := 0
				for _, other := range service.got.RestaurantIDs {
					if other == 7 {
						count++
					}
				}
				if count != 1 {
					t.Fatalf("body %s: the path id was duplicated: %v", body, service.got.RestaurantIDs)
				}
			}
		}
	}
}

// A non-numeric or non-positive path id is a malformed request, not a lookup
// that happens to miss.
func TestRestaurantEvidenceRouteRejectsABadId(t *testing.T) {
	for _, path := range []string{
		"/v1/restaurants/abc/evidence",
		"/v1/restaurants/0/evidence",
		"/v1/restaurants/-3/evidence",
	} {
		service := &stubEvidence{}
		w := postEvidence(t, service, path, `{}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", path, w.Code)
		}
		if service.calls != 0 {
			t.Fatalf("%s: a malformed id must not reach the service", path)
		}
	}
}

// "This restaurant has nothing on record" is a real answer, not a failure.
func TestEvidenceEndpointRendersAnEmptyResultAsAnArray(t *testing.T) {
	service := &stubEvidence{out: EvidenceResult{Trace: &EvidenceTrace{ScopeSize: 1}}}
	w := postEvidence(t, service, "/v1/restaurants/evidence", `{"restaurant_ids":[7]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"evidence":[]`) {
		t.Fatalf("an empty result must render as [], got %s", w.Body.String())
	}
}

// The token budget is a caller-facing knob and has to reach the service.
func TestEvidenceEndpointForwardsTheTokenBudget(t *testing.T) {
	service := &stubEvidence{}
	w := postEvidence(t, service, "/v1/restaurants/evidence",
		`{"restaurant_ids":[7],"token_budget":800,"topic":"wait","top_k":12,"doc_types":["restaurant_review_summary"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", w.Code, w.Body.String())
	}
	if service.got.TokenBudget != 800 {
		t.Fatalf("token_budget = %d, want 800", service.got.TokenBudget)
	}
	if service.got.TopK != 12 || service.got.Topic != "wait" {
		t.Fatalf("request = %+v", service.got)
	}
	if len(service.got.DocTypes) != 1 ||
		service.got.DocTypes[0] != evidence.DocTypeRestaurantReviewSummary {
		t.Fatalf("doc_types = %v", service.got.DocTypes)
	}
}

// A budget too small to hold a citation must not become a 500: the caller has to
// be able to tell it was their number that was wrong.
func TestEvidenceEndpointSurfacesABudgetRejection(t *testing.T) {
	service := &stubEvidence{err: errs.Newf(errs.CodeRetrievalBudgetExceeded,
		"a budget of 5 tokens cannot hold even one citation")}
	w := postEvidence(t, service, "/v1/restaurants/evidence",
		`{"restaurant_ids":[7],"token_budget":5}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), string(errs.CodeRetrievalBudgetExceeded)) {
		t.Fatalf("body = %s", w.Body.String())
	}
}

// The routes only exist when the service is wired. A nil service must 404 rather
// than dereference.
func TestEvidenceRoutesAreAbsentWithoutTheService(t *testing.T) {
	h := NewRouter(Config{Addr: ":0", Version: "test", Logger: quietLogger()})
	for _, path := range []string{"/v1/restaurants/evidence", "/v1/restaurants/7/evidence"} {
		w := ut.PerformRequest(h.Engine, http.MethodPost, path,
			jsonBody(`{"restaurant_ids":[7]}`),
			ut.Header{Key: jsonContentType, Value: "application/json"})
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", path, w.Code)
		}
	}
}
