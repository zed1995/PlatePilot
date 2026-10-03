// Package audit is the side path that records agent runs and tool calls.
//
// Audit writes never decide a turn's outcome: every method swallows repository
// errors and logs them, so an unavailable audit store degrades observability
// but never the user's answer. Arguments are whitelisted and truncated before
// they leave this package; the store only receives the redacted digest.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/zed/platepilot/chat-service/internal/agent/tools"
	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/run"
	"github.com/zed/platepilot/shared/store"
)

const (
	// maxArgumentChars bounds one whitelisted string argument value.
	maxArgumentChars = 256
	// maxSummaryChars bounds the stored tool result summary.
	maxSummaryChars = 512
	// auditWriteTimeout bounds one audit write. It runs detached from the
	// request context: a canceled turn must still leave a terminal run row.
	auditWriteTimeout = 5 * time.Second
)

// auditCtx detaches writes from request cancellation while bounding them.
func auditCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), auditWriteTimeout)
}

// Hooks records run and tool-call audits through RunRepository.
type Hooks struct {
	repo   store.RunRepository
	logger *slog.Logger

	modelProvider string
	modelName     string

	mu       sync.Mutex
	counters map[string]*counters
}

type counters struct {
	toolCalls int
	retrieval int
}

// Options configures Hooks.
type Options struct {
	ModelProvider string
	ModelName     string
}

// New builds audit hooks. logger defaults to slog.Default.
func New(repo store.RunRepository, logger *slog.Logger, opts Options) *Hooks {
	if logger == nil {
		logger = slog.Default()
	}
	return &Hooks{
		repo:          repo,
		logger:        logger,
		modelProvider: opts.ModelProvider,
		modelName:     opts.ModelName,
		counters:      make(map[string]*counters),
	}
}

// Meta identifies one run that has just started.
type Meta struct {
	RunID     string
	TraceID   string
	ThreadID  string
	StartedAt time.Time
}

// RunStart records the running row. A failure is logged, never returned.
func (h *Hooks) RunStart(ctx context.Context, meta Meta) {
	if h == nil || h.repo == nil {
		return
	}
	agentRun := run.AgentRun{
		TraceID:       meta.TraceID,
		ThreadID:      meta.ThreadID,
		RunID:         meta.RunID,
		Status:        run.StatusRunning,
		ModelProvider: h.modelProvider,
		ModelName:     h.modelName,
		StartedAt:     meta.StartedAt,
	}
	writeCtx, cancel := auditCtx(ctx)
	defer cancel()
	if err := h.repo.Start(writeCtx, agentRun); err != nil {
		// Explicit warning: without this row every later Finish/tool call
		// audit is silently rejected by the foreign key.
		h.logger.Warn("audit: record run start failed; audit trail for this run is incomplete",
			slog.String("run_id", meta.RunID), slog.String("error", err.Error()))
	}
	h.mu.Lock()
	h.counters[meta.RunID] = &counters{}
	h.mu.Unlock()
}

// ToolEvent is everything one tool invocation contributes to the audit.
type ToolEvent struct {
	RunID         string
	CallID        string
	Name          string
	Arguments     json.RawMessage
	ResultSummary string
	// Status is "ok" or "error".
	Status    string
	LatencyMS int64
}

// ToolCall records one tool invocation, redacting arguments on the way.
func (h *Hooks) ToolCall(ctx context.Context, ev ToolEvent) {
	if h == nil || h.repo == nil {
		return
	}
	h.bump(ev.RunID, ev.Name)
	record := run.ToolCallRecord{
		CallID:        ev.CallID,
		RunID:         ev.RunID,
		ToolName:      ev.Name,
		Arguments:     RedactArguments(ev.Name, ev.Arguments),
		ResultSummary: truncateRunes(ev.ResultSummary, maxSummaryChars),
		Status:        ev.Status,
		LatencyMS:     ev.LatencyMS,
		CreatedAt:     time.Now().UTC(),
	}
	writeCtx, cancel := auditCtx(ctx)
	defer cancel()
	if err := h.repo.RecordToolCall(writeCtx, record); err != nil {
		h.logger.Error("audit: record tool call failed",
			slog.String("run_id", ev.RunID), slog.String("call_id", ev.CallID),
			slog.String("tool", ev.Name), slog.String("error", err.Error()))
	}
}

