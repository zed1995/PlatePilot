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
type ToolCallRecord struct {
	CallID        string          `json:"call_id"`
	RunID         string          `json:"run_id"`
	ToolName      string          `json:"tool_name"`
	Arguments     json.RawMessage `json:"arguments,omitempty"`
	ResultSummary string          `json:"result_summary,omitempty"`
	Status        string          `json:"status"`
	LatencyMS     int64           `json:"latency_ms,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
}
