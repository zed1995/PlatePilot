package agent_test

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/run"
	"github.com/zed1995/platepilot/shared/idgen"

	"github.com/zed1995/platepilot/chat-service/internal/agent"
	"github.com/zed1995/platepilot/chat-service/internal/agent/audit"
	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
)

// capturingRunRepo is a test RunRepository that records every write and can
// be switched to fail-all mode.
type capturingRunRepo struct {
	mu       sync.Mutex
	starts   []run.AgentRun
	finishes []run.AgentRun
	calls    []run.ToolCallRecord
	nodes    []run.RunNode
	failAll  bool
}

func (r *capturingRunRepo) Start(_ context.Context, agentRun run.AgentRun) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts = append(r.starts, agentRun)
	if r.failAll {
		return errs.ErrInternal
	}
	return nil
}

func (r *capturingRunRepo) Finish(_ context.Context, agentRun run.AgentRun) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finishes = append(r.finishes, agentRun)
	if r.failAll {
		return errs.ErrInternal
	}
	return nil
}

func (r *capturingRunRepo) RecordToolCall(_ context.Context, call run.ToolCallRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
	if r.failAll {
		return errs.ErrInternal
	}
	return nil
}

// The read path satisfies the port without being exercised here: a fake that
// implemented only the writes would stop compiling the moment the interface
// gained a read method, which is exactly the signal these tests want.
func (r *capturingRunRepo) RecordNode(_ context.Context, node run.RunNode) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodes = append(r.nodes, node)
	if r.failAll {
		return errs.ErrInternal
	}
	return nil
}

func (r *capturingRunRepo) GetRun(_ context.Context, runID string) (run.AgentRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.finishes) - 1; i >= 0; i-- {
		if r.finishes[i].RunID == runID {
			return r.finishes[i], nil
		}
	}
	return run.AgentRun{}, errs.ErrNotFound
}

func (r *capturingRunRepo) GetRunByTrace(_ context.Context, traceID string) (run.AgentRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.finishes) - 1; i >= 0; i-- {
		if r.finishes[i].TraceID == traceID {
			return r.finishes[i], nil
		}
	}
	return run.AgentRun{}, errs.ErrNotFound
}

