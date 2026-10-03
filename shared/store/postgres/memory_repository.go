package postgres

import (
	"context"
	"strings"

	"github.com/zed1995/platepilot/shared/domain/errs"
	domainmemory "github.com/zed1995/platepilot/shared/domain/memory"
	"github.com/zed1995/platepilot/shared/idgen"
	"github.com/zed1995/platepilot/shared/store"
)

var _ store.MemoryRepository = (*MemoryRepository)(nil)

// MemoryRepository is the PostgreSQL store.MemoryRepository. Deletes are soft
// so a user-removed memory stays auditable while disappearing from List.
type MemoryRepository struct {
	client *Client
}

// NewMemoryRepository builds a memory repository on an existing client.
func NewMemoryRepository(client *Client) *MemoryRepository {
	return &MemoryRepository{client: client}
}

// List returns a user's live memories, most recently updated first.
func (r *MemoryRepository) List(ctx context.Context, userID string) ([]domainmemory.Memory, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "user_id is required")
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	rows, err := r.client.pool.Query(ctx, `
		SELECT memory_id, user_id, memory_type, content, source, confidence,
		       embedding::text, created_at, updated_at
		FROM user_memories
		WHERE user_id = $1 AND deleted_at IS NULL
		ORDER BY updated_at DESC, created_at DESC, memory_id ASC`,
		userID)
	if err != nil {
		return nil, operationError("postgres: list memories", err)
	}
	defer rows.Close()

	out := make([]domainmemory.Memory, 0)
	for rows.Next() {
		var (
			mem       domainmemory.Memory
			embedding *string
		)
		if err := rows.Scan(&mem.ID, &mem.UserID, &mem.Type, &mem.Content,
			&mem.Source, &mem.Confidence, &embedding,
			&mem.CreatedAt, &mem.UpdatedAt); err != nil {
			return nil, operationError("postgres: scan memory", err)
		}
		mem.Embedding, err = parseVectorLiteral(embedding)
		if err != nil {
			return nil, err
		}
		out = append(out, mem)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate memories", err)
	}
	return out, nil
}

// Upsert inserts or replaces a memory, assigning an ID when one is missing and
// preserving the original created_at.
func (r *MemoryRepository) Upsert(ctx context.Context, mem domainmemory.Memory) error {
	if strings.TrimSpace(mem.UserID) == "" {
		return errs.New(errs.CodeInvalidArgument, "user_id is required")
	}
	if strings.TrimSpace(mem.ID) == "" {
		mem.ID = idgen.NewUUID()
	}
	var embedding any
	if len(mem.Embedding) > 0 {
		embedding = vectorLiteral(mem.Embedding)
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	_, err := r.client.pool.Exec(ctx, `
		INSERT INTO user_memories (
			memory_id, user_id, memory_type, content, source,
			confidence, embedding, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7::vector, now(), now())
		ON CONFLICT (memory_id) DO UPDATE SET
			user_id     = EXCLUDED.user_id,
			memory_type = EXCLUDED.memory_type,
			content     = EXCLUDED.content,
			source      = EXCLUDED.source,
			confidence  = EXCLUDED.confidence,
			embedding   = EXCLUDED.embedding,
			created_at  = user_memories.created_at,
			updated_at  = now(),
			deleted_at  = NULL`,
		mem.ID, mem.UserID, string(mem.Type), mem.Content, mem.Source,
		mem.Confidence, embedding)
	if err != nil {
		return operationError("postgres: upsert memory", err)
	}
	return nil
}

// Delete soft-deletes one memory owned by userID. Deleting an unknown or
// already-deleted memory is a not_found.
func (r *MemoryRepository) Delete(ctx context.Context, userID, memoryID string) error {
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	tag, err := r.client.pool.Exec(ctx, `
		UPDATE user_memories
		SET deleted_at = now(), updated_at = now()
		WHERE memory_id = $1 AND user_id = $2 AND deleted_at IS NULL`,
		memoryID, userID)
	if err != nil {
		return operationError("postgres: delete memory", err)
	}
	if tag.RowsAffected() == 0 {
		return errs.Newf(errs.CodeNotFound, "memory %q not found for user %q", memoryID, userID)
	}
	return nil
}
