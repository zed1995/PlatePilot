package contract

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/run"
	"github.com/zed/platepilot/shared/store"
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
}
