package memory

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zed/platepilot/shared/domain/conversation"
	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/idgen"
)

// ConversationRepository is an in-memory store.ConversationRepository.
type ConversationRepository struct {
	mu            sync.RWMutex
	conversations map[string]conversation.Conversation
	checkpoints   map[string]conversation.Checkpoint
	messages      map[string][]conversation.Message
}

// NewConversationRepository returns an empty in-memory conversation repository.
func NewConversationRepository() *ConversationRepository {
	return &ConversationRepository{
		conversations: make(map[string]conversation.Conversation),
		checkpoints:   make(map[string]conversation.Checkpoint),
		messages:      make(map[string][]conversation.Message),
	}
}

// Get returns thread metadata or a not_found error.
func (r *ConversationRepository) Get(_ context.Context, threadID string) (conversation.Conversation, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	conv, ok := r.conversations[threadID]
	if !ok {
		return conversation.Conversation{}, errs.Newf(errs.CodeNotFound, "conversation %q not found", threadID)
	}
	return conv, nil
}

// Upsert inserts or replaces thread metadata.
func (r *ConversationRepository) Upsert(_ context.Context, conv conversation.Conversation) error {
	if strings.TrimSpace(conv.ThreadID) == "" {
		return errs.New(errs.CodeInvalidArgument, "thread_id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.conversations[conv.ThreadID] = conv
	return nil
}

// SaveCheckpoint stores a checkpoint whose version must be strictly newer than
// the stored one. A stale or equal version is a conflict: recovery only
// recognises the newest version, and silently dropping a write would let a
// caller believe a state was durably captured when it was not.
func (r *ConversationRepository) SaveCheckpoint(_ context.Context, checkpoint conversation.Checkpoint) error {
	if strings.TrimSpace(checkpoint.ThreadID) == "" {
		return errs.New(errs.CodeInvalidArgument, "thread_id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.checkpoints[checkpoint.ThreadID]; ok && existing.Version >= checkpoint.Version {
		return errs.Newf(errs.CodeConflict,
			"checkpoint version %d is not newer than stored version %d for thread %q",
			checkpoint.Version, existing.Version, checkpoint.ThreadID)
	}
	r.checkpoints[checkpoint.ThreadID] = checkpoint
	return nil
}

// LoadCheckpoint returns the latest checkpoint or a not_found error.
func (r *ConversationRepository) LoadCheckpoint(_ context.Context, threadID string) (conversation.Checkpoint, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	checkpoint, ok := r.checkpoints[threadID]
	if !ok {
		return conversation.Checkpoint{}, errs.Newf(errs.CodeNotFound, "checkpoint for %q not found", threadID)
	}
	return checkpoint, nil
}

// AppendMessage stores one message, assigning message id, seq, and timestamp.
func (r *ConversationRepository) AppendMessage(_ context.Context, msg conversation.Message) error {
	if strings.TrimSpace(msg.ThreadID) == "" {
		return errs.New(errs.CodeInvalidArgument, "thread_id is required")
	}
	if strings.TrimSpace(msg.Role) == "" {
		return errs.New(errs.CodeInvalidArgument, "role is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	thread := r.messages[msg.ThreadID]
	if msg.MessageID == "" {
		msg.MessageID = idgen.NewUUID()
	}
	msg.Seq = int64(len(thread)) + 1
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	r.messages[msg.ThreadID] = append(thread, msg)
	return nil
}

// ListMessages returns the messages before beforeID, newest-first truncated to
// limit, then returned in ascending order for history replay.
func (r *ConversationRepository) ListMessages(_ context.Context, threadID string, limit int, beforeID string) ([]conversation.Message, error) {
	if strings.TrimSpace(threadID) == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "thread_id is required")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	thread := r.messages[threadID]
	boundary := int64(len(thread)) + 1 // exclusive seq
	if beforeID != "" {
		boundary = 0
		for _, msg := range thread {
			if msg.MessageID == beforeID {
				boundary = msg.Seq
				break
			}
		}
		if boundary == 0 {
			// An unknown cursor pages from nothing rather than from the head:
			// a stale id must not resurrect messages the caller already saw.
			return []conversation.Message{}, nil
		}
	}

	eligible := make([]conversation.Message, 0, len(thread))
	for _, msg := range thread {
		if msg.Seq < boundary {
			eligible = append(eligible, msg)
		}
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].Seq > eligible[j].Seq })
	if limit > 0 && len(eligible) > limit {
		eligible = eligible[:limit]
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].Seq < eligible[j].Seq })
	return eligible, nil
}
