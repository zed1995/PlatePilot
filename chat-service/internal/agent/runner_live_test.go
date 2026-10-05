package agent_test

// The runtime has two entry points and they answer different questions. Run
// answers "what did the turn produce" and hands back a finished transcript.
// RunLive answers "and when did each part of it happen", forwarding frames into
// a callback while the graph is still running.
//
// The difference is not cosmetic. A streaming transport needs the second: a
// first token delivered after the turn has finished is the whole answer in
// pieces, which is precisely the latency streaming exists to remove. So the
// property worth testing is not "the same events come out" — Run already
// guarantees that — but "the events come out *early*".

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	domaintool "github.com/zed1995/platepilot/shared/domain/tool"

	"github.com/zed1995/platepilot/chat-service/internal/agent"
	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
)

// startGateProvider holds the turn's model rounds until the test opens the
// gate.
//
// It is what makes "the frame arrived while the turn was still running" an
// observable claim rather than a timing coincidence: with an instantaneous
// provider every frame arrives after the graph has finished whether it was
// forwarded live or replayed at the end, so nothing could be distinguished.
//
// Both model entry points are held, not just the tool-calling one. Which of
// them a plan round reaches depends on what the registry offers, and that is
// not what this test is about — a gate that only held one of them would leave
// the assertion resting on a dispatch decision made somewhere else.
type startGateProvider struct {
	*scriptedProvider
	gate chan struct{}
}

