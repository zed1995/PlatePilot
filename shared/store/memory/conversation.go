package memory

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zed1995/platepilot/shared/domain/conversation"
	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/idgen"
)

// ConversationRepository is an in-memory store.ConversationRepository.
type ConversationRepository struct {
	mu            sync.RWMutex
	conversations map[string]conversation.Conversation
	checkpoints   map[string]conversation.Checkpoint
	messages      map[string][]conversation.Message
	candidates    map[string][]conversation.Candidate
}

// NewConversationRepository returns an empty in-memory conversation repository.
func NewConversationRepository() *ConversationRepository {
	return &ConversationRepository{
		conversations: make(map[string]conversation.Conversation),
		checkpoints:   make(map[string]conversation.Checkpoint),
		messages:      make(map[string][]conversation.Message),
		candidates:    make(map[string][]conversation.Candidate),
	}
}

// ReplaceCandidates swaps a thread's candidate snapshot.
func (r *ConversationRepository) ReplaceCandidates(_ context.Context, threadID string, candidates []conversation.Candidate) error {
	if strings.TrimSpace(threadID) == "" {
		return errs.New(errs.CodeInvalidArgument, "thread_id is required")
	}
	stored := make([]conversation.Candidate, 0, len(candidates))
	seen := make(map[int]struct{}, len(candidates))
	for _, candidate := range candidates {
		// Position is the identity of this snapshot, so a duplicate is a
		// caller defect rather than something to silently drop: two rows at
		// position 2 make "第二家" ambiguous, which is the exact confusion the
		// snapshot exists to avoid.
		if candidate.Position <= 0 {
			return errs.Newf(errs.CodeInvalidArgument,
				"candidate position must be positive (got %d)", candidate.Position)
		}
		if candidate.RestaurantID <= 0 {
			return errs.Newf(errs.CodeInvalidArgument,
				"candidate restaurant_id must be positive (got %d)", candidate.RestaurantID)
		}
		if _, dup := seen[candidate.Position]; dup {
			return errs.Newf(errs.CodeInvalidArgument,
				"duplicate candidate position %d", candidate.Position)
		}
		seen[candidate.Position] = struct{}{}
		candidate.ThreadID = threadID
		if candidate.CreatedAt.IsZero() {
			candidate.CreatedAt = time.Now().UTC()
		}
		stored = append(stored, candidate)
	}
	sort.Slice(stored, func(i, j int) bool { return stored[i].Position < stored[j].Position })

	r.mu.Lock()
	defer r.mu.Unlock()
	r.candidates[threadID] = stored
	return nil
}

// ListCandidates returns a thread's candidates in position order.
func (r *ConversationRepository) ListCandidates(_ context.Context, threadID string) ([]conversation.Candidate, error) {
	if strings.TrimSpace(threadID) == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "thread_id is required")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	stored := r.candidates[threadID]
	out := make([]conversation.Candidate, len(stored))
	copy(out, stored)
	sort.Slice(out, func(i, j int) bool { return out[i].Position < out[j].Position })
	return out, nil
}

// ListConversations returns one user's threads, newest first.
//
// Newest-first is ordered by UpdatedAt with ThreadID as the tie-breaker: two
// threads created in the same goroutine carry identical wall-clock timestamps,
// and an unstable order would make the same list render differently between
// two reads — which is what "keep fetch modes identical" is protecting against.
func (r *ConversationRepository) ListConversations(
	_ context.Context, userID string, limit int, beforeID string,
) ([]conversation.Conversation, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "user_id is required")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	eligible := make([]conversation.Conversation, 0, len(r.conversations))
	for _, conv := range r.conversations {
		if conv.UserID != userID {
			continue
		}
		eligible = append(eligible, conv)
	}
	sort.Slice(eligible, func(i, j int) bool {
		if !eligible[i].UpdatedAt.Equal(eligible[j].UpdatedAt) {
			return eligible[i].UpdatedAt.After(eligible[j].UpdatedAt)
		}
		return eligible[i].ThreadID < eligible[j].ThreadID
	})

	start := 0
	if beforeID != "" {
		found := false
		for i, conv := range eligible {
			if conv.ThreadID == beforeID {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			// An unknown cursor pages from nothing rather than from the head,
			// matching ListMessages: a stale id must not resurrect threads the
			// caller already scrolled past.
			return []conversation.Conversation{}, nil
		}
	}
	eligible = eligible[start:]
	if limit > 0 && len(eligible) > limit {
		eligible = eligible[:limit]
	}
	return eligible, nil
}

// Delete removes one thread and the rows that exist only because of it.
//
// The owner check happens under the same lock as the removal, so a thread
// belonging to somebody else is not_found rather than deleted half-way through
// an ownership test made against a stale read.
func (r *ConversationRepository) Delete(_ context.Context, userID, threadID string) error {
	if strings.TrimSpace(userID) == "" {
		return errs.New(errs.CodeInvalidArgument, "user_id is required")
	}
	if strings.TrimSpace(threadID) == "" {
		return errs.New(errs.CodeInvalidArgument, "thread_id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	conv, ok := r.conversations[threadID]
	if !ok || conv.UserID != userID {
		return errs.Newf(errs.CodeNotFound, "conversation %q not found", threadID)
	}
	// The children are dropped by hand because there is no schema here to
	// cascade for us; leaving them would let a later thread reuse an id and
	// inherit a transcript it never had.
	delete(r.conversations, threadID)
	delete(r.checkpoints, threadID)
	delete(r.messages, threadID)
	delete(r.candidates, threadID)
	return nil
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
