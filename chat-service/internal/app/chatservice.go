package app

import (
	"context"
	"encoding/json"
	"time"

	"github.com/zed1995/platepilot/chat-service/internal/agent"
	"github.com/zed1995/platepilot/chat-service/internal/hitl"
	"github.com/zed1995/platepilot/chat-service/internal/httpapi"
	"github.com/zed1995/platepilot/chat-service/internal/memorywrite"
	"github.com/zed1995/platepilot/shared/domain/conversation"
	"github.com/zed1995/platepilot/shared/domain/errs"
	domainmemory "github.com/zed1995/platepilot/shared/domain/memory"
	"github.com/zed1995/platepilot/shared/idgen"
	"github.com/zed1995/platepilot/shared/store"
)

// chatService adapts the agent runner and the conversation/memory stores onto
// the transport's httpapi.ChatService contract. It is the composition point
// that knows both worlds; neither the agent nor the stores know about SSE.
type chatService struct {
	runner        *agent.Runner
	conversations store.ConversationRepository
	memories      store.MemoryRepository
	// runs is the audit half of a conversation. It is optional, and it is held
	// here rather than behind the conversation store because deleting a thread
	// has to take its runs with it while neither port gains knowledge of the
	// other's tables: this adapter is the only place that knows both.
	runs store.RunRepository

	// confirm decides the pending write a turn parked. It is nil in a
	// deployment with no confirmed-write capability — the reservation mock
	// switched off, or no chat provider at all — and the decision endpoint then
	// reports the capability as unavailable rather than pretending the thread
	// has nothing pending.
	confirm *hitl.Service
	// summarize re-renders the approval sentence for a decided call. It is the
	// same renderer the gate used to ask the question, injected rather than
	// reimplemented so the echo in a decision response is literally the words
	// the user approved.
	summarize ApprovalSummarizer
	// memoryWrite applies the edit policy to a user's memories. Listing and
	// deleting go straight to the repository; an edit does not, because an edit
	// has rules — the confidence follows the type, the id is kept, and an id
	// belonging to somebody else is not found — and those rules are the same
	// ones the write path enforces.
	memoryWrite *memorywrite.Service
}

// ApprovalSummarizer renders the sentence a user is asked to approve for one
// call. It is the registry's own renderer behind a function type, so this
// adapter depends on the capability and not on the tool registry.
type ApprovalSummarizer func(ctx context.Context, name string, args json.RawMessage) (string, error)

// chatServiceDeps names what the adapter is assembled from. It is a struct
// rather than a parameter list because the confirmation capability is optional
// and a nil-able positional argument would be a position a caller gets wrong.
type chatServiceDeps struct {
	Runner        *agent.Runner
	Conversations store.ConversationRepository
	Memories      store.MemoryRepository
	Runs          store.RunRepository
	Confirmation  *hitl.Service
	Summarize     ApprovalSummarizer
	MemoryWrite   *memorywrite.Service
}

// newChatService builds the adapter. Runner may be nil when no chat provider
// is configured: thread and memory routes still work, and sending a message
// returns provider_unavailable. Confirmation may be nil for the same reason.
func newChatService(deps chatServiceDeps) *chatService {
	return &chatService{
		runner:        deps.Runner,
		conversations: deps.Conversations,
		memories:      deps.Memories,
		runs:          deps.Runs,
		confirm:       deps.Confirmation,
		summarize:     deps.Summarize,
		memoryWrite:   deps.MemoryWrite,
	}
}

