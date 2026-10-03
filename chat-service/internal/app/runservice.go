package app

import (
	"context"

	"github.com/zed1995/platepilot/chat-service/internal/httpapi"
	"github.com/zed1995/platepilot/shared/domain/run"
	"github.com/zed1995/platepilot/shared/store"
)

// runService adapts the run audit store onto the transport's httpapi.RunService
// contract.
//
// It holds the conversation repository only to answer one question the run store
// cannot: whether the thread exists. Without that check a mistyped thread id
// returns an empty run list, which reads as "this conversation did something and
// recorded nothing" rather than "there is no such conversation" — and a replay
// API whose empty answer is ambiguous is one nobody can debug with.
type runService struct {
	conversations store.ConversationRepository
	runs          store.RunRepository
}

func newRunService(
	conversations store.ConversationRepository, runs store.RunRepository,
) *runService {
	return &runService{conversations: conversations, runs: runs}
}

func (s *runService) ListRuns(
	ctx context.Context, threadID string, limit int, beforeID string,
) (httpapi.RunPage, error) {
	if _, err := s.conversations.Get(ctx, threadID); err != nil {
		return httpapi.RunPage{}, err
	}
	rows, err := s.runs.ListRuns(ctx, threadID, limit, beforeID)
	if err != nil {
		return httpapi.RunPage{}, err
	}
	views := make([]httpapi.RunView, 0, len(rows))
	for _, row := range rows {
		views = append(views, toRunView(row))
	}
	return httpapi.RunPage{Runs: views}, nil
}

func (s *runService) GetRun(ctx context.Context, runID string) (httpapi.RunDetail, error) {
	row, err := s.runs.GetRun(ctx, runID)
	if err != nil {
		return httpapi.RunDetail{}, err
	}
	calls, err := s.runs.ListToolCalls(ctx, runID)
	if err != nil {
		return httpapi.RunDetail{}, err
	}
	views := make([]httpapi.ToolCallView, 0, len(calls))
	for _, call := range calls {
		views = append(views, toToolCallView(call))
	}
	return httpapi.RunDetail{Run: toRunView(row), ToolCalls: views}, nil
}

func toRunView(row run.AgentRun) httpapi.RunView {
	return httpapi.RunView{
		RunID:         row.RunID,
		ThreadID:      row.ThreadID,
		TraceID:       row.TraceID,
		Status:        string(row.Status),
		ModelProvider: row.ModelProvider,
		ModelName:     row.ModelName,
		StartedAt:     row.StartedAt,
		FinishedAt:    row.FinishedAt,
		LatencyMS:     row.LatencyMS,
		TokenInput:    row.TokenInput,
		TokenOutput:   row.TokenOutput,
		ToolCallCount: row.ToolCallCount,
		ErrorCode:     row.ErrorCode,
	}
}

// toToolCallView copies the stored arguments verbatim.
//
// Verbatim is the whole point: the audit redacted them at write time, and
// re-deriving a "safe" version here would mean two definitions of safe, of
// which only the one at the write side is ever tested against real payloads.
func toToolCallView(call run.ToolCallRecord) httpapi.ToolCallView {
	return httpapi.ToolCallView{
		CallID:        call.CallID,
		ToolName:      call.ToolName,
		Status:        call.Status,
		LatencyMS:     call.LatencyMS,
		ResultSummary: call.ResultSummary,
		Arguments:     call.Arguments,
		CreatedAt:     call.CreatedAt,
	}
}
