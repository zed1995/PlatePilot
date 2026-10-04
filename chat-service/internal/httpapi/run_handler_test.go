package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"

	"github.com/zed1995/platepilot/shared/domain/errs"
)

// stubRunService answers the replay reads from fixed rows and records the
// arguments it was called with, so a test can assert on paging rather than only
// on the response body.
type stubRunService struct {
	runs       []RunView
	toolCalls  []ToolCallView
	nodes      []RunNodeView
	err        error
	lastLimit  int
	lastBefore string
	lastThread string
	lastRun    string
	lastTrace  string
}

func (s *stubRunService) ListRuns(
	_ context.Context, threadID string, limit int, beforeID string,
) (RunPage, error) {
	s.lastThread, s.lastLimit, s.lastBefore = threadID, limit, beforeID
	if s.err != nil {
		return RunPage{}, s.err
	}
	return RunPage{Runs: s.runs}, nil
}

func (s *stubRunService) GetRun(_ context.Context, runID string) (RunDetail, error) {
	s.lastRun = runID
	if s.err != nil {
		return RunDetail{}, s.err
	}
	detail := RunDetail{Run: RunView{RunID: runID}, ToolCalls: s.toolCalls, Nodes: s.nodes}
	if len(s.runs) > 0 {
		detail.Run = s.runs[0]
	}
	return detail, nil
}

func (s *stubRunService) ListNodes(_ context.Context, runID string) ([]RunNodeView, error) {
	s.lastRun = runID
	if s.err != nil {
		return nil, s.err
	}
	return s.nodes, nil
}

// GetTrace answers with the run whose trace id matches, so a test can assert
// the route resolves the identifier a client actually holds.
func (s *stubRunService) GetTrace(_ context.Context, traceID string) (RunDetail, error) {
	s.lastTrace = traceID
	if s.err != nil {
		return RunDetail{}, s.err
	}
	for _, row := range s.runs {
		if row.TraceID == traceID {
			return RunDetail{Run: row, ToolCalls: s.toolCalls, Nodes: s.nodes}, nil
		}
	}
	return RunDetail{}, errs.New(errs.CodeNotFound, "run for trace not found")
}

func runRouter(svc RunService) *server.Hertz {
	return NewRouter(Config{
		Addr:    ":0",
		Version: "test",
		Logger:  quietLogger(),
		Runs:    svc,
	})
}

func startedAt() time.Time { return time.Date(2021, 9, 1, 12, 0, 0, 0, time.UTC) }

