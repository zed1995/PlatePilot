package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

// instrumented is supposed to drop a phase.started frame at the entrance
// and a matching phase.finished frame at the exit. A wrapper that fired
// the start but never fired the finish would leave the front-end with an
// unclosable row; the test covers both halves.
func TestInstrumentedEmitsPhasePairAroundTheNode(t *testing.T) {
	em := newEmitter()
	ctx := withEmitter(context.Background(), em)
	ctx = withPhaseCounter(ctx)
	runner := &Runner{cfg: Config{PhaseEvents: true}}
	ran := false
	_, err := instrumented(runner, "plan", "正在制定下一步计划",
		func(_ context.Context, _ struct{}) (struct{}, error) {
			ran = true
			return struct{}{}, nil
		})(ctx, struct{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ran {
		t.Fatalf("wrapper did not call the inner function")
	}
	var captured []Event
	for i := 0; i < 2; i++ {
		select {
		case ev := <-em.ch:
			captured = append(captured, ev)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for event %d", i)
		}
	}
	if captured[0].Type != EventPhaseStarted || captured[1].Type != EventPhaseFinished {
		t.Fatalf("event order = %v, %v", captured[0].Type, captured[1].Type)
	}
	if captured[0].PhaseID == "" || captured[0].PhaseID != captured[1].PhaseID {
		t.Fatalf("phase ids must pair: %q / %q", captured[0].PhaseID, captured[1].PhaseID)
	}
	if captured[0].Phase != "plan" || captured[1].Phase != "plan" {
		t.Fatalf("phase name wrong: %s / %s", captured[0].Phase, captured[1].Phase)
	}
	if captured[0].Title != "正在制定下一步计划" {
		t.Fatalf("title lost: %q", captured[0].Title)
	}
	if captured[0].StartedAt == 0 || captured[1].FinishedAt == 0 {
		t.Fatalf("timestamps missing: %d %d", captured[0].StartedAt, captured[1].FinishedAt)
	}
	if captured[0].StartedAt > captured[1].FinishedAt {
		t.Fatalf("started_at after finished_at: %d > %d", captured[0].StartedAt, captured[1].FinishedAt)
	}
}

// A failure inside the wrapped function must still close the phase. A row
// left in the "running" state forever is a worse failure than the one the
// node was reporting, and the front-end cannot tell the two apart without
// the finished frame's outcome field.
func TestInstrumentedEmitsFinishedEvenWhenTheNodeErrors(t *testing.T) {
	em := newEmitter()
	ctx := withEmitter(context.Background(), em)
	ctx = withPhaseCounter(ctx)
	runner := &Runner{cfg: Config{PhaseEvents: true}}
	sentinel := errors.New("node blew up")
	_, err := instrumented(runner, "answer", "正在生成回答",
		func(_ context.Context, _ struct{}) (struct{}, error) {
			return struct{}{}, sentinel
		})(ctx, struct{}{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("wrapper must return the inner error, got %v", err)
	}
	var captured []Event
	for i := 0; i < 2; i++ {
		select {
		case ev := <-em.ch:
			captured = append(captured, ev)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for event %d", i)
		}
	}
	if captured[1].Outcome != "failed" {
		t.Fatalf("outcome on failure = %q, want failed", captured[1].Outcome)
	}
}

// PhaseEvents off means no frames, and the wrapper is a transparent
// pass-through. The application turns this off to roll back to the older
// wire protocol; a wrapper that still emitted under the off switch would
// defeat the switch.
func TestInstrumentedDoesNotEmitWhenPhaseEventsAreOff(t *testing.T) {
	em := newEmitter()
	ctx := withEmitter(context.Background(), em)
	ctx = withPhaseCounter(ctx)
	runner := &Runner{cfg: Config{PhaseEvents: false}}
	if _, err := instrumented(runner, "plan", "正在制定下一步计划",
		func(_ context.Context, _ struct{}) (struct{}, error) {
			return struct{}{}, nil
		})(ctx, struct{}{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	select {
	case ev := <-em.ch:
		t.Fatalf("expected no events, got %v", ev)
	case <-time.After(50 * time.Millisecond):
		// no frame means the switch held.
	}
}

// Each round of a phase that loops (tools in particular) needs its own
// id so the front-end can pair start and finish across rounds. The
// counter is incremented by the wrapper, not by the node, so the rule
// holds even if the node calls the counter itself for a step.
func TestInstrumentedMintsADistinctIdPerPhaseVisit(t *testing.T) {
	em := newEmitter()
	ctx := withEmitter(context.Background(), em)
	ctx = withPhaseCounter(ctx)
	runner := &Runner{cfg: Config{PhaseEvents: true}}
	for i := 0; i < 3; i++ {
		if _, err := instrumented(runner, "tools", "正在调用工具",
			func(_ context.Context, _ struct{}) (struct{}, error) {
				return struct{}{}, nil
			})(ctx, struct{}{}); err != nil {
			t.Fatalf("unexpected error on round %d: %v", i, err)
		}
	}
	var captured []Event
	for i := 0; i < 6; i++ {
		select {
		case ev := <-em.ch:
			captured = append(captured, ev)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for event %d", i)
		}
	}
	// starts and finishes pair round-by-round.
	for round := 0; round < 3; round++ {
		start := captured[round*2]
		finish := captured[round*2+1]
		if start.PhaseID != finish.PhaseID {
			t.Fatalf("round %d ids differ: %q vs %q", round, start.PhaseID, finish.PhaseID)
		}
	}
	// the three rounds have three different ids.
	ids := map[string]struct{}{
		captured[0].PhaseID: {},
		captured[2].PhaseID: {},
		captured[4].PhaseID: {},
	}
	if len(ids) != 3 {
		t.Fatalf("rounds did not mint distinct ids: %v", ids)
	}
}

// emitStep wraps a sub-action as step.started / step.finished and is
// scoped to whatever phase is currently on the context. A step frame
// without a parent phase would render as a free-floating spinner.
func TestEmitStepPairsUnderTheCurrentPhase(t *testing.T) {
	em := newEmitter()
	ctx := withEmitter(context.Background(), em)
	ctx = withPhaseCounter(ctx)
	ctx = withCurrentPhaseID(ctx, "ingress-1")

	ran := false
	emitStep(ctx, "loading_context", "读取最近会话与候选快照", func() {
		ran = true
	})
	if !ran {
		t.Fatalf("emitStep did not run the body")
	}
	var captured []Event
	for i := 0; i < 2; i++ {
		select {
		case ev := <-em.ch:
			captured = append(captured, ev)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for event %d", i)
		}
	}
	if captured[0].Type != EventStepStarted || captured[1].Type != EventStepFinished {
		t.Fatalf("step order = %v, %v", captured[0].Type, captured[1].Type)
	}
	if captured[0].PhaseID != "ingress-1" || captured[1].PhaseID != "ingress-1" {
		t.Fatalf("step phase id wrong: %q / %q", captured[0].PhaseID, captured[1].PhaseID)
	}
	if captured[0].Phase != "ingress" || captured[0].Step != "loading_context" {
		t.Fatalf("step names wrong: %s/%s", captured[0].Phase, captured[0].Step)
	}
	if captured[0].StepID != "ingress-1-loading_context" {
		t.Fatalf("step id = %q", captured[0].StepID)
	}
}

// A step outside any phase is a caller bug, not a recoverable error:
// emitStep falls back to running the body and skipping the frames so the
// turn still completes. The test makes the visible intent (run, do not
// emit) part of the contract.
func TestEmitStepRunsTheBodyWhenNoPhaseIsOnTheContext(t *testing.T) {
	em := newEmitter()
	ctx := withEmitter(context.Background(), em)
	ctx = withPhaseCounter(ctx)

	ran := false
	emitStep(ctx, "loading_context", "读取最近会话与候选快照", func() {
		ran = true
	})
	if !ran {
		t.Fatalf("emitStep must still run the body without a parent phase")
	}
	select {
	case ev := <-em.ch:
		t.Fatalf("expected no events, got %v", ev)
	case <-time.After(50 * time.Millisecond):
		// no frame means the guard held.
	}
}