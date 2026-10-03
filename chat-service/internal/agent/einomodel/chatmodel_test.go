package einomodel_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/cloudwego/eino/schema"
	einojsonschema "github.com/eino-contrib/jsonschema"

	chatport "github.com/zed1995/platepilot/shared/chat"
	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	domaintool "github.com/zed1995/platepilot/shared/domain/tool"

	"github.com/zed1995/platepilot/chat-service/internal/agent/einomodel"
)

type fakeChat struct {
	completeResp domainchat.ChatResponse
	completeErr  error
	lastRequest  domainchat.ChatRequest

	chunks []domainchat.ChatChunk
}

func (f *fakeChat) Complete(_ context.Context, req domainchat.ChatRequest) (domainchat.ChatResponse, error) {
	f.lastRequest = req
	return f.completeResp, f.completeErr
}

func (f *fakeChat) Stream(_ context.Context, req domainchat.ChatRequest) (chatport.ChatStream, error) {
	f.lastRequest = req
	return &fakeStream{chunks: f.chunks}, nil
}

type fakeTools struct {
	supported bool
	resp      domainchat.ToolCallResponse
	err       error
	lastSpecs []domaintool.ToolSpec
	lastReq   domainchat.ChatRequest
}

func (f *fakeTools) SupportsTools() bool         { return f.supported }
func (f *fakeTools) SupportsParallelTools() bool { return false }
func (f *fakeTools) ChatWithTools(_ context.Context, req domainchat.ChatRequest, specs []domaintool.ToolSpec) (domainchat.ToolCallResponse, error) {
	f.lastReq = req
	f.lastSpecs = specs
	return f.resp, f.err
}

type fakeStream struct {
	chunks []domainchat.ChatChunk
}

func (s *fakeStream) Recv() (domainchat.ChatChunk, error) {
	if len(s.chunks) == 0 {
		return domainchat.ChatChunk{}, io.EOF
	}
	c := s.chunks[0]
	s.chunks = s.chunks[1:]
	return c, nil
}
func (s *fakeStream) Close() error { return nil }

func TestNewRequiresChat(t *testing.T) {
	if _, err := einomodel.New(einomodel.Deps{}); err == nil {
		t.Fatal("expected error without Chat provider")
	}
}

func TestGenerateWithoutToolsUsesComplete(t *testing.T) {
	chat := &fakeChat{completeResp: domainchat.ChatResponse{
		Message:      domainchat.ChatMessage{Role: domainchat.RoleAssistant, Content: "hi"},
		FinishReason: domainchat.FinishReasonStop,
		Usage:        domainchat.TokenUsage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3},
	}}
	m, err := einomodel.New(einomodel.Deps{Chat: chat})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := m.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "hi" || msg.Role != schema.Assistant {
		t.Fatalf("msg = %+v", msg)
	}
	if msg.ResponseMeta == nil || msg.ResponseMeta.FinishReason != "stop" ||
		msg.ResponseMeta.Usage.TotalTokens != 3 {
		t.Fatalf("meta = %+v", msg.ResponseMeta)
	}
}

