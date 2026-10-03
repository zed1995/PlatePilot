package agent

import (
	"context"
	"errors"
	"time"

	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/conversation"
	"github.com/zed1995/platepilot/shared/domain/errs"
)

// historyReplayLimit bounds how many persisted messages ingress replays.
const historyReplayLimit = 20

// loadConversationContext loads the thread metadata's useful parts at ingress:
// the newest checkpoint version and the recent transcript. Both are optional:
// a new thread simply starts from zero.
func (r *Runner) loadConversationContext(ctx context.Context, st *TurnState, in TurnInput) {
	if r.deps.Conversations == nil || st.ThreadID == "" {
		return
	}
	checkpoint, err := r.deps.Conversations.LoadCheckpoint(ctx, st.ThreadID)
	switch {
	case err == nil:
		st.CheckpointVersion = checkpoint.Version
	case errors.Is(err, errs.ErrNotFound):
		// First turn of the thread.
	default:
		st.Warnings = appendUnique(st.Warnings, "会话状态加载失败，本轮按新会话处理")
	}

	messages, err := r.deps.Conversations.ListMessages(ctx, st.ThreadID, historyReplayLimit, "")
	if err != nil {
		st.Warnings = appendUnique(st.Warnings, "历史消息加载失败，本轮不带历史上下文")
		return
	}
	st.replayedHistory = replayTranscript(messages)
}

// replayTranscript converts persisted messages into model transcript entries.
//
// Only user and assistant text messages come back. Tool-call assistant
// messages and their tool results are not persisted (the transcript stores
// final answers), and even if a future milestone starts storing them, an
// assistant tool_calls entry without the following tool messages is invalid
// for chat APIs, so they are stripped here.
func replayTranscript(messages []conversation.Message) []domainchat.ChatMessage {
	out := make([]domainchat.ChatMessage, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case conversation.RoleUser:
			out = append(out, domainchat.ChatMessage{Role: domainchat.RoleUser, Content: msg.Content})
		case conversation.RoleAssistant:
			if msg.Content == "" {
				continue
			}
			out = append(out, domainchat.ChatMessage{Role: domainchat.RoleAssistant, Content: msg.Content})
		}
	}
	return out
}

// ensureConversationRow creates the thread row on first sight and refreshes
// its mutable timestamps. It returns false when the row cannot be written.
func (r *Runner) ensureConversationRow(ctx context.Context, st *TurnState, now time.Time, state conversation.State) bool {
	repo := r.deps.Conversations
	conv, err := repo.Get(ctx, st.ThreadID)
	if err != nil {
		if !errors.Is(err, errs.ErrNotFound) {
			st.Warnings = appendUnique(st.Warnings, "会话状态保存失败，历史可能不完整")
			return false
		}
		conv = conversation.Conversation{
			ThreadID:     st.ThreadID,
			UserID:       st.UserID,
			CurrentState: state,
			CreatedAt:    now,
		}
	}
	conv.CurrentState = state
	conv.UpdatedAt = now
	if state == conversation.StateIdle {
		conv.LastMessageAt = now
	}
	if err := repo.Upsert(ctx, conv); err != nil {
		st.Warnings = appendUnique(st.Warnings, "会话状态保存失败，历史可能不完整")
		return false
	}
	return true
}

// saveCheckpoint writes the next checkpoint version from st and, on success,
// advances st.CheckpointVersion. It is both the finalize checkpoint path and
// the seam a planning node uses to persist a pending state mid-turn so a
// later failure remains recoverable through Resume.
func (r *Runner) saveCheckpoint(ctx context.Context, st *TurnState, now time.Time) error {
	if r.deps.Conversations == nil || st.ThreadID == "" {
		return nil
	}
	state := st.State
	if state == "" {
		state = conversation.StateIdle
	}
	if !r.ensureConversationRow(ctx, st, now, state) {
		return errs.New(errs.CodeInternal, "conversation row unavailable")
	}
	checkpoint := conversation.Checkpoint{
		ThreadID:             st.ThreadID,
		Version:              st.CheckpointVersion + 1,
		State:                state,
		PendingAction:        st.PendingAction,
		MissingSlots:         st.MissingSlots,
		SelectedRestaurantID: st.SelectedRestaurantID,
		EvidenceIDs:          checkpointEvidenceIDs(st),
		CreatedAt:            now,
	}
	if err := r.deps.Conversations.SaveCheckpoint(ctx, checkpoint); err != nil {
		st.Warnings = appendUnique(st.Warnings, "会话检查点保存失败，恢复状态可能不是最新")
		return err
	}
	st.CheckpointVersion++
	return nil
}

