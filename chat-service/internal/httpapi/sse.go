package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/sse"

	domainchat "github.com/zed/platepilot/shared/domain/chat"
	"github.com/zed/platepilot/shared/domain/errs"
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
	StreamStart      StreamEventType = "message.start"
	StreamDelta      StreamEventType = "message.delta"
	StreamToolStart  StreamEventType = "tool.start"
	StreamToolFinish StreamEventType = "tool.finish"
	StreamCitation   StreamEventType = "citation"
	StreamEnd        StreamEventType = "message.end"
	StreamError      StreamEventType = "error"
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
	Delta       string
	EvidenceIDs []int64

	// End event.
	FinishReason string
	Usage        *domainchat.TokenUsage
	Warnings     []string

	// Error event.
	Code    string
	Message string
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
	case StreamToolStart:
		return json.Marshal(sseToolStartData{CallID: ev.CallID, Tool: ev.Tool})
	case StreamToolFinish:
		return json.Marshal(sseToolFinishData{CallID: ev.CallID, Status: ev.ToolStatus, LatencyMS: ev.LatencyMS})
	case StreamCitation:
		return json.Marshal(sseCitationData{EvidenceIDs: ev.EvidenceIDs})
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
	case StreamError:
		return json.Marshal(sseErrorData{Code: ev.Code, Message: ev.Message})
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
