package memory

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestMemoryJSONRoundTrip(t *testing.T) {
	deleted := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	original := Memory{
		ID:         "m1",
		UserID:     "u1",
		Type:       MemoryTypePreference,
		Content:    "prefers quiet restaurants",
		Source:     "user_explicit",
		Confidence: 0.9,
		Embedding:  []float32{0.5, 0.5},
		CreatedAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		DeletedAt:  &deleted,
	}
	var decoded Memory
	data, _ := json.Marshal(original)
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(original, decoded) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", decoded, original)
	}
}
