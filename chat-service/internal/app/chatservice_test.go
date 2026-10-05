package app

import (
	"testing"

	"github.com/zed1995/platepilot/chat-service/internal/agent"
	"github.com/zed1995/platepilot/chat-service/internal/httpapi"
)

// The awaiting-input event has to survive the agent-to-transport hop, and it has
// to do so field by field: the transport's payload() refuses an event type it
// does not know, so a mapping that quietly dropped the pending state would
// arrive as a frame with no state — which a client routes on.
func TestToStreamEventCarriesThePendingState(t *testing.T) {
	out := toStreamEvent(agent.Event{
		Type:          agent.EventAwaitingInput,
		RunID:         "r1",
		ThreadID:      "t1",
		State:         "awaiting_clarification",
		PendingAction: "resolve_restaurant",
		MissingSlots:  []string{"restaurant_id"},
	})

	if out.Type != httpapi.StreamAwaitingInput {
		t.Fatalf("type = %q, want %q", out.Type, httpapi.StreamAwaitingInput)
	}
	if out.State != "awaiting_clarification" {
		t.Fatalf("state = %q", out.State)
	}
	if out.PendingAction != "resolve_restaurant" {
		t.Fatalf("pending action = %q", out.PendingAction)
	}
	if len(out.MissingSlots) != 1 || out.MissingSlots[0] != "restaurant_id" {
		t.Fatalf("missing slots = %v", out.MissingSlots)
	}
}

// A plain turn must not acquire a pending state on the way out: a client that
// keys off the presence of state would treat every answer as a question.
func TestToStreamEventLeavesThePendingStateEmptyOnAnAnswer(t *testing.T) {
	out := toStreamEvent(agent.Event{Type: agent.EventDelta, ThreadID: "t1", Delta: "你好"})
	if out.State != "" || out.PendingAction != "" || len(out.MissingSlots) != 0 {
		t.Fatalf("a text delta grew a pending state: %+v", out)
	}
}

// A confirmation ask is its summary. A frame that reported "something needs
// approving" without saying what would leave the user with nothing to approve,
// so the sentence has to survive the agent-to-transport hop intact.
func TestToStreamEventCarriesTheConfirmationSummary(t *testing.T) {
	const summary = "确认预约：Joe's Pizza\n时间：2026-10-10 19:00\n人数：2 人"

	out := toStreamEvent(agent.Event{
		Type:                agent.EventConfirmationRequired,
		RunID:               "r1",
		ThreadID:            "t1",
		State:               "awaiting_confirmation",
		PendingAction:       "request_reservation",
		ConfirmationSummary: summary,
	})

	if out.Type != httpapi.StreamConfirmationRequired {
		t.Fatalf("type = %q, want %q", out.Type, httpapi.StreamConfirmationRequired)
	}
	if out.ConfirmationSummary != summary {
		t.Fatalf("summary = %q, want it verbatim", out.ConfirmationSummary)
	}
	if out.PendingAction != "request_reservation" {
		t.Fatalf("pending action = %q", out.PendingAction)
	}
}

// A plain turn must not acquire a summary on the way out: a client that keys
// off the presence of one would offer the user a yes/no for an answer.
func TestToStreamEventLeavesTheConfirmationSummaryEmptyOnAnAnswer(t *testing.T) {
	out := toStreamEvent(agent.Event{Type: agent.EventDelta, ThreadID: "t1", Delta: "你好"})
	if out.ConfirmationSummary != "" {
		t.Fatalf("a text delta grew a confirmation summary: %q", out.ConfirmationSummary)
	}
}

// The replacement body has to survive the agent-to-transport hop, and it has to
// land in its own field rather than in Delta: a client that could not tell an
// increment from a replacement would append the corrected answer to the text it
// is meant to replace.
func TestToStreamEventCarriesTheReplacementText(t *testing.T) {
	out := toStreamEvent(agent.Event{
		Type:     agent.EventAnswerReplace,
		RunID:    "r1",
		ThreadID: "t1",
		Text:     "校准后的完整正文[^10]",
	})

	if out.Type != httpapi.StreamReplace {
		t.Fatalf("type = %q, want %q", out.Type, httpapi.StreamReplace)
	}
	if out.ReplaceText != "校准后的完整正文[^10]" {
		t.Fatalf("replace text = %q", out.ReplaceText)
	}
	if out.Delta != "" {
		t.Fatalf("a replacement must not travel as an increment: delta = %q", out.Delta)
	}
}

// A plain delta must not acquire a replacement body on the way out, for the
// same reason in reverse.
func TestToStreamEventLeavesTheReplacementEmptyOnADelta(t *testing.T) {
	out := toStreamEvent(agent.Event{Type: agent.EventDelta, ThreadID: "t1", Delta: "你好"})
	if out.ReplaceText != "" {
		t.Fatalf("a delta grew a replacement body: %q", out.ReplaceText)
	}
}

// The error code has to survive the hop, because it is the only part of a
// failed turn a client can act on: `conflict` means the thread was being
// written by another request and the message is worth retrying, while
// `provider_timeout` is not. An error frame without its code is a dead end.
func TestToStreamEventCarriesTheErrorCode(t *testing.T) {
	out := toStreamEvent(agent.Event{
		Type:     agent.EventError,
		ThreadID: "t1",
		Code:     "conflict",
		Message:  "线程正被另一轮对话修改，请重试",
	})

	if out.Type != httpapi.StreamError {
		t.Fatalf("type = %q, want %q", out.Type, httpapi.StreamError)
	}
	if out.Code != "conflict" {
		t.Fatalf("code = %q, want conflict", out.Code)
	}
	if out.Message == "" {
		t.Fatal("the frame must explain the failure to the user")
	}
}
