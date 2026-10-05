package agent_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	domaintool "github.com/zed1995/platepilot/shared/domain/tool"

	"github.com/zed1995/platepilot/chat-service/internal/agent"
	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
)

// TestIngressEmitsPhaseAndStepFramesBeforeAnyToolCall captures the contract the
// Agent Console UI depends on: as soon as the runner enters ingress, the
// phase.started frame must reach the onEvent callback, and the three
// step.started frames for loading_context / embedding_memory / interpreting
// must also be visible. The whole point of M5-08 is that a slow ingress does
// not look like a hang; an emulator that observes these frames while the model
// round is still held is the only way to assert that.
// TestPlanPhasePublishesProgressHeartbeats guards the second half of the
// M5-08 contract: a synchronous plan call that takes seconds has to give
// the client something to render during the wait, otherwise the user
// sits on "正在制定下一步计划" with no signal that work is happening.
// phase.progress carries the replacement title; the test releases the
// gate after a long enough delay to observe at least one tick.
func TestPlanPhasePublishesProgressHeartbeats(t *testing.T) {
	gate := make(chan struct{})
	provider := &startGateProvider{
		scriptedProvider: &scriptedProvider{
			supportTools: true,
			toolResps:    []domainchat.ToolCallResponse{assistantText("为你好。")},
			completeResps: []domainchat.ChatResponse{{
				Message:      domainchat.ChatMessage{Role: domainchat.RoleAssistant, Content: "为你好。"},
				FinishReason: domainchat.FinishReasonStop,
			}},
		},
		gate: gate,
	}
	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 2, PhaseEvents: true}, agent.Deps{
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
	onEvent := func(ev agent.Event) error {
		mu.Lock()
		frames = append(frames, ev)
		mu.Unlock()
		return nil
	}

	done := make(chan error, 1)
	go func() {
		_, err := runner.RunLive(context.Background(), agent.TurnInput{
			ThreadID: "th-progress", UserID: "u1", UserInput: "你好",
		}, onEvent)
		done <- err
	}()

	// Wait for the first phase.started of plan. The gate will hold the
	// model round indefinitely, so the test owns the clock from now on.
	deadline := time.After(5 * time.Second)
	var planPhaseID string
	for planPhaseID == "" {
		select {
		case <-deadline:
			t.Fatalf("phase.started(plan) never arrived. got: %v", frames)
		case <-time.After(20 * time.Millisecond):
		}
		mu.Lock()
		for _, f := range frames {
			if f.Type == agent.EventPhaseStarted && f.Phase == "plan" {
				planPhaseID = f.PhaseID
				break
			}
		}
		mu.Unlock()
	}

	// Hold the gate a little longer than one heartbeat interval and
	// confirm a phase.progress frame arrived carrying the right phase id.
	time.Sleep(2 * agent.HeartbeatTestInterval)

	mu.Lock()
	got := append([]agent.Event(nil), frames...)
	mu.Unlock()
	var sawProgress bool
	for _, f := range got {
		if f.Type == agent.EventPhaseProgress && f.PhaseID == planPhaseID {
			sawProgress = true
			if !strings.Contains(f.Title, "正在制定下一步计划") {
				t.Fatalf("progress title = %q, want it to mention the phase", f.Title)
			}
		}
	}
	if !sawProgress {
		t.Fatalf("no phase.progress arrived for plan. frames: %v", got)
	}

	close(gate)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunLive: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not finish after gate opened")
	}

	_ = domaintool.ToolSpec{}
}

func TestIngressEmitsPhaseAndStepFramesBeforeAnyToolCall(t *testing.T) {
	gate := make(chan struct{})
	provider := &startGateProvider{
		scriptedProvider: &scriptedProvider{
			supportTools: true,
			toolResps:    []domainchat.ToolCallResponse{assistantText("为你好。")},
			completeResps: []domainchat.ChatResponse{{
				Message:      domainchat.ChatMessage{Role: domainchat.RoleAssistant, Content: "为你好。"},
				FinishReason: domainchat.FinishReasonStop,
			}},
		},
		gate: gate,
	}
	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 2, PhaseEvents: true}, agent.Deps{
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
	onEvent := func(ev agent.Event) error {
		mu.Lock()
		frames = append(frames, ev)
		mu.Unlock()
		return nil
	}

	done := make(chan error, 1)
	go func() {
		_, err := runner.RunLive(context.Background(), agent.TurnInput{
			ThreadID: "th-frames", UserID: "u1", UserInput: "你好",
		}, onEvent)
		done <- err
	}()

	// Wait up to 5s for phase.started to land while the model round is held.
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		seen := make(map[agent.EventType]bool)
		for _, f := range frames {
			seen[f.Type] = true
		}
		mu.Unlock()
		if seen[agent.EventPhaseStarted] {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("phase.started never arrived. got frames: %v", frames)
		case <-time.After(20 * time.Millisecond):
		}
	}

	mu.Lock()
	got := append([]agent.Event(nil), frames...)
	mu.Unlock()

	// First frame must be message.start so the assistant bubble appears.
	if got[0].Type != agent.EventStart {
		t.Fatalf("first frame is %v, want message.start", got[0].Type)
	}
	var sawPhaseStarted, sawLoadingStarted bool
	for _, f := range got {
		if f.Type == agent.EventPhaseStarted {
			sawPhaseStarted = true
		}
		if f.Type == agent.EventStepStarted && f.Step == "loading_context" {
			sawLoadingStarted = true
		}
	}
	if !sawPhaseStarted {
		t.Fatalf("no phase.started arrived. frames: %v", got)
	}
	if !sawLoadingStarted {
		t.Fatalf("no step.started(loading_context). frames: %v", got)
	}

	close(gate)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunLive: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not finish after gate opened")
	}

	// The full sequence up to the moment the model round was released must
	// be: message.start -> phase.started(ingress) -> step.started × 3 ->
	// step.finished × 3 -> phase.finished(ingress). A client that re-orders
	// this list silently breaks the front-end's reducer (message.start
	// resets text/tools, phase.started accumulates into phases, and the wrong
	// arrival order would either show a step timeline that vanishes or a
	// bubble that forgets the step rows it is sitting on).
	want := []agent.EventType{
		agent.EventStart,
		agent.EventPhaseStarted,
		agent.EventStepStarted, // loading_context
		agent.EventStepFinished,
		agent.EventStepStarted, // embedding_memory
		agent.EventStepFinished,
		agent.EventStepStarted, // interpreting
		agent.EventStepFinished,
		agent.EventPhaseFinished,
	}
	for i, w := range want {
		if i >= len(got) {
			t.Fatalf("event %d = EOF, want %v (full: %v)", i, w, got)
		}
		if got[i].Type != w {
			t.Fatalf("event %d = %v, want %v (full: %v)", i, got[i].Type, w, got)
		}
	}

	_ = domaintool.ToolSpec{}
}