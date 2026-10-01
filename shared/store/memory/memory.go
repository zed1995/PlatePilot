package memory

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zed/platepilot/shared/domain/errs"
	domainmemory "github.com/zed/platepilot/shared/domain/memory"
	"github.com/zed/platepilot/shared/idgen"
)

// MemoryRepository is an in-memory store.MemoryRepository. Deletes are soft so
// that user-managed memories can be audited, matching the user_memories schema.
type MemoryRepository struct {
	mu     sync.RWMutex
	byUser map[string]map[string]domainmemory.Memory
}

// NewMemoryRepository returns an empty in-memory memory repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{byUser: make(map[string]map[string]domainmemory.Memory)}
}

// List returns a user's non-deleted memories ordered by creation time.
func (r *MemoryRepository) List(_ context.Context, userID string) ([]domainmemory.Memory, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "user_id is required")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	records := r.byUser[userID]
	out := make([]domainmemory.Memory, 0, len(records))
	for _, record := range records {
		if record.DeletedAt != nil {
			continue
		}
		out = append(out, record)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Upsert inserts or replaces a memory, assigning an ID when one is missing.
func (r *MemoryRepository) Upsert(_ context.Context, mem domainmemory.Memory) error {
	if strings.TrimSpace(mem.UserID) == "" {
		return errs.New(errs.CodeInvalidArgument, "user_id is required")
	}
	if strings.TrimSpace(mem.ID) == "" {
		mem.ID = idgen.NewUUID()
	}
	now := time.Now().UTC()
	if mem.CreatedAt.IsZero() {
		mem.CreatedAt = now
	}
	mem.UpdatedAt = now

	r.mu.Lock()
	defer r.mu.Unlock()
	records, ok := r.byUser[mem.UserID]
	if !ok {
		records = make(map[string]domainmemory.Memory)
		r.byUser[mem.UserID] = records
	}
	if existing, ok := records[mem.ID]; ok && !existing.CreatedAt.IsZero() {
		mem.CreatedAt = existing.CreatedAt
	}
	records[mem.ID] = mem
	return nil
}

// Delete soft-deletes a memory owned by userID.
func (r *MemoryRepository) Delete(_ context.Context, userID, memoryID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	records, ok := r.byUser[userID]
	if !ok {
		return errs.Newf(errs.CodeNotFound, "memory %q not found for user %q", memoryID, userID)
	}
	record, ok := records[memoryID]
	if !ok || record.DeletedAt != nil {
		return errs.Newf(errs.CodeNotFound, "memory %q not found for user %q", memoryID, userID)
	}
	now := time.Now().UTC()
	record.DeletedAt = &now
	record.UpdatedAt = now
	records[memoryID] = record
	return nil
}
