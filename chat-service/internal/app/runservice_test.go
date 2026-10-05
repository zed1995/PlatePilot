package app

import (
	"context"
	"testing"
	"time"

	"github.com/zed1995/platepilot/shared/domain/conversation"
	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/run"
	"github.com/zed1995/platepilot/shared/testkit"
)

func seedThread(t *testing.T, repo interface {
	Upsert(context.Context, conversation.Conversation) error
}, threadID string) {
	t.Helper()
	now := time.Date(2021, 9, 1, 12, 0, 0, 0, time.UTC)
	if err := repo.Upsert(context.Background(), conversation.Conversation{
		ThreadID:     threadID,
		CurrentState: conversation.StateIdle,
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("seed thread: %v", err)
	}
}

// A mistyped thread id must not come back as a thread that recorded nothing.
// The run store cannot tell the two apart, so the adapter asks the conversation
// store before it answers.
func TestListRunsRejectsAnUnknownThread(t *testing.T) {
	svc := newRunService(testkit.NewConversationRepository(), testkit.NewRunRepository())

	_, err := svc.ListRuns(context.Background(), "no-such-thread", 20, "")
	if errs.CodeOf(err) != errs.CodeNotFound {
		t.Fatalf("err = %v, want not_found", err)
	}
}

// A known thread with no runs is an empty page, which is a real answer: the
// thread exists and nothing happened on it yet.
func TestListRunsReturnsAnEmptyPageForAKnownThread(t *testing.T) {
	conversations := testkit.NewConversationRepository()
	seedThread(t, conversations, "th-1")
	svc := newRunService(conversations, testkit.NewRunRepository())

	page, err := svc.ListRuns(context.Background(), "th-1", 20, "")
	if err != nil {
		t.Fatalf("err = %v, want an empty page", err)
	}
	if len(page.Runs) != 0 {
		t.Fatalf("runs = %+v, want none", page.Runs)
	}
}

// The replay is assembled from the two rows the turn actually wrote: the run and
// its tool calls. A projection that dropped a field would be a replay that
// cannot explain the turn it exists to explain.
func TestGetRunAssemblesTheRunAndItsToolChain(t *testing.T) {
	conversations := testkit.NewConversationRepository()
	seedThread(t, conversations, "th-1")
	runs := testkit.NewRunRepository()

	started := time.Date(2021, 9, 1, 12, 0, 0, 0, time.UTC)
	finished := started.Add(1500 * time.Millisecond)
	if err := runs.Start(context.Background(), run.AgentRun{
		TraceID: "tr-1", ThreadID: "th-1", RunID: "run-1",
		Status: run.StatusRunning, ModelName: "test-model", StartedAt: started,
	}); err != nil {
		t.Fatalf("start run: %v", err)
	}
	if err := runs.Finish(context.Background(), run.AgentRun{
		TraceID: "tr-1", ThreadID: "th-1", RunID: "run-1",
		Status: run.StatusSucceeded, ModelName: "test-model",
		StartedAt: started, FinishedAt: &finished,
		LatencyMS: 1500, ToolCallCount: 2,
	}); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	for i, name := range []string{"search_restaurants", "get_restaurant_evidence"} {
		if err := runs.RecordToolCall(context.Background(), run.ToolCallRecord{
			CallID: name, RunID: "run-1", ToolName: name,
			Status: "ok", Seq: i + 1, LatencyMS: int64(10 * (i + 1)), CreatedAt: started,
		}); err != nil {
			t.Fatalf("record tool call: %v", err)
		}
	}

	detail, err := newRunService(conversations, runs).GetRun(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if detail.Run.Status != string(run.StatusSucceeded) {
		t.Fatalf("status = %q, want succeeded", detail.Run.Status)
	}
	if detail.Run.ModelName != "test-model" {
		t.Fatalf("model name = %q", detail.Run.ModelName)
	}
	if detail.Run.LatencyMS != 1500 {
		t.Fatalf("latency = %d", detail.Run.LatencyMS)
	}
	if detail.Run.FinishedAt == nil {
		t.Fatal("finished_at must survive the read")
	}
	if len(detail.ToolCalls) != 2 {
		t.Fatalf("tool calls = %+v, want 2", detail.ToolCalls)
	}
}

// An unknown run id is not_found rather than an empty replay.
func TestGetRunRejectsAnUnknownRun(t *testing.T) {
	svc := newRunService(testkit.NewConversationRepository(), testkit.NewRunRepository())

	_, err := svc.GetRun(context.Background(), "no-such-run")
	if errs.CodeOf(err) != errs.CodeNotFound {
		t.Fatalf("err = %v, want not_found", err)
	}
}
