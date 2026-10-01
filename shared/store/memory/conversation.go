package memory

import (
	"context"
	"strings"
	"sync"

	"github.com/zed/platepilot/shared/domain/conversation"
	"github.com/zed/platepilot/shared/domain/errs"
)

// ConversationRepository is an in-memory store.ConversationRepository.
type ConversationRepository struct {
	mu            sync.RWMutex
	conversations map[string]conversation.Conversation
	checkpoints   map[string]conversation.Checkpoint
}

// NewConversationRepository returns an empty in-memory conversation repository.
func NewConversationRepository() *ConversationRepository {
	return &ConversationRepository{
		conversations: make(map[string]conversation.Conversation),
		checkpoints:   make(map[string]conversation.Checkpoint),
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

// SaveCheckpoint stores a checkpoint when its version is not older than the
// stored one, making replay and retries idempotent.
func (r *ConversationRepository) SaveCheckpoint(_ context.Context, checkpoint conversation.Checkpoint) error {
	if strings.TrimSpace(checkpoint.ThreadID) == "" {
		return errs.New(errs.CodeInvalidArgument, "thread_id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.checkpoints[checkpoint.ThreadID]; ok && existing.Version > checkpoint.Version {
		return nil
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