func (p *startGateProvider) wait(ctx context.Context) error {
	select {
	case <-p.gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *startGateProvider) ChatWithTools(
	ctx context.Context, req domainchat.ChatRequest, specs []domaintool.ToolSpec,
) (domainchat.ToolCallResponse, error) {
	if err := p.wait(ctx); err != nil {
		return domainchat.ToolCallResponse{}, err
	}
	return p.scriptedProvider.ChatWithTools(ctx, req, specs)
}

func (p *startGateProvider) Complete(
	ctx context.Context, req domainchat.ChatRequest,
) (domainchat.ChatResponse, error) {
	if err := p.wait(ctx); err != nil {
		return domainchat.ChatResponse{}, err
	}
	return p.scriptedProvider.Complete(ctx, req)
}

// TestRunLiveForwardsEventsBeforeTheTurnEnds is the claim the SSE handler rests
// on, and it cannot be met by Run: Run has nothing to hand back until Invoke
// has returned, so an event it carries is by construction an event from the
// past.
func TestRunLiveForwardsEventsBeforeTheTurnEnds(t *testing.T) {
	gate := make(chan struct{})
	provider := &startGateProvider{
		scriptedProvider: &scriptedProvider{
			supportTools: true,
			toolResps:    []domainchat.ToolCallResponse{assistantText("你好。")},
			completeResps: []domainchat.ChatResponse{{
				Message:      domainchat.ChatMessage{Role: domainchat.RoleAssistant, Content: "你好。"},
				FinishReason: domainchat.FinishReasonStop,
			}},
		},
		gate: gate,
	}
	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 2}, agent.Deps{
		Chat:        provider,
		ToolCalling: provider,
		Registry:    toolreg.New(0),
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	var (
		mu     sync.Mutex
		frames []agent.Event
	)
	first := make(chan struct{})
	var once sync.Once
	onEvent := func(ev agent.Event) error {
		mu.Lock()
		frames = append(frames, ev)
		mu.Unlock()
		once.Do(func() { close(first) })
		return nil
	}

	done := make(chan error, 1)
	go func() {
		_, err := runner.RunLive(context.Background(), agent.TurnInput{
			ThreadID: "th-live", UserID: "u1", UserInput: "你好",
		}, onEvent)
		done <- err
	}()

	select {
	case <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("no event was forwarded while the turn was running")
	}
	mu.Lock()
	delivered := append([]agent.Event(nil), frames...)
	mu.Unlock()
	if len(delivered) == 0 || delivered[0].Type != agent.EventStart {
		t.Fatalf("the first frame is %+v, want the run's start", delivered)
	}

	// The model round is still held, so the turn cannot have finished. This is
	// the assertion that separates live forwarding from an end-of-turn replay.
	select {
	case err := <-done:
		t.Fatalf("the turn completed while its model round was still held: %v", err)
	default:
	}

	close(gate)
	if err := <-done; err != nil {
		t.Fatalf("RunLive: %v", err)
	}
}

// TestRunLiveStopsTheTurnWhenTheConsumerFails covers the half of the contract a
// closed connection depends on: a consumer that can no longer write must stop
// the model call rather than leave it running into a dead socket.
func TestRunLiveStopsTheTurnWhenTheConsumerFails(t *testing.T) {
	refused := errors.New("connection closed")
	provider := &scriptedProvider{
		supportTools: true,
		toolResps:    []domainchat.ToolCallResponse{assistantText("你好。")},
	}
	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 2}, agent.Deps{
		Chat:        provider,
		ToolCalling: provider,
		Registry:    toolreg.New(0),
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	var frames []agent.Event
	result, err := runner.RunLive(context.Background(), agent.TurnInput{
		ThreadID: "th-dead", UserID: "u1", UserInput: "你好",
	}, func(ev agent.Event) error {
		frames = append(frames, ev)
		// Failing on the run's opening frame is the worst case: nothing has
		// been produced yet, so nothing about the turn has to be salvaged.
		return refused
	})

	if !errors.Is(err, refused) {
		t.Fatalf("err = %v, want the consumer's error", err)
	}
	if result != nil {
		t.Fatalf("result = %+v, want none: a turn whose consumer failed did not finish", result)
	}
	for _, ev := range frames {
		if ev.Type == agent.EventDelta && ev.Delta != "" {
			t.Fatalf("the turn answered after its consumer refused: %q", ev.Delta)
		}
	}
}

// TestTheTwoEntryPointsPublishTheSameEvents guards the seam between them. They
// share invoke, so the risk is not that the graph runs differently but that one
// of them drops or reorders frames on the way out — which would make a test
// written against Run evidence about code the transport never runs.
func TestTheTwoEntryPointsPublishTheSameEvents(t *testing.T) {
	build := func() *agent.Runner {
		provider := &scriptedProvider{completeResps: []domainchat.ChatResponse{{
			Message:      domainchat.ChatMessage{Role: domainchat.RoleAssistant, Content: "为你找到两家意大利餐厅。"},
			FinishReason: domainchat.FinishReasonStop,
		}}}
		runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 2}, agent.Deps{
			Chat:        provider,
			ToolCalling: provider,
			Registry:    toolreg.New(0),
		})
		if err != nil {
			t.Fatalf("NewRunner: %v", err)
		}
		return runner
	}

	// The run ids are minted per turn, so they are compared for presence rather
	// than for equality; everything else is compared as it is.
	describe := func(events []agent.Event) []string {
		out := make([]string, 0, len(events))
		for _, ev := range events {
			out = append(out, strings.Join([]string{
				string(ev.Type),
				ev.Tool,
				ev.Delta,
				ev.Code,
			}, "|"))
		}
		return out
	}

	const turn = "曼哈顿的意大利菜"
	_, stream := build().Run(context.Background(), agent.TurnInput{
		ThreadID: "th-parity", UserID: "u1", UserInput: turn,
	})
	batched := describe(drain(stream))

	var live []agent.Event
	if _, err := build().RunLive(context.Background(), agent.TurnInput{
		ThreadID: "th-parity", UserID: "u1", UserInput: turn,
	}, func(ev agent.Event) error {
		live = append(live, ev)
		return nil
	}); err != nil {
		t.Fatalf("RunLive: %v", err)
	}

	got, want := describe(live), batched
	if len(got) != len(want) {
		t.Fatalf("RunLive published %d frames, Run published %d:\n  live:  %v\n  batch: %v",
			len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("frame %d differs:\n  live:  %v\n  batch: %v", i, got, want)
		}
	}
}
