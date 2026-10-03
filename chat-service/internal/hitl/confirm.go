// Package hitl owns the human-in-the-loop write path: the pending action a
// tool parks instead of executing, and the confirmation that lets it run.
//
// The package exists so that "the model may request a write but only the user
// may authorise one" is a property of where code sits rather than of what a
// prompt says. Two rules follow from that and both are enforced here:
//
//   - A call to a tool that changes something outside the conversation is never
//     executed by the reasoning loop. It is recorded on the thread and the turn
//     ends with a question.
//   - Such a tool refuses to run at all unless the context carries the user's
//     approval, so a handler that somehow got invoked directly still cannot
//     write. Defence in depth is warranted here because the alternative
//     failure — a booking nobody approved — cannot be undone by a retry.
//
// The package deliberately knows nothing about the agent graph, the tool
// registry implementation, or Eino. It is handed a checkpoint store and a way to
// invoke a tool, which is what keeps the gate generic: reservations are the
// first confirmed write, not the reason the mechanism exists.
package hitl

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/zed1995/platepilot/shared/domain/conversation"
	"github.com/zed1995/platepilot/shared/domain/errs"
	domaintool "github.com/zed1995/platepilot/shared/domain/tool"
)

// Decision is what the user answered to a confirmation request.
type Decision string

const (
	// DecisionConfirm authorises the pending call.
	DecisionConfirm Decision = "confirm"
	// DecisionCancel withdraws it.
	DecisionCancel Decision = "cancel"
)

// Valid reports whether the decision is one the service understands.
func (d Decision) Valid() bool {
	return d == DecisionConfirm || d == DecisionCancel
}

// Approval is the user's authorisation of one specific call.
//
// It travels on the context rather than as a tool argument for the same reason
// the turn's plan does: the values are facts the server established (which
// thread, which request, which user), and a model that could pass them could
// pass different ones. A handler that needs to derive something server-owned —
// an idempotency key, say — reads it here and knows it was not chosen by the
// model.
type Approval struct {
	// ThreadID and UserID identify who approved the call.
	ThreadID string
	UserID   string
	// RequestID names the pending request this authorisation is for. It is
	// minted by the server when the call is parked, so it identifies one
	// approval attempt rather than one call site.
	//
	// It is the ingredient a derived idempotency key commits to, and it is
	// deliberately not the checkpoint version: recording the outcome of a
	// confirmation advances that version, so a key derived from it could never
	// be recomputed on a retry — which is the one case idempotency exists for.
	// A server-minted id survives the write it authorises.
	RequestID string
	// Action is the tool name that was parked.
	Action string
	// Arguments are the arguments the user approved, verbatim.
	Arguments json.RawMessage
}

type approvalKey struct{}

// WithApproval attaches a user's authorisation to a context.
func WithApproval(ctx context.Context, approval Approval) context.Context {
	return context.WithValue(ctx, approvalKey{}, approval)
}

// ApprovalFromContext reports the authorisation a tool call is running under.
//
// A tool whose side effects need authorising calls this and refuses to proceed
// without it. The zero result is not usable, and the second return value is what
// a caller checks; there is deliberately no "empty approval" that looks valid.
func ApprovalFromContext(ctx context.Context) (Approval, bool) {
	approval, ok := ctx.Value(approvalKey{}).(Approval)
	if !ok || approval.Action == "" {
		return Approval{}, false
	}
	return approval, true
}

// CheckpointStore is the slice of the conversation store a confirmation reads
// and writes.
type CheckpointStore interface {
	Get(ctx context.Context, threadID string) (conversation.Conversation, error)
	Upsert(ctx context.Context, conv conversation.Conversation) error
	LoadCheckpoint(ctx context.Context, threadID string) (conversation.Checkpoint, error)
	SaveCheckpoint(ctx context.Context, checkpoint conversation.Checkpoint) error
}

// ToolRunner executes one approved call.
//
// It is the registry behind a one-method interface: the confirmation path runs
// a call the model already produced, and routing it through the same invocation
// entry point is what makes the gate generic. A bespoke dispatcher here would
// have to learn each confirmed tool's name.
type ToolRunner interface {
	Invoke(ctx context.Context, call domaintool.ToolCall) domaintool.ToolResult
}

