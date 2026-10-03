package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zed1995/platepilot/shared/domain/conversation"
	"github.com/zed1995/platepilot/shared/domain/errs"
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
	// A stale replay must be reported as a conflict, not silently dropped:
	// the caller has to know its state was not durably captured.
	if err := repo.SaveCheckpoint(ctx, conversation.Checkpoint{ThreadID: "t1", Version: 1, State: conversation.StateAwaitingClarification}); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("stale save: want errs.ErrConflict, got %v", err)
	}
	// Re-saving the same version is also a conflict (lost-update guard).
	if err := repo.SaveCheckpoint(ctx, conversation.Checkpoint{ThreadID: "t1", Version: 2, State: conversation.StateCompleted}); !errors.Is(err, errs.ErrConflict) {
		t.Fatalf("equal-version save: want errs.ErrConflict, got %v", err)
	}
	got, err := repo.LoadCheckpoint(ctx, "t1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Version != 2 || got.State != conversation.StateAwaitingConfirmation {
		t.Fatalf("stale checkpoint overwrote newer state: %+v", got)
	}
	// A strictly newer version overwrites.
	if err := repo.SaveCheckpoint(ctx, conversation.Checkpoint{ThreadID: "t1", Version: 3, State: conversation.StateCompleted}); err != nil {
		t.Fatalf("save newer: %v", err)
	}
}

func TestMessageAppendListAndPaging(t *testing.T) {
	repo := NewConversationRepository()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if err := repo.AppendMessage(ctx, conversation.Message{
			ThreadID: "t1",
			Role:     conversation.RoleUser,
			Content:  "message",
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	got, err := repo.ListMessages(ctx, "t1", 0, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 5 || got[0].Seq != 1 || got[4].Seq != 5 {
		t.Fatalf("list = %+v", got)
	}
	if got[0].MessageID == "" || got[0].CreatedAt.IsZero() {
		t.Fatal("message id and created_at should be assigned")
	}

	// Newest two before the head, still returned oldest-first.
	page, err := repo.ListMessages(ctx, "t1", 2, "")
	if err != nil {
		t.Fatalf("list limit: %v", err)
	}
	if len(page) != 2 || page[0].Seq != 4 || page[1].Seq != 5 {
		t.Fatalf("page = %+v", page)
	}

	// Paging before an explicit cursor is exclusive.
	page, err = repo.ListMessages(ctx, "t1", 10, got[2].MessageID)
	if err != nil {
		t.Fatalf("list before: %v", err)
	}
	if len(page) != 2 || page[0].Seq != 1 || page[1].Seq != 2 {
		t.Fatalf("before page = %+v", page)
	}

	// Threads must never share messages.
	if err := repo.AppendMessage(ctx, conversation.Message{ThreadID: "t2", Role: conversation.RoleUser, Content: "alone"}); err != nil {
		t.Fatalf("append other thread: %v", err)
	}
	other, err := repo.ListMessages(ctx, "t2", 0, "")
	if err != nil || len(other) != 1 {
		t.Fatalf("other thread messages = %+v, err=%v", other, err)
	}
}

func TestMessageAppendRequiresThreadAndRole(t *testing.T) {
	repo := NewConversationRepository()
	ctx := context.Background()
	if err := repo.AppendMessage(ctx, conversation.Message{Role: conversation.RoleUser}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("append without thread_id: want invalid_argument, got %v", err)
	}
	if err := repo.AppendMessage(ctx, conversation.Message{ThreadID: "t1"}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("append without role: want invalid_argument, got %v", err)
	}
	if _, err := repo.ListMessages(ctx, "", 0, ""); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("list without thread_id: want invalid_argument, got %v", err)
	}
}

func TestCheckpointLoadNotFound(t *testing.T) {
	repo := NewConversationRepository()
	_, err := repo.LoadCheckpoint(context.Background(), "missing")
	if !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("want errs.ErrNotFound, got %v", err)
	}
}
