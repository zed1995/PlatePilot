// Package llm calls an OpenAI-compatible chat completion endpoint for the
// batch pipeline.
//
// The pipeline sends one non-streaming completion per document. Streaming,
// tool calling and structured output belong to the online agent, which uses
// Eino directly, so this package deliberately exposes a single port and a
// single adapter.
package llm

import (
	"context"

	"github.com/zed1995/platepilot/shared/domain/chat"
)

// Completer produces one plain, non-streaming completion.
type Completer interface {
	Complete(ctx context.Context, req chat.ChatRequest) (chat.ChatResponse, error)
}
