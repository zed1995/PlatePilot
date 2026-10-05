package agent_test

// OPT-06: how one round's tool calls are scheduled.
//
// The tool under test is a synthetic read-only one rather than a real member of
// the registry, because what is being asserted is *when* a call runs and *where
// its result lands*, not what it returns. A stub under a real tool's name would
// make these look like assertions about that tool, and the scheduling rule is
// the same for every read-only call.
//
// The concurrency is deliberately made observable rather than timed. Each
// handler waits at a rendezvous that only opens once a given number of calls are
// inside their body at the same time, so a serial implementation cannot pass by
// being fast: it blocks, and the test says why. A sleep-and-measure test would
// pass on a machine that happened to run the two calls a millisecond apart for
// unrelated reasons.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/errs"
	domaintool "github.com/zed1995/platepilot/shared/domain/tool"
	"github.com/zed1995/platepilot/shared/store/memory"

	"github.com/zed1995/platepilot/chat-service/internal/agent"
	"github.com/zed1995/platepilot/chat-service/internal/agent/audit"
	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
	"github.com/zed1995/platepilot/chat-service/internal/agent/tools"
)

const (
	// noteToolName is the synthetic read-only tool these tests register.
	noteToolName = "read_note"
	// rendezvousTimeout bounds how long a handler waits for its peers. It only
	// elapses when the calls did not overlap, which is the failure being
	// reported.
	rendezvousTimeout = 3 * time.Second
	// slowNote is the note whose handler takes longer to return after the
	// rendezvous. The delay does two jobs: it makes the call asked for first
	// the one that finishes last, which is what lets a test tell "the rows
	// were written in completion order" apart from "the insert order happened
	// to match the call order"; and it keeps a call in flight long enough for
	// the launch loop to start its successors, which is the only way the
	// concurrency bound is observable at all.
	slowNote = "slow"
	// slowNoteRead is how long that takes. It is a margin rather than a
	// measurement: the work being overlapped is a few microseconds of JSON and
	// a map write, so anything in the tens of milliseconds is orders of
	// magnitude clear of it, and the whole file still runs in well under a
	// second.
	slowNoteRead = 50 * time.Millisecond
)

// toolRendezvous releases the handlers only once want of them are inside at the
// same time, and records the highest number that ever were.
type toolRendezvous struct {
	mu       sync.Mutex
	inFlight int
	peak     int
	ready    chan struct{}
	want     int
}

func newToolRendezvous(want int) *toolRendezvous {
	return &toolRendezvous{ready: make(chan struct{}), want: want}
}

// enter blocks until want handlers are inside. It returns an error instead of
// waiting forever, so a serial implementation fails with a diagnosis rather
// than a hang.
//
// A handler that gives up never got inside, so it withdraws its own count
// before returning. Leaving the count behind would make the next handler look
// like the peer the first one was waiting for, and a serial round would then
// report a peak of two — the fixture would be agreeing with the implementation
// it is supposed to be checking.
func (r *toolRendezvous) enter(ctx context.Context) error {
	r.mu.Lock()
	r.inFlight++
	if r.inFlight > r.peak {
		r.peak = r.inFlight
	}
	if r.inFlight >= r.want {
		select {
		case <-r.ready:
		default:
			close(r.ready)
		}
	}
	peak := r.peak
	r.mu.Unlock()

	select {
	case <-r.ready:
		return nil
	default:
	}
	timer := time.NewTimer(rendezvousTimeout)
	defer timer.Stop()
	select {
	case <-r.ready:
		return nil
	case <-timer.C:
		r.leave()
		return fmt.Errorf("only %d of %d tool calls were ever inside the tool at once: "+
			"the round did not run them concurrently", peak, r.want)
	case <-ctx.Done():
		r.leave()
		return ctx.Err()
	}
}

func (r *toolRendezvous) leave() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inFlight--
}

func (r *toolRendezvous) currentPeak() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.peak
}

