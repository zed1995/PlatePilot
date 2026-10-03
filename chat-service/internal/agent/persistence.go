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
		st.LoadedCheckpoint = &checkpoint
		st.ClarificationCount = checkpoint.ClarificationCount
		// The thread's pin starts this turn where it ended the last one. Without
		// it a follow-up that names no restaurant ("营业时间呢") would write a
		// checkpoint with no restaurant in it, and the turn after that would
		// have to re-derive a scope the user never changed.
		st.SelectedRestaurantID = checkpoint.SelectedRestaurantID
	case errors.Is(err, errs.ErrNotFound):
		// First turn of the thread.
	default:
		st.Warnings = appendUnique(st.Warnings, "会话状态加载失败，本轮按新会话处理")
	}

	// The candidate snapshot is loaded beside the checkpoint because the two are
	// one fact split across two tables: the checkpoint says the thread is
	// waiting for a restaurant, and the snapshot says which restaurants it was
	// offered. A follow-up that could see only one of them would have to guess.
	candidates, err := r.deps.Conversations.ListCandidates(ctx, st.ThreadID)
	switch {
	case err == nil:
		st.LoadedCandidates = candidates
	case errors.Is(err, errs.ErrNotFound):
		// No search has run on this thread yet.
	default:
		st.Warnings = appendUnique(st.Warnings, "会话候选列表加载失败，本轮无法解析指代")
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
		ClarificationCount:   st.ClarificationCount,
		PendingToolCallID:    st.PendingToolCallID,
		PendingArguments:     st.PendingArguments,
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
//
// A store failure is warning-only: checkpoint loss must not abort the running
// turn. A version conflict is not: it means another turn on this thread got
// there first, so the state just written is not the state the thread holds and
// the question about it would refer to nothing. That one is returned.
func (r *Runner) persistPendingCheckpoint(ctx context.Context, st *TurnState) error {
	if err := r.saveCheckpoint(ctx, st, time.Now()); err != nil {
		if errs.CodeOf(err) == errs.CodeConflict {
			return conversationConflict(st)
		}
		// Warning already attached inside saveCheckpoint.
		return nil
	}
	// The candidate snapshot is written here as well as at finalize so a client
	// that reacts to the awaiting-input event by immediately reading the thread
	// sees the list the question was asked about, not the one before it.
	r.persistCandidates(ctx, st)
	return nil
}

// conversationConflict reports that this turn lost a race for its thread.
//
// Threads are claimed optimistically, through the same checkpoint version that
// makes recovery deterministic, and nothing queues a second writer: two requests
// for one thread are two turns, and only one of them can own the state they both
// read. Losing is therefore a client-visible `conflict`, not a silent merge —
// merging would produce a thread whose transcript records both turns while its
// checkpoint describes one, and every later reference ("第二家") would then be
// resolved against a list half the transcript never saw.
func conversationConflict(st *TurnState) error {
	return errs.Newf(errs.CodeConflict,
		"线程 %q 正被另一轮对话修改，请重试", st.ThreadID)
}

// persistCandidates writes this thread's candidate snapshot.
//
// The snapshot is position-indexed and replaced wholesale, which is what makes
// "第二家" a deterministic reference: an ordinal is only meaningful against one
// ordered list, so appending would make it ambiguous the moment a second search
// ran. A turn that produced no candidates leaves the previous snapshot alone —
// clearing it would make a follow-up that referred to the last recommendation
// unresolvable for no reason.
//
// A clarification's options are a candidate list too. When a turn asked which of
// two restaurants the user meant, those two are exactly the list the user's
// answer has to be read against, so they are persisted as such.
func (r *Runner) persistCandidates(ctx context.Context, st *TurnState) {
	if r.deps.Conversations == nil || st.ThreadID == "" {
		return
	}
	source := st.Candidates
	if len(source) == 0 {
		source = st.ClarificationOptions
	}
	if len(source) == 0 {
		return
	}
	now := time.Now()
	snapshot := make([]conversation.Candidate, 0, len(source))
	for i, candidate := range source {
		snapshot = append(snapshot, conversation.Candidate{
			ThreadID:     st.ThreadID,
			Position:     i + 1,
			RestaurantID: candidate.RestaurantID,
			Name:         candidate.Name,
			Score:        candidate.Score,
			Reasons:      candidate.Reasons,
			SnapshotAt:   candidate.SnapshotAt,
			CreatedAt:    now,
		})
	}
	if err := r.deps.Conversations.ReplaceCandidates(ctx, st.ThreadID, snapshot); err != nil {
		st.Warnings = appendUnique(st.Warnings, "会话候选列表保存失败，追问可能无法定位到具体餐厅")
	}
}

func checkpointEvidenceIDs(st *TurnState) []int64 {
	if st.FinalAnswer != nil {
		return st.FinalAnswer.Citations
	}
	return nil
}

// settleTerminalState decides what the thread looks like after this turn.
//
// A turn that finished with nothing parked returns the thread to idle and starts
// the clarification budget over. A turn that parked something keeps the pending
// state and the count, because the thread is genuinely mid-conversation: the
// user owes it an answer, and the next request has to pick up where this one
// stopped rather than start clean.
//
// Resetting unconditionally — what this did before M5 — is exactly what would
// erase a clarification a user has not answered yet, and would make the
// clarification cap unreachable by zeroing its counter every turn.
func (r *Runner) settleTerminalState(st *TurnState) {
	if st.PendingAction != "" || len(st.MissingSlots) > 0 {
		if st.State == "" || st.State == conversation.StateIdle {
			// A pending action with no pending state is a writer bug. Recording
			// it is better than quietly idling the thread, which would strand
			// the parked work with no state saying it exists.
			st.Warnings = appendUnique(st.Warnings,
				"本轮挂起了待处理动作，但未标明等待状态")
			st.State = conversation.StateAwaitingClarification
		}
		return
	}
	st.State = conversation.StateIdle
	st.PendingAction = ""
	st.MissingSlots = nil
	st.ClarificationCount = 0
}

// persistTurn writes the thread row, the next checkpoint, and the two new
// transcript messages (user input and final answer).
//
// Persistence is best-effort by design: the answer is already produced, and
// failing the turn after composition would throw that answer away. A store
// failure degrades future history replay, recorded as a visible warning, rather
// than changing this turn's outcome.
//
// A version conflict is the exception, and it is returned. It is not a store
// failure but a lost race: another turn claimed this thread first, so both the
// checkpoint and the transcript belong to that turn, and writing this one's
// messages anyway would make the thread's history describe a conversation its
// state does not. The messages are skipped and the caller reports the conflict.
func (r *Runner) persistTurn(ctx context.Context, st *TurnState, now time.Time) error {
	if r.deps.Conversations == nil || st.ThreadID == "" {
		return nil
	}
	r.settleTerminalState(st)
	if err := r.saveCheckpoint(ctx, st, now); err != nil {
		if errs.CodeOf(err) == errs.CodeConflict {
			return conversationConflict(st)
		}
		// The conversation row itself being unavailable blocks the messages
		// because they carry a foreign key to that row.
		return nil
	}

	evidenceIDs := checkpointEvidenceIDs(st)
	if err := r.deps.Conversations.AppendMessage(ctx, conversation.Message{
		ThreadID:  st.ThreadID,
		Role:      conversation.RoleUser,
		Content:   st.UserInput,
		CreatedAt: now,
	}); err != nil {
		st.Warnings = appendUnique(st.Warnings, "会话历史保存失败，历史可能不完整")
		return nil
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
	r.persistCandidates(ctx, st)
	return nil
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
