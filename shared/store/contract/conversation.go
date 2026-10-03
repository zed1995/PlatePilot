package contract

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zed1995/platepilot/shared/domain/conversation"
	"github.com/zed1995/platepilot/shared/domain/errs"
	domaintool "github.com/zed1995/platepilot/shared/domain/tool"
	"github.com/zed1995/platepilot/shared/store"
)

// runConversationRepositoryContract pins thread metadata, optimistic
// checkpoints, and per-thread message paging for both adapters.
func runConversationRepositoryContract(t *testing.T, conversations store.ConversationRepository) {
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

	t.Run("conversation_upsert_get", func(t *testing.T) {
		conv := conversation.Conversation{
			ThreadID:     "c-thread-1",
			UserID:       "user-1",
			Title:        "lunch",
			CurrentState: conversation.StateIdle,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := conversations.Upsert(ctx, conv); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		got, err := conversations.Get(ctx, "c-thread-1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.UserID != "user-1" || got.Title != "lunch" || got.CurrentState != conversation.StateIdle {
			t.Fatalf("round trip lost fields: %+v", got)
		}
		conv.CurrentState = conversation.StateAwaitingConfirmation
		conv.Title = "lunch updated"
		if err := conversations.Upsert(ctx, conv); err != nil {
			t.Fatalf("second Upsert: %v", err)
		}
		got, err = conversations.Get(ctx, "c-thread-1")
		if err != nil {
			t.Fatalf("Get after update: %v", err)
		}
		if got.Title != "lunch updated" || got.CurrentState != conversation.StateAwaitingConfirmation {
			t.Fatalf("update not persisted: %+v", got)
		}
		if _, err := conversations.Get(ctx, "c-missing"); !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("Get missing: want ErrNotFound, got %v", err)
		}
	})

	t.Run("checkpoint_optimistic_version", func(t *testing.T) {
		threadID := "c-thread-cp"
		if err := conversations.Upsert(ctx, conversation.Conversation{
			ThreadID: threadID, UserID: "user-1",
			CurrentState: conversation.StateIdle,
			CreatedAt:    now, UpdatedAt: now, LastMessageAt: now,
		}); err != nil {
			t.Fatalf("seed conversation: %v", err)
		}
		first := conversation.Checkpoint{
			ThreadID:             threadID,
			Version:              1,
			State:                conversation.StateAwaitingClarification,
			PendingAction:        "ask_cuisine",
			MissingSlots:         []string{"cuisine"},
			EvidenceIDs:          []int64{10, 20},
			SelectedRestaurantID: 0,
			CreatedAt:            now,
		}
		if err := conversations.SaveCheckpoint(ctx, first); err != nil {
			t.Fatalf("SaveCheckpoint: %v", err)
		}
		stale := first
		stale.Version = 1
		if err := conversations.SaveCheckpoint(ctx, stale); !errors.Is(err, errs.ErrConflict) {
			t.Fatalf("equal version: want ErrConflict, got %v", err)
		}
		stale.Version = 0
		if err := conversations.SaveCheckpoint(ctx, stale); !errors.Is(err, errs.ErrConflict) {
			t.Fatalf("stale version: want ErrConflict, got %v", err)
		}
		got, err := conversations.LoadCheckpoint(ctx, threadID)
		if err != nil {
			t.Fatalf("LoadCheckpoint: %v", err)
		}
		if got.Version != 1 || got.PendingAction != "ask_cuisine" ||
			len(got.MissingSlots) != 1 || got.MissingSlots[0] != "cuisine" ||
			len(got.EvidenceIDs) != 2 || got.EvidenceIDs[1] != 20 {
			t.Fatalf("checkpoint round trip mismatch: %+v", got)
		}

		second := first
		second.Version = 2
		second.State = conversation.StateAwaitingConfirmation
		second.PendingAction = "confirm_restaurant"
		second.SelectedRestaurantID = 42
		if err := conversations.SaveCheckpoint(ctx, second); err != nil {
			t.Fatalf("SaveCheckpoint newer: %v", err)
		}
		got, err = conversations.LoadCheckpoint(ctx, threadID)
		if err != nil {
			t.Fatalf("LoadCheckpoint after update: %v", err)
		}
		if got.Version != 2 || got.SelectedRestaurantID != 42 || got.State != conversation.StateAwaitingConfirmation {
			t.Fatalf("newer checkpoint not stored: %+v", got)
		}
		if _, err := conversations.LoadCheckpoint(ctx, "c-missing"); !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("LoadCheckpoint missing: want ErrNotFound, got %v", err)
		}
	})

	t.Run("messages_append_paginate_isolate", func(t *testing.T) {
		threadID := "c-thread-msgs"
		if err := conversations.Upsert(ctx, conversation.Conversation{
			ThreadID: threadID, UserID: "user-1",
			CurrentState: conversation.StateIdle,
			CreatedAt:    now, UpdatedAt: now, LastMessageAt: now,
		}); err != nil {
			t.Fatalf("seed conversation: %v", err)
		}
		appendMsg := func(role, content string, toolCalls []domaintool.ToolCall, evidence []int64) conversation.Message {
			msg := conversation.Message{
				ThreadID:    threadID,
				Role:        role,
				Content:     content,
				ToolCalls:   toolCalls,
				EvidenceIDs: evidence,
				CreatedAt:   now,
			}
			if err := conversations.AppendMessage(ctx, msg); err != nil {
				t.Fatalf("AppendMessage %s: %v", content, err)
			}
			return msg
		}
		appendMsg(conversation.RoleUser, "想吃意大利菜", nil, nil)
		assistantCall := []domaintool.ToolCall{{ID: "call-1", Name: "search_restaurants", Arguments: []byte(`{"query":"italian"}`)}}
		appendMsg(conversation.RoleAssistant, "", assistantCall, nil)
		appendMsg(conversation.RoleTool, "found 3", nil, nil)
		appendMsg(conversation.RoleAssistant, "推荐这几家 [^1]", nil, []int64{1})

		all, err := conversations.ListMessages(ctx, threadID, 0, "")
		if err != nil {
			t.Fatalf("ListMessages: %v", err)
		}
		if len(all) != 4 {
			t.Fatalf("len = %d, want 4", len(all))
		}
		for i, msg := range all {
			if msg.Seq != int64(i+1) || msg.MessageID == "" {
				t.Fatalf("seq/id not assigned: %+v", msg)
			}
		}
		if len(all[1].ToolCalls) != 1 || all[1].ToolCalls[0].Name != "search_restaurants" {
			t.Fatalf("tool_calls not persisted: %+v", all[1].ToolCalls)
		}
		if len(all[3].EvidenceIDs) != 1 || all[3].EvidenceIDs[0] != 1 {
			t.Fatalf("evidence_ids not persisted: %+v", all[3].EvidenceIDs)
		}

		// Newest two, still oldest-first.
		page, err := conversations.ListMessages(ctx, threadID, 2, "")
		if err != nil {
			t.Fatalf("ListMessages paged: %v", err)
		}
		if len(page) != 2 || page[0].Content != "found 3" || page[1].Content != "推荐这几家 [^1]" {
			t.Fatalf("page = %+v", page)
		}

		// Exclusive cursor.
		before, err := conversations.ListMessages(ctx, threadID, 10, all[2].MessageID)
		if err != nil {
			t.Fatalf("ListMessages before: %v", err)
		}
		if len(before) != 2 || before[0].Seq != 1 || before[1].Seq != 2 {
			t.Fatalf("before page = %+v", before)
		}

		// No cross-thread leakage.
		other, err := conversations.ListMessages(ctx, "c-other-thread", 0, "")
		if err != nil {
			t.Fatalf("ListMessages other: %v", err)
		}
		if len(other) != 0 {
			t.Fatalf("cross-thread leakage: %+v", other)
		}
	})
}
