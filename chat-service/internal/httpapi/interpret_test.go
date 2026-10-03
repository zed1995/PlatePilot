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
	"github.com/zed1995/platepilot/shared/domain/search"
)

// stubInterpreter records the sentence it was handed and returns a fixed plan.
type stubInterpreter struct {
	got   string
	out   InterpretResult
	err   error
	calls int
}

func (s *stubInterpreter) Interpret(_ context.Context, text string) (InterpretResult, error) {
	s.calls++
	s.got = text
	return s.out, s.err
}

func interpretRouter(service SlotInterpreter) *server.Hertz {
	return NewRouter(Config{Addr: ":0", Version: "test", Logger: quietLogger(), Interpret: service})
}

func postInterpret(t *testing.T, service SlotInterpreter, body string) *ut.ResponseRecorder {
	t.Helper()
	h := interpretRouter(service)
	return ut.PerformRequest(h.Engine, http.MethodPost, "/v1/restaurants/interpret",
		jsonBody(body),
		ut.Header{Key: jsonContentType, Value: "application/json"},
	)
}

// TestInterpretEndpointReturnsThePlanProjection covers the acceptance case: the
// sentence the milestone names must come back split into a borough, a rating
// floor, and a cuisine, with no soft condition.
func TestInterpretEndpointReturnsThePlanProjection(t *testing.T) {
	rating := 4.0
	service := &stubInterpreter{out: InterpretResult{
		Intent: "discover",
		Query:  "曼哈顿中城 4 星以上意大利菜",
		HardFilters: search.RestaurantFilter{
			Borough:   "manhattan",
			MinRating: &rating,
			Cuisines:  []string{"italian"},
		},
		Source: "rules",
	}}
	w := postInterpret(t, service, `{"text":"曼哈顿中城 4 星以上意大利菜"}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if service.got != "曼哈顿中城 4 星以上意大利菜" {
		t.Fatalf("handler passed %q", service.got)
	}
	var body InterpretResult
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.HardFilters.Borough != "manhattan" {
		t.Errorf("borough = %q", body.HardFilters.Borough)
	}
	if body.HardFilters.MinRating == nil || *body.HardFilters.MinRating != 4 {
		t.Errorf("min rating = %v", body.HardFilters.MinRating)
	}
	if len(body.HardFilters.Cuisines) != 1 || body.HardFilters.Cuisines[0] != "italian" {
		t.Errorf("cuisines = %v", body.HardFilters.Cuisines)
	}
	if len(body.SoftConditions) != 0 {
		t.Errorf("soft conditions = %v, want none", body.SoftConditions)
	}
	if body.Source != "rules" {
		t.Errorf("source = %q", body.Source)
	}
	// Empty collections are arrays, not null: a client iterating the response
	// should not need a nil check for the common case.
	if body.SoftConditions == nil || body.NamedRestaurants == nil || body.MissingSlots == nil {
		t.Fatalf("empty collections must serialise as []: soft=%v named=%v missing=%v",
			body.SoftConditions, body.NamedRestaurants, body.MissingSlots)
	}
}

// TestInterpretEndpointKeepsSoftConditionsOutOfTheFilter is the contract the
// front end depends on: the two lists never merge.
func TestInterpretEndpointKeepsSoftConditionsOutOfTheFilter(t *testing.T) {
	service := &stubInterpreter{out: InterpretResult{
		Intent: "recommend",
		Query:  "安静、适合约会的日料",
		SoftConditions: []InterpretSoftCondition{
			{Text: "安静", Topic: "ambience"},
			{Text: "适合约会", Topic: "ambience"},
		},
		Source: "rules",
	}}
	w := postInterpret(t, service, `{"text":"安静、适合约会的日料"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", w.Code, w.Body.String())
	}
	var body InterpretResult
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.SoftConditions) != 2 {
		t.Fatalf("soft conditions = %v", body.SoftConditions)
	}
	if !body.HardFilters.IsEmpty() {
		t.Fatalf("a soft-only sentence must leave the hard filter empty, got %+v", body.HardFilters)
	}
}

// TestInterpretEndpointRejectsEmptyText pins the one case where returning the
// empty plan would be actively misleading: it looks exactly like a successfully
// understood sentence that happened to constrain nothing.
func TestInterpretEndpointRejectsEmptyText(t *testing.T) {
	service := &stubInterpreter{}
	for name, body := range map[string]string{
		"empty":         `{"text":""}`,
		"whitespace":    `{"text":"   "}`,
		"missing field": `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := postInterpret(t, service, body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
			}
			if service.calls != 0 {
				t.Fatal("an invalid request must not reach the interpreter")
			}
		})
	}
}

// TestInterpretEndpointReportsInterpreterFailure keeps a real failure a failure
// rather than an empty plan.
func TestInterpretEndpointReportsInterpreterFailure(t *testing.T) {
	service := &stubInterpreter{err: errs.New(errs.CodeProviderUnavailable, "no provider")}
	w := postInterpret(t, service, `{"text":"意大利菜"}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %s)", w.Code, w.Body.String())
	}
}

// TestInterpretRouteIsIndependentOfTheChatSurface keeps the probe usable in the
// deployment it is most useful in: one with no chat provider at all.
func TestInterpretRouteIsIndependentOfTheChatSurface(t *testing.T) {
	service := &stubInterpreter{out: InterpretResult{Intent: "discover", Source: "rules"}}
	h := interpretRouter(service)
	w := ut.PerformRequest(h.Engine, http.MethodPost, "/v1/restaurants/interpret",
		jsonBody(`{"text":"意大利菜"}`),
		ut.Header{Key: jsonContentType, Value: "application/json"},
	)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 without a chat service (body %s)", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "chat") {
		t.Fatalf("the response must not mention the chat surface: %s", w.Body.String())
	}
}
