package postgres

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/zed1995/platepilot/shared/domain/conversation"
	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/idgen"
	"github.com/zed1995/platepilot/shared/store"
)

var _ store.ConversationRepository = (*ConversationRepository)(nil)

// ConversationRepository is the PostgreSQL store.ConversationRepository.
type ConversationRepository struct {
	client *Client
}

// NewConversationRepository builds a conversation repository on an existing
// client.
func NewConversationRepository(client *Client) *ConversationRepository {
	return &ConversationRepository{client: client}
}

// Get returns thread metadata.
func (r *ConversationRepository) Get(ctx context.Context, threadID string) (conversation.Conversation, error) {
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	var conv conversation.Conversation
	err := r.client.pool.QueryRow(ctx, `
		SELECT thread_id, user_id, title, current_state,
		       created_at, updated_at, last_message_at
		FROM conversations WHERE thread_id = $1`, threadID).Scan(
		&conv.ThreadID, &conv.UserID, &conv.Title, &conv.CurrentState,
		&conv.CreatedAt, &conv.UpdatedAt, &conv.LastMessageAt)
	if err != nil {
		return conversation.Conversation{}, operationError("postgres: get conversation", err)
	}
	return conv, nil
}

// Upsert inserts or replaces thread metadata, preserving the original
// created_at.
func (r *ConversationRepository) Upsert(ctx context.Context, conv conversation.Conversation) error {
	if strings.TrimSpace(conv.ThreadID) == "" {
		return errs.New(errs.CodeInvalidArgument, "thread_id is required")
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	_, err := r.client.pool.Exec(ctx, `
		INSERT INTO conversations (
			thread_id, user_id, title, current_state,
			created_at, updated_at, last_message_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (thread_id) DO UPDATE SET
			user_id         = EXCLUDED.user_id,
			title           = EXCLUDED.title,
			current_state   = EXCLUDED.current_state,
			created_at      = conversations.created_at,
			updated_at      = EXCLUDED.updated_at,
			last_message_at = EXCLUDED.last_message_at`,
		conv.ThreadID, conv.UserID, conv.Title, string(conv.CurrentState),
		conv.CreatedAt, conv.UpdatedAt, conv.LastMessageAt)
	if err != nil {
		return operationError("postgres: upsert conversation", err)
	}
	return nil
}

// SaveCheckpoint stores the checkpoint when its version is strictly newer than
// the stored one.
func (r *ConversationRepository) SaveCheckpoint(ctx context.Context, checkpoint conversation.Checkpoint) error {
	if strings.TrimSpace(checkpoint.ThreadID) == "" {
		return errs.New(errs.CodeInvalidArgument, "thread_id is required")
	}
	if checkpoint.MissingSlots == nil {
		checkpoint.MissingSlots = []string{}
	}
	if checkpoint.EvidenceIDs == nil {
		checkpoint.EvidenceIDs = []int64{}
	}
	// jsonb rejects a NULL, and an absent parked action must read back as the
	// empty object rather than as a missing value.
	pendingArguments := checkpoint.PendingArguments
	if len(pendingArguments) == 0 {
		pendingArguments = []byte(`{}`)
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	tag, err := r.client.pool.Exec(ctx, `
		INSERT INTO conversation_checkpoints (
			thread_id, version, state, pending_action, missing_slots,
			evidence_ids, selected_restaurant_id, created_at,
			pending_tool_call_id, pending_arguments, clarification_count
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (thread_id) DO UPDATE SET
			version                = EXCLUDED.version,
			state                  = EXCLUDED.state,
			pending_action         = EXCLUDED.pending_action,
			missing_slots          = EXCLUDED.missing_slots,
			evidence_ids           = EXCLUDED.evidence_ids,
			selected_restaurant_id = EXCLUDED.selected_restaurant_id,
			created_at             = EXCLUDED.created_at,
			pending_tool_call_id   = EXCLUDED.pending_tool_call_id,
			pending_arguments      = EXCLUDED.pending_arguments,
			clarification_count    = EXCLUDED.clarification_count
		WHERE conversation_checkpoints.version < EXCLUDED.version`,
		checkpoint.ThreadID, checkpoint.Version, string(checkpoint.State),
		checkpoint.PendingAction, checkpoint.MissingSlots,
		checkpoint.EvidenceIDs, checkpoint.SelectedRestaurantID, checkpoint.CreatedAt,
		checkpoint.PendingToolCallID, pendingArguments, checkpoint.ClarificationCount)
	if err != nil {
		return operationError("postgres: save checkpoint", err)
	}
	if tag.RowsAffected() == 0 {
		return errs.Newf(errs.CodeConflict,
			"checkpoint version %d is not newer than the stored version for thread %q",
			checkpoint.Version, checkpoint.ThreadID)
	}
	return nil
}

// LoadCheckpoint returns the stored checkpoint.
func (r *ConversationRepository) LoadCheckpoint(ctx context.Context, threadID string) (conversation.Checkpoint, error) {
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	var (
		checkpoint       conversation.Checkpoint
		pendingArguments []byte
	)
	err := r.client.pool.QueryRow(ctx, `
		SELECT thread_id, version, state, pending_action, missing_slots,
		       evidence_ids, selected_restaurant_id, created_at,
		       pending_tool_call_id, pending_arguments, clarification_count
		FROM conversation_checkpoints WHERE thread_id = $1`, threadID).Scan(
		&checkpoint.ThreadID, &checkpoint.Version, &checkpoint.State,
		&checkpoint.PendingAction, &checkpoint.MissingSlots,
		&checkpoint.EvidenceIDs, &checkpoint.SelectedRestaurantID, &checkpoint.CreatedAt,
		&checkpoint.PendingToolCallID, &pendingArguments, &checkpoint.ClarificationCount)
	if err != nil {
		return conversation.Checkpoint{}, operationError("postgres: load checkpoint", err)
	}
	if len(pendingArguments) > 0 {
		checkpoint.PendingArguments = pendingArguments
	}
	return checkpoint, nil
}

// ReplaceCandidates swaps a thread's candidate snapshot inside one transaction.
//
// Delete-then-insert rather than an upsert keyed on position: the snapshot's
// length changes between turns, and a merge would leave a stale tail behind —
// position 4 still pointing at a restaurant from the previous search.
func (r *ConversationRepository) ReplaceCandidates(ctx context.Context, threadID string, candidates []conversation.Candidate) error {
	if strings.TrimSpace(threadID) == "" {
		return errs.New(errs.CodeInvalidArgument, "thread_id is required")
	}
	seen := make(map[int]struct{}, len(candidates))
	for i := range candidates {
		candidate := &candidates[i]
		if candidate.Position <= 0 {
			return errs.Newf(errs.CodeInvalidArgument,
				"candidate position must be positive (got %d)", candidate.Position)
		}
		if candidate.RestaurantID <= 0 {
			return errs.Newf(errs.CodeInvalidArgument,
				"candidate restaurant_id must be positive (got %d)", candidate.RestaurantID)
		}
		if _, dup := seen[candidate.Position]; dup {
			return errs.Newf(errs.CodeInvalidArgument, "duplicate candidate position %d", candidate.Position)
		}
		seen[candidate.Position] = struct{}{}
		candidate.ThreadID = threadID
		if candidate.CreatedAt.IsZero() {
			candidate.CreatedAt = time.Now().UTC()
		}
		if candidate.Reasons == nil {
			candidate.Reasons = []string{}
		}
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	tx, err := r.client.pool.Begin(ctx)
	if err != nil {
		return operationError("postgres: begin replace candidates", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx, `DELETE FROM conversation_candidates WHERE thread_id = $1`, threadID); err != nil {
		return operationError("postgres: clear candidates", err)
	}
	for _, candidate := range candidates {
		if _, err := tx.Exec(ctx, `
			INSERT INTO conversation_candidates (
				thread_id, position, restaurant_id, name, score,
				reasons, snapshot_at, created_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			candidate.ThreadID, candidate.Position, candidate.RestaurantID, candidate.Name,
			candidate.Score, candidate.Reasons, candidate.SnapshotAt, candidate.CreatedAt); err != nil {
			return operationError("postgres: insert candidate", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return operationError("postgres: commit replace candidates", err)
	}
	return nil
}

// ListCandidates returns a thread's candidates in position order.
func (r *ConversationRepository) ListCandidates(ctx context.Context, threadID string) ([]conversation.Candidate, error) {
	if strings.TrimSpace(threadID) == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "thread_id is required")
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	rows, err := r.client.pool.Query(ctx, `
		SELECT thread_id, position, restaurant_id, name, score, reasons, snapshot_at, created_at
		FROM conversation_candidates
		WHERE thread_id = $1
		ORDER BY position ASC`, threadID)
	if err != nil {
		return nil, operationError("postgres: list candidates", err)
	}
	defer rows.Close()

	out := make([]conversation.Candidate, 0)
	for rows.Next() {
		var candidate conversation.Candidate
		if err := rows.Scan(&candidate.ThreadID, &candidate.Position, &candidate.RestaurantID,
			&candidate.Name, &candidate.Score, &candidate.Reasons,
			&candidate.SnapshotAt, &candidate.CreatedAt); err != nil {
			return nil, operationError("postgres: scan candidate", err)
		}
		if candidate.Reasons == nil {
			candidate.Reasons = []string{}
		}
		out = append(out, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate candidates", err)
	}
	return out, nil
}

// AppendMessage stores one message, assigning seq as max(seq)+1 for the
// thread.
func (r *ConversationRepository) AppendMessage(ctx context.Context, msg conversation.Message) error {
	if strings.TrimSpace(msg.ThreadID) == "" {
		return errs.New(errs.CodeInvalidArgument, "thread_id is required")
	}
	if strings.TrimSpace(msg.Role) == "" {
		return errs.New(errs.CodeInvalidArgument, "role is required")
	}
	if strings.TrimSpace(msg.MessageID) == "" {
		msg.MessageID = idgen.NewUUID()
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	// pgx encodes a nil slice as SQL NULL, which the NOT NULL columns reject;
	// an empty value and a missing value must stay distinct from NULL anyway.
	if msg.EvidenceIDs == nil {
		msg.EvidenceIDs = []int64{}
	}
	toolCalls, err := json.Marshal(msg.ToolCalls)
	if err != nil {
		return errs.Wrap(errs.CodeInternal, "postgres: encode tool_calls", err)
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	_, err = r.client.pool.Exec(ctx, `
		INSERT INTO conversation_messages (
			message_id, thread_id, role, content, tool_calls,
			evidence_ids, seq, created_at
		)
		SELECT $1, $2, $3, $4, $5, $6,
		       COALESCE((SELECT max(seq) FROM conversation_messages WHERE thread_id = $2), 0) + 1,
		       $7`,
		msg.MessageID, msg.ThreadID, msg.Role, msg.Content, toolCalls,
		msg.EvidenceIDs, msg.CreatedAt)
	if err != nil {
		return operationError("postgres: append message", err)
	}
	return nil
}

// ListConversations returns one user's threads, newest first.
//
// The user id is a predicate in the statement rather than a check afterwards
// for the same reason the transport requires it: reading every thread and
// filtering in Go would put another user's conversation title in memory, which
// is one refactor away from putting it in a response. Ordering is updated_at
// with thread_id as the tie-breaker, because ties are real (two threads written
// in one turn) and an unstable order makes the same list render differently on
// two consecutive reads.
func (r *ConversationRepository) ListConversations(ctx context.Context, userID string, limit int, beforeID string) ([]conversation.Conversation, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "user_id is required")
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	var limitArg any
	if limit > 0 {
		limitArg = limit
	}
	rows, err := r.client.pool.Query(ctx, `
		SELECT thread_id, user_id, title, current_state,
		       created_at, updated_at, last_message_at
		FROM conversations
		WHERE user_id = $1
		  AND ($2 = '' OR updated_at < (
		      SELECT updated_at FROM conversations WHERE thread_id = $2)
		      OR (updated_at = (SELECT updated_at FROM conversations WHERE thread_id = $2)
		          AND thread_id > $2))
		ORDER BY updated_at DESC, thread_id ASC
		LIMIT $3`,
		userID, beforeID, limitArg)
	if err != nil {
		return nil, operationError("postgres: list conversations", err)
	}
	defer rows.Close()

	out := make([]conversation.Conversation, 0)
	for rows.Next() {
		var conv conversation.Conversation
		if err := rows.Scan(&conv.ThreadID, &conv.UserID, &conv.Title, &conv.CurrentState,
			&conv.CreatedAt, &conv.UpdatedAt, &conv.LastMessageAt); err != nil {
			return nil, operationError("postgres: scan conversation", err)
		}
		out = append(out, conv)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate conversations", err)
	}
	return out, nil
}

// ListMessages returns the newest limit messages before beforeID, in
// ascending chronological order.
func (r *ConversationRepository) ListMessages(ctx context.Context, threadID string, limit int, beforeID string) ([]conversation.Message, error) {
	if strings.TrimSpace(threadID) == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "thread_id is required")
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	var limitArg any
	if limit > 0 {
		limitArg = limit
	}
	rows, err := r.client.pool.Query(ctx, `
		SELECT message_id, thread_id, role, content, tool_calls,
		       evidence_ids, seq, created_at
		FROM (
			SELECT message_id, thread_id, role, content, tool_calls,
			       evidence_ids, seq, created_at
			FROM conversation_messages
			WHERE thread_id = $1
			  AND ($2 = '' OR seq < (
			      SELECT seq FROM conversation_messages WHERE message_id = $2))
			ORDER BY seq DESC
			LIMIT $3
		) AS paged
		ORDER BY seq ASC`,
		threadID, beforeID, limitArg)
	if err != nil {
		return nil, operationError("postgres: list messages", err)
	}
	defer rows.Close()

	out := make([]conversation.Message, 0)
	for rows.Next() {
		var (
			msg       conversation.Message
			toolCalls []byte
		)
		if err := rows.Scan(&msg.MessageID, &msg.ThreadID, &msg.Role, &msg.Content,
			&toolCalls, &msg.EvidenceIDs, &msg.Seq, &msg.CreatedAt); err != nil {
			return nil, operationError("postgres: scan message", err)
		}
		if err := json.Unmarshal(orEmptyArray(toolCalls), &msg.ToolCalls); err != nil {
			return nil, errs.Wrap(errs.CodeInternal, "postgres: decode tool_calls", err)
		}
		if len(msg.EvidenceIDs) == 0 {
			msg.EvidenceIDs = []int64{}
		}
		out = append(out, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate messages", err)
	}
	return out, nil
}
