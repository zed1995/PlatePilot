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
		StreamReplace,
		StreamToolStart,
		StreamToolFinish,
		StreamCitation,
		StreamAwaitingInput,
		StreamConfirmationRequired,
		StreamMemorySaved,
		StreamEnd,
		StreamError,
		StreamPhaseStarted,
		StreamPhaseFinished,
		StreamPhaseProgress,
		StreamStepStarted,
		StreamStepFinished,
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

// The replacement frame carries a whole answer body, not a diff. A client
// cannot compute one from the other, because the corrected text is a fresh
// generation rather than an edit of the first.
func TestReplaceStreamEventCarriesTheWholeText(t *testing.T) {
	data, err := (StreamEvent{
		Type:        StreamReplace,
		ReplaceText: "校准后的完整正文[^10]",
	}).payload()
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("payload is not valid JSON: %v (%s)", err, data)
	}
	if got := decoded["text"]; got != "校准后的完整正文[^10]" {
		t.Fatalf("text = %v", got)
	}
	// A replacement is not an increment: carrying the same bytes in `delta`
	// would make the two indistinguishable to a client that keys off the field.
	if _, present := decoded["delta"]; present {
		t.Fatalf("a replace frame must not carry a delta field: %s", data)
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

// The phase.started frame is the cue the front-end uses to switch the
// visible status spinner from "loading the thread" to whatever the new
// phase's title says. The frame must therefore carry a stable phase id, a
// machine-readable phase name, a human title, and a started_at instant;
// without the id a second plan round would replace the first round's row
// and the user would never know a tools loop happened.
func TestPhaseStartedFrameCarriesIdAndTitle(t *testing.T) {
	data, err := (StreamEvent{
		Type:      StreamPhaseStarted,
		PhaseID:   "plan-1",
		Phase:     "plan",
		Title:     "正在制定下一步计划",
		StartedAt: 1717700000000,
	}).payload()
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"phase_id", "phase", "title", "started_at"} {
		if _, present := decoded[want]; !present {
			t.Fatalf("phase.started must carry %s: %s", want, data)
		}
	}
	if decoded["phase_id"] != "plan-1" || decoded["phase"] != "plan" {
		t.Fatalf("id/name round-trip failed: %s", data)
	}
}

// A phase without an outcome is a started frame masquerading as a finished
// one. The field is omitted on success to keep the wire shape minimal, and
// is required on failure so the front-end can mark the row red instead of
// leaving it hanging.
func TestPhaseFinishedFrameOmitsOutcomeWhenEmpty(t *testing.T) {
	data, err := (StreamEvent{
		Type:       StreamPhaseFinished,
		PhaseID:    "plan-1",
		Phase:      "plan",
		FinishedAt: 1717700005000,
	}).payload()
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, present := decoded["outcome"]; present {
		t.Fatalf("outcome must be omitted when empty: %s", data)
	}
}

// The step frames are scoped inside a phase, so they carry the parent
// phase_id alongside their own step_id. A step row that could not be
// attached to its phase would be a free-floating spinner the front-end has
// to guess at, which is what phase_id is here to prevent.
func TestStepStartedFrameCarriesParentPhase(t *testing.T) {
	data, err := (StreamEvent{
		Type:      StreamStepStarted,
		PhaseID:   "ingress-1",
		Phase:     "ingress",
		StepID:    "i2",
		Step:      "embedding_memory",
		Title:     "检索长期记忆",
		StartedAt: 1717700001000,
	}).payload()
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["phase_id"] != "ingress-1" || decoded["step_id"] != "i2" {
		t.Fatalf("step ids round-trip failed: %s", data)
	}
	if decoded["step"] != "embedding_memory" {
		t.Fatalf("step name = %v", decoded["step"])
	}
}

// A phase.progress frame carries the new title for the row the front-end
// was already rendering. PhaseID is the only required key; title is the
// replacement string the server picked (typically "正在推理（已 N 秒）").
func TestPhaseProgressFrameRewritesOnlyTitleAndPhaseID(t *testing.T) {
	data, err := (StreamEvent{
		Type:    StreamPhaseProgress,
		PhaseID: "plan-1",
		Title:   "正在推理（已 6 秒）",
	}).payload()
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["phase_id"] != "plan-1" || decoded["title"] != "正在推理（已 6 秒）" {
		t.Fatalf("phase_id/title round-trip failed: %s", data)
	}
	// The shape must be minimal: a progress frame that also carried
	// finished_at or step fields would tempt a client into rendering a
	// closed phase on top of a running one.
	for _, absent := range []string{"phase", "step", "step_started_at", "finished_at", "outcome"} {
		if _, present := decoded[absent]; present {
			t.Fatalf("%s must be omitted from phase.progress: %s", absent, data)
		}
	}
}
