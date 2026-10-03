package chat

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/zed1995/platepilot/shared/domain/tool"
)

func TestChatRequestJSONRoundTrip(t *testing.T) {
	temperature := 0.2
	original := ChatRequest{
		ThreadID: "thread-1",
		Messages: []ChatMessage{
			{Role: RoleSystem, Content: "you are a helpful guide"},
			{
				Role:    RoleAssistant,
				Content: "",
				ToolCalls: []tool.ToolCall{
					{ID: "call-1", Name: "search_restaurants", Arguments: json.RawMessage(`{"top_k":3}`)},
				},
			},
			{Role: RoleTool, ToolCallID: "call-1", Name: "search_restaurants", Content: `{"ok":true}`},
		},
		Model:           "test-model",
		Temperature:     &temperature,
		MaxTokens:       256,
		Metadata:        map[string]string{"locale": "zh-CN"},
		ProviderOptions: map[string]any{"reasoning_effort": "low"},
	}

	var decoded ChatRequest
	roundTrip(t, original, &decoded)
	if !reflect.DeepEqual(original, decoded) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", decoded, original)
	}
}

func TestChatResponseJSONRoundTrip(t *testing.T) {
	original := ChatResponse{
		Message:      ChatMessage{Role: RoleAssistant, Content: "hello"},
		FinishReason: FinishReasonStop,
		Usage:        TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		Model:        "test-model",
	}
	var decoded ChatResponse
	roundTrip(t, original, &decoded)
	if !reflect.DeepEqual(original, decoded) {
		t.Fatalf("round trip mismatch: got %+v want %+v", decoded, original)
	}
}

func TestRoleValid(t *testing.T) {
	for _, role := range []Role{RoleSystem, RoleUser, RoleAssistant, RoleTool} {
		if !role.Valid() {
			t.Errorf("%q should be valid", role)
		}
	}
	if Role("narrator").Valid() {
		t.Error("unknown roles must be invalid")
	}
}

func roundTrip(t *testing.T, in any, out any) {
	t.Helper()
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
}
