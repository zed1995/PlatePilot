package conversation

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestConversationAndCheckpointJSONRoundTrip(t *testing.T) {
	now := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	conv := Conversation{
		ThreadID:      "t1",
		UserID:        "u1",
		Title:         "quiet dinner",
		CurrentState:  StateAwaitingClarification,
		CreatedAt:     now,
		UpdatedAt:     now,
		LastMessageAt: now,
	}
	checkpoint := Checkpoint{
		ThreadID:             "t1",
		Version:              3,
		State:                StateAwaitingConfirmation,
		PendingAction:        "confirm_reservation",
		MissingSlots:         []string{"party_size"},
		EvidenceIDs:          []int64{1, 2},
		SelectedRestaurantID: 1,
		CreatedAt:            now,
	}

	var convDecoded Conversation
	data, _ := json.Marshal(conv)
	if err := json.Unmarshal(data, &convDecoded); err != nil {
		t.Fatalf("unmarshal conversation: %v", err)
	}
	if !reflect.DeepEqual(conv, convDecoded) {
		t.Fatalf("conversation round trip mismatch: got %+v want %+v", convDecoded, conv)
	}

	var checkpointDecoded Checkpoint
	data, _ = json.Marshal(checkpoint)
	if err := json.Unmarshal(data, &checkpointDecoded); err != nil {
		t.Fatalf("unmarshal checkpoint: %v", err)
	}
	if !reflect.DeepEqual(checkpoint, checkpointDecoded) {
		t.Fatalf("checkpoint round trip mismatch: got %+v want %+v", checkpointDecoded, checkpoint)
	}
}
