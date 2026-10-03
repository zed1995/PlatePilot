package agent

import (
	"context"
	"testing"
	"time"

	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/conversation"
	"github.com/zed1995/platepilot/shared/testkit"
)

// TestMidTurnFailureLeavesRecoverablePendingCheckpoint covers M4-08's
// recovery contract: when a node decides more user input is needed and saves
// the pending checkpoint, a crash before finalize must leave a Resume-readable
// pending_action/missing_slots with no transcript messages.
func TestMidTurnFailureLeavesRecoverablePendingCheckpoint(t *testing.T) {
	// Only the persistence seam is under test, so construct directly and
	// skip NewRunner's chat-provider and graph-compilation requirements.
	runner := &Runner{deps: Deps{Conversations: testkit.NewConversationRepository()}}
	ctx := context.Background()

	// Simulates the planning node's decision, mid-turn before finalize:
	st := &TurnState{
		ThreadID:      "th-pending",
		UserID:        "user-9",
		State:         conversation.StateAwaitingClarification,
		PendingAction: "ask_missing_slots",
		MissingSlots:  []string{"borough", "cuisine"},
	}
	runner.persistPendingCheckpoint(ctx, st)
	if st.CheckpointVersion != 1 {
		t.Fatalf("checkpoint version = %d, want 1", st.CheckpointVersion)
	}

	// ...the turn then fails before finalize (no persistTurn call). Resume
	// must still surface the pending state.
	resumed, err := runner.Resume(ctx, "th-pending")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed.Checkpoint == nil {
		t.Fatal("expected a checkpoint")
	}
	cp := resumed.Checkpoint
	if cp.State != conversation.StateAwaitingClarification {
		t.Fatalf("state = %q, want awaiting_clarification", cp.State)
	}
	if cp.PendingAction != "ask_missing_slots" {
		t.Fatalf("pending action = %q", cp.PendingAction)
	}
	if len(cp.MissingSlots) != 2 || cp.MissingSlots[0] != "borough" || cp.MissingSlots[1] != "cuisine" {
		t.Fatalf("missing slots = %v", cp.MissingSlots)
	}
	if cp.Version != 1 {
		t.Fatalf("version = %d, want 1", cp.Version)
	}
	if len(resumed.Messages) != 0 {
		t.Fatalf("crashed turn must leave no transcript messages, got %+v", resumed.Messages)
	}
	if resumed.Conversation.CurrentState != conversation.StateAwaitingClarification {
		t.Fatalf("conversation state = %q", resumed.Conversation.CurrentState)
	}

	// A later completed turn settles the thread and increments the version.
	st2 := &TurnState{
		RunID:             "run-2",
		ThreadID:          "th-pending",
		UserID:            "user-9",
		UserInput:         "在曼哈顿吃日料",
		FinalAnswer:       &domainchat.Answer{Text: "为你找到曼哈顿日料"},
		CheckpointVersion: 1,
	}
	now := time.Now()
	runner.persistTurn(ctx, st2, now)

	resumed2, err := runner.Resume(ctx, "th-pending")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if resumed2.Checkpoint.Version != 2 || resumed2.Checkpoint.State != conversation.StateIdle {
		t.Fatalf("checkpoint after completed turn = %+v", resumed2.Checkpoint)
	}
	if resumed2.Checkpoint.PendingAction != "" || len(resumed2.Checkpoint.MissingSlots) != 0 {
		t.Fatalf("pending fields must be cleared: %+v", resumed2.Checkpoint)
	}
	if len(resumed2.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(resumed2.Messages))
	}
}
