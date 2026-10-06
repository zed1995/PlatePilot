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
	"encoding/json"
	"time"

	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/conversation"
	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/domain/search"

	"github.com/zed1995/platepilot/chat-service/internal/agent/slots"
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
	// LoadedCheckpoint is the checkpoint read at ingress, nil for a new thread.
	//
	// It is kept as a whole rather than flattened into the fields above because
	// a follow-up turn has to read state the current turn does not otherwise
	// care about: which slot was left open, which action was parked, and which
	// restaurant the conversation had settled on.
	LoadedCheckpoint *conversation.Checkpoint
	// LoadedCandidates is the thread's candidate snapshot as it stood before
	// this turn, in position order. It is separate from Candidates, which holds
	// what this turn's own search returned: "第二家" refers to the list the user
	// was shown last time, and a field that mixed the two would make an ordinal
	// mean something different depending on when it was read.
	LoadedCandidates []conversation.Candidate
	// MemoryContext is the system-segment built from the user's long-term
	// memories. Empty when there is nothing to inject.
	MemoryContext string
	// DroppedMemoryCount records how many of the user's live memories the
	// injection window left out. Constraints are never counted here: they are
	// injected in full, so only retrieved preferences and facts can be cut.
	DroppedMemoryCount int

	Intent     Intent
	Candidates []search.RestaurantCandidate
	Evidence   []evidence.Evidence

	// Plan is this turn's interpretation of the user's message: the intent, the
	// hard filters a search can enforce, and the soft conditions only reviews
	// can support. It is produced once, at ingress, and then consumed by every
	// tool that needs a default — which is what keeps one turn from containing
	// two different readings of the same sentence.
	Plan slots.Plan

	// Recoverable conversation state, persisted into the checkpoint. Nodes
	// that decide the turn needs more user input set State to a pending value
	// together with PendingAction/MissingSlots and trigger a mid-turn
	// checkpoint save; a completed turn settles back to idle.
	State                conversation.State
	PendingAction        string
	MissingSlots         []string
	SelectedRestaurantID int64

	// PendingToolCallID names the write this turn parked for approval, and
	// PendingArguments carries the call verbatim. They are stored beside the
	// checkpoint because the approval arrives as a later request: without them
	// the confirmation would know which tool to run but not with which
	// arguments, and a booking is entirely made of its arguments.
	//
	// The id is minted here rather than taken from the model's call, so it
	// identifies this approval attempt and cannot collide with a provider that
	// reuses call ids across requests. It is what an idempotency key derived
	// downstream commits to.
	PendingToolCallID string
	PendingArguments  json.RawMessage

	// ConfirmationSummary is the sentence shown to the user for the parked
	// call, and ConfirmationRequired records that this turn ended by asking for
	// approval rather than by answering.
	ConfirmationSummary  string
	ConfirmationRequired bool

	// UsedTools records that at least one tool ran this turn. It lets the answer
	// node distinguish pure chit-chat from a restaurant question that retrieved
	// nothing citable.
	UsedTools bool
	// RetrievalEmpty records that a retrieval tool ran and found neither
	// candidates nor evidence. The answer node then uses the fixed refusal
	// template instead of shipping an ungrounded generation.
	RetrievalEmpty bool

	// AvailabilityRestaurantIDs and AvailableSlotIDs record what this turn's
	// availability lookups saw. They are the value domain a mocked booking's
	// arguments are checked against: a slot id the turn never read is a slot id
	// the model invented, whatever the schema says about its type.
	AvailabilityRestaurantIDs []int64
	AvailableSlotIDs          []string

	// ClarificationOptions are the restaurants a name matched equally well. When
	// non-empty the turn stops and asks, because answering about "the" Katz's
	// when the user meant a different one is a wrong answer that reads as a
	// confident one.
	ClarificationOptions []search.RestaurantCandidate
	// ClarificationCount is how many consecutive clarification rounds this
	// thread has had, loaded from the checkpoint at ingress. The cap exists so a
	// name that cannot be disambiguated ends in a stated assumption rather than
	// an unbounded question loop.
	ClarificationCount int
	// ClarificationAssumed records that the cap was reached and the turn
	// proceeded with the best match instead of asking again. The answer has to
	// say so; a silent assumption is the failure the cap is supposed to avoid,
	// not a way around it.
	ClarificationAssumed bool

	// ToolRounds counts completed tool-execution rounds.
	ToolRounds int
	// ToolCallSeq numbers this run's audit rows for tool calls, from 1, in the
	// order the model asked for them. It lives on the turn rather than in the
	// tools node because it has to survive across rounds: a run may execute
	// several rounds, and the read path needs one order for the whole run.
	//
	// It is assigned on the goroutine that owns the round, before any call is
	// launched, so concurrent calls cannot race for a number and the numbering
	// cannot depend on which store answered first.
	ToolCallSeq int
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
	//
	// Under answer streaming it is a provisional increment rather than the
	// whole answer: a client accumulates them, and discards what it accumulated
	// only if an EventAnswerReplace arrives.
	EventDelta EventType = "message.delta"
	// EventAnswerReplace carries the corrected full answer text.
	//
	// It exists because a streamed answer that violates citation closure cannot
	// be un-sent: the first generation has already been rendered by the time
	// the violation is known. The replacement is a whole body, not a
	// correction, which is why it travels in Text rather than reusing Delta —
	// a client that could not tell an increment from a replacement would append
	// the corrected text to the text it is meant to replace.
	EventAnswerReplace EventType = "message.replace"
	// EventThinking is one increment of the model's own reasoning.
	//
	// It travels on a channel of its own rather than as a message.delta because
	// reasoning is not answer text: a client must be able to show it as
	// "thinking" without ever appending it to the answer the user keeps. On a
	// reasoning model it is also the only output for the long stretch before
	// the first answer token, and a blank wait of a minute is the symptom it
	// exists to remove.
	EventThinking EventType = "thinking.delta"
	// EventToolStart marks the beginning of one tool invocation.
	EventToolStart EventType = "tool.start"
	// EventToolFinish marks the end of one tool invocation.
	EventToolFinish EventType = "tool.finish"
	// EventCitation carries the validated evidence IDs attached to the answer.
	EventCitation EventType = "citation"
	// EventAwaitingInput reports that the thread parked a question for the
	// user: it is waiting for a clarification or a confirmation, and the turn
	// ended without an answer.
	//
	// It is a separate event from EventEnd because a client that only reads
	// message.end cannot tell "the answer is complete" from "the answer is
	// deliberately empty and something is expected of you".
	EventAwaitingInput EventType = "state.awaiting_input"
	// EventConfirmationRequired reports that the turn parked a write and is
	// waiting for the user to approve it.
	//
	// It is separate from EventAwaitingInput because the two asks are answered
	// by different endpoints: a clarification is answered by sending another
	// message, a confirmation by POSTing a decision. A client that could not
	// tell them apart would offer the user a text box for a yes/no.
	EventConfirmationRequired EventType = "confirmation.required"
	// EventMemorySaved reports that the turn wrote one long-term memory because
	// the user asked it to.
	//
	// It is emitted so the client can show that something was kept without
	// scraping the answer text. A memory is a durable side effect the user can
	// later list and delete, so "when did that get saved" has to have an
	// answer in the stream rather than only in the store.
	EventMemorySaved EventType = "memory.saved"
	// EventEnd closes a successful turn.
	EventEnd EventType = "message.end"
	// EventError closes a failed turn; the code is an errs.Code.
	EventError EventType = "error"

	// EventPhaseStarted marks the entry of one of the runner's coarse phases:
	// ingress (load context + interpret), plan (decide next step), tools
	// (execute read-only calls), or answer (compose grounded text).
	//
	// PhaseID is a run-unique id used to pair start/finish frames across the
	// plan<->tools loop: a client that keyed off Phase alone would replace the
	// first round's running row on the second round and never show the loop.
	EventPhaseStarted EventType = "phase.started"
	// EventPhaseFinished closes a phase. Outcome is "ok" or "failed"; empty
	// means the caller left it blank, which is treated as ok by the transport.
	EventPhaseFinished EventType = "phase.finished"
	// EventStepStarted marks one of the three sub-actions of ingress
	// (loading_context, embedding_memory, interpreting). Steps exist only
	// inside ingress today; if a future phase needs them, the Step vocabulary
	// grows but the contract (scoped under a phase_id) does not.
	EventStepStarted EventType = "step.started"
	// EventStepFinished closes a step with the same outcome vocabulary as
	// phases.
	EventStepFinished EventType = "step.finished"
	// EventPhaseProgress updates a running phase's title without closing
	// it. A synchronous LLM call inside plan can take tens of seconds and
	// would otherwise show no bytes for the whole stretch; a heartbeat that
	// rewords the active phase row is the difference between "still thinking"
	// and "stuck". The frame is paired to a phase by PhaseID, and the client
	// replaces the existing row rather than appending a new one.
	EventPhaseProgress EventType = "phase.progress"
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

	// Text / citation events. Delta is one increment of answer text, or — on a
	// thinking.delta event — one increment of the model's reasoning; the two
	// never share an event, so the channel is the discriminator.
	Delta     string
	Citations []int64
	// Text is the whole replacement body carried by a message.replace event.
	// It is separate from Delta so "increment" and "replace" never share a
	// field and a client cannot accidentally append one to the other.
	Text string

	// Awaiting-input event.
	State         string
	PendingAction string
	MissingSlots  []string

	// Confirmation event.
	ConfirmationSummary string

	// Memory event: the row the turn wrote.
	MemoryID      string
	MemoryType    string
	MemoryContent string
	// MemoryRefreshed reports that an identical memory already existed, so a
	// client can say "already remembered" instead of announcing a new one.
	MemoryRefreshed bool

	// End / error events.
	FinishReason string
	Usage        *domainchat.TokenUsage
	Warnings     []string
	Code         string
	Message      string

	// Phase / step events.
	//
	// Phase is "ingress" / "plan" / "tools" / "answer"; Step is empty on
	// phase frames and one of "loading_context" / "embedding_memory" /
	// "interpreting" on step frames. PhaseID and StepID are run-unique
	// server-assigned ids a client uses to match start and finish frames
	// across the plan<->tools loop.
	//
	// StartedAt / FinishedAt are unix milliseconds stamped by the runner at
	// the moment of transition; the transport passes them through unchanged
	// so a client's elapsed counter agrees with the server's.
	Phase      string
	PhaseID    string
	Step       string
	StepID     string
	Title      string
	StartedAt  int64
	FinishedAt int64
	Outcome    string
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
