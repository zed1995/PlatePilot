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
		if err := runs.Finish(ctx, agentRun); err != nil {
			t.Fatalf("Finish failed status: %v", err)
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
		// Two tool calls on the middle run, written out of chronological order
		// so the read has to sort them.
		for _, item := range []struct {
			callID    string
			createdAt time.Time
		}{
			{"read-call-b", startedAt.Add(time.Minute).Add(20 * time.Millisecond)},
			{"read-call-a", startedAt.Add(time.Minute).Add(10 * time.Millisecond)},
		} {
			if err := runs.RecordToolCall(ctx, run.ToolCallRecord{
				CallID:        item.callID,
				RunID:         "run-read-1",
				ToolName:      "search_restaurants",
				Arguments:     json.RawMessage(`{"query":"ramen"}`),
				ResultSummary: "3 candidates",
				Status:        "ok",
				LatencyMS:     7,
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
			t.Fatalf("tool calls out of order: %s, %s", calls[0].CallID, calls[1].CallID)
		}
		if calls[0].ToolName != "search_restaurants" || calls[0].Status != "ok" ||
			calls[0].LatencyMS != 7 || calls[0].ResultSummary != "3 candidates" {
			t.Fatalf("tool call round trip lost fields: %+v", calls[0])
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
}
