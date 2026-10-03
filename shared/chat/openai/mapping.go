package openai

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zed/platepilot/shared/domain/chat"
	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/tool"
)

// toWireMessages maps project messages onto the protocol shape. Every domain
// role already matches the protocol, but going through an explicit mapping
// keeps vendor-neutral DTOs out of the (un)marshal path and gives one place to
// enforce the tool-call message invariants.
func toWireMessages(in []chat.ChatMessage) []wireMessage {
	out := make([]wireMessage, 0, len(in))
	for _, m := range in {
		wm := wireMessage{
			Role:       string(m.Role),
			Content:    m.Content,
			Name:       m.Name,
			ToolCallID: m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			wm.ToolCalls = append(wm.ToolCalls, toWireToolCall(tc))
		}
		out = append(out, wm)
	}
	return out
}

func toWireTools(in []tool.ToolSpec) []wireTool {
	out := make([]wireTool, 0, len(in))
	for _, spec := range in {
		params := spec.Parameters
		if len(params) == 0 {
			// The protocol requires a schema object; an empty schema means
			// "no parameters", not "schema omitted".
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, wireTool{
			Type: "function",
			Function: wireToolFunction{
				Name:        spec.Name,
				Description: spec.Description,
				Parameters:  params,
			},
		})
	}
	return out
}

func toWireToolCall(tc tool.ToolCall) wireToolCall {
	return wireToolCall{
		ID:   tc.ID,
		Type: "function",
		Function: wireFunctionCall{
			Name:      tc.Name,
			Arguments: functionArguments{Raw: tc.Arguments},
		},
	}
}

// toDomainMessage maps a response message back to the domain DTO and validates
// the invariants the agent relies on. A model that names tools but returns
// unparseable arguments has broken the contract: that is a provider fault, and
// surfacing the arguments to JSON Schema validation later would only hide the
// real cause.
func toDomainMessage(wm wireMessage) (chat.ChatMessage, error) {
	msg := chat.ChatMessage{
		Role:       chat.Role(wm.Role),
		Content:    wm.Content,
		Name:       wm.Name,
		ToolCallID: wm.ToolCallID,
	}
	for _, wtc := range wm.ToolCalls {
		name := strings.TrimSpace(wtc.Function.Name)
		if name == "" {
			name = wtc.Function.Name
		}
		args := wtc.Function.Arguments.Raw
		if len(args) > 0 {
			// Arguments must be usable JSON; the tool registry performs the
			// schema-level check afterwards.
			if !json.Valid(args) {
				return chat.ChatMessage{}, errs.Newf(errs.CodeProviderUnavailable,
					"openai: tool %q returned arguments that are not valid JSON: %s",
					name, truncate(string(args), 256))
			}
		}
		msg.ToolCalls = append(msg.ToolCalls, tool.ToolCall{
			ID:        wtc.ID,
			Name:      name,
			Arguments: args,
		})
	}
	return msg, nil
}

func toUsage(u completionUsage) chat.TokenUsage {
	total := u.TotalTokens
	if total == 0 {
		total = u.PromptTokens + u.CompletionTokens
	}
	return chat.TokenUsage{
		InputTokens:  u.PromptTokens,
		OutputTokens: u.CompletionTokens,
		TotalTokens:  total,
	}
}

// mapFinishReason passes the protocol reason through. The domain type is a
// string so unknown future values survive instead of being blanked out; the
// agent only branches on the four values it knows.
func mapFinishReason(reason string) chat.FinishReason {
	return chat.FinishReason(reason)
}

// mergeExtraBody folds the caller's extra_body keys into the marshalled request
// object. Protocol-owned keys always win: a vendor knob must not be able to
// hijack model/messages/tools/stream or the response format.
func mergeExtraBody(core any, extra map[string]any) (json.RawMessage, error) {
	raw, err := json.Marshal(core)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, "openai: encode request", err)
	}
	if len(extra) == 0 {
		return raw, nil
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, errs.Wrap(errs.CodeInternal, "openai: re-encode request", err)
	}
	for key, value := range extra {
		if _, taken := body[key]; taken {
			continue
		}
		body[key] = value
	}
	merged, err := json.Marshal(body)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, "openai: encode extra_body", err)
	}
	return merged, nil
}

// asStringMap pulls a map[string]any out of ProviderOptions without panicking
// on a caller-supplied wrong type. Unknown keys and wrong types are ignored:
// ProviderOptions is a vendor escape hatch, not validated input.
func asStringMap(opts map[string]any, key string) map[string]any {
	v, ok := opts[key]
	if !ok || v == nil {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return m
}

// asHeaderMap pulls extra_headers out of ProviderOptions. Both
// map[string]string (natural for Go callers) and map[string]any (decoded
// from JSON) are accepted; non-string values are skipped.
func asHeaderMap(opts map[string]any) map[string]string {
	v, ok := opts["extra_headers"]
	if !ok || v == nil {
		return nil
	}
	switch m := v.(type) {
	case map[string]string:
		out := make(map[string]string, len(m))
		for k, val := range m {
			out[k] = val
		}
		return out
	case map[string]any:
		out := make(map[string]string, len(m))
		for k, val := range m {
			if s, ok := val.(string); ok {
				out[k] = s
			}
		}
		return out
	default:
		return nil
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("...(%d bytes)", len(s)-n)
}
