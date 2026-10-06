package answer

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	chatport "github.com/zed1995/platepilot/shared/chat"
	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/evidence"
)

// streamChat is a scripted double for the streaming surface.
//
// Stream serves a queue of raw chunks so a test controls exactly where the
// provider's token boundaries fall — the composer's line buffer only works if
// it survives a FOLLOWUPS tail split across two chunks. Complete serves a
// separate queue, which is what the corrective retry and the degraded fallback
// both reach for.
type streamChat struct {
	chunks []string
	// reasoning is served as reasoning-only chunks before chunks, so a test can
	// prove the thinking channel reaches the caller separately from the answer.
	reasoning []string
	streamErr error
	// recvErr, once the stream has produced errAfter chunks, is returned
	// instead of continuing. It reproduces a connection that dies mid-answer.
	recvErr  error
	errAfter int

	completes []string
	complete  []domainchat.ChatRequest

	reads     int
	opens     int
	closes    int
	completed int
}

func (s *streamChat) Stream(_ context.Context, _ domainchat.ChatRequest) (chatport.ChatStream, error) {
	if s.streamErr != nil {
		return nil, s.streamErr
	}
	s.opens++
	return &countingStream{chat: s}, nil
}

func (s *streamChat) Complete(_ context.Context, req domainchat.ChatRequest) (domainchat.ChatResponse, error) {
	s.completed++
	s.complete = append(s.complete, req)
	if len(s.completes) == 0 {
		return domainchat.ChatResponse{}, errors.New("streamChat: no more Complete responses queued")
	}
	content := s.completes[0]
	s.completes = s.completes[1:]
	return domainchat.ChatResponse{
		Message:      domainchat.ChatMessage{Role: domainchat.RoleAssistant, Content: content},
		FinishReason: domainchat.FinishReasonStop,
	}, nil
}

type countingStream struct{ chat *streamChat }

func (c *countingStream) Recv() (domainchat.ChatChunk, error) {
	if c.chat.recvErr != nil && c.chat.reads >= c.chat.errAfter {
		return domainchat.ChatChunk{}, c.chat.recvErr
	}
	if c.chat.reads < len(c.chat.reasoning) {
		reasoning := c.chat.reasoning[c.chat.reads]
		c.chat.reads++
		return domainchat.ChatChunk{Reasoning: reasoning}, nil
	}
	index := c.chat.reads - len(c.chat.reasoning)
	if index >= len(c.chat.chunks) {
		return domainchat.ChatChunk{}, io.EOF
	}
	delta := c.chat.chunks[index]
	c.chat.reads++
	return domainchat.ChatChunk{Delta: delta}, nil
}

func (c *countingStream) Close() error { c.chat.closes++; return nil }

// collectingSink records the deltas in order and hands them back.
func collectingSink() (Sink, *[]string) {
	deltas := &[]string{}
	return func(delta string) error {
		*deltas = append(*deltas, delta)
		return nil
	}, deltas
}

