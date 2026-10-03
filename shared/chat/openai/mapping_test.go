package openai

import (
	"encoding/json"
	"testing"

	"github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/tool"
)

func TestFunctionArgumentsEnvelope(t *testing.T) {
	// The protocol carries arguments as a JSON string containing JSON.
	raw, err := json.Marshal(wireToolCall{
		ID:   "call_1",
		Type: "function",
		Function: wireFunctionCall{
			Name:      "search_restaurants",
			Arguments: functionArguments{Raw: json.RawMessage(`{"borough":"manhattan"}`)},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got wireToolCall
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Function.Name != "search_restaurants" {
		t.Errorf("name = %q", got.Function.Name)
	}
	if string(got.Function.Arguments.Raw) != `{"borough":"manhattan"}` {
		t.Errorf("arguments roundtrip = %q", got.Function.Arguments.Raw)
	}
}

func TestFunctionArgumentsRejectsNonString(t *testing.T) {
	var got functionArguments
	err := json.Unmarshal([]byte(`{"oops":1}`), &got)
	if err == nil {
		t.Fatal("want error decoding a non-string arguments value")
	}
}

func TestToWireMessagesAndTools(t *testing.T) {
	msgs := toWireMessages([]chat.ChatMessage{
		{Role: chat.RoleSystem, Content: "be brief"},
		{Role: chat.RoleUser, Content: "dinner ideas"},
		{Role: chat.RoleAssistant, ToolCalls: []tool.ToolCall{
			{ID: "c1", Name: "search_restaurants", Arguments: json.RawMessage(`{"top_k":3}`)},
		}},
		{Role: chat.RoleTool, ToolCallID: "c1", Content: `{"restaurants":[]}`},
	})
	if len(msgs) != 4 || msgs[2].ToolCalls[0].Function.Name != "search_restaurants" {
		t.Fatalf("messages mapping wrong: %+v", msgs)
	}
	if msgs[3].ToolCallID != "c1" {
		t.Errorf("tool_call_id lost: %+v", msgs[3])
	}

	tools := toWireTools([]tool.ToolSpec{{
		Name:        "search_restaurants",
		Description: "find restaurants",
		Parameters:  json.RawMessage(`{"type":"object"}`),
	}})
	if tools[0].Type != "function" || tools[0].Function.Name != "search_restaurants" {
		t.Fatalf("tool mapping wrong: %+v", tools[0])
	}
	// An empty schema becomes an explicit object schema, never a null.
	empty := toWireTools([]tool.ToolSpec{{Name: "noop"}})
	if string(empty[0].Function.Parameters) == "null" {
		t.Errorf("empty parameters must default to an object schema, got null")
	}
}

func TestToDomainMessageRejectsBadArguments(t *testing.T) {
	wm := wireMessage{
		Role: "assistant",
		ToolCalls: []wireToolCall{{
			ID:       "c1",
			Function: wireFunctionCall{Name: "search_restaurants", Arguments: functionArguments{Raw: json.RawMessage(`not-json`)}},
		}},
	}
	_, err := toDomainMessage(wm)
	if err == nil || errs.CodeOf(err) != errs.CodeProviderUnavailable {
		t.Fatalf("want provider_unavailable for bad arguments, got %v", err)
	}
}

func TestMergeExtraBodyReservesCoreKeys(t *testing.T) {
	core := completionRequest{Model: "vendor/model", Messages: []wireMessage{{Role: "user", Content: "hi"}}}
	merged, err := mergeExtraBody(core, map[string]any{
		"model":     "hijacked",
		"provider":  map[string]any{"route": "fallback"},
		"reasoning": map[string]any{"effort": "low"},
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(merged, &body); err != nil {
		t.Fatalf("decode merged: %v", err)
	}
	if body["model"] != "vendor/model" {
		t.Errorf("core model key must win, got %v", body["model"])
	}
	if body["provider"] == nil || body["reasoning"] == nil {
		t.Errorf("extra_body keys missing: %v", body)
	}
}

func TestExtractJSONObject(t *testing.T) {
	cases := map[string]bool{
		`{"a":1}`:                      true,
		"```json\n{\"a\":1}\n```":      true,
		"Sure! ```{\"a\":1}``` thanks": true,
		`Here: {"a":1} done`:           true,
		"":                             false,
		"no json here":                 false,
		`{"a":}`:                       false,
	}
	for in, wantOK := range cases {
		got := extractJSONObject(in)
		if (got != nil) != wantOK {
			t.Errorf("extractJSONObject(%q) = %s, want ok=%v", in, got, wantOK)
		}
	}
}
