package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/zed/platepilot/shared/domain/errs"
	domainmemory "github.com/zed/platepilot/shared/domain/memory"
)

func TestMemoryUpsertAssignsIDAndList(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	if err := repo.Upsert(ctx, domainmemory.Memory{UserID: "u1", Content: "likes quiet places", Type: domainmemory.MemoryTypePreference}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := repo.List(ctx, "u1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("list returned %d memories, want 1", len(got))
	}
	if got[0].ID == "" {
		t.Fatal("an ID should be assigned when missing")
	}
	if got[0].CreatedAt.IsZero() || got[0].UpdatedAt.IsZero() {
		t.Fatal("timestamps should be set")
	}
}

func TestMemoryDeleteIsSoftAndExcludesFromList(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	if err := repo.Upsert(ctx, domainmemory.Memory{ID: "m1", UserID: "u1", Content: "prefers outdoor seating"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, "u1", "m1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, err := repo.List(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("deleted memory still listed: %+v", got)
	}
}

func TestMemoryDeleteUnknownIsNotFound(t *testing.T) {
	repo := NewMemoryRepository()
	if err := repo.Delete(context.Background(), "u1", "missing"); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("want errs.ErrNotFound, got %v", err)
	}
}

func TestMemoryUpsertRequiresUserID(t *testing.T) {
	repo := NewMemoryRepository()
	if err := repo.Upsert(context.Background(), domainmemory.Memory{Content: "x"}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("want invalid_argument, got %v", err)
	}
}

func TestMemoryListRequiresUserID(t *testing.T) {
	repo := NewMemoryRepository()
	if _, err := repo.List(context.Background(), ""); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("want invalid_argument, got %v", err)
	}
}