// The replay API returns the run's own fields flat, next to its tool chain: a
// reader checking "why did it recommend this" wants one object, not an envelope.
func TestGetRunReturnsTheToolChainInOrder(t *testing.T) {
	svc := &stubRunService{
		runs: []RunView{{
			RunID: "run-1", ThreadID: "th-1", Status: "succeeded",
			StartedAt: startedAt(), ToolCallCount: 2,
		}},
		toolCalls: []ToolCallView{
			{CallID: "c1", ToolName: "search_restaurants", Status: "succeeded", LatencyMS: 12},
			{CallID: "c2", ToolName: "get_restaurant_evidence", Status: "succeeded", LatencyMS: 30},
		},
	}
	h := runRouter(svc)

	w := ut.PerformRequest(h.Engine, http.MethodGet, "/v1/runs/run-1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}

	var body struct {
		RunID     string         `json:"run_id"`
		Status    string         `json:"status"`
		ThreadID  string         `json:"thread_id"`
		ToolCalls []ToolCallView `json:"tool_calls"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", w.Body.String(), err)
	}
	if body.RunID != "run-1" || body.Status != "succeeded" || body.ThreadID != "th-1" {
		t.Fatalf("run fields must be flat on the response, got %+v", body)
	}
	if len(body.ToolCalls) != 2 {
		t.Fatalf("tool calls = %d, want 2", len(body.ToolCalls))
	}
	if body.ToolCalls[0].ToolName != "search_restaurants" ||
		body.ToolCalls[1].ToolName != "get_restaurant_evidence" {
		t.Fatalf("tool calls must keep invocation order, got %+v", body.ToolCalls)
	}
	if body.ToolCalls[0].LatencyMS != 12 {
		t.Fatalf("latency must survive the read: %+v", body.ToolCalls[0])
	}
	if svc.lastRun != "run-1" {
		t.Fatalf("service saw run id %q", svc.lastRun)
	}
}

// The node timeline rides along on the run detail: a replay that named the
// tools but not the steps would still leave "the turn was slow" unanswerable,
// because the slow steps are the ones that called a model and left no tool row.
func TestGetRunReturnsTheNodeTimelineInOrder(t *testing.T) {
	svc := &stubRunService{
		runs: []RunView{{RunID: "run-1", Status: "succeeded", StartedAt: startedAt()}},
		nodes: []RunNodeView{
			{NodeID: "n1", Node: "ingress", Seq: 1, Status: "ok", LatencyMS: 3},
			{NodeID: "n2", Node: "plan", Seq: 2, Status: "ok", LatencyMS: 900,
				Detail: json.RawMessage(`{"round":1}`)},
			{NodeID: "n3", Node: "answer", Seq: 3, Status: "error", LatencyMS: 40,
				ErrorCode: "provider_timeout"},
		},
	}
	h := runRouter(svc)

	w := ut.PerformRequest(h.Engine, http.MethodGet, "/v1/runs/run-1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var body struct {
		Nodes []RunNodeView `json:"nodes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", w.Body.String(), err)
	}
	if len(body.Nodes) != 3 {
		t.Fatalf("nodes = %d, want 3", len(body.Nodes))
	}
	if body.Nodes[0].Node != "ingress" || body.Nodes[2].Node != "answer" {
		t.Fatalf("nodes must keep execution order, got %+v", body.Nodes)
	}
	// The failing span is the reason the timeline exists: named and timed, so
	// "the turn failed" says where.
	if body.Nodes[2].Status != "error" || body.Nodes[2].ErrorCode != "provider_timeout" {
		t.Fatalf("failing span lost its diagnostic fields: %+v", body.Nodes[2])
	}
	if string(body.Nodes[1].Detail) == "" {
		t.Fatalf("detail must survive the read: %+v", body.Nodes[1])
	}
}

// The timeline has its own route because it is read on a different cadence
// from the replay: fetched repeatedly while a turn is diagnosed, without
// re-sending the run row and every tool call each time.
func TestListRunNodesReturnsOnlyTheTimeline(t *testing.T) {
	svc := &stubRunService{
		nodes: []RunNodeView{
			{NodeID: "n1", Node: "ingress", Seq: 1, Status: "ok"},
			{NodeID: "n2", Node: "finalize", Seq: 2, Status: "ok", LatencyMS: 2},
		},
	}
	h := runRouter(svc)

	w := ut.PerformRequest(h.Engine, http.MethodGet, "/v1/runs/run-7/nodes", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var body struct {
		RunID string        `json:"run_id"`
		Nodes []RunNodeView `json:"nodes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", w.Body.String(), err)
	}
	if body.RunID != "run-7" {
		t.Fatalf("run id = %q, want run-7", body.RunID)
	}
	if len(body.Nodes) != 2 || body.Nodes[1].Node != "finalize" {
		t.Fatalf("nodes = %+v", body.Nodes)
	}
	if svc.lastRun != "run-7" {
		t.Fatalf("service saw run id %q", svc.lastRun)
	}
}

// A run with no spans returns an empty array, not null.
func TestListRunNodesWithoutSpansReturnsAnEmptyArray(t *testing.T) {
	h := runRouter(&stubRunService{})
	w := ut.PerformRequest(h.Engine, http.MethodGet, "/v1/runs/run-1/nodes", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if got := string(body["nodes"]); got != "[]" {
		t.Fatalf("nodes = %s, want []", got)
	}
}

// A trace id is the identifier a client holds: it is echoed in the response and
// in every event, while the run id is internal. The trace route is what makes
// "any failure can be located from its trace id" true for that client.
func TestGetTraceResolvesTheRunAndItsTimeline(t *testing.T) {
	svc := &stubRunService{
		runs: []RunView{{
			RunID: "run-1", TraceID: "trace-abc", Status: "failed",
			StartedAt: startedAt(), ErrorCode: "provider_timeout",
		}},
		nodes: []RunNodeView{
			{NodeID: "n1", Node: "ingress", Seq: 1, Status: "ok"},
			{NodeID: "n2", Node: "plan", Seq: 2, Status: "error", ErrorCode: "provider_timeout"},
		},
	}
	h := runRouter(svc)

	w := ut.PerformRequest(h.Engine, http.MethodGet, "/v1/traces/trace-abc", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var body struct {
		RunID     string        `json:"run_id"`
		TraceID   string        `json:"trace_id"`
		ErrorCode string        `json:"error_code"`
		Nodes     []RunNodeView `json:"nodes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", w.Body.String(), err)
	}
	if body.RunID != "run-1" || body.TraceID != "trace-abc" {
		t.Fatalf("trace must resolve to the run: %+v", body)
	}
	if body.ErrorCode != "provider_timeout" {
		t.Fatalf("error code = %q", body.ErrorCode)
	}
	if len(body.Nodes) != 2 || body.Nodes[1].Node != "plan" {
		t.Fatalf("trace must carry the timeline, got %+v", body.Nodes)
	}
	if svc.lastTrace != "trace-abc" {
		t.Fatalf("service saw trace id %q", svc.lastTrace)
	}
}

