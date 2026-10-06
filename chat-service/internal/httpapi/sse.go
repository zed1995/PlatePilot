package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/sse"

	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/errs"
)

// heartbeatInterval is the gap between SSE comment frames. It must stay below
// the server's per-write timeout (30s by default) so idle turns — a model
// thinking for a while — are not mistaken for dead connections by proxies.
const heartbeatInterval = 15 * time.Second

// StreamEventType enumerates the events the chat service can emit. The values
// are the §2.3.2 front-end contract and must not be renamed without a version
// bump.
type StreamEventType string

const (
	StreamStart   StreamEventType = "message.start"
	StreamDelta   StreamEventType = "message.delta"
	StreamReplace StreamEventType = "message.replace"
	// StreamThinking is one increment of the model's own reasoning. It is a
	// distinct event from StreamDelta because reasoning is not answer text: the
	// client renders it as "thinking" and must never append it to the answer.
	// On a reasoning model it is the only output during the long stretch before
	// the first answer token.
	StreamThinking      StreamEventType = "thinking.delta"
	StreamToolStart     StreamEventType = "tool.start"
	StreamToolFinish    StreamEventType = "tool.finish"
	StreamCitation      StreamEventType = "citation"
	StreamAwaitingInput StreamEventType = "state.awaiting_input"
	// StreamConfirmationRequired reports that the turn parked a write and is
	// waiting for the user to approve it. It is a distinct event from
	// StreamAwaitingInput because the two are answered by different endpoints:
	// a clarification by another message, a confirmation by a decision POST.
	StreamConfirmationRequired StreamEventType = "confirmation.required"
	// StreamMemorySaved reports that the turn wrote one long-term memory because
	// the user asked it to. It is a durable side effect the user can later list
	// and delete, so the moment it happened belongs in the stream.
	StreamMemorySaved StreamEventType = "memory.saved"
	StreamEnd         StreamEventType = "message.end"
	StreamError       StreamEventType = "error"

	// Phase / step events carry the lifecycle of an in-flight turn: which of
	// the runner's coarse phases (ingress / plan / tools / answer) is active,
	// and — inside ingress — which of its three sub-actions is running. They
	// exist so the front-end can render a turn as a sequence of named
	// actions rather than a blank spinner; a two-minute plan call that does
	// not produce a byte of its own is not a hang, and showing the user which
	// step is in flight is the difference.
	//
	// They are additive: existing clients ignore them by event name, the
	// closed-set sweep below catches their mis-rematch, and a deployment that
	// does not want them flips one config switch off without changing the
	// transport.
	StreamPhaseStarted  StreamEventType = "phase.started"
	StreamPhaseFinished StreamEventType = "phase.finished"
	StreamStepStarted   StreamEventType = "step.started"
	StreamStepFinished  StreamEventType = "step.finished"
	// StreamPhaseProgress rewrites a running phase's title without
	// closing it. It exists so a synchronous LLM round that takes tens of
	// seconds can show progress ("正在推理（已 6 秒）") rather than a
	// silent spinner. The frame is matched by phase_id, and a client that
	// kept a "phase started" row replaces its title rather than appending
	// a new row.
	StreamPhaseProgress StreamEventType = "phase.progress"
)

// StreamEvent is the transport-neutral shape of one turn event. The
// application layer maps agent events onto it; this layer owns the SSE
// encoding, so the agent never knows about Server-Sent Events.
type StreamEvent struct {
	Type StreamEventType

	RunID    string
	ThreadID string

	// Tool events.
	CallID     string
	Tool       string
	ToolStatus string
	LatencyMS  int64

	// Text / citation events.
	//
	// Delta is provisional under answer streaming: a client accumulates it and
	// must replace everything it accumulated when ReplaceText arrives. The two
	// are separate fields because a replacement is not an increment, and a
	// client that appended one to the other would render the answer twice.
	Delta       string
	ReplaceText string
	EvidenceIDs []int64

	// Awaiting-input event: the thread parked a question for the user.
	State         string
	PendingAction string
	MissingSlots  []string

	// Confirmation event: the thread parked a write for the user to approve.
	// The summary is the same sentence the turn answered with, so a client can
	// render the decision without re-reading the message stream.
	ConfirmationSummary string

	// Memory event: the row the turn wrote.
	MemoryID        string
	MemoryType      string
	MemoryContent   string
	MemoryRefreshed bool

	// End event.
	FinishReason string
	Usage        *domainchat.TokenUsage
	Warnings     []string

	// Error event.
	Code    string
	Message string

	// Phase / step events.
	//
	// Phase is one of "ingress" / "plan" / "tools" / "answer"; Step is empty
	// for phase frames and one of "loading_context" / "embedding_memory" /
	// "interpreting" for the three sub-actions of ingress. PhaseID and StepID
	// are run-unique, server-assigned strings a client uses to pair start and
	// finish frames when several rounds of the same phase happen — tools in
	// particular loops plan->tools->plan, and a client that keyed off Phase
	// alone would replace the first round's running row on the second.
	//
	// StartedAt / FinishedAt are unix milliseconds, computed once on the
	// server; the client does not get to disagree about how long a step took.
	Phase      string
	PhaseID    string
	Step       string
	StepID     string
	Title      string
	StartedAt  int64
	FinishedAt int64
	Outcome    string
}

