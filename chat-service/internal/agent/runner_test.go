package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	chatport "github.com/zed/platepilot/shared/chat"
	domainchat "github.com/zed/platepilot/shared/domain/chat"
	domaintool "github.com/zed/platepilot/shared/domain/tool"

	"github.com/zed/platepilot/chat-service/internal/agent"
	"github.com/zed/platepilot/chat-service/internal/agent/toolreg"
)

// scriptedProvider is a queue-driven fake of all three chat ports for the
// graph tests. Each response queue pops strictly in order so a test fails
// loudly if the agent makes a call it did not script.
type scriptedProvider struct {
	mu sync.Mutex

	completeResps []domainchat.ChatResponse
	completeErr   error
	completeReqs  []domainchat.ChatRequest

	toolResps     []domainchat.ToolCallResponse
	toolErr       error
	toolReqs      []toolCallReq
	receivedSpecs [][]domaintool.ToolSpec

	supportTools    bool
	supportParallel bool

	chunks    []domainchat.ChatChunk
	streamErr error
}

type toolCallReq struct {
	req   domainchat.ChatRequest
	specs []domaintool.ToolSpec
}

func (p *scriptedProvider) Complete(_ context.Context, req domainchat.ChatRequest) (domainchat.ChatResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.completeReqs = append(p.completeReqs, req)
	if p.completeErr != nil {
		return domainchat.ChatResponse{}, p.completeErr
	}
	if len(p.completeResps) == 0 {
		return domainchat.ChatResponse{}, errors.New("scripted provider: no more Complete responses queued")
	}
	resp := p.completeResps[0]
	p.completeResps = p.completeResps[1:]
	return resp, nil
}

func (p *scriptedProvider) Stream(_ context.Context, req domainchat.ChatRequest) (chatport.ChatStream, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.completeReqs = append(p.completeReqs, req)
	chunks := p.chunks
	return &scriptedStream{chunks: chunks, err: p.streamErr}, nil
}

func (p *scriptedProvider) SupportsTools() bool         { return p.supportTools }
func (p *scriptedProvider) SupportsParallelTools() bool { return p.supportParallel }

