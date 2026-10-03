// Package agent runs one user turn through an explicit Eino state machine:
// ingress, plan, tool execution, grounded answer composition, and finalize.
//
// The state between nodes is a project-owned TurnState; schema.Message never
// crosses a node boundary — it lives behind the einomodel adapter. Events
// emitted while the graph runs mirror the SSE contract the transport layer will
// forward in M4-11.
package agent

import (
	"context"
	"time"

	domainchat "github.com/zed/platepilot/shared/domain/chat"
	"github.com/zed/platepilot/shared/domain/conversation"
	"github.com/zed/platepilot/shared/domain/evidence"
	"github.com/zed/platepilot/shared/domain/search"
)

// Intent labels the minimal M4 routing decision.
type Intent string

const (
	// IntentUnknown is the pre-routing value.
	IntentUnknown Intent = "unknown"
	// IntentChitChat needs no restaurant tools.
	IntentChitChat Intent = "chit_chat"
	// IntentRestaurantQA may need the search and evidence tools.
	IntentRestaurantQA Intent = "restaurant_qa"
)

// TurnInput is one conversation turn request.
type TurnInput struct {
	TraceID  string
	ThreadID string
	UserID   string
	// UserInput is the new user message for this turn.
	UserInput string
	// History carries the prior conversation, oldest first. M4-08 populates it
	// from the conversation store; slice 1 runs without it.
	History []domainchat.ChatMessage
}

// TurnState is the mutable state every graph node reads and writes.
type TurnState struct {
	RunID     string
	TraceID   string
	ThreadID  string
	UserID    string
	StartedAt time.Time

	UserInput string
	// Messages is the live transcript sent to the model: history, the new user
	// message, assistant messages, and tool messages.
	Messages []domainchat.ChatMessage
	// replayedHistory is the persisted transcript replayed at ingress. It is
	// kept separate from Messages so finalize never double-persists it.
	replayedHistory []domainchat.ChatMessage
	// CheckpointVersion is the version loaded at ingress, or zero for a new
	// thread; finalize writes version+1.
	CheckpointVersion int64
	// MemoryContext is the system-segment built from the user's long-term
	// memories. Empty when there is nothing to inject.
	MemoryContext string
	// DroppedMemoryCount records how many memories the window cut.
	DroppedMemoryCount int

	Intent     Intent
	Candidates []search.RestaurantCandidate
	Evidence   []evidence.Evidence

	// Recoverable conversation state, persisted into the checkpoint. Nodes
	// that decide the turn needs more user input set State to a pending value
	// together with PendingAction/MissingSlots and trigger a mid-turn
	// checkpoint save; a completed turn settles back to idle.
	State                conversation.State
	PendingAction        string
	MissingSlots         []string
	SelectedRestaurantID int64

	// UsedTools records that at least one tool ran this turn. It lets the answer
	// node distinguish pure chit-chat from a restaurant question that retrieved
	// nothing citable.
	UsedTools bool
	// RetrievalEmpty records that a retrieval tool ran and found neither
	// candidates nor evidence. The answer node then uses the fixed refusal
	// template instead of shipping an ungrounded generation.
	RetrievalEmpty bool

	// ToolRounds counts completed tool-execution rounds.
	ToolRounds int
	// PendingToolCalls reports whether the most recent model response requested
	// another tool round.
	PendingToolCalls bool
	// RoundsCapped records that another round was requested but refused.
	RoundsCapped bool

	FinalAnswer  *domainchat.Answer
	Warnings     []string
	FinishReason domainchat.FinishReason
	Usage        domainchat.TokenUsage
}

// TurnResult is what Run returns when a turn finishes.
type TurnResult struct {
	RunID        string
	TraceID      string
	ThreadID     string
	Answer       *domainchat.Answer
	FinishReason domainchat.FinishReason
	Usage        domainchat.TokenUsage
	ToolRounds   int
	Warnings     []string
}

// EventType mirrors the SSE event names defined in the M4 contract.
type EventType string

const (
	// EventStart opens a turn.
	EventStart EventType = "message.start"
	// EventDelta is one answer text increment.
	EventDelta EventType = "message.delta"
	// EventToolStart marks the beginning of one tool invocation.
	EventToolStart EventType = "tool.start"
	// EventToolFinish marks the end of one tool invocation.
	EventToolFinish EventType = "tool.finish"
	// EventCitation carries the validated evidence IDs attached to the answer.
	EventCitation EventType = "citation"
	// EventEnd closes a successful turn.
	EventEnd EventType = "message.end"
	// EventError closes a failed turn; the code is an errs.Code.
	EventError EventType = "error"
)

// Event is one observable occurrence in a turn.
type Event struct {
	Type     EventType
	RunID    string
	ThreadID string

	// Tool events.
	CallID    string
	Tool      string
	OK        bool
	LatencyMS int64
	Error     string

	// Text / citation events.
	Delta     string
	Citations []int64

	// End / error events.
	FinishReason string
	Usage        *domainchat.TokenUsage
	Warnings     []string
	Code         string
	Message      string
}

type emitterKey struct{}

// withEmitter stores the per-run emitter on the context.
func withEmitter(ctx context.Context, e *emitter) context.Context {
	return context.WithValue(ctx, emitterKey{}, e)
}

// emitFromContext publishes an event when an emitter is present. Nodes outside
// a Runner run simply produce no events.
func emitFromContext(ctx context.Context, ev Event) {
	if e, ok := ctx.Value(emitterKey{}).(*emitter); ok && e != nil {
		e.send(ctx, ev)
	}
}
