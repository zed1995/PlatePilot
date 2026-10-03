package openai

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"

	chatport "github.com/zed1995/platepilot/shared/chat"
	"github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/tool"
)

// sseLineLimit bounds one SSE line. Tool-call frames stay small (fragments),
// but a generous megabyte means an unusual delta cannot kill the scan.
const sseLineLimit = 1 << 20

// sseStream turns an OpenAI-compatible text/event-stream body into domain
// chat chunks. Tool-call deltas arrive as fragments keyed by index (id/name on
// the first fragment, arguments appended afterwards); they are accumulated
// here and emitted as snapshots, which is the shape ChatChunk already speaks.
type sseStream struct {
	body io.ReadCloser
	scan *bufio.Scanner

	finished bool
	closed   bool

	// Accumulated tool calls keyed by the wire index, in first-seen order.
	toolOrder []int
	toolAcc   map[int]*tool.ToolCall
	// Raw argument fragments per index, joined on snapshot.
	toolArgs map[int]*strings.Builder
}

var _ chatport.ChatStream = (*sseStream)(nil)

func newSSEStream(body io.ReadCloser) *sseStream {
	s := &sseStream{
		body:     body,
		toolAcc:  make(map[int]*tool.ToolCall),
		toolArgs: make(map[int]*strings.Builder),
	}
	s.scan = bufio.NewScanner(body)
	s.scan.Buffer(make([]byte, 0, 4096), sseLineLimit)
	return s
}

// Recv returns the next chunk. io.EOF marks a clean end of stream (a [DONE]
// frame or the server closing after a terminal chunk).
func (s *sseStream) Recv() (chat.ChatChunk, error) {
	if s.finished {
		return chat.ChatChunk{}, io.EOF
	}
	for s.scan.Scan() {
		line := strings.TrimSpace(s.scan.Text())
		if line == "" {
			continue // event separator
		}
		if strings.HasPrefix(line, ":") {
			continue // SSE comment / heartbeat
		}
		if !strings.HasPrefix(line, "data:") {
			// Non-data events (event:/id:/retry:) are not used by this
			// protocol; ignore them rather than failing the turn.
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			s.finished = true
			if err := s.validateToolCalls(); err != nil {
				return chat.ChatChunk{}, err
			}
			return chat.ChatChunk{}, io.EOF
		}

		chunk, terminal, err := s.decodeFrame([]byte(data))
		if err != nil {
			return chat.ChatChunk{}, err
		}
		if terminal {
			s.finished = true
		}
		// Terminal frames are always delivered even when their delta is empty:
		// the finish reason is information the agent branches on.
		if terminal {
			return chunk, nil
		}
		// Skip empty keepalive frames some providers send before the first
		// real delta.
		if chunk.Delta == "" && len(chunk.ToolCalls) == 0 &&
			chunk.FinishReason == "" && chunk.Usage == nil {
			continue
		}
		return chunk, nil
	}
	if err := s.scan.Err(); err != nil {
		if err == io.EOF {
			return chat.ChatChunk{}, io.EOF
		}
		return chat.ChatChunk{}, errs.Wrap(errs.CodeProviderUnavailable,
			"openai: reading chat stream failed", err)
	}
	s.finished = true
	return chat.ChatChunk{}, io.EOF
}

// Close releases the response body. It is idempotent.
func (s *sseStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	s.finished = true
	return s.body.Close()
}

// decodeFrame maps one data frame to a chunk. terminal reports a finish
// reason, letting Recv stop on the next call.
func (s *sseStream) decodeFrame(data []byte) (chunk chat.ChatChunk, terminal bool, err error) {
	// Some gateways (OpenRouter included) surface mid-stream failures as a
	// data frame carrying an error object.
	var apiErr apiError
	if decErr := json.Unmarshal(data, &apiErr); decErr == nil && strings.TrimSpace(apiErr.Error.Message) != "" {
		return chat.ChatChunk{}, false, errs.Newf(errs.CodeProviderUnavailable,
			"openai: stream error: %s", strings.TrimSpace(apiErr.Error.Message))
	}

	var frame completionChunk
	if err := json.Unmarshal(data, &frame); err != nil {
		return chat.ChatChunk{}, false, errs.Wrap(errs.CodeProviderUnavailable,
			"openai: decode stream frame", err)
	}
	if frame.Usage != nil {
		u := toUsage(*frame.Usage)
		chunk.Usage = &u
	}
	for _, choice := range frame.Choices {
		chunk.Delta += choice.Delta.Content
		if choice.FinishReason != "" {
			chunk.FinishReason = mapFinishReason(choice.FinishReason)
			terminal = true
		}
		for _, d := range choice.Delta.ToolCalls {
			s.mergeToolDelta(d)
		}
	}
	if snaps := s.toolSnapshots(); len(snaps) > 0 {
		chunk.ToolCalls = snaps
	}
	return chunk, terminal, nil
}

// mergeToolDelta accumulates one tool-call fragment keyed by its index.
func (s *sseStream) mergeToolDelta(d wireToolCallDelta) {
	acc, ok := s.toolAcc[d.Index]
	if !ok {
		acc = &tool.ToolCall{}
		s.toolAcc[d.Index] = acc
		s.toolArgs[d.Index] = &strings.Builder{}
		s.toolOrder = append(s.toolOrder, d.Index)
	}
	if d.ID != "" {
		acc.ID = d.ID
	}
	if d.Function.Name != "" {
		acc.Name = d.Function.Name
	}
	if d.Function.Arguments != "" {
		s.toolArgs[d.Index].WriteString(d.Function.Arguments)
	}
}

// toolSnapshots returns accumulated tool calls in index order with the
// arguments joined so far. Mid-stream snapshots intentionally are not JSON
// validated: a partial fragment like `{"city":"Ne` is not valid JSON yet.
func (s *sseStream) toolSnapshots() []tool.ToolCall {
	out := make([]tool.ToolCall, 0, len(s.toolOrder))
	for _, idx := range s.toolOrder {
		acc := s.toolAcc[idx]
		call := tool.ToolCall{ID: acc.ID, Name: acc.Name}
		if raw := s.toolArgs[idx].String(); raw != "" {
			call.Arguments = json.RawMessage(raw)
		}
		out = append(out, call)
	}
	return out
}

// validateToolCalls checks the final accumulated tool calls once the stream is
// over, at which point the argument strings must be complete JSON.
func (s *sseStream) validateToolCalls() error {
	for _, idx := range s.toolOrder {
		raw := strings.TrimSpace(s.toolArgs[idx].String())
		if raw == "" {
			continue
		}
		if !json.Valid([]byte(raw)) {
			return errs.Newf(errs.CodeProviderUnavailable,
				"openai: streamed tool %q arguments are not valid JSON: %s",
				s.toolAcc[idx].Name, truncate(raw, 256))
		}
	}
	return nil
}
