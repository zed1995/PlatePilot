package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zed/platepilot/shared/domain/conversation"
	"github.com/zed/platepilot/shared/domain/errs"
)

func TestConversationUpsertAndGet(t *testing.T) {
	repo := NewConversationRepository()
	ctx := context.Background()
	now := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	conv := conversation.Conversation{ThreadID: "t1", UserID: "u1", CurrentState: conversation.StateIdle, CreatedAt: now}

	if err := repo.Upsert(ctx, conv); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := repo.Get(ctx, "t1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.UserID != "u1" {
		t.Fatalf("user id = %q", got.UserID)
	}
}

func TestConversationGetNotFound(t *testing.T) {
	repo := NewConversationRepository()
	_, err := repo.Get(context.Background(), "missing")
	if !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("want errs.ErrNotFound, got %v", err)
	}
}

func TestConversationUpsertRequiresThreadID(t *testing.T) {
	repo := NewConversationRepository()
	if err := repo.Upsert(context.Background(), conversation.Conversation{}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("want invalid_argument, got %v", err)
	}
}

func TestCheckpointSaveLoadAndVersioning(t *testing.T) {
	repo := NewConversationRepository()
	ctx := context.Background()

	if err := repo.SaveCheckpoint(ctx, conversation.Checkpoint{ThreadID: "t1", Version: 2, State: conversation.StateAwaitingConfirmation}); err != nil {
		t.Fatalf("save: %v", err)
	}
	// A stale replay must not overwrite a newer checkpoint.
	if err := repo.SaveCheckpoint(ctx, conversation.Checkpoint{ThreadID: "t1", Version: 1, State: conversation.StateAwaitingClarification}); err != nil {
		t.Fatalf("save stale: %v", err)
	}
	got, err := repo.LoadCheckpoint(ctx, "t1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Version != 2 || got.State != conversation.StateAwaitingConfirmation {
		t.Fatalf("stale checkpoint overwrote newer state: %+v", got)
	}
}

func TestCheckpointLoadNotFound(t *testing.T) {
	repo := NewConversationRepository()
	_, err := repo.LoadCheckpoint(context.Background(), "missing")
	if !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("want errs.ErrNotFound, got %v", err)
	}
}