// A streamed answer is only usable if concatenating the deltas reproduces the
// validated text byte for byte: the client renders deltas, and the caller
// decides whether to send a replacement by comparing the two. Any drift — a
// missing newline, a leaked tail — would make every turn look corrected.
func TestComposeStreamConcatenatesToTheValidatedAnswer(t *testing.T) {
	chat := &streamChat{chunks: []string{
		"结论：A 更好",
		"[^10]",
		"[^20]。",
		"\n细节如此。",
		"\nFOLLOWUPS: [\"能订位吗?\"]",
	}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	sink, deltas := collectingSink()

	ans, err := c.ComposeStream(context.Background(), Input{
		Question: "哪家好？",
		Evidence: adequateEvidence(),
	}, StreamHandlers{Delta: sink})
	if err != nil {
		t.Fatal(err)
	}

	const want = "结论：A 更好[^10][^20]。\n细节如此。"
	if ans.Text != want {
		t.Fatalf("text = %q, want %q", ans.Text, want)
	}
	if got := strings.Join(*deltas, ""); got != want {
		t.Fatalf("streamed %q, validated %q", got, want)
	}
	if len(*deltas) < 2 {
		t.Fatalf("expected the answer to arrive incrementally, got %q", *deltas)
	}
	if got := citationIDs(ans); !equalInt64(got, []int64{10, 20}) {
		t.Fatalf("citations = %v, want [10 20]", got)
	}
	if len(ans.FollowUps) != 1 || ans.FollowUps[0] != "能订位吗?" {
		t.Fatalf("followUps = %v", ans.FollowUps)
	}
	if chat.closes != 1 {
		t.Fatalf("the stream was closed %d times, want 1", chat.closes)
	}
}

// The FOLLOWUPS line is machine-readable and must never be shown. The tail is
// split across chunks on purpose: a composer that decided on chunk boundaries
// instead of newline boundaries would publish the marker as it arrived.
func TestComposeStreamNeverPublishesTheFollowUpsTail(t *testing.T) {
	chat := &streamChat{chunks: []string{
		"正文在此。",
		"\nFOLLOW",
		"UPS: [\"a\",\"b\"]",
	}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	sink, deltas := collectingSink()

	ans, err := c.ComposeStream(context.Background(), Input{
		Question: "q",
		Evidence: adequateEvidence(),
	}, StreamHandlers{Delta: sink})
	if err != nil {
		t.Fatal(err)
	}

	streamed := strings.Join(*deltas, "")
	if streamed != "正文在此。" {
		t.Fatalf("streamed %q, want just the body", streamed)
	}
	for _, delta := range *deltas {
		for _, forbidden := range []string{"FOLLOW", "UPS:", "[^"} {
			if strings.Contains(delta, forbidden) {
				t.Fatalf("delta %q leaked %q", delta, forbidden)
			}
		}
	}
	if len(ans.FollowUps) != 2 {
		t.Fatalf("followUps = %v, want the parsed tail", ans.FollowUps)
	}
}

// A paragraph with no newline must still stream. Holding back the whole last
// line meant a one-paragraph answer published nothing until it was finished —
// the "everything appeared at once after two minutes" symptom.
func TestComposeStreamPublishesParagraphTextBeforeItEnds(t *testing.T) {
	chat := &streamChat{chunks: []string{
		"这是一段",
		"没有任何换行的",
		"长回答[^10]，后面还会继续。",
	}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	sink, deltas := collectingSink()

	ans, err := c.ComposeStream(context.Background(), Input{
		Question: "q",
		Evidence: adequateEvidence(),
	}, StreamHandlers{Delta: sink})
	if err != nil {
		t.Fatal(err)
	}
	if len(*deltas) < 3 {
		t.Fatalf("a paragraph must stream incrementally, got %d delta(s): %q", len(*deltas), *deltas)
	}
	if got := strings.Join(*deltas, ""); got != ans.Text {
		t.Fatalf("streamed %q, validated %q", got, ans.Text)
	}
}

// Reasoning travels on its own channel: it reaches the caller in order, and it
// never leaks into the answer the user keeps.
func TestComposeStreamPublishesReasoningSeparatelyFromTheAnswer(t *testing.T) {
	chat := &streamChat{
		reasoning: []string{"先看证据", "，再下结论"},
		chunks:    []string{"结论：A 更好[^10]"},
	}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	sink, deltas := collectingSink()
	thoughts := &[]string{}

	ans, err := c.ComposeStream(context.Background(), Input{
		Question: "q",
		Evidence: adequateEvidence(),
	}, StreamHandlers{
		Delta: sink,
		Thinking: func(reasoning string) error {
			*thoughts = append(*thoughts, reasoning)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(*thoughts, ""); got != "先看证据，再下结论" {
		t.Fatalf("thinking = %q", got)
	}
	for _, delta := range *deltas {
		for _, forbidden := range []string{"先看证据", "再下结论"} {
			if strings.Contains(delta, forbidden) {
				t.Fatalf("reasoning %q leaked into the answer text: %q", forbidden, delta)
			}
		}
	}
	if strings.Join(*deltas, "") != ans.Text {
		t.Fatalf("answer stream drifted from the validated text")
	}
}

// A provider that renders the answer without a thinking channel must still
// work: a nil Thinking handler simply hides the reasoning.
func TestComposeStreamToleratesMissingThinkingHandler(t *testing.T) {
	chat := &streamChat{reasoning: []string{"thinking"}, chunks: []string{"答案[^10]"}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	sink, deltas := collectingSink()

	if _, err := c.ComposeStream(context.Background(), Input{
		Question: "q",
		Evidence: adequateEvidence(),
	}, StreamHandlers{Delta: sink}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(*deltas, ""); !strings.Contains(got, "答案") {
		t.Fatalf("answer = %q", got)
	}
}

// The adequacy caveat is generated locally, so it can and must precede the
// model's first token. Emitting it afterwards would make the caveat appear
// above text the user had already read.
func TestComposeStreamEmitsTheAdequacyCaveatFirst(t *testing.T) {
	chat := &streamChat{chunks: []string{"结论：这家很好[^10]"}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	sink, deltas := collectingSink()

	items := []evidence.Evidence{ev(10)}
	ans, err := c.ComposeStream(context.Background(),
		Input{Question: "这家怎么样？", Evidence: items}, StreamHandlers{Delta: sink})
	if err != nil {
		t.Fatal(err)
	}
	lead := measureAdequacy(items).lead()
	if lead == "" {
		t.Fatal("the fixture must produce a caveat for this test to mean anything")
	}
	if len(*deltas) == 0 || (*deltas)[0] != lead {
		t.Fatalf("first delta = %q, want the caveat %q", (*deltas), lead)
	}
	if !strings.HasPrefix(ans.Text, lead) {
		t.Fatalf("text = %q, want it prefixed by %q", ans.Text, lead)
	}
	if got := strings.Join(*deltas, ""); got != ans.Text {
		t.Fatalf("streamed %q, validated %q", got, ans.Text)
	}
}

// An out-of-range marker is grounds to stop reading: the rest of the generation
// is going to be discarded, so paying for it is waste. The read counter is what
// proves the composer stopped rather than draining the stream.
func TestComposeStreamStopsReadingOnAnOutOfRangeCitation(t *testing.T) {
	chat := &streamChat{
		chunks:    []string{"结论", "[^99]", "后面还有很多", "还有很多"},
		completes: []string{"修正回答[^10]\nFOLLOWUPS: []"},
	}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	sink, deltas := collectingSink()

	ans, err := c.ComposeStream(context.Background(), Input{
		Question: "q",
		Evidence: adequateEvidence(),
	}, StreamHandlers{Delta: sink})
	if err != nil {
		t.Fatal(err)
	}
	if chat.reads >= len(chat.chunks) {
		t.Fatalf("the stream was drained (%d reads) despite a violation", chat.reads)
	}
	if ans.Text != "修正回答[^10]" {
		t.Fatalf("text = %q, want the corrected body", ans.Text)
	}
	// The corrected body is not what the client was shown, which is exactly the
	// signal the runner uses to send message.replace.
	if strings.Join(*deltas, "") == ans.Text {
		t.Fatalf("streamed and validated text are identical (%q); a replacement was owed", ans.Text)
	}
	if chat.completed != 1 {
		t.Fatalf("Complete called %d times, want 1 corrective retry", chat.completed)
	}
	last := chat.complete[0].Messages[len(chat.complete[0].Messages)-1]
	if last.Role != domainchat.RoleUser || !strings.Contains(last.Content, "99") {
		t.Fatalf("correction message = %+v", last)
	}
}

// A second violation is the end of the budget: the turn fails with the citation
// code so unverifiable text is never shipped.
func TestComposeStreamFailsAfterASecondViolation(t *testing.T) {
	chat := &streamChat{
		chunks:    []string{"第一次[^99]"},
		completes: []string{"第二次还是[^99]\nFOLLOWUPS: []"},
	}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	sink, _ := collectingSink()

	_, err := c.ComposeStream(context.Background(), Input{
		Question: "q",
		Evidence: []evidence.Evidence{ev(10)},
	}, StreamHandlers{Delta: sink})
	if errs.CodeOf(err) != errs.CodeAgentCitationViolation {
		t.Fatalf("err = %v, want agent_citation_violation", err)
	}
	if chat.completed != 1 {
		t.Fatalf("Complete called %d times, want 1", chat.completed)
	}
}

// A provider that cannot stream at all is reported before anything is
// published, so the caller can start over in one shot with nothing to retract.
func TestComposeStreamReportsAnUnopenableStreamBeforePublishing(t *testing.T) {
	chat := &streamChat{streamErr: errs.New(errs.CodeProviderUnavailable, "no stream")}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	sink, deltas := collectingSink()

	_, err := c.ComposeStream(context.Background(), Input{
		Question: "q",
		Evidence: adequateEvidence(),
	}, StreamHandlers{Delta: sink})
	if !errors.Is(err, ErrStreamUnavailable) {
		t.Fatalf("err = %v, want ErrStreamUnavailable", err)
	}
	if len(*deltas) != 0 {
		t.Fatalf("nothing must be published when the stream never opened: %q", *deltas)
	}
	if chat.completed != 0 {
		t.Fatalf("the composer must not spend a completion it was not asked for")
	}
}

// A stream that dies mid-answer has already put text on the wire. Recovering
// with one whole completion is honest as long as the caller replaces what was
// rendered — which it can detect, because the two differ.
func TestComposeStreamRecoversFromAMidStreamFailure(t *testing.T) {
	chat := &streamChat{
		chunks:    []string{"结论", "后面还有"},
		recvErr:   errors.New("connection reset"),
		errAfter:  1,
		completes: []string{"完整回答[^10]\nFOLLOWUPS: []"},
	}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	sink, deltas := collectingSink()

	ans, err := c.ComposeStream(context.Background(), Input{
		Question: "q",
		Evidence: adequateEvidence(),
	}, StreamHandlers{Delta: sink})
	if err != nil {
		t.Fatal(err)
	}
	if ans.Text != "完整回答[^10]" {
		t.Fatalf("text = %q", ans.Text)
	}
	if strings.Join(*deltas, "") == ans.Text {
		t.Fatal("a mid-stream failure must leave a replacement owed")
	}
	if chat.completed != 1 {
		t.Fatalf("Complete called %d times, want 1", chat.completed)
	}
}

// With no citable evidence there is nothing to ground a generation on, so the
// fixed refusal is published through the same channel as any other answer
// rather than reaching the client by a path of its own.
func TestComposeStreamPublishesTheRefusalWithoutCallingTheModel(t *testing.T) {
	chat := &streamChat{}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	sink, deltas := collectingSink()

	ans, err := c.ComposeStream(context.Background(), Input{Question: "哪家好？"}, StreamHandlers{Delta: sink})
	if err != nil {
		t.Fatal(err)
	}
	if ans.Text != RefusalAnswer {
		t.Fatalf("text = %q, want the fixed refusal", ans.Text)
	}
	if got := strings.Join(*deltas, ""); got != RefusalAnswer {
		t.Fatalf("streamed %q, want the refusal", got)
	}
	if chat.opens != 0 || chat.completed != 0 {
		t.Fatalf("the refusal must not cost a model call: opens=%d completes=%d",
			chat.opens, chat.completed)
	}
}

func TestComposeStreamRequiresAQuestion(t *testing.T) {
	c := NewComposer(Deps{Chat: &streamChat{}})
	sink, _ := collectingSink()
	_, err := c.ComposeStream(context.Background(), Input{Evidence: []evidence.Evidence{ev(1)}}, StreamHandlers{Delta: sink})
	if errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
}

// The one-shot and streaming paths must agree on the answer for the same
// scripted output; otherwise the switch would be a behaviour change rather than
// a delivery change.
func TestComposeStreamAgreesWithComposeOnTheSameOutput(t *testing.T) {
	const body = "结论：[^10]和[^20]。\n\n细节。\nFOLLOWUPS: [\"a\"]"

	streamed := &streamChat{chunks: []string{"结论：", "[^10]和[^20]。", "\n\n细节。", "\nFOLLOWUPS: [\"a\"]"}}
	c := NewComposer(Deps{Chat: streamed, Model: "m"})
	sink, _ := collectingSink()
	fromStream, err := c.ComposeStream(context.Background(), Input{
		Question: "q",
		Evidence: adequateEvidence(),
	}, StreamHandlers{Delta: sink})
	if err != nil {
		t.Fatal(err)
	}

	oneShot := NewComposer(Deps{Chat: &scriptedChat{contents: []string{body}}, Model: "m"})
	fromComplete, err := oneShot.Compose(context.Background(), Input{
		Question: "q",
		Evidence: adequateEvidence(),
	})
	if err != nil {
		t.Fatal(err)
	}

	if fromStream.Text != fromComplete.Text {
		t.Fatalf("streamed %q, one-shot %q", fromStream.Text, fromComplete.Text)
	}
	if !equalInt64(fromStream.Citations, fromComplete.Citations) {
		t.Fatalf("citations %v vs %v", fromStream.Citations, fromComplete.Citations)
	}
	if len(fromStream.FollowUps) != len(fromComplete.FollowUps) {
		t.Fatalf("followUps %v vs %v", fromStream.FollowUps, fromComplete.FollowUps)
	}
}
