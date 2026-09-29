package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/run"
)

func TestRunStartAndFinish(t *testing.T) {
	repo := NewRunRepository()
	ctx := context.Background()
	agentRun := run.AgentRun{RunID: "run-1", TraceID: "trace-1", Status: run.StatusRunning, StartedAt: time.Now().UTC()}

	if err := repo.Start(ctx, agentRun); err != nil {
		t.Fatalf("start: %v", err)
	}
	agentRun.Status = run.StatusSucceeded
	if err := repo.Finish(ctx, agentRun); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

func TestRunFinishUnknownIsNotFound(t *testing.T) {
	repo := NewRunRepository()
	if err := repo.Finish(context.Background(), run.AgentRun{RunID: "missing"}); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("want errs.ErrNotFound, got %v", err)
	}
}

func TestRunStartRequiresRunID(t *testing.T) {
	repo := NewRunRepository()
	if err := repo.Start(context.Background(), run.AgentRun{}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("want invalid_argument, got %v", err)
	}
}

func TestRunRecordToolCall(t *testing.T) {
	repo := NewRunRepository()
	ctx := context.Background()
	if err := repo.Start(ctx, run.AgentRun{RunID: "run-1", Status: run.StatusRunning}); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordToolCall(ctx, run.ToolCallRecord{CallID: "c1", RunID: "run-1", ToolName: "search_restaurants", Status: "ok"}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := repo.RecordToolCall(ctx, run.ToolCallRecord{CallID: "c2", RunID: "missing"}); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("want errs.ErrNotFound for unknown run, got %v", err)
	}
}