// Outcome is the result of deciding a confirmation request.
type Outcome struct {
	ThreadID string
	Action   string
	Decision Decision
	// Arguments are the arguments that were decided, verbatim. They are carried
	// out because the transport echoes the sentence the user approved, and the
	// only thing that can re-render that sentence is the tool that produced it.
	// Handing the caller the arguments keeps the renderer out of this package.
	Arguments json.RawMessage
	// Result is the approved call's own result, nil when the user cancelled or
	// when the confirmation was a replay that produced nothing new.
	Result *domaintool.ToolResult
	// Cancelled reports that the pending action was withdrawn and nothing was
	// executed.
	Cancelled bool
	// Replayed reports that an identical confirmation had already been decided.
	//
	// The caller treats it exactly like a first-time success: same booking, same
	// body. It is surfaced separately only so the transport can log it and so a
	// test can tell "idempotency worked" from "the write happened to be
	// harmless".
	Replayed bool
	State    conversation.State
}

// DefaultClock returns the current UTC time.
func DefaultClock() time.Time { return time.Now().UTC() }

// Service decides confirmation requests.
type Service struct {
	store CheckpointStore
	tools ToolRunner
	clock func() time.Time
}

// Config holds the service's dependencies.
type Config struct {
	Checkpoints CheckpointStore
	Tools       ToolRunner
	// Clock is injectable so a test can decide what "now" is. Nil means the
	// wall clock.
	Clock func() time.Time
}

// NewService builds the confirmation service.
func NewService(cfg Config) (*Service, error) {
	if cfg.Checkpoints == nil {
		return nil, errs.New(errs.CodeInvalidArgument, "hitl requires a checkpoint store")
	}
	if cfg.Tools == nil {
		return nil, errs.New(errs.CodeInvalidArgument, "hitl requires a tool runner")
	}
	clock := cfg.Clock
	if clock == nil {
		clock = DefaultClock
	}
	return &Service{store: cfg.Checkpoints, tools: cfg.Tools, clock: clock}, nil
}

// Decide applies the user's answer to the thread's pending action.
//
// The read-check-write sequence is deliberately short and the write is
// optimistic, through the same checkpoint version the rest of the runtime uses.
// Two confirmations racing on one thread are two writers for one version: the
// loser is told `conflict` rather than silently re-deciding an action that has
// already been decided.
func (s *Service) Decide(ctx context.Context, threadID string, decision Decision) (Outcome, error) {
	if !decision.Valid() {
		return Outcome{}, errs.Newf(errs.CodeInvalidArgument,
			"decision must be %q or %q (got %q)", DecisionConfirm, DecisionCancel, decision)
	}
	conv, err := s.store.Get(ctx, threadID)
	if err != nil {
		return Outcome{}, err
	}
	checkpoint, err := s.store.LoadCheckpoint(ctx, threadID)
	if err != nil {
		return Outcome{}, err
	}
	if checkpoint.PendingAction == "" {
		return Outcome{}, errs.New(errs.CodeAgentNoPendingAction,
			"该会话没有待确认的动作")
	}

	// A thread already past its confirmation is a replay, not a new decision:
	// the client retried a request whose response was lost. It must not be able
	// to answer "cancel" now and undo a booking the user made, so the recorded
	// decision governs and the stored outcome is returned unchanged.
	if checkpoint.State == conversation.StateCompleted {
		return s.replay(ctx, conv, checkpoint, decision)
	}
	if checkpoint.State != conversation.StateAwaitingConfirmation {
		// A pending action written outside the awaiting state is a writer bug;
		// deciding it would guess which of the two fields to believe.
		return Outcome{}, errs.Newf(errs.CodeInternal,
			"线程 %q 有挂起动作但状态为 %q", threadID, checkpoint.State)
	}

	if decision == DecisionCancel {
		return s.cancel(ctx, conv, checkpoint)
	}
	return s.execute(ctx, conv, checkpoint)
}

// cancel withdraws the pending action without running it.
//
// Nothing is released, and that is the point: the gate guarantees an
// unconfirmed call has no side effects, so there is no hold to undo. A cancel
// that had to compensate would mean the write had already happened.
func (s *Service) cancel(
	ctx context.Context, conv conversation.Conversation, checkpoint conversation.Checkpoint,
) (Outcome, error) {
	action := checkpoint.PendingAction
	if err := s.clearPending(ctx, conv, checkpoint); err != nil {
		return Outcome{}, err
	}
	return Outcome{
		ThreadID:  conv.ThreadID,
		Action:    action,
		Decision:  DecisionCancel,
		Arguments: checkpoint.PendingArguments,
		Cancelled: true,
		State:     conversation.StateIdle,
	}, nil
}

