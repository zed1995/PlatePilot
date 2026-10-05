package agent

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"
)

// phaseCounter assigns each phase invocation its own id so the
// phase.started/phase.finished frames can be paired across the plan<->tools
// loop. A client that keyed off phase name alone would replace the first
// round's running row on the second round and never show the loop.
//
// One counter per phase rather than one global counter: that way the same
// phase's id is small and predictable (tools-1, tools-2, ...) and a client
// can sort rounds without parsing the suffix.
type phaseCounter struct {
	ingress int64
	plan    int64
	tools   int64
	answer  int64
	clarify int64
}

type phaseCounterKey struct{}

// phaseIDKey names the context value that carries the phase id of the
// currently-running node. Sub-actions inside ingress read it through
// currentPhaseID instead of recomputing the suffix from the counter —
// recomputing would mint a new id and break the start/finish pairing.
type phaseIDKey struct{}

// withPhaseCounter stores a fresh per-run counter on the context.
//
// withRunMeta already allocates spanSeq the same way (lazily, on first read)
// — but a counter is per-bag-of-counters not per-go, and reusing the lazy
// pattern here would make the counter live on runMeta instead of the
// context. A dedicated helper keeps the counter bound to the run lifetime
// rather than to whatever else grows on runMeta.
func withPhaseCounter(ctx context.Context) context.Context {
	return context.WithValue(ctx, phaseCounterKey{}, &phaseCounter{})
}

// withCurrentPhaseID stores the phase id of the node currently running, so
// sub-actions inside that node (today, the three ingress steps) can frame
// themselves as belonging to the right phase without recomputing the
// suffix.
func withCurrentPhaseID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, phaseIDKey{}, id)
}

// currentPhaseID returns the phase id of the node currently running. The
// zero value is the empty string, which a sub-action's step frame would
// carry as phase_id: an absent parent means the caller forgot to wrap the
// node, which is a bug worth surfacing rather than papering over.
func currentPhaseID(ctx context.Context) string {
	if id, ok := ctx.Value(phaseIDKey{}).(string); ok {
		return id
	}
	return ""
}

// nextSequence takes the next id for a phase name. Unknown phase names get a
// 1-once suffix; the runner only emits the five known phases, so this branch
// is defensive rather than load-bearing.
func (p *phaseCounter) nextSequence(phase string) string {
	var n *int64
	switch phase {
	case "ingress":
		n = &p.ingress
	case "plan":
		n = &p.plan
	case "tools":
		n = &p.tools
	case "answer":
		n = &p.answer
	case "clarify":
		n = &p.clarify
	default:
		return phase + "-1"
	}
	return phase + "-" + strconv.FormatInt(atomic.AddInt64(n, 1), 10)
}

// instrumented wraps a graph node so every execution emits a
// phase.started / phase.finished pair onto the run's stream.
//
// The pair is fired even when the node panics or returns an error: a phase
// that started but never finished would be a row the front-end cannot close,
// and that is a worse failure than the one the node was reporting. Outcome
// is "failed" on any non-nil error so the client can mark the row red
// without inferring it from the absence of a finish frame.
//
// When the runner's PhaseEvents switch is off the wrapper is a transparent
// pass-through. Tests that build a runner with the zero-value Config must not
// pay the wire cost or the new event noise; the closed-set sweep in httpapi
// will not catch a counter-frame emitted in a test either.
func instrumented[I, O any](
	r *Runner,
	phase, title string,
	fn func(context.Context, I) (O, error),
) func(context.Context, I) (O, error) {
	return func(ctx context.Context, in I) (out O, err error) {
		if !r.cfg.PhaseEvents {
			return fn(ctx, in)
		}
		pc, _ := ctx.Value(phaseCounterKey{}).(*phaseCounter)
		phaseID := phase + "-1"
		if pc != nil {
			phaseID = pc.nextSequence(phase)
		}
		ctx = withCurrentPhaseID(ctx, phaseID)
		started := time.Now()
		emitFromContext(ctx, Event{
			Type:      EventPhaseStarted,
			RunID:     runMetaFromContext(ctx).runID,
			Phase:     phase,
			PhaseID:   phaseID,
			Title:     title,
			StartedAt: started.UnixMilli(),
		})
		defer func() {
			outcome := "ok"
			if err != nil {
				outcome = "failed"
			}
			emitFromContext(ctx, Event{
				Type:       EventPhaseFinished,
				RunID:      runMetaFromContext(ctx).runID,
				Phase:      phase,
				PhaseID:    phaseID,
				FinishedAt: time.Now().UnixMilli(),
				Outcome:    outcome,
			})
		}()
		return fn(ctx, in)
	}
}

