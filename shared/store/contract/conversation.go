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
			ClarificationCount:   2,
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
		// The clarification counter has to survive a round trip, because the
		// cap it feeds is the only thing standing between a hard-to-disambiguate
		// name and an unbounded question loop — and every one of those rounds is
		// a separate request.
		if got.ClarificationCount != 2 {
			t.Fatalf("clarification count = %d, want 2", got.ClarificationCount)
		}

		second := first
		second.Version = 2
		second.State = conversation.StateAwaitingConfirmation
		second.PendingAction = "confirm_restaurant"
		second.SelectedRestaurantID = 42
		second.ClarificationCount = 0
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
		// Zeroing it has to work as well as setting it: a thread that resolved
		// its ambiguity must get a fresh budget rather than inherit a spent one.
		if got.ClarificationCount != 0 {
			t.Fatalf("clarification count = %d, want it reset to 0", got.ClarificationCount)
		}
		if _, err := conversations.LoadCheckpoint(ctx, "c-missing"); !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("LoadCheckpoint missing: want ErrNotFound, got %v", err)
		}
	})

	// The thread list is the only read that is scoped by owner rather than by
	// thread, so the property worth pinning is the boundary: everything the
	// caller owns, nothing they do not. Ordering belongs here too, because
	// "newest first" is the difference between picking up the thread that is
	// waiting for you and scrolling to find it.
	t.Run("threads_list_scoped_to_owner", func(t *testing.T) {
		upsert := func(threadID, userID string, updatedAt time.Time) {
			if err := conversations.Upsert(ctx, conversation.Conversation{
				ThreadID:      threadID,
				UserID:        userID,
				CurrentState:  conversation.StateIdle,
				CreatedAt:     now,
				UpdatedAt:     updatedAt,
				LastMessageAt: updatedAt,
			}); err != nil {
				t.Fatalf("upsert %s: %v", threadID, err)
			}
		}
		upsert("c-list-a1", "user-list", now.Add(3*time.Hour))
		upsert("c-list-a2", "user-list", now.Add(1*time.Hour))
		upsert("c-list-a3", "user-list", now.Add(2*time.Hour))
		upsert("c-list-b1", "user-other", now.Add(9*time.Hour))

		got, err := conversations.ListConversations(ctx, "user-list", 0, "")
		if err != nil {
			t.Fatalf("ListConversations: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("len = %d, want the caller's three threads", len(got))
		}
		for _, conv := range got {
			if conv.UserID != "user-list" {
				t.Fatalf("another user's thread came back: %+v", conv)
			}
		}
		wantOrder := []string{"c-list-a1", "c-list-a3", "c-list-a2"}
		for i, threadID := range wantOrder {
			if got[i].ThreadID != threadID {
				t.Fatalf("order = %+v, want newest first (%v)", got, wantOrder)
			}
		}

		page, err := conversations.ListConversations(ctx, "user-list", 2, "")
		if err != nil {
			t.Fatalf("ListConversations paged: %v", err)
		}
		if len(page) != 2 || page[0].ThreadID != "c-list-a1" {
			t.Fatalf("paged = %+v", page)
		}

		tail, err := conversations.ListConversations(ctx, "user-list", 0, "c-list-a1")
		if err != nil {
			t.Fatalf("ListConversations before: %v", err)
		}
		if len(tail) != 2 {
			t.Fatalf("exclusive cursor returned %d rows, want the two below it", len(tail))
		}
		for _, conv := range tail {
			if conv.ThreadID == "c-list-a1" {
				t.Fatal("the cursor row came back with the page below it")
			}
		}

		none, err := conversations.ListConversations(ctx, "user-nobody", 0, "")
		if err != nil {
			t.Fatalf("ListConversations empty: %v", err)
		}
		if len(none) != 0 {
			t.Fatalf("a user with no threads owns %d of them", len(none))
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

	t.Run("candidates_replace_and_isolate", func(t *testing.T) {
		threadID := "c-thread-cand"
		if err := conversations.Upsert(ctx, conversation.Conversation{
			ThreadID: threadID, UserID: "user-1",
			CurrentState: conversation.StateIdle,
			CreatedAt:    now, UpdatedAt: now, LastMessageAt: now,
		}); err != nil {
			t.Fatalf("seed conversation: %v", err)
		}

		// An empty snapshot is a valid request and returns an empty list, not
		// nil: a thread that has never searched and one whose snapshot was
		// cleared both have zero candidates.
		empty, err := conversations.ListCandidates(ctx, threadID)
		if err != nil {
			t.Fatalf("ListCandidates (empty): %v", err)
		}
		if len(empty) != 0 {
			t.Fatalf("new thread has candidates: %+v", empty)
		}

		first := []conversation.Candidate{
			{Position: 2, RestaurantID: 22, Name: "Second", Score: 0.7, Reasons: []string{"quiet (ambience)"}, SnapshotAt: now},
			{Position: 1, RestaurantID: 11, Name: "First", Score: 0.9, Reasons: []string{"matches borough"}, SnapshotAt: now},
			{Position: 3, RestaurantID: 33, Name: "Third", Score: 0.5, SnapshotAt: now},
		}
		if err := conversations.ReplaceCandidates(ctx, threadID, first); err != nil {
			t.Fatalf("ReplaceCandidates: %v", err)
		}
		got, err := conversations.ListCandidates(ctx, threadID)
		if err != nil {
			t.Fatalf("ListCandidates: %v", err)
		}
		// Position order, regardless of insert order.
		if len(got) != 3 || got[0].Position != 1 || got[1].Position != 2 || got[2].Position != 3 {
			t.Fatalf("candidates not in position order: %+v", got)
		}
		if got[1].RestaurantID != 22 || got[1].Name != "Second" || got[1].Score != 0.7 {
			t.Fatalf("position 2 lost fields: %+v", got[1])
		}
		if len(got[1].Reasons) != 1 || got[1].Reasons[0] != "quiet (ambience)" {
			t.Fatalf("reasons not persisted: %+v", got[1].Reasons)
		}
		if !got[0].SnapshotAt.Equal(now) {
			t.Fatalf("snapshot_at = %s, want %s", got[0].SnapshotAt, now)
		}
		for _, candidate := range got {
			if candidate.ThreadID != threadID {
				t.Fatalf("candidate carries thread %q, want %q", candidate.ThreadID, threadID)
			}
		}

		// Replacing with a shorter list must drop the stale tail. A merge would
		// leave position 3 pointing at a restaurant from the previous search,
		// and "第三家" would resolve to a place the user never saw this turn.
		if err := conversations.ReplaceCandidates(ctx, threadID, []conversation.Candidate{
			{Position: 1, RestaurantID: 44, Name: "Only", SnapshotAt: now},
		}); err != nil {
			t.Fatalf("ReplaceCandidates (shorter): %v", err)
		}
		got, err = conversations.ListCandidates(ctx, threadID)
		if err != nil {
			t.Fatalf("ListCandidates after replace: %v", err)
		}
		if len(got) != 1 || got[0].RestaurantID != 44 {
			t.Fatalf("replace left a stale tail: %+v", got)
		}

		// Duplicate positions are a caller defect: two restaurants at position 1
		// make the ordinal meaningless.
		if err := conversations.ReplaceCandidates(ctx, threadID, []conversation.Candidate{
			{Position: 1, RestaurantID: 1},
			{Position: 1, RestaurantID: 2},
		}); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("duplicate position: want invalid_argument, got %v", err)
		}

		// Thread isolation: another thread's snapshot is untouched.
		other, err := conversations.ListCandidates(ctx, "c-other-thread")
		if err != nil {
			t.Fatalf("ListCandidates other: %v", err)
		}
		if len(other) != 0 {
			t.Fatalf("cross-thread candidate leakage: %+v", other)
		}

		// Clearing the snapshot is explicit and empty.
		if err := conversations.ReplaceCandidates(ctx, threadID, nil); err != nil {
			t.Fatalf("ReplaceCandidates (clear): %v", err)
		}
		got, err = conversations.ListCandidates(ctx, threadID)
		if err != nil {
			t.Fatalf("ListCandidates after clear: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("snapshot not cleared: %+v", got)
		}
	})
}
