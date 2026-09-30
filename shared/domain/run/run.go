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