func (r *capturingRunRepo) ListRuns(_ context.Context, threadID string, limit int, _ string) ([]run.AgentRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []run.AgentRun
	for _, record := range r.finishes {
		if record.ThreadID != threadID {
			continue
		}
		out = append(out, record)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

func (r *capturingRunRepo) ListToolCalls(_ context.Context, runID string) ([]run.ToolCallRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []run.ToolCallRecord
	for _, call := range r.calls {
		if call.RunID == runID {
			out = append(out, call)
		}
	}
	return out, nil
}

func (r *capturingRunRepo) ListNodes(_ context.Context, runID string) ([]run.RunNode, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []run.RunNode
	for _, node := range r.nodes {
		if node.RunID == runID {
			out = append(out, node)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

func (r *capturingRunRepo) snapshotNodes() []run.RunNode {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]run.RunNode(nil), r.nodes...)
}

func (r *capturingRunRepo) snapshot() (starts, finishes []run.AgentRun, calls []run.ToolCallRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]run.AgentRun(nil), r.starts...),
		append([]run.AgentRun(nil), r.finishes...),
		append([]run.ToolCallRecord(nil), r.calls...)
}

func newAuditedRunner(t *testing.T, p *scriptedProvider, reg *toolreg.Registry, repo *capturingRunRepo) *agent.Runner {
	t.Helper()
	hooks := audit.New(repo, nil, audit.Options{ModelProvider: "test-provider", ModelName: "test-model"})
	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 3}, agent.Deps{
		Chat:        p,
		ToolCalling: p,
		Registry:    reg,
		Auditor:     hooks,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner
}

func TestRunnerAuditsSuccessfulToolTurn(t *testing.T) {
	reg := toolreg.New(time.Second)
	if err := reg.Register(echoEntry()); err != nil {
		t.Fatalf("register: %v", err)
	}
	provider := &scriptedProvider{
		supportTools: true,
		toolResps: []domainchat.ToolCallResponse{
			toolCallResponse("call-1", "echo_ping", `{"word":"hi"}`),
			assistantText("审计完成"),
		},
	}
	repo := &capturingRunRepo{}
	runner := newAuditedRunner(t, provider, reg, repo)

	result, events := drainPair(runner.Run(context.Background(), agent.TurnInput{
		ThreadID:  "th-audit",
		TraceID:   "trace-fixed",
		UserInput: "ping 一下",
	}))
	drain(events)
	if result == nil {
		t.Fatal("expected successful turn")
	}

	starts, finishes, calls := repo.snapshot()
	if len(starts) != 1 {
		t.Fatalf("starts = %d, want 1", len(starts))
	}
	if starts[0].Status != run.StatusRunning || starts[0].RunID != result.RunID {
		t.Fatalf("start row = %+v, result run id = %q", starts[0], result.RunID)
	}
	if starts[0].TraceID != "trace-fixed" {
		t.Fatalf("trace id = %q, want trace-fixed", starts[0].TraceID)
	}
	if result.TraceID != "trace-fixed" {
		t.Fatalf("result trace id = %q", result.TraceID)
	}
	if len(calls) != 1 {
		t.Fatalf("tool call records = %d, want 1", len(calls))
	}
	if calls[0].CallID != "call-1" || calls[0].RunID != result.RunID {
		t.Fatalf("tool call row = %+v", calls[0])
	}
	// echo_ping is not on the argument allowlist: arguments collapse.
	if string(calls[0].Arguments) != "{}" {
		t.Fatalf("unknown-tool arguments must collapse to {}, got %s", calls[0].Arguments)
	}
	if len(finishes) != 1 {
		t.Fatalf("finish rows = %d, want 1", len(finishes))
	}
	fin := finishes[0]
	if fin.Status != run.StatusSucceeded {
		t.Fatalf("finish status = %q", fin.Status)
	}
	if fin.ToolCallCount != 1 || fin.RetrievalCount != 0 {
		t.Fatalf("counters = calls %d retrieval %d", fin.ToolCallCount, fin.RetrievalCount)
	}
	if fin.FinishedAt == nil || fin.LatencyMS < 0 {
		t.Fatalf("timing missing: %+v", fin)
	}
}

func TestRunnerMintsTraceAndRunIDs(t *testing.T) {
	provider := &scriptedProvider{
		supportTools: true,
		completeResps: []domainchat.ChatResponse{{
			Message:      domainchat.ChatMessage{Role: domainchat.RoleAssistant, Content: "ok"},
			FinishReason: domainchat.FinishReasonStop,
		}},
	}
	repo := &capturingRunRepo{}
	runner := newAuditedRunner(t, provider, toolreg.New(0), repo)

	result, _ := drainPair(runner.Run(context.Background(), agent.TurnInput{
		ThreadID:  "th-id",
		UserInput: "hi",
	}))
	if result == nil {
		t.Fatal("expected result")
	}
	if result.RunID == "" || result.TraceID == "" || result.RunID == result.TraceID {
		t.Fatalf("expected distinct minted ids, got run=%q trace=%q", result.RunID, result.TraceID)
	}
	starts, _, _ := repo.snapshot()
	if len(starts) != 1 || starts[0].RunID != result.RunID || starts[0].TraceID != result.TraceID {
		t.Fatalf("start row ids mismatch: %+v vs result %+v", starts, result)
	}
}

func TestRunnerAuditsFailedTurn(t *testing.T) {
	// No queued responses: the first model call fails, so the turn errors
	// before finalize and the audit must record the failed row.
	provider := &scriptedProvider{supportTools: true}
	repo := &capturingRunRepo{}
	runner := newAuditedRunner(t, provider, toolreg.New(0), repo)

	result, eventsCh := runner.Run(context.Background(), agent.TurnInput{
		ThreadID:  "th-fail",
		UserInput: "注定失败",
	})
	events := drain(eventsCh)
	if result != nil {
		t.Fatal("expected nil result")
	}
	var runID string
	var sawError bool
	for _, ev := range events {
		if ev.Type == agent.EventError {
			sawError = true
			runID = ev.RunID
		}
	}
	if !sawError || runID == "" {
		t.Fatalf("error event must carry the run id, events = %+v", events)
	}
	_, finishes, _ := repo.snapshot()
	if len(finishes) != 1 {
		t.Fatalf("finish rows = %d, want 1", len(finishes))
	}
	fin := finishes[0]
	if fin.Status != run.StatusFailed || fin.RunID != runID {
		t.Fatalf("failed finish row = %+v, event run id %q", fin, runID)
	}
	if fin.ErrorCode != string(errs.CodeInternal) {
		t.Fatalf("error code = %q, want internal", fin.ErrorCode)
	}
	if fin.FinishedAt == nil {
		t.Fatal("failed row must carry finished_at")
	}
}

func TestRunnerAuditStoreFailureNeverBreaksTurn(t *testing.T) {
	provider := &scriptedProvider{
		supportTools: true,
		completeResps: []domainchat.ChatResponse{{
			Message:      domainchat.ChatMessage{Role: domainchat.RoleAssistant, Content: "即使审计挂了也要回答"},
			FinishReason: domainchat.FinishReasonStop,
		}},
	}
	repo := &capturingRunRepo{failAll: true}
	runner := newAuditedRunner(t, provider, toolreg.New(0), repo)

	result, eventsCh := runner.Run(context.Background(), agent.TurnInput{
		ThreadID:  "th-broken-audit",
		UserInput: "你好",
	})
	events := drain(eventsCh)
	if result == nil || result.Answer.Text != "即使审计挂了也要回答" {
		t.Fatalf("turn must succeed despite audit failures, result = %+v", result)
	}
	for _, ev := range events {
		if ev.Type == agent.EventError {
			t.Fatalf("audit failure must not surface as turn error: %+v", ev)
		}
	}
	starts, finishes, _ := repo.snapshot()
	if len(starts) != 1 || len(finishes) != 1 {
		t.Fatalf("writes still attempted: starts=%d finishes=%d", len(starts), len(finishes))
	}
}

func TestRunnerWithoutAuditorStillRuns(t *testing.T) {
	provider := &scriptedProvider{
		supportTools: true,
		completeResps: []domainchat.ChatResponse{{
			Message:      domainchat.ChatMessage{Role: domainchat.RoleAssistant, Content: idgen.NewUUID()},
			FinishReason: domainchat.FinishReasonStop,
		}},
	}
	runner := newTestRunner(t, provider, toolreg.New(0), 3)
	result, _ := drainPair(runner.Run(context.Background(), agent.TurnInput{
		ThreadID:  "th-no-audit",
		UserInput: "hi",
	}))
	if result == nil {
		t.Fatal("nil auditor must be a safe no-op")
	}
}

func TestRunnerRecordsANodeSpanPerGraphNode(t *testing.T) {
	reg := toolreg.New(time.Second)
	if err := reg.Register(echoEntry()); err != nil {
		t.Fatalf("register: %v", err)
	}
	provider := &scriptedProvider{
		supportTools: true,
		toolResps: []domainchat.ToolCallResponse{
			toolCallResponse("call-1", "echo_ping", `{"word":"hi"}`),
			assistantText("审计完成"),
		},
	}
	repo := &capturingRunRepo{}
	runner := newAuditedRunner(t, provider, reg, repo)

	result, events := drainPair(runner.Run(context.Background(), agent.TurnInput{
		ThreadID: "th-spans", TraceID: "trace-spans", UserInput: "ping 一下",
	}))
	drain(events)
	if result == nil {
		t.Fatal("expected successful turn")
	}

	nodes := repo.snapshotNodes()
	if len(nodes) == 0 {
		t.Fatal("a turn with an auditor must record node spans")
	}
	// The trace is only a trace while every span names the run it belongs to.
	for i, node := range nodes {
		if node.RunID != result.RunID {
			t.Fatalf("span %d run id = %q, want %q", i, node.RunID, result.RunID)
		}
		if node.TraceID != "trace-spans" {
			t.Fatalf("span %d trace id = %q", i, node.TraceID)
		}
		if node.Seq != i+1 {
			t.Fatalf("span %d seq = %d, want %d: spans must be numbered in execution order",
				i, node.Seq, i+1)
		}
		if node.LatencyMS < 0 {
			t.Fatalf("span %d latency = %d", i, node.LatencyMS)
		}
		if node.Status != run.NodeOK {
			t.Fatalf("span %d (%s) status = %q: %s", i, node.Node, node.Status, node.ErrorCode)
		}
	}
	// A turn that ran a tool visits plan twice, once per round, and the trace
	// has to show both: "the model was called twice" is exactly the fact a
	// latency report hides.
	var names []string
	for _, node := range nodes {
		names = append(names, node.Node)
	}
	want := []string{"ingress", "plan", "tools", "plan", "answer", "finalize"}
	if len(names) != len(want) {
		t.Fatalf("span names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("span %d = %q, want %q (full: %v)", i, names[i], want[i], names)
		}
	}
}

func TestRunnerRecordsTheSpanOfTheNodeThatFailed(t *testing.T) {
	// No queued responses: plan's model call fails. The turn never reaches
	// finalize, and the failing span is the reason a span exists at all —
	// "the turn failed" has to say which node it failed in.
	provider := &scriptedProvider{supportTools: true}
	repo := &capturingRunRepo{}
	runner := newAuditedRunner(t, provider, toolreg.New(0), repo)

	result, eventsCh := runner.Run(context.Background(), agent.TurnInput{
		ThreadID: "th-span-fail", UserInput: "注定失败",
	})
	drain(eventsCh)
	if result != nil {
		t.Fatal("expected nil result")
	}

	nodes := repo.snapshotNodes()
	if len(nodes) == 0 {
		t.Fatal("a failed turn must still record the spans it completed")
	}
	last := nodes[len(nodes)-1]
	if last.Node != "plan" {
		t.Fatalf("last span = %q, want plan (spans: %v)", last.Node, nodes)
	}
	if last.Status != run.NodeError {
		t.Fatalf("failing span status = %q, want error", last.Status)
	}
	if last.ErrorCode == "" {
		t.Fatal("failing span must carry the error code")
	}
	// Ingress ran before the failure and succeeded; the trace ends mid-turn,
	// which is what distinguishes "failed in plan" from "failed to start".
	if nodes[0].Node != "ingress" || nodes[0].Status != run.NodeOK {
		t.Fatalf("first span = %q/%q, want ingress/ok", nodes[0].Node, nodes[0].Status)
	}
}

// drainPair is a terse helper for tests that ignore the event channel.
func drainPair(result *agent.TurnResult, events <-chan agent.Event) (*agent.TurnResult, <-chan agent.Event) {
	go func() {
		for range events {
		}
	}()
	return result, events
}
