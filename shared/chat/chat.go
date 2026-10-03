package chat

import (
	"context"
	"encoding/json"

	"github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/tool"
)

// ChatStream is a vendor-neutral streaming completion. Recv returns io.EOF once
// the stream is exhausted; callers must Close to release resources.
type ChatStream interface {
	Recv() (chat.ChatChunk, error)
	Close() error
}

// ChatProvider produces plain text completions.
type ChatProvider interface {
	Complete(ctx context.Context, req chat.ChatRequest) (chat.ChatResponse, error)
	Stream(ctx context.Context, req chat.ChatRequest) (ChatStream, error)
}

// ToolCallingProvider produces completions that may request tool calls.
type ToolCallingProvider interface {
	SupportsTools() bool
	SupportsParallelTools() bool
	ChatWithTools(ctx context.Context, req chat.ChatRequest, tools []tool.ToolSpec) (chat.ToolCallResponse, error)
}

// StructuredOutputProvider produces schema-validated structured completions.
type StructuredOutputProvider interface {
	SupportsJSONSchema() bool
	CompleteStructured(ctx context.Context, req chat.StructuredRequest, schema json.RawMessage) (chat.StructuredResponse, error)
}
