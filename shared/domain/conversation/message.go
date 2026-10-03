package conversation

import (
	"time"

	domaintool "github.com/zed1995/platepilot/shared/domain/tool"
)

// Message roles persisted in conversation_messages.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Message is one persisted conversation message. It is the normalized
// alternative to stuffing the transcript into the checkpoint JSON.
type Message struct {
	MessageID string
	ThreadID  string
	// Role is one of RoleUser / RoleAssistant / RoleTool.
	Role string
	// Content is the message text.
	Content string
	// ToolCalls is populated for assistant messages that requested tools.
	ToolCalls []domaintool.ToolCall `json:",omitempty"`
	// EvidenceIDs references the evidence the assistant answer cited.
	EvidenceIDs []int64 `json:",omitempty"`
	// Seq is the per-thread monotonically increasing position, assigned by the
	// repository. Callers leave it zero on AppendMessage.
	Seq       int64
	CreatedAt time.Time
}
