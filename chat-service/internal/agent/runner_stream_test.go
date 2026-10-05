package agent_test

import (
	"context"
	"strings"
	"testing"

	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/errs"

	"github.com/zed1995/platepilot/chat-service/internal/agent"
	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
	"github.com/zed1995/platepilot/chat-service/internal/agent/tools"
)

// streamingRunner assembles a turn that recalls evidence for restaurant 42 and
// then composes an answer over it. The plan's second response ends the tool loop
// with plain assistant text, which is what a real model does when it has
// nothing left to look up.
func streamingRunner(t *testing.T, provider *scriptedProvider, streaming bool) *agent.Runner {
	t.Helper()
	registry := toolreg.New(0)
	register(t, registry, tools.RestaurantEvidenceEntry(&followUpEvidence{}))
	runner, err := agent.NewRunner(agent.Config{
		MaxToolRounds:   3,
		AnswerStreaming: streaming,
	}, agent.Deps{
		Chat:        provider,
		ToolCalling: provider,
		Registry:    registry,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner
}

// evidenceToolTurn scripts one evidence round: the model asks for restaurant
// 42's documents, then reports that it has them.
func evidenceToolTurn() []domainchat.ToolCallResponse {
	return []domainchat.ToolCallResponse{
		toolCallResponse("c1", tools.RestaurantEvidenceToolName,
			`{"restaurant_ids":[42],"query":"这家安静吗"}`),
		assistantText("资料已取到。"),
	}
}

// deltaChunks turns a list of text fragments into the chunk queue the scripted
// provider streams, so a test controls where the provider's token boundaries
// fall.
func deltaChunks(fragments ...string) []domainchat.ChatChunk {
	chunks := make([]domainchat.ChatChunk, 0, len(fragments))
	for _, fragment := range fragments {
		chunks = append(chunks, domainchat.ChatChunk{Delta: fragment})
	}
	return chunks
}

func eventTypes(events []agent.Event) []agent.EventType {
	types := make([]agent.EventType, 0, len(events))
	for _, ev := range events {
		types = append(types, ev.Type)
	}
	return types
}

func deltasOf(events []agent.Event) []string {
	var out []string
	for _, ev := range events {
		if ev.Type == agent.EventDelta {
			out = append(out, ev.Delta)
		}
	}
	return out
}

func findEvent(events []agent.Event, want agent.EventType) (agent.Event, bool) {
	for _, ev := range events {
		if ev.Type == want {
			return ev, true
		}
	}
	return agent.Event{}, false
}

// Under streaming the answer arrives as several message.delta frames that
// concatenate to exactly the text the turn persisted. That equality is what
// lets a client render deltas and still end up with the answer the server
// validated, without ever needing a replacement.
func TestStreamingTurnPublishesDeltasThatReconstructTheAnswer(t *testing.T) {
	provider := &scriptedProvider{
		supportTools: true,
		toolResps:    evidenceToolTurn(),
		chunks: []domainchat.ChatChunk{
			{Delta: "结论：这家很安静[^4201]。\n"},
			{Delta: "细节如下。\n"},
			{Delta: "FOLLOWUPS: [\"能订位吗?\"]"},
		},
	}
	runner := streamingRunner(t, provider, true)

	result, eventsCh := runner.Run(context.Background(), agent.TurnInput{
		ThreadID:  "th-stream",
		UserInput: "这家安静吗？",
	})
	events := drain(eventsCh)

	if result == nil || result.Answer == nil {
		t.Fatal("the turn must produce an answer")
	}
	const want = "结论：这家很安静[^4201]。\n细节如下。"
	if result.Answer.Text != want {
		t.Fatalf("text = %q, want %q", result.Answer.Text, want)
	}

	deltas := deltasOf(events)
	if len(deltas) < 2 {
		t.Fatalf("expected the answer to be streamed incrementally, got %q", deltas)
	}
	if got := strings.Join(deltas, ""); got != want {
		t.Fatalf("deltas concatenate to %q, want %q", got, want)
	}
	// The machine-readable tail is never part of the answer, so it must not
	// appear in a frame either.
	for _, delta := range deltas {
		if strings.Contains(delta, "FOLLOWUPS") {
			t.Fatalf("delta %q leaked the follow-ups tail", delta)
		}
	}
	if _, replaced := findEvent(events, agent.EventAnswerReplace); replaced {
		t.Fatalf("a valid first generation needs no replacement: %v", eventTypes(events))
	}
	citation, ok := findEvent(events, agent.EventCitation)
	if !ok {
		t.Fatalf("no citation frame in %v", eventTypes(events))
	}
	if len(citation.Citations) != 1 || citation.Citations[0] != 4201 {
		t.Fatalf("citations = %v, want [4201]", citation.Citations)
	}
}

// When the first generation cites something that is not in this turn's evidence
// set, the text is already on the wire. The correction has to arrive as a whole
// replacement — appending it would render the answer twice.
func TestStreamingTurnReplacesTheAnswerAfterACitationViolation(t *testing.T) {
	provider := &scriptedProvider{
		supportTools: true,
		toolResps:    evidenceToolTurn(),
		// The first chunk ends a line so a delta really is published before the
		// violation is spotted — that is what makes the replacement necessary
		// rather than merely courteous.
		chunks: deltaChunks("结论：这家很安静。\n", "[^999]。后面还有"),
		completeResps: []domainchat.ChatResponse{{
			Message: domainchat.ChatMessage{
				Role:    domainchat.RoleAssistant,
				Content: "校准后的回答[^4201]。\nFOLLOWUPS: []",
			},
			FinishReason: domainchat.FinishReasonStop,
		}},
	}
	runner := streamingRunner(t, provider, true)

	result, eventsCh := runner.Run(context.Background(), agent.TurnInput{
		ThreadID:  "th-replace",
		UserInput: "这家安静吗？",
	})
	events := drain(eventsCh)

	if result == nil || result.Answer == nil {
		t.Fatal("the turn must produce an answer")
	}
	if result.Answer.Text != "校准后的回答[^4201]。" {
		t.Fatalf("text = %q, want the corrected body", result.Answer.Text)
	}

	streamed := strings.Join(deltasOf(events), "")
	if streamed == result.Answer.Text {
		t.Fatal("the fixture must actually stream something that needs replacing")
	}

	replacement, ok := findEvent(events, agent.EventAnswerReplace)
	if !ok {
		t.Fatalf("no message.replace frame in %v", eventTypes(events))
	}
	if replacement.Text != result.Answer.Text {
		t.Fatalf("replacement = %q, want the validated answer %q",
			replacement.Text, result.Answer.Text)
	}

	// Order matters: the replacement has to land after the text it corrects and
	// before the citations that annotate it, or a client would either append it
	// to the old body or attach footnotes to text it is about to discard.
	types := eventTypes(events)
	lastDelta, replaceAt, citationAt := -1, -1, -1
	for i, typ := range types {
		switch typ {
		case agent.EventDelta:
			lastDelta = i
		case agent.EventAnswerReplace:
			replaceAt = i
		case agent.EventCitation:
			citationAt = i
		}
	}
	if replaceAt < lastDelta || replaceAt > citationAt {
		t.Fatalf("replace out of order in %v", types)
	}
	if provider.completeReqs == nil {
		t.Fatal("the corrective retry must go through Complete")
	}
	last := provider.completeReqs[len(provider.completeReqs)-1]
	correction := last.Messages[len(last.Messages)-1]
	if !strings.Contains(correction.Content, "999") {
		t.Fatalf("the retry was not told what it got wrong: %q", correction.Content)
	}
}

// With streaming switched off the turn behaves exactly as it did before the
// switch existed: one delta carrying the whole answer, and no replacement. This
// is the documented fallback for a client that does not understand the new
// event.
func TestTurnWithoutStreamingSendsOneDeltaAndNoReplacement(t *testing.T) {
	provider := &scriptedProvider{
		supportTools: true,
		toolResps:    evidenceToolTurn(),
		completeResps: []domainchat.ChatResponse{{
			Message: domainchat.ChatMessage{
				Role:    domainchat.RoleAssistant,
				Content: "结论：这家很安静[^4201]。\nFOLLOWUPS: []",
			},
			FinishReason: domainchat.FinishReasonStop,
		}},
	}
	runner := streamingRunner(t, provider, false)

	result, eventsCh := runner.Run(context.Background(), agent.TurnInput{
		ThreadID:  "th-off",
		UserInput: "这家安静吗？",
	})
	events := drain(eventsCh)

	const want = "结论：这家很安静[^4201]。"
	if result == nil || result.Answer == nil || result.Answer.Text != want {
		t.Fatalf("answer = %+v, want %q", result.Answer, want)
	}
	deltas := deltasOf(events)
	if len(deltas) != 1 || deltas[0] != want {
		t.Fatalf("deltas = %q, want a single frame carrying the answer", deltas)
	}
	if _, replaced := findEvent(events, agent.EventAnswerReplace); replaced {
		t.Fatalf("the one-shot path never replaces: %v", eventTypes(events))
	}
}

// A provider that cannot stream must not cost the turn. Nothing has been
// published when the stream fails to open, so the composer composes in one shot
// and the turn records why.
func TestStreamingTurnFallsBackWhenTheProviderCannotStream(t *testing.T) {
	provider := &scriptedProvider{
		supportTools: true,
		toolResps:    evidenceToolTurn(),
		streamErr:    errs.New(errs.CodeProviderUnavailable, "endpoint has no streaming support"),
		completeResps: []domainchat.ChatResponse{{
			Message: domainchat.ChatMessage{
				Role:    domainchat.RoleAssistant,
				Content: "结论：这家很安静[^4201]。\nFOLLOWUPS: []",
			},
			FinishReason: domainchat.FinishReasonStop,
		}},
	}
	runner := streamingRunner(t, provider, true)

	result, eventsCh := runner.Run(context.Background(), agent.TurnInput{
		ThreadID:  "th-fallback",
		UserInput: "这家安静吗？",
	})
	events := drain(eventsCh)

	const want = "结论：这家很安静[^4201]。"
	if result == nil || result.Answer == nil || result.Answer.Text != want {
		t.Fatalf("answer = %+v, want %q", result.Answer, want)
	}
	deltas := deltasOf(events)
	if len(deltas) != 1 || deltas[0] != want {
		t.Fatalf("deltas = %q, want one frame carrying the whole answer", deltas)
	}
	if _, replaced := findEvent(events, agent.EventAnswerReplace); replaced {
		t.Fatalf("nothing was published, so nothing needs replacing: %v", eventTypes(events))
	}
	if !hasWarning(result.Warnings, "答案流式不可用") {
		t.Fatalf("warnings = %v, want the streaming degradation recorded", result.Warnings)
	}
}

func hasWarning(warnings []string, needle string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, needle) {
			return true
		}
	}
	return false
}
