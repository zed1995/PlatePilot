// Package tool defines vendor-neutral tool calling DTOs.
//
// Tool specifications and calls are expressed with project-owned types so that
// Eino, OpenAI-compatible APIs, and MCP never leak into the domain layer.
package tool

import (
	"encoding/json"

	"github.com/zed1995/platepilot/shared/domain/errs"
)

// ToolSpec describes a tool the model may call. Parameters holds a JSON Schema.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	ReadOnly    bool            `json:"read_only"`
	TimeoutMS   int             `json:"timeout_ms,omitempty"`
}

// ToolCall is a model-requested invocation of a tool.
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// ToolStatus is the outcome of a tool invocation.
type ToolStatus string

const (
	ToolStatusOK    ToolStatus = "ok"
	ToolStatusError ToolStatus = "error"
)

// ToolResult is the audited outcome of a tool invocation.
type ToolResult struct {
	CallID  string          `json:"call_id"`
	Name    string          `json:"name"`
	Status  ToolStatus      `json:"status"`
	Content string          `json:"content,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   *errs.Error     `json:"error,omitempty"`
}
