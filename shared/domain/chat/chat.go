// Package chat defines vendor-neutral chat DTOs.
//
// These types are the only chat vocabulary the domain and application layers
// use. Concrete provider payloads (OpenAI-compatible, Eino, MCP) are mapped to
// and from these types inside the adapter layer.
package chat

import "github.com/zed/platepilot/shared/domain/tool"

// Role identifies the author of a chat message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Valid reports whether the role is one of the known roles.
func (r Role) Valid() bool {
	switch r {
	case RoleSystem, RoleUser, RoleAssistant, RoleTool:
		return true
	default:
		return false
	}
}

// FinishReason describes why the model stopped generating.
type FinishReason string

const (
	FinishReasonStop          FinishReason = "stop"
	FinishReasonLength        FinishReason = "length"
	FinishReasonToolCalls     FinishReason = "tool_calls"
	FinishReasonContentFilter FinishReason = "content_filter"
)

// ChatMessage is one message in a conversation.
type ChatMessage struct {
	Role       Role            `json:"role"`
	Content    string          `json:"content"`
	Name       string          `json:"name,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolCalls  []tool.ToolCall `json:"tool_calls,omitempty"`
}

// ChatRequest is a single completion request.
//
// ProviderOptions is the only field that may carry vendor-specific knobs; every
// other field is provider-neutral by construction.
type ChatRequest struct {
	ThreadID        string            `json:"thread_id,omitempty"`
	Messages        []ChatMessage     `json:"messages"`
	Model           string            `json:"model,omitempty"`
	Temperature     *float64          `json:"temperature,omitempty"`
	MaxTokens       int               `json:"max_tokens,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	ProviderOptions map[string]any    `json:"provider_options,omitempty"`
}

// TokenUsage reports token consumption for a response.
type TokenUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// ChatResponse is a complete (non-streaming) completion.
type ChatResponse struct {
	Message      ChatMessage  `json:"message"`
	FinishReason FinishReason `json:"finish_reason,omitempty"`
	Usage        TokenUsage   `json:"usage"`
	Model        string       `json:"model,omitempty"`
}

// ChatChunk is one incremental delta of a streaming response.
type ChatChunk struct {
	Delta        string          `json:"delta,omitempty"`
	ToolCalls    []tool.ToolCall `json:"tool_calls,omitempty"`
	FinishReason FinishReason    `json:"finish_reason,omitempty"`
	Usage        *TokenUsage     `json:"usage,omitempty"`
}

// StructuredRequest asks for a JSON-object response validated against a schema.
type StructuredRequest struct {
	ThreadID string            `json:"thread_id,omitempty"`
	Messages []ChatMessage     `json:"messages"`
	Model    string            `json:"model,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// StructuredResponse is a schema-validated structured completion.
type StructuredResponse struct {
	Content string     `json:"content"`
	Usage   TokenUsage `json:"usage"`
	Model   string     `json:"model,omitempty"`
}

// ToolCallResponse is a completion that may contain tool calls.
type ToolCallResponse struct {
	Message      ChatMessage  `json:"message"`
	FinishReason FinishReason `json:"finish_reason,omitempty"`
	Usage        TokenUsage   `json:"usage"`
	Model        string       `json:"model,omitempty"`
}

// Answer is the grounded final answer the agent hands back for one turn.
//
// Citations references evidence by EvidenceID; only IDs that were actually in
// the turn's evidence set may appear here, and the answer layer enforces that
// before the answer leaves the agent. FollowUps are suggested next questions,
// at most three.
type Answer struct {
	Text      string   `json:"text"`
	Citations []int64  `json:"citations,omitempty"`
	FollowUps []string `json:"follow_ups,omitempty"`
}
