package httpapi

import (
	"context"
	"encoding/json"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"

	"github.com/zed1995/platepilot/chat-service/internal/httperr"
)

// Run page limits. They mirror the transcript's because both are "give me the
// newest page of a thread's history", and two different bounds for the same
// gesture would be a rule nobody can remember.
const (
	defaultRunPageSize = 20
	maxRunPageSize     = 100
)

// RunService is the read-only replay surface: what the agent did, and in what
// order.
//
// It is separate from ChatService because it is a different question about a
// different row set. A thread's transcript is what the user saw; a run's tool
// calls are what the system did to produce it, and the deployment that can
// answer the first is not necessarily the one that can answer the second — a
// service assembled without a run store still serves conversations.
type RunService interface {
	// ListRuns returns one thread's runs, newest first. beforeID is an
	// exclusive cursor, in the same style as the message paging API.
	ListRuns(ctx context.Context, threadID string, limit int, beforeID string) (RunPage, error)
	// GetRun returns one run with its tool calls in invocation order.
	GetRun(ctx context.Context, runID string) (RunDetail, error)
}

// RunView is one agent run on the wire.
//
// It deliberately mirrors the audit row rather than the agent's internal state:
// a replay is a record of what was persisted, and a view that added derived
// fields would be a second, silently diverging description of the same run.
type RunView struct {
	RunID         string     `json:"run_id"`
	ThreadID      string     `json:"thread_id"`
	TraceID       string     `json:"trace_id,omitempty"`
	Status        string     `json:"status"`
	ModelProvider string     `json:"model_provider,omitempty"`
	ModelName     string     `json:"model_name,omitempty"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	LatencyMS     int64      `json:"latency_ms,omitempty"`
	TokenInput    int        `json:"token_input,omitempty"`
	TokenOutput   int        `json:"token_output,omitempty"`
	ToolCallCount int        `json:"tool_call_count,omitempty"`
	ErrorCode     string     `json:"error_code,omitempty"`
}

// ToolCallView is one tool invocation on the wire.
//
// Arguments is the redacted digest the audit assembled at write time, not the
// raw model payload: the read path returns what was stored rather than widening
// it, because a read endpoint that reconstructed the original arguments would
// undo the redaction exactly where nobody is looking for it.
type ToolCallView struct {
	CallID        string          `json:"call_id"`
	ToolName      string          `json:"tool_name"`
	Status        string          `json:"status"`
	LatencyMS     int64           `json:"latency_ms,omitempty"`
	ResultSummary string          `json:"result_summary,omitempty"`
	Arguments     json.RawMessage `json:"arguments,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
}

// RunPage is one page of a thread's runs.
type RunPage struct {
	Runs []RunView
}

// RunDetail is one run together with its tool chain.
type RunDetail struct {
	Run       RunView
	ToolCalls []ToolCallView
}

type runListResponse struct {
	Runs []RunView `json:"runs"`
}

// runDetailResponse flattens the run's own fields next to its tool calls, so a
// replay reads as one object rather than an envelope a client has to unwrap.
type runDetailResponse struct {
	RunView
	ToolCalls []ToolCallView `json:"tool_calls"`
}

// registerRunRoutes mounts the replay surface.
//
// The two routes answer the same question at two resolutions — "which runs
// happened on this thread" and "what did this run do" — which is why they are
// registered together and behind one service.
func registerRunRoutes(h *server.Hertz, svc RunService) {
	v1 := h.Group("/v1")
	v1.GET("/conversations/:id/runs", ListRunsHandler(svc))
	v1.GET("/runs/:run_id", GetRunHandler(svc))
}

// ListRunsHandler answers GET /v1/conversations/:id/runs.
func ListRunsHandler(svc RunService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		limit, err := queryIntValue(c, "limit")
		if err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		if limit == 0 {
			limit = defaultRunPageSize
		}
		if limit > maxRunPageSize {
			limit = maxRunPageSize
		}
		page, err := svc.ListRuns(ctx, c.Param("id"), limit, queryValue(c, "before_id"))
		if err != nil {
			httperr.Write(ctx, c, err)
			return
		}
		resp := runListResponse{Runs: page.Runs}
		if resp.Runs == nil {
			resp.Runs = []RunView{}
		}
		c.JSON(200, resp)
	}
}

// GetRunHandler answers GET /v1/runs/:run_id.
func GetRunHandler(svc RunService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		detail, err := svc.GetRun(ctx, c.Param("run_id"))
		if err != nil {
			httperr.Write(ctx, c, err)
			return
		}
		resp := runDetailResponse{RunView: detail.Run, ToolCalls: detail.ToolCalls}
		if resp.ToolCalls == nil {
			resp.ToolCalls = []ToolCallView{}
		}
		c.JSON(200, resp)
	}
}