func (s *chatService) CreateThread(ctx context.Context, userID, title string) (conversation.Conversation, error) {
	now := time.Now().UTC()
	conv := conversation.Conversation{
		ThreadID:     idgen.NewUUID(),
		UserID:       userID,
		Title:        title,
		CurrentState: conversation.StateIdle,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.conversations.Upsert(ctx, conv); err != nil {
		return conversation.Conversation{}, err
	}
	return conv, nil
}

// ListThreads returns one user's threads, newest first.
//
// It is a straight projection of the repository read: the list is a mirror of
// what is stored, and adding anything derived here would give the client a
// second, quietly different answer to "what is this thread's state?" that GET
// /v1/conversations/:id does not share.
func (s *chatService) ListThreads(
	ctx context.Context, userID string, limit int, beforeID string,
) (httpapi.ThreadPage, error) {
	convs, err := s.conversations.ListConversations(ctx, userID, limit, beforeID)
	if err != nil {
		return httpapi.ThreadPage{}, err
	}
	return httpapi.ThreadPage{Conversations: convs}, nil
}

func (s *chatService) GetThread(ctx context.Context, threadID string) (httpapi.ThreadDetail, error) {
	conv, err := s.conversations.Get(ctx, threadID)
	if err != nil {
		return httpapi.ThreadDetail{}, err
	}
	detail := httpapi.ThreadDetail{Conversation: conv}
	checkpoint, err := s.conversations.LoadCheckpoint(ctx, threadID)
	if err == nil {
		detail.Checkpoint = &checkpoint
	} else if errs.CodeOf(err) != errs.CodeNotFound {
		return httpapi.ThreadDetail{}, err
	}
	return detail, nil
}

// DeleteThread removes a conversation and the run audit it produced.
//
// The conversation store goes first because it is the operation that carries
// the ownership check: it refuses a thread belonging to somebody else, so
// nothing is touched — including no run — before the caller is known to own
// what they named. The run cleanup then follows, and a failure of that second
// step is reported rather than swallowed: the audit rows it leaves behind point
// at a thread the read paths no longer accept, so a quiet success there would
// be a claim about a store that is in fact still holding them.
//
// A deployment with no run store has nothing to clean, and the delete still
// succeeds: the conversation is what the caller asked to remove.
func (s *chatService) DeleteThread(ctx context.Context, userID, threadID string) error {
	if err := s.conversations.Delete(ctx, userID, threadID); err != nil {
		return err
	}
	if s.runs == nil {
		return nil
	}
	return s.runs.DeleteByThread(ctx, threadID)
}

func (s *chatService) ListMessages(
	ctx context.Context, threadID string, limit int, beforeID string,
) (httpapi.MessagePage, error) {
	messages, err := s.conversations.ListMessages(ctx, threadID, limit, beforeID)
	if err != nil {
		return httpapi.MessagePage{}, err
	}
	return httpapi.MessagePage{Messages: messages}, nil
}

// ListCandidates returns the thread's current candidate snapshot.
//
// Read-only on purpose. The snapshot is written by a turn that searched, and
// exposing it does not change what it says or when it is replaced. The
// thread-existence check comes first so a mistyped thread is not_found rather
// than a page of nothing, matching the transcript's behaviour.
func (s *chatService) ListCandidates(
	ctx context.Context, threadID string,
) (httpapi.CandidatePage, error) {
	if _, err := s.conversations.Get(ctx, threadID); err != nil {
		return httpapi.CandidatePage{}, err
	}
	candidates, err := s.conversations.ListCandidates(ctx, threadID)
	if err != nil {
		return httpapi.CandidatePage{}, err
	}
	return httpapi.CandidatePage{Candidates: candidates}, nil
}

func (s *chatService) ListMemories(ctx context.Context, userID string) (httpapi.MemoryPage, error) {
	memories, err := s.memories.List(ctx, userID)
	if err != nil {
		return httpapi.MemoryPage{}, err
	}
	views := make([]httpapi.MemoryView, 0, len(memories))
	for _, mem := range memories {
		views = append(views, toMemoryView(mem))
	}
	return httpapi.MemoryPage{Memories: views}, nil
}

func (s *chatService) DeleteMemory(ctx context.Context, userID, memoryID string) error {
	return s.memories.Delete(ctx, userID, memoryID)
}

// UpdateMemory applies a user's edit to one of their memories.
//
// The edit is routed through the write policy rather than the repository so the
// rules a memory obeys hold on both paths: the id is kept (an edit is not a
// soft-delete plus an insert, which would show the user two rows for one
// preference), the confidence follows the type, and an id belonging to somebody
// else is not found rather than forbidden.
func (s *chatService) UpdateMemory(
	ctx context.Context, in httpapi.UpdateMemoryInput,
) (httpapi.MemoryView, error) {
	if s.memoryWrite == nil {
		return httpapi.MemoryView{}, errs.New(errs.CodeInvalidArgument,
			"当前部署未启用记忆管理")
	}
	updated, err := s.memoryWrite.Update(ctx, in.UserID, in.MemoryID, memorywrite.Set{
		Content: in.Content,
		Type:    in.Type,
	})
	if err != nil {
		return httpapi.MemoryView{}, err
	}
	return toMemoryView(updated), nil
}

func toMemoryView(mem domainmemory.Memory) httpapi.MemoryView {
	return httpapi.MemoryView{
		ID:         mem.ID,
		Type:       string(mem.Type),
		Content:    mem.Content,
		Source:     mem.Source,
		Confidence: mem.Confidence,
		CreatedAt:  mem.CreatedAt,
		UpdatedAt:  mem.UpdatedAt,
	}
}

// ConfirmAction applies the user's answer to the thread's pending write.
//
// It is deliberately not a turn: no model runs, nothing is streamed, and the
// endpoint answers with one outcome. Routing a yes/no through the chat path
// would make the booking depend on the model reading "确认" correctly, which is
// the dependency the whole gate exists to remove.
//
// The caller's identity is not compared against the thread's owner, matching
// the rest of this surface (GetThread, ListMessages) on the mock deployment.
// The approval the tool receives does carry the thread's stored owner, so a
// derived idempotency key still commits to the real user rather than to
// whoever posted the decision.
func (s *chatService) ConfirmAction(
	ctx context.Context, in httpapi.ConfirmInput,
) (httpapi.ConfirmResult, error) {
	if s.confirm == nil {
		return httpapi.ConfirmResult{}, errs.New(errs.CodeInvalidArgument,
			"当前部署未启用预约确认")
	}
	outcome, err := s.confirm.Decide(ctx, in.ThreadID, hitl.Decision(in.Decision))
	if err != nil {
		return httpapi.ConfirmResult{}, err
	}

	result := httpapi.ConfirmResult{
		ThreadID:      outcome.ThreadID,
		Decision:      string(outcome.Decision),
		PendingAction: outcome.Action,
		State:         string(outcome.State),
		Replayed:      outcome.Replayed,
		Message:       confirmMessage(outcome),
		Summary:       s.echoApproval(ctx, outcome),
	}
	if outcome.Result != nil {
		result.Output = outcome.Result.Data
	}
	return result, nil
}

// confirmMessage states the outcome in the terms the user just acted in.
func confirmMessage(outcome hitl.Outcome) string {
	switch {
	case outcome.Cancelled:
		return "已取消该预约请求，没有产生任何预订。"
	case outcome.Replayed:
		return "该确认此前已经处理过，返回的是同一笔预约。"
	default:
		return "预约已确认。"
	}
}

// echoApproval re-renders the sentence the user approved.
//
// A failure here does not fail the response: the booking either happened or did
// not, and reporting "the confirmation failed" because a display string could
// not be rebuilt would be false — and would invite the client to retry a write
// that already took place. The empty field is the honest degradation.
func (s *chatService) echoApproval(ctx context.Context, outcome hitl.Outcome) string {
	if s.summarize == nil || len(outcome.Arguments) == 0 {
		return ""
	}
	summary, err := s.summarize(ctx, outcome.Action, outcome.Arguments)
	if err != nil {
		return ""
	}
	return summary
}

// SendMessage runs one turn and forwards its events to emit in order.
//
// Runner.Run blocks for the whole turn while publishing onto a buffered
// channel, so the run executes in a goroutine that forwards onto an
// unbuffered channel this method drains: this keeps backpressure honest — a
// slow SSE client throttles the run instead of letting 128 buffered events
// pile up.
//
// The run runs on a derived, cancelable context. A dead sink (a failed emit)
// cancels it even when the transport does not notice the disconnect itself,
// so model calls and tool rounds stop as soon as there is no client left.
func (s *chatService) SendMessage(
	ctx context.Context, in httpapi.SendMessageInput, emit func(httpapi.StreamEvent) error,
) error {
	if s.runner == nil {
		return errs.New(errs.CodeProviderUnavailable, "chat provider is not configured")
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// RunLive rather than Run: Run only hands back its events once the turn is
	// over, so every frame — including the answer's first token — would reach
	// the client at the same moment. The graph is driven on its own goroutine
	// and the frames are forwarded as they are produced, which is the whole
	// point of an SSE turn. RunLive cancels the run when emit fails, so a
	// disconnected client stops the model call instead of draining into a dead
	// socket.
	_, err := s.runner.RunLive(ctx, agent.TurnInput{
		TraceID:   in.TraceID,
		ThreadID:  in.ThreadID,
		UserID:    in.UserID,
		UserInput: in.Content,
	}, func(ev agent.Event) error {
		return emit(toStreamEvent(ev))
	})
	return err
}

// toStreamEvent maps an agent event onto the transport-neutral stream event.
// The two share event-name strings by contract, but never share a type.
func toStreamEvent(ev agent.Event) httpapi.StreamEvent {
	out := httpapi.StreamEvent{
		Type:          httpapi.StreamEventType(ev.Type),
		RunID:         ev.RunID,
		ThreadID:      ev.ThreadID,
		CallID:        ev.CallID,
		Tool:          ev.Tool,
		LatencyMS:     ev.LatencyMS,
		Delta:         ev.Delta,
		ReplaceText:   ev.Text,
		EvidenceIDs:   ev.Citations,
		FinishReason:  ev.FinishReason,
		Usage:         ev.Usage,
		Warnings:      ev.Warnings,
		Code:          ev.Code,
		Message:       ev.Message,
		State:         ev.State,
		PendingAction: ev.PendingAction,
		MissingSlots:  ev.MissingSlots,
		// The approval sentence has to survive this hop for the same reason the
		// pending state does: it is the whole content of a confirmation ask, and
		// a frame that reported "something needs confirming" without saying what
		// would leave the user with nothing to approve.
		ConfirmationSummary: ev.ConfirmationSummary,
		// The memory event is its payload for the same reason: "something was
		// saved" is only actionable if it says what.
		MemoryID:        ev.MemoryID,
		MemoryType:      ev.MemoryType,
		MemoryContent:   ev.MemoryContent,
		MemoryRefreshed: ev.MemoryRefreshed,
	}
	if ev.Type == agent.EventToolFinish {
		if ev.OK {
			out.ToolStatus = "succeeded"
		} else {
			out.ToolStatus = "failed"
		}
	}
	return out
}