// Per-event data payloads, matching the §2.3.2 contract exactly so the front
// end parses one documented shape per event name.
type sseStartData struct {
	RunID    string `json:"run_id"`
	ThreadID string `json:"thread_id"`
}

type sseDeltaData struct {
	Delta string `json:"delta"`
}

// sseReplaceData tells the client to discard what it rendered for this run and
// keep this text instead.
//
// It is a whole body rather than a diff because the corrected answer is a fresh
// generation, not an edit of the first one: character offsets from the first
// body do not survive into the second.
type sseReplaceData struct {
	Text string `json:"text"`
}

type sseToolStartData struct {
	CallID string `json:"call_id"`
	Tool   string `json:"tool"`
}

type sseToolFinishData struct {
	CallID    string `json:"call_id"`
	Status    string `json:"status"`
	LatencyMS int64  `json:"latency_ms"`
}

type sseCitationData struct {
	EvidenceIDs []int64 `json:"evidence_ids"`
}

// sseAwaitingInputData tells the client what the thread is waiting for.
//
// The three fields are the checkpoint's own vocabulary rather than a bespoke
// shape: a client that stored them could reconstruct the pending state, and the
// same values are what GET /v1/conversations/:id reports, so the stream and the
// read path cannot disagree about what "waiting" means.
type sseAwaitingInputData struct {
	State         string   `json:"state"`
	PendingAction string   `json:"pending_action,omitempty"`
	MissingSlots  []string `json:"missing_slots,omitempty"`
}

// sseConfirmationData tells the client what it is being asked to approve.
//
// The summary is carried rather than derived from pending_action because the
// action names a tool and the user is approving a sentence about a booking.
// A client that had to render the tool name would be asking its user to approve
// "request_reservation", which is not a decision anyone can make.
type sseConfirmationData struct {
	State   string `json:"state"`
	Action  string `json:"pending_action"`
	Summary string `json:"summary"`
}

// sseMemorySavedData tells the client what was kept.
//
// The content is carried because it is the memory: a client that showed only an
// id would be telling the user "something was saved" and asking them to go look
// it up, when the whole point of the turn was the sentence. The id travels
// beside it so the row can be edited or deleted without a list round-trip.
type sseMemorySavedData struct {
	MemoryID   string `json:"memory_id"`
	MemoryType string `json:"memory_type"`
	Content    string `json:"content"`
	// Refreshed distinguishes "remembered" from "already remembered". The two
	// are the same row, and a client that announced a new memory each time
	// would be describing a duplicate that does not exist.
	Refreshed bool `json:"refreshed,omitempty"`
}

type sseUsageData struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type sseEndData struct {
	FinishReason string        `json:"finish_reason"`
	Usage        *sseUsageData `json:"usage,omitempty"`
	Warnings     []string      `json:"warnings,omitempty"`
}

type sseErrorData struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Phase / step payload shapes.
//
// The started frame carries the wall-clock instant the runner actually
// began work (not when it queued the frame) so the client can render an
// elapsed counter even if the SSE pipe is buffered; finished_at carries the
// matching instant so the step's duration is one subtraction rather than
// two server-side timestamps the client has to reconcile.
type ssePhaseStartedData struct {
	PhaseID   string `json:"phase_id"`
	Phase     string `json:"phase"`
	Title     string `json:"title"`
	StartedAt int64  `json:"started_at"`
}

type ssePhaseFinishedData struct {
	PhaseID    string `json:"phase_id"`
	Phase      string `json:"phase"`
	FinishedAt int64  `json:"finished_at"`
	Outcome    string `json:"outcome,omitempty"`
}

type sseStepStartedData struct {
	PhaseID   string `json:"phase_id"`
	Phase     string `json:"phase"`
	StepID    string `json:"step_id"`
	Step      string `json:"step"`
	Title     string `json:"title"`
	StartedAt int64  `json:"started_at"`
}

type sseStepFinishedData struct {
	PhaseID    string `json:"phase_id"`
	StepID     string `json:"step_id"`
	FinishedAt int64  `json:"finished_at"`
	Outcome    string `json:"outcome,omitempty"`
}

// ssePhaseProgressData rewrites a running phase's title. PhaseID is the
// only required field; Title is the replacement for what the client
// rendered when phase.started arrived. There is no StartedAt because the
// timestamp does not change — a row is still running.
type ssePhaseProgressData struct {
	PhaseID string `json:"phase_id"`
	Title   string `json:"title"`
}

// streamWriter encodes StreamEvents as SSE frames. Every frame flushes
// immediately: tool.start must reach the browser before the tool returns,
// buffering it until message.end would defeat the point of streaming.
type streamWriter struct {
	writer *sse.Writer
}