// noteEntry is a read-only tool with no side effects on the turn's state: it
// returns what it was given, so a tool message can be traced back to the call
// that produced it.
func noteEntry(rv *toolRendezvous, calls *sync.Map) toolreg.Entry {
	return toolreg.Entry{
		Spec: domaintool.ToolSpec{
			Name:        noteToolName,
			Description: "test stub that reads a note",
			Parameters: json.RawMessage(
				`{"type":"object","properties":{"note":{"type":"string"}},` +
					`"required":["note"],"additionalProperties":false}`),
			ReadOnly: true,
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (domaintool.ToolResult, error) {
			var args struct {
				Note string `json:"note"`
			}
			if err := json.Unmarshal(raw, &args); err != nil {
				return domaintool.ToolResult{}, err
			}
			if calls != nil {
				calls.Store(args.Note, true)
			}
			if err := rv.enter(ctx); err != nil {
				return domaintool.ToolResult{
					Status: domaintool.ToolStatusError,
					Error:  errs.New(errs.CodeInternal, err.Error()),
				}, nil
			}
			defer rv.leave()
			if args.Note == slowNote {
				time.Sleep(slowNoteRead)
			}
			return domaintool.ToolResult{Status: domaintool.ToolStatusOK, Content: "note: " + args.Note}, nil
		},
	}
}

// noteCall is one call to the read_note stub: the id the assistant gives the
// call, and the note it asks that call to read. The note is kept separate from
// the id because the two order differently in the fixtures below — the note
// asked for first is the one that takes longer to read — and a helper that
// derived one from the other could not express that.
type noteCall struct{ id, note string }

// multiCallResponse scripts one assistant message that asks for several tools at
// once, which is the shape a provider configured for parallel tool calls
// returns.
func multiCallResponse(calls ...noteCall) domainchat.ToolCallResponse {
	message := domainchat.ChatMessage{Role: domainchat.RoleAssistant}
	for _, call := range calls {
		message.ToolCalls = append(message.ToolCalls, domaintool.ToolCall{
			ID:        call.id,
			Name:      noteToolName,
			Arguments: json.RawMessage(`{"note":"` + call.note + `"}`),
		})
	}
	return domainchat.ToolCallResponse{Message: message, FinishReason: domainchat.FinishReasonToolCalls}
}

// TestOneRoundsReadOnlyCallsRunConcurrentlyWithEachOther pins the three things
// that make a concurrent round indistinguishable from a serial one at every
// layer above it: the calls overlap, the transcript is in call order even when
// the calls finish in the other order, and the audit rows are written in
// completion order while still reading back in call order.
func TestOneRoundsReadOnlyCallsRunConcurrentlyWithEachOther(t *testing.T) {
	ctx := context.Background()
	rendezvous := newToolRendezvous(2)

	// note "c1" is asked for first and finishes last; "c2" is asked for second
	// and finishes first. Anything that folded results in completion order
	// would hand the model the two answers swapped.
	provider := &scriptedProvider{
		supportTools: true,
		toolResps: []domainchat.ToolCallResponse{
			multiCallResponse(
				noteCall{id: "c1", note: slowNote},
				noteCall{id: "c2", note: "quick"},
			),
			assistantText("读完了。"),
		},
	}
	registry := toolreg.New(0)
	register(t, registry, noteEntry(rendezvous, nil))

	runs := memory.NewRunRepository()
	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 3}, agent.Deps{
		Chat:        provider,
		ToolCalling: provider,
		Registry:    registry,
		Auditor:     audit.New(runs, slog.New(slog.NewTextHandler(io.Discard, nil)), audit.Options{}),
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	result, events := runner.Run(ctx, agent.TurnInput{
		ThreadID:  "th-parallel",
		UserID:    "u1",
		UserInput: "读一下这两条笔记",
	})
	drain(events)

	if result == nil {
		t.Fatal("the turn produced no result")
	}
	if rendezvous.currentPeak() < 2 {
		t.Fatalf("peak concurrent tool calls = %d, want 2", rendezvous.currentPeak())
	}

	// The transcript the next planning round reads, which is the thing the
	// concurrency must not reorder.
	if len(provider.toolReqs) < 2 {
		t.Fatalf("planning rounds = %d, want a second round carrying the tool results",
			len(provider.toolReqs))
	}
	transcript := provider.toolReqs[1].req.Messages
	var toolMessages []domainchat.ChatMessage
	for _, msg := range transcript {
		if msg.Role == domainchat.RoleTool {
			toolMessages = append(toolMessages, msg)
		}
	}
	if len(toolMessages) != 2 {
		t.Fatalf("tool messages = %d, want 2: %+v", len(toolMessages), toolMessages)
	}
	for i, want := range []noteCall{{id: "c1", note: slowNote}, {id: "c2", note: "quick"}} {
		if toolMessages[i].ToolCallID != want.id {
			t.Fatalf("tool message %d is for %q, want %q: the transcript is not in call order",
				i, toolMessages[i].ToolCallID, want.id)
		}
		if !strings.Contains(toolMessages[i].Content, "note: "+want.note) {
			t.Fatalf("tool message %d carries the wrong result: %q",
				i, toolMessages[i].Content)
		}
	}

	// The audit trail: written as each call finished, read back in call order.
	calls, err := runs.ListToolCalls(ctx, result.RunID)
	if err != nil {
		t.Fatalf("ListToolCalls: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("tool call rows = %d, want 2", len(calls))
	}
	if calls[0].CallID != "c1" || calls[1].CallID != "c2" {
		t.Fatalf("tool calls read back as %s, %s; want c1 then c2",
			calls[0].CallID, calls[1].CallID)
	}
	// The order above is seq's doing, so seq has to be there and has to be the
	// numbering the launch loop assigned rather than one the calls handed
	// themselves: a counter incremented inside the goroutines would be handed
	// out in completion order, which would read back as c2, c1.
	if calls[0].Seq != 1 || calls[1].Seq != 2 {
		t.Fatalf("seq = %d, %d; want 1, 2 in call order", calls[0].Seq, calls[1].Seq)
	}
	for i, call := range calls {
		if call.StartedAt.IsZero() {
			t.Fatalf("tool call %d has no start time, so the round's overlap is invisible", i)
		}
		if !call.StartedAt.Before(call.CreatedAt) {
			t.Fatalf("tool call %d started at or after it finished: %s → %s",
				i, call.StartedAt, call.CreatedAt)
		}
	}
	// c1 was asked for first and returned last, which is what proves the rows
	// were written out of call order — and therefore that nothing about the
	// order the store handed them back could be an accident of insertion.
	if !calls[0].CreatedAt.After(calls[1].CreatedAt) {
		t.Fatalf("the first call finished at %s, not after the second at %s: "+
			"this fixture no longer distinguishes insertion order from call order",
			calls[0].CreatedAt, calls[1].CreatedAt)
	}
}

// TestARoundsReadOnlyCallsStayWithinTheirLimit checks the fan-out is bounded.
//
// The bound is asserted from the inside, by counting how many handlers were ever
// running at once: an unbounded implementation launches every call before the
// first one has returned, and the count says so. Every note is the slow one, so
// the calls are still inside when the launch loop starts their successors —
// without that a handler could finish before its neighbour was even scheduled,
// and the peak would read low for a reason that has nothing to do with the
// bound.
func TestARoundsReadOnlyCallsStayWithinTheirLimit(t *testing.T) {
	ctx := context.Background()
	const calls = 6

	// One arrival is enough to open the gate, so these calls simply run; the
	// gate is here only so a call cannot be released before it is inside.
	rendezvous := newToolRendezvous(1)

	scripted := make([]noteCall, 0, calls)
	for i := 0; i < calls; i++ {
		scripted = append(scripted, noteCall{id: fmt.Sprintf("c%d", i), note: slowNote})
	}
	provider := &scriptedProvider{
		supportTools: true,
		toolResps: []domainchat.ToolCallResponse{
			multiCallResponse(scripted...),
			assistantText("读完了。"),
		},
	}
	registry := toolreg.New(0)
	register(t, registry, noteEntry(rendezvous, nil))

	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 3}, agent.Deps{
		Chat:        provider,
		ToolCalling: provider,
		Registry:    registry,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	_, events := runner.Run(ctx, agent.TurnInput{
		ThreadID:  "th-bounded",
		UserID:    "u1",
		UserInput: "读一下这几条笔记",
	})
	drain(events)

	if peak := rendezvous.currentPeak(); peak != readOnlyToolLimit {
		t.Fatalf("peak concurrent tool calls = %d, want %d", peak, readOnlyToolLimit)
	}
}

// readOnlyToolLimit mirrors the runner's own bound. It is repeated here rather
// than exported because it is a scheduling knob: the assertion is that the
// runtime honours the number it documents, and a test that read the constant out
// of the implementation would pass whatever that number was.
const readOnlyToolLimit = 3

// TestAWriteStopsTheRoundBeforeTheReadsAfterIt pins the boundary of the
// concurrency. A round that mixes reads and a write runs the reads up to the
// write, parks the write, and stops: a checkpoint holds one pending action, so a
// second one could not be recorded, and a read after the write would be a read
// taken on the assumption the write went through.
func TestAWriteStopsTheRoundBeforeTheReadsAfterIt(t *testing.T) {
	ctx := context.Background()
	rendezvous := newToolRendezvous(1)

	var ran sync.Map
	registry := toolreg.New(0)
	register(t, registry, availabilityStub(&ran))
	register(t, registry, reservationStub(&ran))
	register(t, registry, noteEntry(rendezvous, &ran))

	provider := &scriptedProvider{
		supportTools: true,
		toolResps: []domainchat.ToolCallResponse{
			{
				Message: domainchat.ChatMessage{
					Role: domainchat.RoleAssistant,
					ToolCalls: []domaintool.ToolCall{
						{
							ID:   "c1",
							Name: tools.GetAvailabilityToolName,
							Arguments: json.RawMessage(
								`{"restaurant_id":31,"date":"2026-10-10","party_size":2}`),
						},
						{
							ID:   "c2",
							Name: tools.RequestReservationToolName,
							Arguments: json.RawMessage(
								`{"restaurant_id":31,"slot_id":"slot-31-1900","party_size":2}`),
						},
						{
							ID:        "c3",
							Name:      noteToolName,
							Arguments: json.RawMessage(`{"note":"after-the-write"}`),
						},
					},
				},
				FinishReason: domainchat.FinishReasonToolCalls,
			},
		},
	}

	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 3}, agent.Deps{
		Chat:        provider,
		ToolCalling: provider,
		Registry:    registry,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	result, events := runner.Run(ctx, agent.TurnInput{
		ThreadID:  "th-write-stops",
		UserID:    "u1",
		UserInput: "帮我订 31 号店 19:00 两个人的位置",
	})
	drained := drain(events)

	if result == nil {
		t.Fatal("the turn produced no result")
	}
	if _, ran := ran.Load("availability"); !ran {
		t.Fatal("the read before the write did not run")
	}
	if _, ran := ran.Load("after-the-write"); ran {
		t.Fatal("a read after a parked write ran: the round did not stop at the write")
	}
	if _, ran := ran.Load("reservation"); ran {
		t.Fatal("the write tool's handler ran: it must be parked, never invoked")
	}

	// The turn ends by asking for the confirmation rather than answering.
	var asked bool
	for _, ev := range drained {
		if ev.Type == agent.EventConfirmationRequired {
			asked = true
			if ev.PendingAction != tools.RequestReservationToolName {
				t.Fatalf("pending action = %q, want the reservation tool", ev.PendingAction)
			}
		}
	}
	if !asked {
		t.Fatalf("the turn never asked for confirmation: %+v", drained)
	}
}

// availabilityStub is a read-only get_availability that names one restaurant and
// one slot. It is a stub under the real name because the scheduling rule reads
// whether a tool needs confirmation, and that policy lives on the real entry —
// a private name would let the test decide the answer.
//
// It records that it ran, which is how the mixed round below can tell "the read
// before the write happened" from "nothing happened at all".
func availabilityStub(ran *sync.Map) toolreg.Entry {
	return toolreg.Entry{
		Spec: domaintool.ToolSpec{
			Name:        tools.GetAvailabilityToolName,
			Description: "test stub that reports one slot",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			ReadOnly:    true,
		},
		Handler: func(_ context.Context, _ json.RawMessage) (domaintool.ToolResult, error) {
			ran.Store("availability", true)
			return domaintool.ToolResult{
				Status:  domaintool.ToolStatusOK,
				Content: "19:00 有空位。",
				Data: json.RawMessage(
					`{"restaurant_id":31,"slots":[{"slot_id":"slot-31-1900"}]}`),
			}, nil
		},
	}
}

// reservationStub is a write that declares itself confirmation-gated, so the
// runtime must park it. Its handler records that it was reached, which is the
// assertion: a parked call is never invoked.
func reservationStub(ran *sync.Map) toolreg.Entry {
	return toolreg.Entry{
		Spec: domaintool.ToolSpec{
			Name:        tools.RequestReservationToolName,
			Description: "test stub that would book a table",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			ReadOnly:    false,
		},
		Confirmation: toolreg.ConfirmationRequired,
		SummarizeApproval: func(context.Context, json.RawMessage) (string, error) {
			return "确认预约：31 号店 · 19:00 · 2 人", nil
		},
		Handler: func(_ context.Context, _ json.RawMessage) (domaintool.ToolResult, error) {
			ran.Store("reservation", true)
			return domaintool.ToolResult{Status: domaintool.ToolStatusOK, Content: "已预约。"}, nil
		},
	}
}