// execute runs the approved call and records the outcome on the thread.
func (s *Service) execute(
	ctx context.Context, conv conversation.Conversation, checkpoint conversation.Checkpoint,
) (Outcome, error) {
	action := checkpoint.PendingAction
	result := s.tools.Invoke(WithApproval(ctx, approvalFor(conv, checkpoint)), callFor(checkpoint))
	if result.Status != domaintool.ToolStatusOK {
		// The write did not happen, so the thread keeps its pending action and
		// the user can decide again — after fixing whatever the tool reported.
		// Clearing it here would lose the request the user was in the middle of.
		if result.Error != nil {
			return Outcome{}, result.Error
		}
		return Outcome{}, errs.New(errs.CodeInternal,
			fmt.Sprintf("确认动作 %q 执行失败", action))
	}

	// The pending block is kept, with the state moved to completed. It is no
	// longer work awaiting approval — nothing will run it again on its own —
	// but it is what lets a duplicated confirmation recognise itself and answer
	// with the booking it already made, instead of reporting a missing pending
	// action for a booking that exists. The next turn overwrites the block with
	// its own values, so the record lives exactly as long as the retry window.
	checkpoint.Version++
	checkpoint.State = conversation.StateCompleted
	checkpoint.CreatedAt = s.now()
	if err := s.store.SaveCheckpoint(ctx, checkpoint); err != nil {
		return Outcome{}, err
	}
	conv.CurrentState = conversation.StateCompleted
	conv.UpdatedAt = checkpoint.CreatedAt
	if err := s.store.Upsert(ctx, conv); err != nil {
		return Outcome{}, err
	}
	return Outcome{
		ThreadID:  conv.ThreadID,
		Action:    action,
		Decision:  DecisionConfirm,
		Arguments: checkpoint.PendingArguments,
		Result:    &result,
		State:     conversation.StateCompleted,
	}, nil
}

// replay answers a confirmation that has already been decided.
//
// It re-invokes the tool rather than caching its result, and that is safe
// because idempotency is a requirement of the policy, not a hope: the key a
// confirmed tool derives from its approval names one request, so a second
// invocation finds the row the first one created. Re-running is what keeps the
// replay honest — the answered body is the tool's real output for this request,
// not a remembered copy that could drift from it.
//
// Only `confirm` can be replayed. A `cancel` arriving after a completed
// confirmation is asking to undo a booking through an endpoint that has no way
// to express that, and silently reporting success would tell the user something
// untrue.
func (s *Service) replay(
	ctx context.Context, conv conversation.Conversation, checkpoint conversation.Checkpoint,
	decision Decision,
) (Outcome, error) {
	if decision != DecisionConfirm {
		return Outcome{}, errs.New(errs.CodeAgentNoPendingAction,
			"该会话的确认已经完成，无法再取消")
	}
	action := checkpoint.PendingAction
	result := s.tools.Invoke(WithApproval(ctx, approvalFor(conv, checkpoint)), callFor(checkpoint))
	if result.Status != domaintool.ToolStatusOK {
		if result.Error != nil {
			return Outcome{}, result.Error
		}
		return Outcome{}, errs.New(errs.CodeInternal,
			fmt.Sprintf("重放动作 %q 失败", action))
	}
	return Outcome{
		ThreadID:  conv.ThreadID,
		Action:    action,
		Decision:  DecisionConfirm,
		Arguments: checkpoint.PendingArguments,
		Result:    &result,
		Replayed:  true,
		State:     conversation.StateCompleted,
	}, nil
}

// approvalFor projects a parked checkpoint into the authorisation a tool reads.
func approvalFor(conv conversation.Conversation, checkpoint conversation.Checkpoint) Approval {
	return Approval{
		ThreadID:  conv.ThreadID,
		UserID:    conv.UserID,
		RequestID: checkpoint.PendingToolCallID,
		Action:    checkpoint.PendingAction,
		Arguments: checkpoint.PendingArguments,
	}
}

// callFor rebuilds the call that was parked.
func callFor(checkpoint conversation.Checkpoint) domaintool.ToolCall {
	return domaintool.ToolCall{
		ID:        checkpoint.PendingToolCallID,
		Name:      checkpoint.PendingAction,
		Arguments: checkpoint.PendingArguments,
	}
}

// clearPending writes a checkpoint with no pending action.
func (s *Service) clearPending(
	ctx context.Context, conv conversation.Conversation, checkpoint conversation.Checkpoint,
) error {
	checkpoint.Version++
	checkpoint.State = conversation.StateIdle
	checkpoint.PendingAction = ""
	checkpoint.PendingToolCallID = ""
	checkpoint.PendingArguments = nil
	checkpoint.MissingSlots = nil
	checkpoint.CreatedAt = s.now()
	if err := s.store.SaveCheckpoint(ctx, checkpoint); err != nil {
		return err
	}
	conv.CurrentState = conversation.StateIdle
	conv.UpdatedAt = checkpoint.CreatedAt
	return s.store.Upsert(ctx, conv)
}

func (s *Service) now() time.Time { return s.clock().UTC() }