// persistPendingCheckpoint is the mid-turn entry point a node calls once it
// decides the thread must wait for user input (clarification, confirmation).
// A failure is warning-only: checkpoint loss must not abort the running turn.
func (r *Runner) persistPendingCheckpoint(ctx context.Context, st *TurnState) {
	if err := r.saveCheckpoint(ctx, st, time.Now()); err != nil {
		// Warning already attached inside saveCheckpoint.
		return
	}
}

func checkpointEvidenceIDs(st *TurnState) []int64 {
	if st.FinalAnswer != nil {
		return st.FinalAnswer.Citations
	}
	return nil
}

// persistTurn writes the thread row, the next checkpoint, and the two new
// transcript messages (user input and final answer).
//
// Persistence is best-effort by design: the answer is already produced, and
// failing the turn after composition would throw that answer away. A failure
// degrades future history replay, recorded as a visible warning, rather than
// changing this turn's outcome.
func (r *Runner) persistTurn(ctx context.Context, st *TurnState, now time.Time) {
	if r.deps.Conversations == nil || st.ThreadID == "" {
		return
	}
	// A completed turn leaves the thread ready for the next request. M5
	// introduces states that survive between turns (awaiting confirmation).
	st.State = conversation.StateIdle
	st.PendingAction = ""
	st.MissingSlots = nil
	if err := r.saveCheckpoint(ctx, st, now); err != nil {
		// A checkpoint version conflict or storage error does not block the
		// messages (history replay's source of truth). Only the conversation
		// row itself being unavailable blocks them: the messages carry a
		// foreign key to that row.
		if errs.CodeOf(err) != errs.CodeConflict {
			return
		}
	}

	evidenceIDs := checkpointEvidenceIDs(st)
	if err := r.deps.Conversations.AppendMessage(ctx, conversation.Message{
		ThreadID:  st.ThreadID,
		Role:      conversation.RoleUser,
		Content:   st.UserInput,
		CreatedAt: now,
	}); err != nil {
		st.Warnings = appendUnique(st.Warnings, "会话历史保存失败，历史可能不完整")
		return
	}
	if err := r.deps.Conversations.AppendMessage(ctx, conversation.Message{
		ThreadID:    st.ThreadID,
		Role:        conversation.RoleAssistant,
		Content:     st.FinalAnswer.Text,
		EvidenceIDs: evidenceIDs,
		CreatedAt:   now,
	}); err != nil {
		st.Warnings = appendUnique(st.Warnings, "会话历史保存失败，历史可能不完整")
	}
}

// ResumeState is the recoverable view of a thread used by Resume.
type ResumeState struct {
	Conversation conversation.Conversation
	// Checkpoint is nil when the thread has no checkpoint yet.
	Checkpoint *conversation.Checkpoint
	// Messages are the newest historyReplayLimit persisted messages, oldest
	// first.
	Messages []conversation.Message
}

// Resume loads a thread's recovery state. M5's human-in-the-loop flows resume
// pending actions from the returned checkpoint.
func (r *Runner) Resume(ctx context.Context, threadID string) (*ResumeState, error) {
	if r.deps.Conversations == nil {
		return nil, errs.New(errs.CodeProviderUnavailable, "conversation store is not configured")
	}
	conv, err := r.deps.Conversations.Get(ctx, threadID)
	if err != nil {
		return nil, err
	}
	state := &ResumeState{Conversation: conv}
	checkpoint, err := r.deps.Conversations.LoadCheckpoint(ctx, threadID)
	switch {
	case err == nil:
		state.Checkpoint = &checkpoint
	case errors.Is(err, errs.ErrNotFound):
		// A thread can exist without a checkpoint if its first turn never
		// reached finalize.
	default:
		return nil, err
	}
	messages, err := r.deps.Conversations.ListMessages(ctx, threadID, historyReplayLimit, "")
	if err != nil {
		return nil, err
	}
	state.Messages = messages
	return state, nil
}