// newStreamWriter opens the SSE response with a 200 status. Callers must only
// use it once request validation has passed; pre-stream rejections still go
// through the canonical JSON error envelope.
func newStreamWriter(c *app.RequestContext) *streamWriter {
	c.SetStatusCode(200)
	// Disables response buffering for common reverse proxies (nginx).
	c.Response.Header.Set("X-Accel-Buffering", "no")
	return &streamWriter{writer: sse.NewWriter(c)}
}

// write encodes and flushes one event. A returned error means the connection
// is gone; the caller stops the turn instead of draining into a dead socket.
func (s *streamWriter) write(ev StreamEvent) error {
	data, err := ev.payload()
	if err != nil {
		return err
	}
	return s.writer.WriteEvent("", string(ev.Type), data)
}

// heartbeat writes one SSE comment frame. Comments start with ':' and are
// ignored by EventSource clients.
func (s *streamWriter) heartbeat() error {
	return s.writer.WriteComment("keep-alive")
}

func (ev StreamEvent) payload() ([]byte, error) {
	switch ev.Type {
	case StreamStart:
		return json.Marshal(sseStartData{RunID: ev.RunID, ThreadID: ev.ThreadID})
	case StreamDelta:
		return json.Marshal(sseDeltaData{Delta: ev.Delta})
	case StreamThinking:
		// Same increment shape as a delta, on a channel of its own: the event
		// name is what tells the client whether to append to the answer or to
		// the thinking trace.
		return json.Marshal(sseDeltaData{Delta: ev.Delta})
	case StreamReplace:
		return json.Marshal(sseReplaceData{Text: ev.ReplaceText})
	case StreamToolStart:
		return json.Marshal(sseToolStartData{CallID: ev.CallID, Tool: ev.Tool})
	case StreamToolFinish:
		return json.Marshal(sseToolFinishData{CallID: ev.CallID, Status: ev.ToolStatus, LatencyMS: ev.LatencyMS})
	case StreamCitation:
		return json.Marshal(sseCitationData{EvidenceIDs: ev.EvidenceIDs})
	case StreamAwaitingInput:
		return json.Marshal(sseAwaitingInputData{
			State:         ev.State,
			PendingAction: ev.PendingAction,
			MissingSlots:  ev.MissingSlots,
		})
	case StreamEnd:
		data := sseEndData{FinishReason: ev.FinishReason, Warnings: ev.Warnings}
		if ev.Usage != nil {
			data.Usage = &sseUsageData{
				InputTokens:  ev.Usage.InputTokens,
				OutputTokens: ev.Usage.OutputTokens,
				TotalTokens:  ev.Usage.TotalTokens,
			}
		}
		return json.Marshal(data)
	case StreamConfirmationRequired:
		return json.Marshal(sseConfirmationData{
			State:   ev.State,
			Action:  ev.PendingAction,
			Summary: ev.ConfirmationSummary,
		})
	case StreamMemorySaved:
		return json.Marshal(sseMemorySavedData{
			MemoryID:   ev.MemoryID,
			MemoryType: ev.MemoryType,
			Content:    ev.MemoryContent,
			Refreshed:  ev.MemoryRefreshed,
		})
	case StreamError:
		return json.Marshal(sseErrorData{Code: ev.Code, Message: ev.Message})
	case StreamPhaseStarted:
		return json.Marshal(ssePhaseStartedData{
			PhaseID:   ev.PhaseID,
			Phase:     ev.Phase,
			Title:     ev.Title,
			StartedAt: ev.StartedAt,
		})
	case StreamPhaseFinished:
		return json.Marshal(ssePhaseFinishedData{
			PhaseID:    ev.PhaseID,
			Phase:      ev.Phase,
			FinishedAt: ev.FinishedAt,
			Outcome:    ev.Outcome,
		})
	case StreamStepStarted:
		return json.Marshal(sseStepStartedData{
			PhaseID:   ev.PhaseID,
			Phase:     ev.Phase,
			StepID:    ev.StepID,
			Step:      ev.Step,
			Title:     ev.Title,
			StartedAt: ev.StartedAt,
		})
	case StreamStepFinished:
		return json.Marshal(sseStepFinishedData{
			PhaseID:    ev.PhaseID,
			StepID:     ev.StepID,
			FinishedAt: ev.FinishedAt,
			Outcome:    ev.Outcome,
		})
	case StreamPhaseProgress:
		return json.Marshal(ssePhaseProgressData{
			PhaseID: ev.PhaseID,
			Title:   ev.Title,
		})
	default:
		// An unknown event type would desynchronize the client's state machine;
		// dropping it with an error is safer than emitting a nameless frame.
		return nil, errUnknownStreamEvent(ev.Type)
	}
}

func errUnknownStreamEvent(t StreamEventType) error {
	return fmt.Errorf("%w: unknown stream event type %q", errs.ErrInternal, t)
}

// runHeartbeats emits a comment frame every heartbeatInterval until stop is
// called. The writer is safe for concurrent use with event writes.
func runHeartbeats(ctx context.Context, sw *streamWriter, stop <-chan struct{}) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := sw.heartbeat(); err != nil {
				return
			}
		}
	}
}
