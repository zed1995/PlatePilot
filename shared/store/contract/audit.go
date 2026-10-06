package contract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/run"
	"github.com/zed1995/platepilot/shared/store"
)

// runRunRepositoryContract pins the audit lifecycle both adapters must share:
// running runs transition to a terminal status, tool calls require their run,
// and a repeated Start is idempotent.
func runRunRepositoryContract(t *testing.T, runs store.RunRepository) {
	ctx := context.Background()
	startedAt := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)

	t.Run("start_finish_succeeded", func(t *testing.T) {
		agentRun := run.AgentRun{
			TraceID:       "trace-success",
			ThreadID:      "thread-a",
			RunID:         "run-success",
			Status:        run.StatusRunning,
			ModelProvider: "openai_compatible",
			ModelName:     "test-model",
			StartedAt:     startedAt,
		}
		if err := runs.Start(ctx, agentRun); err != nil {
			t.Fatalf("Start: %v", err)
		}
		finishedAt := startedAt.Add(250 * time.Millisecond)
		agentRun.Status = run.StatusSucceeded
		agentRun.FinishedAt = &finishedAt
		agentRun.LatencyMS = 250
		agentRun.TokenInput = 100
		agentRun.TokenOutput = 42
		agentRun.RetrievalCount = 2
		agentRun.ToolCallCount = 2
		if err := runs.Finish(ctx, agentRun); err != nil {
			t.Fatalf("Finish: %v", err)
		}
	})

	t.Run("running_to_failed", func(t *testing.T) {
		agentRun := run.AgentRun{
			TraceID:   "trace-failed",
			ThreadID:  "thread-a",
			RunID:     "run-failed",
			Status:    run.StatusRunning,
			StartedAt: startedAt,
		}
		if err := runs.Start(ctx, agentRun); err != nil {
			t.Fatalf("Start: %v", err)
		}
		agentRun.Status = run.StatusFailed
		agentRun.ErrorCode = "provider_timeout"
		agentRun.ErrorMessage = "upstream returned 504 for model test-model"
		if err := runs.Finish(ctx, agentRun); err != nil {
			t.Fatalf("Finish failed status: %v", err)
		}
		// The code classifies the failure; the message is what a reader acts on.
		// Both have to survive the round trip, or a failed run is again only
		// "something went wrong".
		got, err := runs.GetRun(ctx, "run-failed")
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if got.ErrorCode != "provider_timeout" ||
			got.ErrorMessage != "upstream returned 504 for model test-model" {
			t.Fatalf("failure diagnostics did not round trip: %+v", got)
		}
	})

	t.Run("start_is_idempotent", func(t *testing.T) {
		agentRun := run.AgentRun{
			TraceID: "trace-retry", ThreadID: "thread-b", RunID: "run-retry",
			Status: run.StatusRunning, StartedAt: startedAt,
		}
		if err := runs.Start(ctx, agentRun); err != nil {
			t.Fatalf("first Start: %v", err)
		}
		// A retried Start after a lost acknowledgement must not error and must
		// not block the subsequent Finish.
		if err := runs.Start(ctx, agentRun); err != nil {
			t.Fatalf("retried Start: %v", err)
		}
		agentRun.Status = run.StatusSucceeded
		if err := runs.Finish(ctx, agentRun); err != nil {
			t.Fatalf("Finish after retried Start: %v", err)
		}
	})

	t.Run("finish_unknown_run_is_not_found", func(t *testing.T) {
		err := runs.Finish(ctx, run.AgentRun{RunID: "missing", Status: run.StatusFailed})
		if !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("Finish missing run: want errs.ErrNotFound, got %v", err)
		}
	})

	t.Run("start_requires_run_id", func(t *testing.T) {
		if err := runs.Start(ctx, run.AgentRun{Status: run.StatusRunning}); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("Start without run_id: want invalid_argument, got %v", err)
		}
	})

	t.Run("tool_call_requires_run", func(t *testing.T) {
		if err := runs.Start(ctx, run.AgentRun{
			TraceID: "trace-tools", ThreadID: "thread-c", RunID: "run-tools",
			Status: run.StatusRunning, StartedAt: startedAt,
		}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		record := run.ToolCallRecord{
			CallID:    "call-1",
			RunID:     "run-tools",
			ToolName:  "search_restaurants",
			Arguments: json.RawMessage(`{"query":"italian"}`),
			Status:    "ok",
			Seq:       1,
			LatencyMS: 12,
			CreatedAt: startedAt.Add(10 * time.Millisecond),
		}
		if err := runs.RecordToolCall(ctx, record); err != nil {
			t.Fatalf("RecordToolCall: %v", err)
		}
		// A tool call for an unknown run violates the run foreign key.
		missing := record
		missing.CallID = "call-orphan"
		missing.RunID = "run-missing"
		if err := runs.RecordToolCall(ctx, missing); !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("RecordToolCall for missing run: want errs.ErrNotFound, got %v", err)
		}
		// An empty run id is a request defect, not a missing row.
		empty := record
		empty.CallID = "call-empty"
		empty.RunID = "  "
		if err := runs.RecordToolCall(ctx, empty); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("RecordToolCall without run_id: want invalid_argument, got %v", err)
		}
		// A position is required for the same reason: the read path orders by
		// it, and a row without one would sort somewhere arbitrary. PostgreSQL
		// refuses it through the table's check constraint, so the in-memory
		// adapter has to refuse it too or the two are not interchangeable.
		unpositioned := record
		unpositioned.CallID = "call-unpositioned"
		unpositioned.Seq = 0
		if err := runs.RecordToolCall(ctx, unpositioned); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("RecordToolCall without seq: want invalid_argument, got %v", err)
		}
	})

	// The start column is required, so a caller that did not observe the start
	// cannot leave it unset. It can still say when the call finished and how
	// long it took, and the two adapters have to derive the same answer from
	// that — a store that stored a zero time instead would return a row no
	// other adapter could produce.
	t.Run("an_unobserved_start_is_derived_from_the_end", func(t *testing.T) {
		if err := runs.Start(ctx, run.AgentRun{
			TraceID: "trace-derived", ThreadID: "thread-derived", RunID: "run-derived",
			Status: run.StatusRunning, StartedAt: startedAt,
		}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		finishedAt := startedAt.Add(time.Minute)
		if err := runs.RecordToolCall(ctx, run.ToolCallRecord{
			CallID:    "derived-call",
			RunID:     "run-derived",
			ToolName:  "search_restaurants",
			Status:    "ok",
			Seq:       1,
			LatencyMS: 40,
			CreatedAt: finishedAt,
		}); err != nil {
			t.Fatalf("RecordToolCall: %v", err)
		}
		got, err := runs.ListToolCalls(ctx, "run-derived")
		if err != nil {
			t.Fatalf("ListToolCalls: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("ListToolCalls returned %d rows, want 1", len(got))
		}
		want := finishedAt.Add(-40 * time.Millisecond)
		if !got[0].StartedAt.Equal(want) {
			t.Fatalf("started_at = %s, want the end less the duration (%s)",
				got[0].StartedAt, want)
		}
	})

	t.Run("read_path_get_list_and_tool_calls", func(t *testing.T) {
		const threadID = "thread-read"
		// Three runs at distinct instants so "newest first" is deterministic
		// rather than a tie the store could break either way.
		for i := 0; i < 3; i++ {
			started := startedAt.Add(time.Duration(i) * time.Minute)
			agentRun := run.AgentRun{
				TraceID:   fmt.Sprintf("trace-read-%d", i),
				ThreadID:  threadID,
				RunID:     fmt.Sprintf("run-read-%d", i),
				Status:    run.StatusRunning,
				StartedAt: started,
			}
			if err := runs.Start(ctx, agentRun); err != nil {
				t.Fatalf("Start: %v", err)
			}
			finished := started.Add(time.Second)
			agentRun.Status = run.StatusSucceeded
			agentRun.FinishedAt = &finished
			agentRun.LatencyMS = 1000
			agentRun.TokenInput = 10
			agentRun.TokenOutput = 5
			if err := runs.Finish(ctx, agentRun); err != nil {
				t.Fatalf("Finish: %v", err)
			}
		}
		// Two tool calls from one round, written in the order they finished
		// rather than the order they were asked for: read-call-a was second and
		// took longer than read-call-b, so it lands second. That is what a
		// round of concurrent reads produces, and it is the case the read path
		// has to undo — a store that ordered by insertion would hand the caller
		// the tool chain backwards.
		//
		// The two are given the same started_at on purpose. It is what the
		// runtime actually produces: the calls of a round are launched
		// microseconds apart and the column is a wall clock stored at
		// microsecond resolution, so the readings tie far more often than not.
		// A fixture that spaced them out would let a store that ordered by
		// start time pass here and be wrong in production, which is precisely
		// how the timestamp ordering this replaces was retired.
		roundStart := startedAt.Add(time.Minute)
		for _, item := range []struct {
			callID    string
			seq       int
			startedAt time.Time
			latencyMS int64
			createdAt time.Time
		}{
			{
				callID:    "read-call-b",
				seq:       2,
				startedAt: roundStart,
				latencyMS: 5,
				createdAt: roundStart.Add(5 * time.Millisecond),
			},
			{
				callID:    "read-call-a",
				seq:       1,
				startedAt: roundStart,
				latencyMS: 90,
				createdAt: roundStart.Add(90 * time.Millisecond),
			},
		} {
			if err := runs.RecordToolCall(ctx, run.ToolCallRecord{
				CallID:        item.callID,
				RunID:         "run-read-1",
				ToolName:      "search_restaurants",
				Arguments:     json.RawMessage(`{"query":"ramen"}`),
				ResultSummary: "3 candidates",
				Status:        "ok",
				Seq:           item.seq,
				LatencyMS:     item.latencyMS,
				StartedAt:     item.startedAt,
				CreatedAt:     item.createdAt,
			}); err != nil {
				t.Fatalf("RecordToolCall: %v", err)
			}
		}

		got, err := runs.GetRun(ctx, "run-read-1")
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if got.Status != run.StatusSucceeded || got.LatencyMS != 1000 ||
			got.TokenInput != 10 || got.TokenOutput != 5 || got.FinishedAt == nil {
			t.Fatalf("run round trip lost fields: %+v", got)
		}
		if _, err := runs.GetRun(ctx, "run-missing"); !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("GetRun missing: want ErrNotFound, got %v", err)
		}

		// A trace id is what a client actually has, so it has to resolve to
		// the same run the run id does.
		byTrace, err := runs.GetRunByTrace(ctx, "trace-read-1")
		if err != nil {
			t.Fatalf("GetRunByTrace: %v", err)
		}
		if byTrace.RunID != "run-read-1" || byTrace.ThreadID != threadID {
			t.Fatalf("GetRunByTrace = %+v, want run-read-1 on %q", byTrace, threadID)
		}
		if _, err := runs.GetRunByTrace(ctx, "trace-nope"); !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("GetRunByTrace missing: want ErrNotFound, got %v", err)
		}
		if _, err := runs.GetRunByTrace(ctx, "  "); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("GetRunByTrace without trace_id: want invalid_argument, got %v", err)
		}

		// Newest first, bounded by limit.
		page, err := runs.ListRuns(ctx, threadID, 2, "")
		if err != nil {
			t.Fatalf("ListRuns: %v", err)
		}
		if len(page) != 2 {
			t.Fatalf("ListRuns returned %d, want 2", len(page))
		}
		if page[0].RunID != "run-read-2" || page[1].RunID != "run-read-1" {
			t.Fatalf("ListRuns not newest first: %s, %s", page[0].RunID, page[1].RunID)
		}

		// The cursor is exclusive and pages strictly older than the named run.
		older, err := runs.ListRuns(ctx, threadID, 10, "run-read-1")
		if err != nil {
			t.Fatalf("ListRuns before: %v", err)
		}
		if len(older) != 1 || older[0].RunID != "run-read-0" {
			t.Fatalf("ListRuns before cursor = %+v, want only run-read-0", older)
		}
		// An unknown cursor pages from nothing rather than from the head.
		unknown, err := runs.ListRuns(ctx, threadID, 10, "run-nope")
		if err != nil {
			t.Fatalf("ListRuns unknown cursor: %v", err)
		}
		if len(unknown) != 0 {
			t.Fatalf("unknown cursor returned %d runs, want none", len(unknown))
		}

		// Thread isolation.
		isolated, err := runs.ListRuns(ctx, "thread-nobody", 10, "")
		if err != nil {
			t.Fatalf("ListRuns other thread: %v", err)
		}
		if len(isolated) != 0 {
			t.Fatalf("cross-thread run leakage: %+v", isolated)
		}

		calls, err := runs.ListToolCalls(ctx, "run-read-1")
		if err != nil {
			t.Fatalf("ListToolCalls: %v", err)
		}
		if len(calls) != 2 {
			t.Fatalf("ListToolCalls returned %d, want 2", len(calls))
		}
		if calls[0].CallID != "read-call-a" || calls[1].CallID != "read-call-b" {
			t.Fatalf("tool calls not in invocation order: %s, %s",
				calls[0].CallID, calls[1].CallID)
		}
		if calls[0].Seq != 1 || calls[1].Seq != 2 {
			t.Fatalf("seq did not round trip: %d, %d", calls[0].Seq, calls[1].Seq)
		}
		if calls[0].ToolName != "search_restaurants" || calls[0].Status != "ok" ||
			calls[0].LatencyMS != 90 || calls[0].ResultSummary != "3 candidates" {
			t.Fatalf("tool call round trip lost fields: %+v", calls[0])
		}
		// The order above is seq's doing and nothing else's, and this is what
		// says so. Both rows began at the same instant, so a store that ordered
		// by started_at would have to fall back on a tiebreak to decide, and a
		// store that ordered by insertion would put read-call-b first. Pinning
		// the tie is also what stops the fixture from drifting into one where a
		// start-time sort happens to be right.
		if !calls[0].StartedAt.Equal(calls[1].StartedAt) {
			t.Fatalf("fixture no longer ties on start time (%s vs %s): "+
				"the case that justifies ordering by seq is gone",
				calls[0].StartedAt, calls[1].StartedAt)
		}
		if !calls[1].CreatedAt.Before(calls[0].CreatedAt) {
			t.Fatalf("the first call finished at %s, not before the second at %s: "+
				"insertion order no longer differs from invocation order, "+
				"so the read order proves nothing",
				calls[1].CreatedAt, calls[0].CreatedAt)
		}
		if !calls[0].StartedAt.Equal(roundStart) {
			t.Fatalf("started_at = %s, want %s", calls[0].StartedAt, roundStart)
		}
		if !calls[0].CreatedAt.Equal(roundStart.Add(90 * time.Millisecond)) {
			t.Fatalf("created_at = %s, want the completion instant", calls[0].CreatedAt)
		}
		// Compared as parsed JSON: a jsonb column does not preserve the exact
		// spelling (key order, spaces), and the contract is about the value
		// surviving, not about byte-identical formatting.
		var args map[string]any
		if err := json.Unmarshal(calls[0].Arguments, &args); err != nil {
			t.Fatalf("arguments are not valid JSON (%s): %v", calls[0].Arguments, err)
		}
		if args["query"] != "ramen" {
			t.Fatalf("arguments round trip = %s, want query=ramen", calls[0].Arguments)
		}

		// A run that called no tools, and an unknown run, are both "no rows":
		// the caller that needs to tell them apart reads the run first.
		none, err := runs.ListToolCalls(ctx, "run-read-0")
		if err != nil {
			t.Fatalf("ListToolCalls (no calls): %v", err)
		}
		if len(none) != 0 {
			t.Fatalf("run with no tool calls returned %d rows", len(none))
		}
		unknownCalls, err := runs.ListToolCalls(ctx, "run-nope")
		if err != nil {
			t.Fatalf("ListToolCalls (unknown run): %v", err)
		}
		if len(unknownCalls) != 0 {
			t.Fatalf("unknown run returned tool calls: %+v", unknownCalls)
		}
	})

	// Deleting a thread's runs is the cleanup half of deleting a conversation.
	// Two things have to hold: the runs go along with their tool calls and
	// spans, and no other thread's audit is touched — a sweep keyed on the
	// wrong column would quietly erase the history of every conversation the
	// user ever had, and nothing downstream would notice until someone needed
	// to explain a past answer.
	t.Run("delete_by_thread_removes_only_that_threads_runs", func(t *testing.T) {
		seed := func(threadID, runID string) {
			if err := runs.Start(ctx, run.AgentRun{
				TraceID: "trace-" + runID, ThreadID: threadID, RunID: runID,
				Status: run.StatusRunning, StartedAt: startedAt,
			}); err != nil {
				t.Fatalf("Start %s: %v", runID, err)
			}
			if err := runs.RecordToolCall(ctx, run.ToolCallRecord{
				CallID: "call-" + runID, RunID: runID, ToolName: "search_restaurants",
				Status: "ok", Seq: 1, LatencyMS: 5,
				StartedAt: startedAt, CreatedAt: startedAt.Add(5 * time.Millisecond),
			}); err != nil {
				t.Fatalf("RecordToolCall %s: %v", runID, err)
			}
			if err := runs.RecordNode(ctx, run.RunNode{
				NodeID: "node-" + runID, RunID: runID, TraceID: "trace-" + runID,
				Node: "plan", Seq: 1, Status: run.NodeOK, StartedAt: startedAt,
			}); err != nil {
				t.Fatalf("RecordNode %s: %v", runID, err)
			}
		}
		seed("thread-del", "run-del-1")
		seed("thread-del", "run-del-2")
		seed("thread-del-keep", "run-keep")

		// A thread with no runs is the common case in a fresh deployment, and
		// it must not be reported as a failure: there was nothing to clean.
		if err := runs.DeleteByThread(ctx, "thread-nothing-recorded"); err != nil {
			t.Fatalf("DeleteByThread on a thread with no runs: %v", err)
		}

		if err := runs.DeleteByThread(ctx, "thread-del"); err != nil {
			t.Fatalf("DeleteByThread: %v", err)
		}
		gone, err := runs.ListRuns(ctx, "thread-del", 0, "")
		if err != nil {
			t.Fatalf("ListRuns after delete: %v", err)
		}
		if len(gone) != 0 {
			t.Fatalf("runs outlived their thread: %+v", gone)
		}
		for _, runID := range []string{"run-del-1", "run-del-2"} {
			if _, err := runs.GetRun(ctx, runID); !errors.Is(err, errs.ErrNotFound) {
				t.Fatalf("GetRun(%s) after delete: want ErrNotFound, got %v", runID, err)
			}
			calls, err := runs.ListToolCalls(ctx, runID)
			if err != nil {
				t.Fatalf("ListToolCalls(%s): %v", runID, err)
			}
			if len(calls) != 0 {
				t.Fatalf("tool calls of %s outlived the run: %+v", runID, calls)
			}
			nodes, err := runs.ListNodes(ctx, runID)
			if err != nil {
				t.Fatalf("ListNodes(%s): %v", runID, err)
			}
			if len(nodes) != 0 {
				t.Fatalf("node spans of %s outlived the run: %+v", runID, nodes)
			}
		}
		// The trace lookup is the one read that is not thread-scoped, so it is
		// where a half-deleted run would still be found.
		if _, err := runs.GetRunByTrace(ctx, "trace-run-del-1"); !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("deleted run still resolves by trace: %v", err)
		}

		kept, err := runs.ListRuns(ctx, "thread-del-keep", 0, "")
		if err != nil {
			t.Fatalf("ListRuns (other thread): %v", err)
		}
		if len(kept) != 1 || kept[0].RunID != "run-keep" {
			t.Fatalf("another thread's audit was swept up: %+v", kept)
		}
		if _, err := runs.ListToolCalls(ctx, "run-keep"); err != nil {
			t.Fatalf("ListToolCalls (other thread): %v", err)
		}

		if err := runs.DeleteByThread(ctx, "  "); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("DeleteByThread without a thread id: want invalid_argument, got %v", err)
		}
	})

	t.Run("node_spans_order_and_round_trip", func(t *testing.T) {
		if err := runs.Start(ctx, run.AgentRun{
			TraceID: "trace-nodes", ThreadID: "thread-nodes", RunID: "run-nodes",
			Status: run.StatusRunning, StartedAt: startedAt,
		}); err != nil {
			t.Fatalf("Start: %v", err)
		}
		// Written out of order on purpose. Sorted by seq on the way out, they
		// come back in execution order; a store that returned them in
		// insertion order would look correct here and be wrong for any run
		// whose writes were interleaved.
		for _, node := range []run.RunNode{
			{
				NodeID: "node-2", RunID: "run-nodes", TraceID: "trace-nodes",
				Node: "plan", Seq: 2, Status: run.NodeOK,
				StartedAt: startedAt.Add(20 * time.Millisecond), LatencyMS: 30,
				Detail: json.RawMessage(`{"round":1,"pending_tool_calls":true}`),
			},
			{
				NodeID: "node-1", RunID: "run-nodes", TraceID: "trace-nodes",
				Node: "ingress", Seq: 1, Status: run.NodeOK,
				StartedAt: startedAt.Add(5 * time.Millisecond), LatencyMS: 15,
				Detail: json.RawMessage(`{"intent":"restaurant_qa"}`),
			},
			{
				NodeID: "node-3", RunID: "run-nodes", TraceID: "trace-nodes",
				Node: "answer", Seq: 3, Status: run.NodeError,
				StartedAt: startedAt.Add(50 * time.Millisecond), LatencyMS: 400,
				ErrorCode: "provider_timeout",
			},
		} {
			if err := runs.RecordNode(ctx, node); err != nil {
				t.Fatalf("RecordNode(%s): %v", node.NodeID, err)
			}
		}

		got, err := runs.ListNodes(ctx, "run-nodes")
		if err != nil {
			t.Fatalf("ListNodes: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("ListNodes returned %d spans, want 3", len(got))
		}
		wantOrder := []string{"ingress", "plan", "answer"}
		for i, node := range got {
			if node.Node != wantOrder[i] {
				t.Fatalf("span %d = %q, want %q (order %v)", i, node.Node, wantOrder[i], got)
			}
			if node.Seq != i+1 {
				t.Fatalf("span %d seq = %d, want %d", i, node.Seq, i+1)
			}
		}
		if got[0].TraceID != "trace-nodes" || got[0].LatencyMS != 15 ||
			got[0].Status != run.NodeOK {
			t.Fatalf("span round trip lost fields: %+v", got[0])
		}
		// Compared as parsed JSON: a jsonb column does not preserve the exact
		// spelling of the stored document.
		var detail map[string]any
		if err := json.Unmarshal(got[0].Detail, &detail); err != nil {
			t.Fatalf("detail is not valid JSON (%s): %v", got[0].Detail, err)
		}
		if detail["intent"] != "restaurant_qa" {
			t.Fatalf("detail round trip = %s, want intent=restaurant_qa", got[0].Detail)
		}
		// The failing span is the reason the table exists: named, timed, and
		// carrying the code, so "the turn failed" says where.
		if got[2].Status != run.NodeError || got[2].ErrorCode != "provider_timeout" ||
			got[2].LatencyMS != 400 {
			t.Fatalf("error span lost its diagnostic fields: %+v", got[2])
		}

		// A run with no spans, and an unknown run, are both "no rows".
		none, err := runs.ListNodes(ctx, "run-read-0")
		if err != nil {
			t.Fatalf("ListNodes (no spans): %v", err)
		}
		if len(none) != 0 {
			t.Fatalf("run with no spans returned %d rows", len(none))
		}
		unknown, err := runs.ListNodes(ctx, "run-nope")
		if err != nil {
			t.Fatalf("ListNodes (unknown run): %v", err)
		}
		if len(unknown) != 0 {
			t.Fatalf("unknown run returned spans: %+v", unknown)
		}

		// A span without a run violates the run foreign key.
		if err := runs.RecordNode(ctx, run.RunNode{
			NodeID: "node-orphan", RunID: "run-missing", Node: "plan", Seq: 1,
			Status: run.NodeOK, StartedAt: startedAt,
		}); !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("RecordNode for missing run: want ErrNotFound, got %v", err)
		}
		// An empty run id is a request defect, not a missing row.
		if err := runs.RecordNode(ctx, run.RunNode{
			NodeID: "node-empty", RunID: "  ", Node: "plan", Seq: 1,
			Status: run.NodeOK, StartedAt: startedAt,
		}); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("RecordNode without run_id: want invalid_argument, got %v", err)
		}
		// A span without a position would sort somewhere arbitrary in the very
		// read this table exists for. PostgreSQL refuses it through the check
		// constraint, so the in-memory adapter has to refuse it too.
		if err := runs.RecordNode(ctx, run.RunNode{
			NodeID: "node-unpositioned", RunID: "run-nodes", TraceID: "trace-nodes",
			Node: "plan", Seq: 0, Status: run.NodeOK, StartedAt: startedAt,
		}); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("RecordNode without seq: want invalid_argument, got %v", err)
		}
	})
}
