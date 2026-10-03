package httpapi

import (
	"encoding/json"
	"testing"
)

// everyStreamEventType is the closed set the transport promises the front end.
//
// It is listed here rather than derived from the constants so that adding an
// event is a deliberate edit in two places: payload() returns an error for an
// unmapped type, and this list is what proves every mapped type is reachable.
func everyStreamEventType() []StreamEventType {
	return []StreamEventType{
		StreamStart,
		StreamDelta,
		StreamToolStart,
		StreamToolFinish,
		StreamCitation,
		StreamAwaitingInput,
		StreamConfirmationRequired,
		StreamMemorySaved,
		StreamEnd,
		StreamError,
	}
}

// An unmapped event type desynchronizes the client's state machine, so payload
// refuses rather than emitting a nameless frame. That makes an incomplete sweep
// a runtime failure, which is why the sweep is asserted.
func TestEveryStreamEventTypeHasAPayload(t *testing.T) {
	for _, eventType := range everyStreamEventType() {
		// Parenthesised because a composite literal followed by a method call is
		// ambiguous with the if-block.
		if _, err := (StreamEvent{Type: eventType}).payload(); err != nil {
			t.Fatalf("event %q has no payload: %v", eventType, err)
		}
	}
}

func TestUnknownStreamEventTypeIsRefused(t *testing.T) {
	if _, err := (StreamEvent{Type: "state.made_up"}).payload(); err == nil {
		t.Fatal("an unmapped event type must not be serialised")
	}
}

// The memory frame carries the sentence that was kept, because that is the
// memory: a frame with only an id would tell the user something was saved and
// make them go look it up.
func TestMemorySavedStreamEventCarriesTheContent(t *testing.T) {
	data, err := (StreamEvent{
		Type:          StreamMemorySaved,
		MemoryID:      "mem-1",
		MemoryType:    "constraint",
		MemoryContent: "不吃辣",
	}).payload()
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("payload is not valid JSON: %v (%s)", err, data)
	}
	if decoded["memory_id"] != "mem-1" {
		t.Fatalf("memory_id = %v", decoded["memory_id"])
	}
	if decoded["memory_type"] != "constraint" {
		t.Fatalf("memory_type = %v", decoded["memory_type"])
	}
	if decoded["content"] != "不吃辣" {
		t.Fatalf("content = %v", decoded["content"])
	}
	// A first save is not a refresh, and the flag is omitted rather than sent
	// false: a client keying off its presence must not read a first save as a
	// duplicate.
	if _, present := decoded["refreshed"]; present {
		t.Fatalf("refreshed must be omitted on a first save: %s", data)
	}
}

func TestMemorySavedStreamEventMarksARefresh(t *testing.T) {
	data, err := (StreamEvent{
		Type: StreamMemorySaved, MemoryID: "mem-1", MemoryContent: "不吃辣", MemoryRefreshed: true,
	}).payload()
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["refreshed"] != true {
		t.Fatalf("refreshed = %v, want true", decoded["refreshed"])
	}
}

// The awaiting-input frame carries the checkpoint's own vocabulary, so a client
// that stored it could reconstruct the pending state and the read path cannot
// disagree with the stream about what "waiting" means.
func TestAwaitingInputStreamEventCarriesThePendingState(t *testing.T) {
	data, err := (StreamEvent{
		Type:          StreamAwaitingInput,
		State:         "awaiting_clarification",
		PendingAction: "resolve_restaurant",
		MissingSlots:  []string{"restaurant_id"},
	}).payload()
	if err != nil {
		t.Fatal(err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("payload is not valid JSON: %v (%s)", err, data)
	}
	if got := decoded["state"]; got != "awaiting_clarification" {
		t.Fatalf("state = %v", got)
	}
	if got := decoded["pending_action"]; got != "resolve_restaurant" {
		t.Fatalf("pending_action = %v", got)
	}
	slots, ok := decoded["missing_slots"].([]any)
	if !ok || len(slots) != 1 || slots[0] != "restaurant_id" {
		t.Fatalf("missing_slots = %v", decoded["missing_slots"])
	}
}

// The two optional fields are omitted when empty: a client that keys off the
// presence of pending_action must not see an empty string as a parked action.
func TestAwaitingInputStreamEventOmitsEmptyPendingFields(t *testing.T) {
	data, err := (StreamEvent{Type: StreamAwaitingInput, State: "awaiting_clarification"}).payload()
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"pending_action", "missing_slots"} {
		if _, present := decoded[absent]; present {
			t.Fatalf("%s must be omitted when empty: %s", absent, data)
		}
	}
}
