package contract

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zed1995/platepilot/shared/domain/errs"
	domainmemory "github.com/zed1995/platepilot/shared/domain/memory"
	"github.com/zed1995/platepilot/shared/store"
)

// runMemoryRepositoryContract pins the user-memory lifecycle for both
// adapters: upsert with generated id, updated-at-desc ordering, and soft
// delete that hides rows from List without destroying them.
func runMemoryRepositoryContract(t *testing.T, memories store.MemoryRepository) {
	ctx := context.Background()
	const userID = "mem-user-1"

	t.Run("upsert_assigns_id_and_preserves_created_at", func(t *testing.T) {
		embedding := make([]float32, 1024)
		embedding[0] = 0.5
		embedding[1023] = 0.25
		record := domainmemory.Memory{
			UserID:     userID,
			Type:       domainmemory.MemoryTypeConstraint,
			Content:    "不吃辣",
			Source:     "user_explicit",
			Confidence: 0.9,
			Embedding:  embedding,
		}
		if err := memories.Upsert(ctx, record); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		got, err := memories.List(ctx, userID)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1", len(got))
		}
		saved := got[0]
		if saved.ID == "" {
			t.Fatal("ID should be assigned")
		}
		if saved.CreatedAt.IsZero() || saved.UpdatedAt.IsZero() {
			t.Fatal("timestamps should be assigned")
		}
		if saved.Confidence != 0.9 || saved.Source != "user_explicit" || saved.Type != domainmemory.MemoryTypeConstraint {
			t.Fatalf("scalar fields lost: %+v", saved)
		}
		if len(saved.Embedding) != 1024 || saved.Embedding[0] != 0.5 || saved.Embedding[1023] != 0.25 {
			t.Fatalf("embedding round trip failed: len=%d head=%v tail=%v",
				len(saved.Embedding), firstFloats(saved.Embedding), lastFloats(saved.Embedding))
		}

		// An update keeps the original identity and creation time.
		createdAt := saved.CreatedAt
		saved.Content = "不吃辣，也不吃香菜"
		saved.Confidence = 0.95
		if err := memories.Upsert(ctx, saved); err != nil {
			t.Fatalf("Upsert update: %v", err)
		}
		got, err = memories.List(ctx, userID)
		if err != nil {
			t.Fatalf("List after update: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1 (update must not insert)", len(got))
		}
		if got[0].Content != "不吃辣，也不吃香菜" || got[0].Confidence != 0.95 {
			t.Fatalf("update not persisted: %+v", got[0])
		}
		if !got[0].CreatedAt.Equal(createdAt) {
			t.Fatalf("created_at changed: got %s want %s", got[0].CreatedAt, createdAt)
		}
	})

	t.Run("list_orders_by_updated_at_desc_and_filters_deleted", func(t *testing.T) {
		user := "mem-user-order"
		// Sequential inserts stamp increasing updated_at.
		var ids [3]string
		contents := [3]string{"oldest", "middle", "newest"}
		for i := range ids {
			m := domainmemory.Memory{UserID: user, Type: domainmemory.MemoryTypeFact, Content: contents[i], Confidence: 0.5}
			if err := memories.Upsert(ctx, m); err != nil {
				t.Fatalf("Upsert %d: %v", i, err)
			}
			listed, err := memories.List(ctx, user)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			ids[i] = listed[0].ID
		}
		got, err := memories.List(ctx, user)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 3 || got[0].ID != ids[2] || got[1].ID != ids[1] || got[2].ID != ids[0] {
			t.Fatalf("insert order = %+v, want newest first %v", idOrder(got), ids)
		}

		// Touching the oldest row must move it to the head (updated_at desc,
		// not created_at desc). The sleep clears the stores' timestamp
		// resolution (PostgreSQL now() is microsecond granular).
		time.Sleep(2 * time.Millisecond)
		oldest := got[2]
		oldest.Content = "refreshed oldest"
		if err := memories.Upsert(ctx, oldest); err != nil {
			t.Fatalf("Upsert refresh: %v", err)
		}
		got, err = memories.List(ctx, user)
		if err != nil {
			t.Fatalf("List after refresh: %v", err)
		}
		if len(got) != 3 || got[0].ID != oldest.ID {
			t.Fatalf("refreshed row should be first: %+v", idOrder(got))
		}

		// Soft delete hides the row from List.
		if err := memories.Delete(ctx, user, ids[1]); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		got, err = memories.List(ctx, user)
		if err != nil {
			t.Fatalf("List after delete: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("deleted row still listed: %+v", idOrder(got))
		}
		for _, m := range got {
			if m.ID == ids[1] {
				t.Fatalf("deleted memory returned by List: %+v", m)
			}
		}

		// Deleting again is a not_found (the row is already gone from the
		// user-visible set), and so is deleting another user's memory.
		if err := memories.Delete(ctx, user, ids[1]); !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("double delete: want ErrNotFound, got %v", err)
		}
		if err := memories.Delete(ctx, "mem-user-other", oldest.ID); !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("cross-user delete: want ErrNotFound, got %v", err)
		}
		// The other user's listing is unaffected.
		if _, err := memories.List(ctx, "mem-user-other"); err != nil {
			t.Fatalf("empty List should not error: %v", err)
		}
	})

	t.Run("requires_user_id", func(t *testing.T) {
		if err := memories.Upsert(ctx, domainmemory.Memory{Content: "x"}); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("Upsert without user_id: want invalid_argument, got %v", err)
		}
		if _, err := memories.List(ctx, ""); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("List without user_id: want invalid_argument, got %v", err)
		}
	})
}

func idOrder(records []domainmemory.Memory) []string {
	out := make([]string, len(records))
	for i, m := range records {
		out[i] = m.ID
	}
	return out
}

func firstFloats(vec []float32) []float32 {
	if len(vec) < 3 {
		return vec
	}
	return vec[:3]
}

func lastFloats(vec []float32) []float32 {
	if len(vec) < 3 {
		return vec
	}
	return vec[len(vec)-3:]
}