func (p *scriptedProvider) ChatWithTools(ctx context.Context, req domainchat.ChatRequest, specs []domaintool.ToolSpec) (domainchat.ToolCallResponse, error) {
	if err := ctx.Err(); err != nil {
		return domainchat.ToolCallResponse{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.toolReqs = append(p.toolReqs, toolCallReq{req: req, specs: specs})
	p.receivedSpecs = append(p.receivedSpecs, specs)
	if p.toolErr != nil {
		return domainchat.ToolCallResponse{}, p.toolErr
	}
	if len(p.toolResps) == 0 {
		return domainchat.ToolCallResponse{}, errors.New("scripted provider: no more ChatWithTools responses queued")
	}
	resp := p.toolResps[0]
	p.toolResps = p.toolResps[1:]
	return resp, nil
}

type scriptedStream struct {
	chunks []domainchat.ChatChunk
	err    error
}

func (s *scriptedStream) Recv() (domainchat.ChatChunk, error) {
	if s.err != nil {
		return domainchat.ChatChunk{}, s.err
	}
	if len(s.chunks) == 0 {
		return domainchat.ChatChunk{}, io.EOF
	}
	chunk := s.chunks[0]
	s.chunks = s.chunks[1:]
	return chunk, nil
}

func (s *scriptedStream) Close() error { return nil }

func assistantText(content string) domainchat.ToolCallResponse {
	return domainchat.ToolCallResponse{
		Message:      domainchat.ChatMessage{Role: domainchat.RoleAssistant, Content: content},
		FinishReason: domainchat.FinishReasonStop,
	}
}

func toolCallResponse(id, name, args string) domainchat.ToolCallResponse {
	return domainchat.ToolCallResponse{
		Message: domainchat.ChatMessage{
			Role: domainchat.RoleAssistant,
			ToolCalls: []domaintool.ToolCall{{
				ID: id, Name: name, Arguments: json.RawMessage(args),
			}},
		},
		FinishReason: domainchat.FinishReasonToolCalls,
	}
}

func drain(events <-chan agent.Event) []agent.Event {
	var out []agent.Event
	for ev := range events {
		out = append(out, ev)
	}
	return out
}

func newTestRunner(t *testing.T, p *scriptedProvider, reg *toolreg.Registry, maxRounds int) *agent.Runner {
	t.Helper()
	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: maxRounds}, agent.Deps{
		Chat:        p,
		ToolCalling: p,
		Registry:    reg,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner
}

func echoEntry() toolreg.Entry {
	return toolreg.Entry{
		Spec: domaintool.ToolSpec{
			Name:        "echo_ping",
			Description: "test stub that pings",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"word":{"type":"string"}},"additionalProperties":false}`),
			ReadOnly:    true,
		},
		Handler: func(_ context.Context, raw json.RawMessage) (domaintool.ToolResult, error) {
			return domaintool.ToolResult{
				Status:  domaintool.ToolStatusOK,
				Content: "pong: " + string(raw),
			}, nil
		},
	}
}

func TestRunnerDirectAnswerWithoutTools(t *testing.T) {
	provider := &scriptedProvider{
		supportTools: true,
		completeResps: []domainchat.ChatResponse{{
			Message:      domainchat.ChatMessage{Role: domainchat.RoleAssistant, Content: "你好，我是 PlatePilot。"},
			FinishReason: domainchat.FinishReasonStop,
			Usage:        domainchat.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		}},
	}
	runner := newTestRunner(t, provider, toolreg.New(0), 3)

	result, eventsCh := runner.Run(context.Background(), agent.TurnInput{
		ThreadID:  "th-1",
		UserInput: "你好",
	})
	events := drain(eventsCh)

	if result == nil {
		t.Fatal("expected a result")
	}
	if result.Answer == nil || result.Answer.Text != "你好，我是 PlatePilot。" {
		t.Fatalf("unexpected answer: %+v", result.Answer)
	}
	if result.ToolRounds != 0 {
		t.Fatalf("ToolRounds = %d, want 0", result.ToolRounds)
	}
	if result.Usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	assertEventSequence(t, events,
		agent.EventStart, agent.EventDelta, agent.EventEnd)
	if events[1].Delta != "你好，我是 PlatePilot。" {
		t.Fatalf("delta = %q", events[1].Delta)
	}
	if len(provider.toolReqs) != 0 {
		t.Fatalf("expected no tool-capable calls, got %d", len(provider.toolReqs))
	}
}

func TestRunnerToolCallThenSecondAnswer(t *testing.T) {
	reg := toolreg.New(time.Second)
	if err := reg.Register(echoEntry()); err != nil {
		t.Fatalf("register: %v", err)
	}
	provider := &scriptedProvider{
		supportTools: true,
		toolResps: []domainchat.ToolCallResponse{
			toolCallResponse("call-1", "echo_ping", `{"word":"hi"}`),
			assistantText("第二轮直答完成"),
		},
	}
	runner := newTestRunner(t, provider, reg, 3)

	result, eventsCh := runner.Run(context.Background(), agent.TurnInput{
		ThreadID:  "th-2",
		UserInput: "帮我 ping 一下",
	})
	events := drain(eventsCh)

	if result == nil || result.Answer.Text != "第二轮直答完成" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.ToolRounds != 1 {
		t.Fatalf("ToolRounds = %d, want 1", result.ToolRounds)
	}
	assertEventSequence(t, events,
		agent.EventStart, agent.EventToolStart, agent.EventToolFinish,
		agent.EventDelta, agent.EventEnd)
	toolStart := events[1]
	if toolStart.CallID != "call-1" || toolStart.Tool != "echo_ping" {
		t.Fatalf("tool.start = %+v", toolStart)
	}
	toolFinish := events[2]
	if !toolFinish.OK || toolFinish.LatencyMS < 0 {
		t.Fatalf("tool.finish = %+v", toolFinish)
	}
	if len(provider.toolReqs) != 2 {
		t.Fatalf("expected 2 tool-capable model calls, got %d", len(provider.toolReqs))
	}
	specs := provider.receivedSpecs[0]
	if len(specs) != 1 || specs[0].Name != "echo_ping" {
		t.Fatalf("bound specs = %+v", specs)
	}
}

func TestRunnerMaxToolRoundsCapsLoop(t *testing.T) {
	reg := toolreg.New(time.Second)
	if err := reg.Register(echoEntry()); err != nil {
		t.Fatalf("register: %v", err)
	}
	provider := &scriptedProvider{
		supportTools: true,
		toolResps: []domainchat.ToolCallResponse{
			toolCallResponse("c1", "echo_ping", `{}`),
			toolCallResponse("c2", "echo_ping", `{}`),
			toolCallResponse("c3", "echo_ping", `{}`),
		},
	}
	runner := newTestRunner(t, provider, reg, 2)

	result, eventsCh := runner.Run(context.Background(), agent.TurnInput{
		ThreadID:  "th-3",
		UserInput: "一直调工具",
	})
	events := drain(eventsCh)

	if result == nil {
		t.Fatal("expected a capped but successful turn")
	}
	if result.ToolRounds != 2 {
		t.Fatalf("ToolRounds = %d, want 2", result.ToolRounds)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("expected a round-cap warning")
	}
	finishCount := 0
	for _, ev := range events {
		if ev.Type == agent.EventToolFinish {
			finishCount++
		}
		if ev.Type == agent.EventError {
			t.Fatalf("unexpected error event: %s", ev.Message)
		}
	}
	if finishCount != 2 {
		t.Fatalf("tool.finish count = %d, want 2", finishCount)
	}
	if events[len(events)-1].Type != agent.EventEnd {
		t.Fatalf("last event = %s, want message.end", events[len(events)-1].Type)
	}
}

func TestRunnerContextCanceledReturnsNoSuccess(t *testing.T) {
	started := make(chan struct{})
	reg := toolreg.New(time.Second)
	err := reg.Register(toolreg.Entry{
		Spec: domaintool.ToolSpec{
			Name:       "blocking_wait",
			Parameters: json.RawMessage(`{"type":"object"}`),
			ReadOnly:   true,
		},
		Handler: func(ctx context.Context, _ json.RawMessage) (domaintool.ToolResult, error) {
			close(started)
			<-ctx.Done()
			return domaintool.ToolResult{}, ctx.Err()
		},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	provider := &scriptedProvider{
		supportTools: true,
		toolResps: []domainchat.ToolCallResponse{
			toolCallResponse("c1", "blocking_wait", `{}`),
			assistantText("不应到达"),
		},
	}
	runner := newTestRunner(t, provider, reg, 3)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()

	result, eventsCh := runner.Run(ctx, agent.TurnInput{
		ThreadID:  "th-4",
		UserInput: "阻塞中取消",
	})
	events := drain(eventsCh)

	if result != nil {
		t.Fatalf("expected nil result on cancellation, got %+v", result)
	}
	var sawError bool
	for _, ev := range events {
		if ev.Type == agent.EventEnd {
			t.Fatal("must not emit message.end on a canceled turn")
		}
		if ev.Type == agent.EventError {
			sawError = true
		}
	}
	if !sawError {
		t.Fatal("expected an error event on cancellation")
	}
}

func TestRunnerRejectsEmptyInput(t *testing.T) {
	provider := &scriptedProvider{supportTools: true}
	runner := newTestRunner(t, provider, toolreg.New(0), 3)

	result, eventsCh := runner.Run(context.Background(), agent.TurnInput{ThreadID: "th-5"})
	events := drain(eventsCh)
	if result != nil {
		t.Fatalf("expected nil result, got %+v", result)
	}
	if len(events) != 1 || events[0].Type != agent.EventError || events[0].Code != "invalid_argument" {
		t.Fatalf("events = %+v", events)
	}
}

func TestRunnerRejectsToolsWithoutProviderSupport(t *testing.T) {
	reg := toolreg.New(time.Second)
	if err := reg.Register(echoEntry()); err != nil {
		t.Fatalf("register: %v", err)
	}
	provider := &scriptedProvider{supportTools: false}
	runner := newTestRunner(t, provider, reg, 3)

	result, eventsCh := runner.Run(context.Background(), agent.TurnInput{
		ThreadID:  "th-6",
		UserInput: "需要工具",
	})
	events := drain(eventsCh)
	if result != nil {
		t.Fatalf("expected nil result, got %+v", result)
	}
	if len(events) != 1 || events[0].Code != "invalid_argument" {
		t.Fatalf("events = %+v", events)
	}
}

func assertEventSequence(t *testing.T, events []agent.Event, want ...agent.EventType) {
	t.Helper()
	if len(events) != len(want) {
		got := make([]agent.EventType, len(events))
		for i, ev := range events {
			got[i] = ev.Type
		}
		t.Fatalf("event sequence = %v, want %v", got, want)
	}
	for i, wantType := range want {
		if events[i].Type != wantType {
			t.Fatalf("event %d = %s, want %s", i, events[i].Type, wantType)
		}
	}
}