// RunFinish records the terminal run row and discards the in-memory counters.
func (h *Hooks) RunFinish(ctx context.Context, agentRun run.AgentRun) {
	if h == nil || h.repo == nil {
		return
	}
	agentRun.ModelProvider = h.modelProvider
	agentRun.ModelName = h.modelName
	if c := h.take(agentRun.RunID); c != nil {
		agentRun.ToolCallCount = c.toolCalls
		agentRun.RetrievalCount = c.retrieval
	}
	writeCtx, cancel := auditCtx(ctx)
	defer cancel()
	if err := h.repo.Finish(writeCtx, agentRun); err != nil {
		h.logger.Error("audit: finish run failed",
			slog.String("run_id", agentRun.RunID), slog.String("status", string(agentRun.Status)),
			slog.String("error", err.Error()))
	}
}

// Fail records a failed terminal row. It is the error-path counterpart of
// RunFinish for turns that never reached finalize.
func (h *Hooks) Fail(ctx context.Context, meta Meta, err error) {
	if h == nil || h.repo == nil {
		return
	}
	finishedAt := time.Now().UTC()
	h.RunFinish(ctx, run.AgentRun{
		TraceID:    meta.TraceID,
		ThreadID:   meta.ThreadID,
		RunID:      meta.RunID,
		Status:     run.StatusFailed,
		StartedAt:  meta.StartedAt,
		FinishedAt: &finishedAt,
		LatencyMS:  time.Since(meta.StartedAt).Milliseconds(),
		ErrorCode:  string(errs.CodeOf(err)),
	})
}

func (h *Hooks) bump(runID, toolName string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.counters[runID]
	if c == nil {
		c = &counters{}
		h.counters[runID] = c
	}
	c.toolCalls++
	if isRetrievalTool(toolName) {
		c.retrieval++
	}
}

func (h *Hooks) take(runID string) *counters {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.counters[runID]
	delete(h.counters, runID)
	return c
}

// isRetrievalTool reports whether a tool call counts toward retrieval_count.
func isRetrievalTool(name string) bool {
	return name == tools.SearchRestaurantsToolName || name == tools.RestaurantEvidenceToolName
}

// argumentAllowlist is the only set of keys the audit stores per tool.
// Everything else — including any contact field a future tool might accept —
// is dropped by default.
var argumentAllowlist = map[string]map[string]struct{}{
	tools.SearchRestaurantsToolName: {
		"query": {}, "borough": {}, "cuisine": {},
		"min_rating": {}, "open_now": {}, "top_k": {},
	},
	tools.RestaurantEvidenceToolName: {
		"restaurant_ids": {}, "query": {}, "topic": {},
		"doc_types": {}, "top_k": {},
	},
}

// RedactArguments keeps whitelisted business fields and truncates their string
// values. Unknown tools and malformed JSON collapse to an empty object.
func RedactArguments(toolName string, raw json.RawMessage) json.RawMessage {
	allowed, known := argumentAllowlist[toolName]
	if !known || len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return json.RawMessage(`{}`)
	}
	redacted := make(map[string]any, len(args))
	for key, value := range args {
		if _, ok := allowed[key]; !ok {
			continue
		}
		if s, ok := value.(string); ok {
			value = truncateRunes(s, maxArgumentChars)
		}
		redacted[key] = value
	}
	data, err := json.Marshal(redacted)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return data
}

// truncateRunes cuts s at the last complete rune before max and marks the cut.
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