// An unknown trace is a 404: a client debugging a failed request has to be able
// to tell "no such trace" from "a trace that recorded nothing".
func TestGetTraceReportsAnUnknownTrace(t *testing.T) {
	h := runRouter(&stubRunService{})
	w := ut.PerformRequest(h.Engine, http.MethodGet, "/v1/traces/trace-nope", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// A run with no tool calls returns an empty array, not null: a client that has
// to special-case null learns nothing from it, and "no tools" is a real answer.
func TestGetRunWithoutToolCallsReturnsAnEmptyArray(t *testing.T) {
	svc := &stubRunService{runs: []RunView{{RunID: "run-1", Status: "running"}}}
	h := runRouter(svc)

	w := ut.PerformRequest(h.Engine, http.MethodGet, "/v1/runs/run-1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if got := string(body["tool_calls"]); got != "[]" {
		t.Fatalf("tool_calls = %s, want []", got)
	}
}

// An unknown run is a 404 rather than an empty replay: a client that cannot tell
// a missing run from a silent one cannot use the endpoint to debug anything.
func TestGetRunReportsAnUnknownRun(t *testing.T) {
	svc := &stubRunService{err: errs.New(errs.CodeNotFound, "run not found")}
	h := runRouter(svc)

	w := ut.PerformRequest(h.Engine, http.MethodGet, "/v1/runs/nope", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", w.Code, w.Body.String())
	}
	if code := decodeError(t, w.Body.Bytes()).Error.Code; code != "not_found" {
		t.Fatalf("error code = %q, want not_found", code)
	}
}

// The thread's run list echoes the thread id and applies the paging defaults the
// message API established.
func TestListRunsAppliesThePagingDefaults(t *testing.T) {
	svc := &stubRunService{runs: []RunView{{RunID: "run-2", ThreadID: "th-1"}}}
	h := runRouter(svc)

	w := ut.PerformRequest(h.Engine, http.MethodGet, "/v1/conversations/th-1/runs", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if svc.lastThread != "th-1" {
		t.Fatalf("thread id = %q", svc.lastThread)
	}
	if svc.lastLimit != defaultRunPageSize {
		t.Fatalf("limit = %d, want %d", svc.lastLimit, defaultRunPageSize)
	}
	if svc.lastBefore != "" {
		t.Fatalf("before_id = %q, want empty", svc.lastBefore)
	}

	var body struct {
		Runs []RunView `json:"runs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Runs) != 1 || body.Runs[0].RunID != "run-2" {
		t.Fatalf("runs = %+v", body.Runs)
	}
}

// The limit is clamped rather than rejected, for the same reason the transcript
// clamps: a caller asking for too much is asking a fair question badly.
func TestListRunsClampsTheLimitAndPassesTheCursor(t *testing.T) {
	svc := &stubRunService{}
	h := runRouter(svc)

	ut.PerformRequest(h.Engine, http.MethodGet,
		"/v1/conversations/th-1/runs?limit=5000&before_id=run-9", nil)
	if svc.lastLimit != maxRunPageSize {
		t.Fatalf("limit = %d, want the cap %d", svc.lastLimit, maxRunPageSize)
	}
	if svc.lastBefore != "run-9" {
		t.Fatalf("before_id = %q, want run-9", svc.lastBefore)
	}
}

// A malformed limit is a client defect and says so, rather than silently
// becoming the default page size.
func TestListRunsRejectsANonNumericLimit(t *testing.T) {
	h := runRouter(&stubRunService{})

	w := ut.PerformRequest(h.Engine, http.MethodGet, "/v1/conversations/th-1/runs?limit=lots", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
	if code := decodeError(t, w.Body.Bytes()).Error.Code; code != "invalid_argument" {
		t.Fatalf("error code = %q, want invalid_argument", code)
	}
}

// The replay returns the digest the audit stored. It never widens it back into
// raw model arguments — the redaction happened where the real payload was
// visible, and this endpoint is not a second chance to leak it.
func TestGetRunReturnsTheRedactedArgumentsDigest(t *testing.T) {
	stored := json.RawMessage(`{"query":"italian","borough":"manhattan"}`)
	svc := &stubRunService{
		runs:      []RunView{{RunID: "run-1", Status: "succeeded"}},
		toolCalls: []ToolCallView{{CallID: "c1", ToolName: "search_restaurants", Arguments: stored}},
	}
	h := runRouter(svc)

	w := ut.PerformRequest(h.Engine, http.MethodGet, "/v1/runs/run-1", nil)
	var body struct {
		ToolCalls []struct {
			Arguments map[string]string `json:"arguments"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", body.ToolCalls)
	}
	if body.ToolCalls[0].Arguments["query"] != "italian" {
		t.Fatalf("arguments = %+v, want the stored digest", body.ToolCalls[0].Arguments)
	}
}

// With no run service the routes do not exist. Registering them and failing
// every call would advertise a capability the deployment does not have.
func TestRunRoutesAreAbsentWithoutAService(t *testing.T) {
	h := NewRouter(Config{Addr: ":0", Version: "test", Logger: quietLogger()})

	for _, path := range []string{"/v1/runs/run-1", "/v1/conversations/th-1/runs"} {
		w := ut.PerformRequest(h.Engine, http.MethodGet, path, nil)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", path, w.Code)
		}
	}
}

// The run routes must not shadow the transcript routes they sit beside: both
// hang off /v1/conversations/:id, and a registration order that collapsed them
// would be invisible until someone listed messages.
func TestRunRoutesCoexistWithTheTranscriptRoutes(t *testing.T) {
	h := NewRouter(Config{
		Addr:    ":0",
		Version: "test",
		Logger:  quietLogger(),
		Chat:    newFakeChatService(),
		Runs:    &stubRunService{runs: []RunView{{RunID: "run-1"}}},
	})

	// Both routes must reach a handler. Which status they return is the
	// handler's business; what matters here is that neither is NoRoute, which
	// answers 404 with the framework's not_found body.
	for _, path := range []string{
		"/v1/conversations/th-1/runs",
		"/v1/conversations/th-1/messages",
	} {
		w := ut.PerformRequest(h.Engine, http.MethodGet, path, nil)
		if w.Code == http.StatusNotFound {
			t.Fatalf("%s was not routed (body %s)", path, w.Body.String())
		}
	}
}
