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

// GetTrace resolves a trace id to its run and returns the same detail the run
// id would.
//
// It is the lookup the audit exists to support: a trace id is what a client
// holds, and a run id is what the service minted and never told anyone.
func (s *runService) GetTrace(ctx context.Context, traceID string) (httpapi.RunDetail, error) {
	row, err := s.runs.GetRunByTrace(ctx, traceID)
	if err != nil {
		return httpapi.RunDetail{}, err
	}
	return s.detail(ctx, row)
}

func (s *runService) GetRun(ctx context.Context, runID string) (httpapi.RunDetail, error) {
	row, err := s.runs.GetRun(ctx, runID)
	if err != nil {
		return httpapi.RunDetail{}, err
	}
	return s.detail(ctx, row)
}

func (s *runService) detail(ctx context.Context, row run.AgentRun) (httpapi.RunDetail, error) {
	runID := row.RunID
	calls, err := s.runs.ListToolCalls(ctx, runID)
	if err != nil {
		return httpapi.RunDetail{}, err
	}
	toolViews := make([]httpapi.ToolCallView, 0, len(calls))
	for _, call := range calls {
		toolViews = append(toolViews, toToolCallView(call))
	}
	nodes, err := s.listNodes(ctx, runID)
	if err != nil {
		return httpapi.RunDetail{}, err
	}
	return httpapi.RunDetail{Run: toRunView(row), ToolCalls: toolViews, Nodes: nodes}, nil
}

// ListNodes returns one run's node spans in execution order.
//
// Unlike GetRun it does not read the run row first, which is the difference
// between the two routes rather than an oversight: the timeline is the surface
// read while a run is still being diagnosed, and the store's contract is that
// an unknown run yields no rows, so a run that has not finished writing has a
// partial timeline instead of a 404.
func (s *runService) ListNodes(ctx context.Context, runID string) ([]httpapi.RunNodeView, error) {
	return s.listNodes(ctx, runID)
}

func (s *runService) listNodes(ctx context.Context, runID string) ([]httpapi.RunNodeView, error) {
	rows, err := s.runs.ListNodes(ctx, runID)
	if err != nil {
		return nil, err
	}
	views := make([]httpapi.RunNodeView, 0, len(rows))
	for _, row := range rows {
		views = append(views, toRunNodeView(row))
	}
	return views, nil
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
		ErrorMessage:  row.ErrorMessage,
	}
}

// toRunNodeView copies one span.
//
// StartedAt is a pointer because a span is written once and never updated, and
// a zero time sent as a real timestamp would put the node at the epoch on any
// client that renders it — a trace that claims the turn began in 1970 is worse
// than one that says it does not know.
func toRunNodeView(row run.RunNode) httpapi.RunNodeView {
	view := httpapi.RunNodeView{
		NodeID:    row.NodeID,
		RunID:     row.RunID,
		TraceID:   row.TraceID,
		Node:      row.Node,
		Seq:       row.Seq,
		Status:    string(row.Status),
		LatencyMS: row.LatencyMS,
		Detail:    row.Detail,
		ErrorCode: row.ErrorCode,
	}
	if !row.StartedAt.IsZero() {
		started := row.StartedAt.UTC()
		view.StartedAt = &started
	}
	return view
}

// toToolCallView copies the stored arguments verbatim.
//
// Verbatim is the whole point: the audit redacted them at write time, and
// re-deriving a "safe" version here would mean two definitions of safe, of
// which only the one at the write side is ever tested against real payloads.
func toToolCallView(call run.ToolCallRecord) httpapi.ToolCallView {
	view := httpapi.ToolCallView{
		CallID:        call.CallID,
		Seq:           call.Seq,
		ToolName:      call.ToolName,
		Status:        call.Status,
		LatencyMS:     call.LatencyMS,
		ResultSummary: call.ResultSummary,
		Arguments:     call.Arguments,
		CreatedAt:     call.CreatedAt,
	}
	// Pointer, like the node spans: the field is omitted rather than sent as a
	// zero time when a store hands back a record without one.
	if !call.StartedAt.IsZero() {
		started := call.StartedAt.UTC()
		view.StartedAt = &started
	}
	return view
}