// emitStep frames one ingress sub-action as step.started / step.finished.
//
// Sub-actions run inside ingress in a fixed order (loading_context ->
// embedding_memory -> interpreting); the caller is responsible for that
// order and for naming the three of them with the canonical Step string.
// A step that runs outside ingress would be a stray row in the front-end's
// timeline; the helper is unexported on purpose so the import surface
// makes it obvious where step events come from.
//
// The phase_id is read from the context set by instrumented so the step
// frame points at the same id as the surrounding phase.started /
func emitStep(ctx context.Context, step, title string, run func()) {
	phaseID := currentPhaseID(ctx)
	if phaseID == "" {
		// No surrounding phase means the caller forgot to wrap the node in
		// instrumented, which would render the step row as a free-floating
		// spinner in the front-end. Running the body anyway and skipping the
		// frames keeps the run correct without making the bug invisible.
		run()
		return
	}
	stepID := phaseID + "-" + step
	started := time.Now()
	emitFromContext(ctx, Event{
		Type:      EventStepStarted,
		RunID:     runMetaFromContext(ctx).runID,
		Phase:     "ingress",
		PhaseID:   phaseID,
		Step:      step,
		StepID:    stepID,
		Title:     title,
		StartedAt: started.UnixMilli(),
	})
	run()
	emitFromContext(ctx, Event{
		Type:       EventStepFinished,
		RunID:      runMetaFromContext(ctx).runID,
		Phase:      "ingress",
		PhaseID:    phaseID,
		Step:       step,
		StepID:     stepID,
		FinishedAt: time.Now().UnixMilli(),
	})
}

// progressHeartbeatInterval is how often a long-running LLM round publishes a
// phase.progress frame so the front-end can show "正在推理（已 N 秒）".
//
// Three rather than one: a one-second ticker floods the emitter channel
// during a two-minute turn, which the buffered 128-frame channel will
// eventually drain against; three seconds is enough for the user to see
// the row tick, and the total event volume per turn stays well below the
// buffer ceiling (a 60s turn → 20 frames; even a worst-case 6-minute turn
// is 120).
//
// HeartbeatTestInterval is the same value exported for the e2e test, which
// needs the exact tick to compute how long to hold the gate provider.
const (
	progressHeartbeatInterval = 3 * time.Second
	HeartbeatTestInterval     = progressHeartbeatInterval
)

// startPhaseProgress publishes phase.started title immediately, then a
// phase.progress frame every progressHeartbeatInterval until stop is
// closed. The first frame covers the gap between phase.started and the
// first ticker tick so a fast call that finishes inside the tick still
// shows the plan row, while a slow call gets a steady stream of "已 N 秒".
//
// The helper returns a stop channel the caller closes on exit so the
// heartbeat does not outlive the call it is observing. When the surrounding
// phase is absent (currentPhaseID returns ""), the function publishes
// nothing and returns nil — callers must treat a nil stop as "no
// heartbeat was started" and skip the close, because closing a fresh
// channel twice is a panic the runtime catches but at the cost of taking
// down the graph goroutine.
//
// Calling without a wrapped phase (currentPhaseID returns "") is a no-op:
// the same "forgot to wrap the node" guard emitStep applies, so the
// heartbeat never publishes frames a client cannot pair to a row.
func startPhaseProgress(ctx context.Context, title string) chan struct{} {
	phaseID := currentPhaseID(ctx)
	if phaseID == "" {
		return nil
	}
	stop := make(chan struct{})
	runID := runMetaFromContext(ctx).runID
	// The first progress frame is published synchronously so a turn whose
	// LLM round returns inside the first tick still shows a "plan"
	// row — a client that only saw phase.started followed by
	// phase.finished would not know whether plan ever began.
	publishProgress(ctx, runID, phaseID, title)
	go func() {
		ticker := time.NewTicker(progressHeartbeatInterval)
		defer ticker.Stop()
		seconds := 0
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				seconds += int(progressHeartbeatInterval / time.Second)
				publishProgress(ctx, runID, phaseID,
					fmt.Sprintf("%s（已 %d 秒）", title, seconds))
			}
		}
	}()
	return stop
}

// publishProgress emits one phase.progress frame. It is split out of
// startPhaseProgress so the synchronous first frame and the goroutine's
// periodic frames share one writer.
func publishProgress(ctx context.Context, runID, phaseID, title string) {
	emitFromContext(ctx, Event{
		Type:    EventPhaseProgress,
		RunID:   runID,
		PhaseID: phaseID,
		Title:   title,
	})
}