func TestGenerateWithToolsUsesChatWithTools(t *testing.T) {
	tools := &fakeTools{
		supported: true,
		resp: domainchat.ToolCallResponse{
			Message: domainchat.ChatMessage{
				Role: domainchat.RoleAssistant,
				ToolCalls: []domaintool.ToolCall{{
					ID: "tc-1", Name: "search_restaurants",
					Arguments: json.RawMessage(`{"query":"italian"}`),
				}},
			},
			FinishReason: domainchat.FinishReasonToolCalls,
		},
	}
	base, err := einomodel.New(einomodel.Deps{Chat: &fakeChat{}, ToolCalling: tools})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := base.WithTools([]*schema.ToolInfo{{
		Name: "search_restaurants",
		Desc: "search",
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(mustSchema(t, `{
			"type":"object",
			"properties":{"query":{"type":"string"}},
			"required":["query"]
		}`)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := bound.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "italian?"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "tc-1" ||
		msg.ToolCalls[0].Function.Name != "search_restaurants" ||
		msg.ToolCalls[0].Function.Arguments != `{"query":"italian"}` {
		t.Fatalf("tool calls = %+v", msg.ToolCalls)
	}
	if msg.ResponseMeta.FinishReason != "tool_calls" {
		t.Fatalf("finish = %q", msg.ResponseMeta.FinishReason)
	}
	if len(tools.lastSpecs) != 1 || tools.lastSpecs[0].Name != "search_restaurants" {
		t.Fatalf("specs = %+v", tools.lastSpecs)
	}
	var specArgs map[string]any
	if err := json.Unmarshal(tools.lastSpecs[0].Parameters, &specArgs); err != nil {
		t.Fatalf("spec params not json: %v", err)
	}
	if specArgs["type"] != "object" {
		t.Fatalf("spec params lost schema: %s", tools.lastSpecs[0].Parameters)
	}
	if tools.lastReq.Messages[0].Role != domainchat.RoleUser {
		t.Fatalf("message role mapping = %q", tools.lastReq.Messages[0].Role)
	}
}

func TestGenerateWithToolsUnsupported(t *testing.T) {
	tools := &fakeTools{supported: false}
	base, err := einomodel.New(einomodel.Deps{Chat: &fakeChat{}, ToolCalling: tools})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := base.WithTools([]*schema.ToolInfo{{Name: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bound.Generate(context.Background(), []*schema.Message{}); err == nil {
		t.Fatal("expected error when provider lacks tool support")
	}
}

func TestGeneratePropagatesProviderError(t *testing.T) {
	want := errors.New("boom")
	chat := &fakeChat{completeErr: want}
	m, _ := einomodel.New(einomodel.Deps{Chat: chat})
	if _, err := m.Generate(context.Background(), nil); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestStreamPumpsDeltasAndTerminalMeta(t *testing.T) {
	chat := &fakeChat{chunks: []domainchat.ChatChunk{
		{Delta: "你"},
		{Delta: "好"},
		{
			FinishReason: domainchat.FinishReasonStop,
			Usage:        &domainchat.TokenUsage{InputTokens: 4, OutputTokens: 2, TotalTokens: 6},
		},
	}}
	m, _ := einomodel.New(einomodel.Deps{Chat: chat})
	reader, err := m.Stream(context.Background(), []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	var content string
	var finish string
	var usage *schema.TokenUsage
	for {
		msg, recvErr := reader.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatal(recvErr)
		}
		content += msg.Content
		if msg.ResponseMeta != nil {
			finish = msg.ResponseMeta.FinishReason
			usage = msg.ResponseMeta.Usage
		}
	}
	if content != "你好" {
		t.Fatalf("content = %q", content)
	}
	if finish != "stop" || usage == nil || usage.TotalTokens != 6 {
		t.Fatalf("finish=%q usage=%+v", finish, usage)
	}
}

func TestStreamToolCallChunks(t *testing.T) {
	chat := &fakeChat{chunks: []domainchat.ChatChunk{
		{ToolCalls: []domaintool.ToolCall{{ID: "tc-9", Name: "search_restaurants",
			Arguments: json.RawMessage(`{"query":"ramen"}`)}}},
		{FinishReason: domainchat.FinishReasonToolCalls},
	}}
	m, _ := einomodel.New(einomodel.Deps{Chat: chat})
	reader, err := m.Stream(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	first, err := reader.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if len(first.ToolCalls) != 1 || first.ToolCalls[0].Function.Name != "search_restaurants" {
		t.Fatalf("first chunk tool calls = %+v", first.ToolCalls)
	}
}

// TestToEinoMessagesPreservesToolRoundTripFields guards the tool-call wire
// invariant: on the second plan round the model receives a tool result
// message, and strict OpenAI-compatible providers reject a tool message whose
// tool_call_id is empty with "tool messages must include a non-empty string
// tool_call_id". The id must survive the domain -> Eino -> domain hop.
func TestToEinoMessagesPreservesToolRoundTripFields(t *testing.T) {
	msgs := []domainchat.ChatMessage{
		{Role: domainchat.RoleUser, Content: "midtown sushi?"},
		{Role: domainchat.RoleAssistant, ToolCalls: []domaintool.ToolCall{{
			ID: "call-1", Name: "search_restaurants", Arguments: json.RawMessage(`{"q":"x"}`),
		}}},
		{Role: domainchat.RoleTool, ToolCallID: "call-1", Name: "search_restaurants", Content: `{"ok":true}`},
	}
	out := einomodel.ToEinoMessages(msgs)
	if len(out) != 3 {
		t.Fatalf("messages = %d, want 3", len(out))
	}
	if got := out[2].ToolCallID; got != "call-1" {
		t.Fatalf("tool message ToolCallID = %q, want call-1", got)
	}
	if got := out[1].ToolCalls[0].ID; got != "call-1" {
		t.Fatalf("assistant tool call id = %q, want call-1", got)
	}
}

func mustSchema(t *testing.T, raw string) *einojsonschema.Schema {
	t.Helper()
	s := &einojsonschema.Schema{}
	if err := json.Unmarshal([]byte(raw), s); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	return s
}
