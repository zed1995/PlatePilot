// Package run defines agent run and tool call audit DTOs.
package run

import (
	"encoding/json"
	"time"
)

// Status is the lifecycle status of an agent run.
type Status string

const (
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// AgentRun is the audit record for a single agent run.
type AgentRun struct {
	TraceID        string     `json:"trace_id"`
	ThreadID       string     `json:"thread_id"`
	RunID          string     `json:"run_id"`
	Status         Status     `json:"status"`
	ModelProvider  string     `json:"model_provider,omitempty"`
	ModelName      string     `json:"model_name,omitempty"`
	StartedAt      time.Time  `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	LatencyMS      int64      `json:"latency_ms,omitempty"`
	TokenInput     int        `json:"token_input,omitempty"`
	TokenOutput    int        `json:"token_output,omitempty"`
	RetrievalCount int        `json:"retrieval_count,omitempty"`
	ToolCallCount  int        `json:"tool_call_count,omitempty"`
	ErrorCode      string     `json:"error_code,omitempty"`
}

// NodeStatus is the outcome of one graph node.
type NodeStatus string

const (
	// NodeOK is a node that returned without error.
	NodeOK NodeStatus = "ok"
	// NodeError is a node that returned an error. The run-level error code is
	// recorded with it, so a failure can be located from the node that raised
	// it rather than only from the terminal run row.
	NodeError NodeStatus = "error"
)

// RunNode is one span of a run: a graph node, with when it started, how long
// it took, and whether it succeeded.
//
// Runs and tool calls were not enough to locate a failure. A run row says the
// turn failed and after how long; a tool call row says which tool ran. Neither
// says which of the six nodes the failure came from, and "the turn failed
// after 30 seconds" is not actionable when four of those nodes call a model.
// The span is the unit a trace is read in: ingress, plan, tools, answer,
// finalize, each with its own latency, so the slow or broken step is named
// rather than inferred.
type RunNode struct {
	NodeID  string `json:"node_id"`
	RunID   string `json:"run_id"`
	TraceID string `json:"trace_id,omitempty"`
	// Node is the graph node name: ingress, plan, tools, clarify, answer,
	// finalize.
	Node string `json:"node"`
	// Seq is the position of this span within its run, from 1. Two nodes can
	// start in the same millisecond, so the read order cannot be derived from
	// the timestamp alone.
	Seq       int        `json:"seq"`
	Status    NodeStatus `json:"status"`
	StartedAt time.Time  `json:"started_at"`
	LatencyMS int64      `json:"latency_ms,omitempty"`
	// Detail carries node-specific, non-sensitive facts: how many candidates
	// the search returned, how many tool calls this round ran. It is written
	// by the node that produced it and is never read on the answer path.
	Detail    json.RawMessage `json:"detail,omitempty"`
	ErrorCode string          `json:"error_code,omitempty"`
}

// ToolCallRecord is the audit record for a single tool invocation. Arguments
// must be stored redacted: never persist secrets or full contact details.
//
// Seq and StartedAt answer two different questions, and the split is not
// bookkeeping. A round may run several read-only tools at once, so a call that
// was asked for first can finish last: the rows are therefore written in
// completion order — that is when each write happens, and holding them back to
// sort them would mean buffering audit rows in memory to no end — and Seq is
// what puts the tool chain back in the order the model asked for.
//
// Seq rather than StartedAt, because timestamps cannot carry that order: two
// calls are launched microseconds apart and the column is a wall clock, so they
// routinely share a microsecond and the read order would come down to a
// tiebreak. This is the same reasoning run_nodes.seq records; what StartedAt is
// for is the other question — when each call actually began, which is what
// makes an overlap visible in the trail at all.
type ToolCallRecord struct {
	CallID        string          `json:"call_id"`
	RunID         string          `json:"run_id"`
	ToolName      string          `json:"tool_name"`
	Arguments     json.RawMessage `json:"arguments,omitempty"`
	ResultSummary string          `json:"result_summary,omitempty"`
	Status        string          `json:"status"`
	// Seq is the position of this call within its run, from 1, counted in the
	// order the model asked for the calls. It is assigned by the caller that
	// owns the round, because only it knows the request order once the calls
	// are running at the same time.
	Seq       int       `json:"seq"`
	LatencyMS int64     `json:"latency_ms,omitempty"`
	StartedAt time.Time `json:"started_at"`
	CreatedAt time.Time `json:"created_at"`
}

// Start returns the invocation time to store for a tool call.
//
// A caller that did not observe the start still knows the end and the duration,
// and the start is recoverable from them — the same derivation the migration
// uses for rows written before the column existed. It lives here rather than in
// an adapter because both stores have to answer "what does an unset start mean"
// the same way, or the two differ on rows that are otherwise identical.
func (c ToolCallRecord) Start() time.Time {
	if !c.StartedAt.IsZero() {
		return c.StartedAt
	}
	if c.LatencyMS <= 0 {
		return c.CreatedAt
	}
	return c.CreatedAt.Add(-time.Duration(c.LatencyMS) * time.Millisecond)
}
