package agent

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/run"

	"github.com/zed1995/platepilot/chat-service/internal/agent/audit"
)

// traced wraps one graph node so every execution of it records a span: which
// node ran, how long it took, and whether it succeeded.
//
// The span is written on the node's error path as much as on its success path.
// A trace that only recorded successful nodes would be a latency report; the
// point of recording the failing one is that "this turn failed" becomes "this
// turn failed in answer after 4.2s", which is the difference between knowing
// something broke and knowing where to look.
//
// Wrapping is done here rather than inside each node because six spans written
// six ways are not a trace. The measurement, the status rule, and the detail
// shape are decided once and every node inherits them.
func traced[I, O any](
	r *Runner,
	name string,
	fn func(context.Context, I) (O, error),
	detail func(O, error) json.RawMessage,
) func(context.Context, I) (O, error) {
	if r.deps.Auditor == nil {
		return fn
	}
	return func(ctx context.Context, in I) (out O, err error) {
		meta := runMetaFromContext(ctx)
		if meta.runID == "" {
			// A node invoked outside Run has no run to attach a span to. This
			// is true of the compile-time probes and of any future caller that
			// reuses a node directly, and dropping the span is right for both:
			// a span with no run is a row nothing can read.
			return fn(ctx, in)
		}
		started := time.Now()
		defer func() {
			status := run.NodeOK
			code := ""
			if err != nil {
				status = run.NodeError
				code = string(errs.CodeOf(err))
			}
			var payload json.RawMessage
			if detail != nil {
				payload = detail(out, err)
			}
			r.deps.Auditor.Node(ctx, audit.NodeEvent{
				RunID:     meta.runID,
				TraceID:   meta.traceID,
				Node:      name,
				Seq:       int(atomic.AddInt64(meta.spanSeq, 1)),
				Status:    status,
				LatencyMS: time.Since(started).Milliseconds(),
				Detail:    payload,
				ErrorCode: code,
			})
		}()
		return fn(ctx, in)
	}
}

// nodeDetail marshals a node's facts into the span detail. A detail that could
// fail the node it describes would be worse than no detail, so a value that
// cannot be marshalled is dropped rather than propagated.
func nodeDetail(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}

// The detail each node records is chosen to answer the question a reader of
// that span actually has. Counts, not content: the trace says how many
// candidates the search returned, never what the user asked, and never
// anything from the transcript.

func ingressDetail(st *TurnState, _ error) json.RawMessage {
	if st == nil {
		return nil
	}
	return nodeDetail(map[string]any{
		"intent":             string(st.Intent),
		"transcript_len":     len(st.Messages),
		"memories_dropped":   st.DroppedMemoryCount,
		"checkpoint_version": st.CheckpointVersion,
	})
}

func planDetail(st *TurnState, _ error) json.RawMessage {
	if st == nil {
		return nil
	}
	return nodeDetail(map[string]any{
		"round":              st.ToolRounds + 1,
		"pending_tool_calls": st.PendingToolCalls,
		"clarifying":         st.needsClarification(),
	})
}

func toolsDetail(st *TurnState, _ error) json.RawMessage {
	if st == nil {
		return nil
	}
	return nodeDetail(map[string]any{
		"round":      st.ToolRounds,
		"candidates": len(st.Candidates),
		"evidence":   len(st.Evidence),
		"used_tools": st.UsedTools,
	})
}

func clarifyDetail(st *TurnState, _ error) json.RawMessage {
	if st == nil {
		return nil
	}
	return nodeDetail(map[string]any{
		"options":     len(st.ClarificationOptions),
		"missing":     st.MissingSlots,
		"round":       st.ClarificationCount,
		"assumed":     st.ClarificationAssumed,
		"pending":     st.State != "",
		"awaiting_id": st.PendingToolCallID != "",
	})
}

func answerDetail(st *TurnState, _ error) json.RawMessage {
	if st == nil {
		return nil
	}
	return nodeDetail(map[string]any{
		"candidates":      len(st.Candidates),
		"evidence":        len(st.Evidence),
		"retrieval_empty": st.RetrievalEmpty,
		"used_tools":      st.UsedTools,
	})
}

func finalizeDetail(result *TurnResult, _ error) json.RawMessage {
	if result == nil {
		return nil
	}
	return nodeDetail(map[string]any{
		"finish_reason": string(result.FinishReason),
		"tool_rounds":   result.ToolRounds,
		"warnings":      len(result.Warnings),
		"answered":      result.Answer != nil,
	})
}
