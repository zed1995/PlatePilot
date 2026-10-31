package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/errs"
	domaintool "github.com/zed1995/platepilot/shared/domain/tool"

	"github.com/zed1995/platepilot/chat-service/internal/agent"
	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
)

// scriptedProvider is a queue-driven fake of the chat model for the graph
// tests. Each response queue pops strictly in order so a test fails loudly if
// the agent makes a call it did not script.
//
// It implements model.ToolCallingChatModel directly: the runner binds the
// registry's tools through WithTools and then calls Generate on the returned
// view, so the fake models both the bound and the unbound call instead of three
// separate domain ports.
type scriptedProvider struct {
	mu sync.Mutex

	// name identifies the instance. A test that routes planning to one model
	// and answering to another asserts which instance recorded the call,
	// because a model instance carries its own identity and no request field
	// does.
	name string
	// gate, when set, runs before every model call on this instance. The
	// blocking tests use it to hold a turn inside a model round; it is a field
	// rather than an overridden method so a WithTools view still honours it.
	gate func(context.Context) error

	completeResps []domainchat.ChatResponse
	completeErr   error
	completeReqs  []domainchat.ChatRequest

	toolResps     []domainchat.ToolCallResponse
	toolErr       error
	toolReqs      []toolCallReq
	receivedSpecs [][]domaintool.ToolSpec

	// supportTools reports whether a bound view accepts tools at all. The
	// runner decides that from configuration (Deps.ToolsUnsupported), so this
	// only guards against a test scripting a tool round against a provider it
	// declared incapable.
	supportTools bool

	structuredResps []domainchat.StructuredResponse
	structuredErr   error
	structuredReqs  []domainchat.StructuredRequest

	chunks    []domainchat.ChatChunk
	streamErr error
	// chunkDelay paces the stream, one wait per chunk; completeDelay is the
	// same cost charged to a whole completion. They exist for the perf harness,
	// where an instantaneous fake would make time-to-first-text unmeasurable:
	// the point there is to hold the decode cost still and measure what the
	// runtime does with it. Both are zero everywhere else.
	chunkDelay    time.Duration
	completeDelay time.Duration
}

type toolCallReq struct {
	req   domainchat.ChatRequest
	specs []domaintool.ToolSpec
}

var _ model.ToolCallingChatModel = (*scriptedProvider)(nil)

// WithTools returns a view bound to the given tools. It is a copy, matching the
// interface contract: the unbound provider stays usable for answer composition.
func (p *scriptedProvider) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return &boundProvider{base: p, tools: tools}, nil
}

func (p *scriptedProvider) Generate(
	ctx context.Context, in []*schema.Message, opts ...model.Option,
) (*schema.Message, error) {
	return p.generate(ctx, in, nil, opts...)
}

func (p *scriptedProvider) Stream(
	ctx context.Context, in []*schema.Message, _ ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	if err := p.wait(ctx); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.completeReqs = append(p.completeReqs, toDomainMessages(in))
	// An error here is a provider that cannot stream at all, which is what the
	// answer composer recovers from by falling back to a one-shot completion.
	// A stream that opens and then dies is a different case, and tests that want
	// it drive it through the chunk queue.
	if p.streamErr != nil {
		return nil, p.streamErr
	}
	chunks := p.chunks
	if len(chunks) == 0 && len(p.completeResps) > 0 {
		// No streamed script: serve the next one-shot answer as a single chunk.
		// It keeps a turn that only queued completeResps behaving the same
		// whether or not streaming is switched on, so enabling the flag cannot
		// silently change what an existing test asserts.
		resp := p.completeResps[0]
		p.completeResps = p.completeResps[1:]
		if resp.Message.Content != "" {
			chunks = []domainchat.ChatChunk{{
				Delta:        resp.Message.Content,
				FinishReason: resp.FinishReason,
			}}
		}
	}
	delay := p.chunkDelay
	reader, writer := schema.Pipe[*schema.Message](0)
	go func() {
		defer writer.Close()
		for _, chunk := range chunks {
			// The wait models decode time, so it comes before a token and not
			// before the end of the stream.
			if delay > 0 {
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return
				}
			}
			if writer.Send(chunkToSchema(chunk), nil) {
				// The consumer closed the stream; further sends are pointless.
				return
			}
		}
	}()
	return reader, nil
}

// wait holds the call until the instance's gate opens, or reports that the
// caller's context ended. An ungated provider returns immediately.
func (p *scriptedProvider) wait(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.gate != nil {
		return p.gate(ctx)
	}
	return nil
}

// generate serves one model round. A tool-bound call consumes the tool queue;
// an unbound call — which is also what a bound view with no tools asks for —
// consumes the completion queue.
func (p *scriptedProvider) generate(
	ctx context.Context, in []*schema.Message, tools []*schema.ToolInfo, _ ...model.Option,
) (*schema.Message, error) {
	if err := p.wait(ctx); err != nil {
		return nil, err
	}
	if len(tools) > 0 && !p.supportTools {
		return nil, errs.New(errs.CodeInvalidArgument,
			"scripted provider: configured without tool support")
	}
	if len(tools) == 0 && p.completeDelay > 0 {
		// A completion pays the same decode cost as the equivalent stream — the
		// tokens take that long either way; the difference under test is when
		// the runtime lets the client see them, not how fast the model is.
		select {
		case <-time.After(p.completeDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	req := toDomainMessages(in)
	if len(tools) == 0 {
		p.completeReqs = append(p.completeReqs, req)
		if p.completeErr != nil {
			return nil, p.completeErr
		}
		if len(p.completeResps) == 0 {
			return nil, errors.New("scripted provider: no more Complete responses queued")
		}
		resp := p.completeResps[0]
		p.completeResps = p.completeResps[1:]
		return responseToSchema(resp), nil
	}

	specs := toDomainSpecs(tools)
	p.toolReqs = append(p.toolReqs, toolCallReq{req: req, specs: specs})
	p.receivedSpecs = append(p.receivedSpecs, specs)
	if p.toolErr != nil {
		return nil, p.toolErr
	}
	if len(p.toolResps) == 0 {
		return nil, errors.New("scripted provider: no more tool responses queued")
	}
	resp := p.toolResps[0]
	p.toolResps = p.toolResps[1:]
	return responseToSchema(domainchat.ChatResponse{
		Message:      resp.Message,
		FinishReason: resp.FinishReason,
		Usage:        resp.Usage,
		Model:        resp.Model,
	}), nil
}

// boundProvider is the tool-bound view WithTools returns.
//
// A non-empty tool list consumes the tool-response queue and records which
// specs were bound; an empty list falls through to the completion queue, which
// is what a runner with no registered tools actually asks for.
type boundProvider struct {
	base  *scriptedProvider
	tools []*schema.ToolInfo
}

var _ model.ToolCallingChatModel = (*boundProvider)(nil)

func (b *boundProvider) Generate(
	ctx context.Context, in []*schema.Message, opts ...model.Option,
) (*schema.Message, error) {
	return b.base.generate(ctx, in, b.tools, opts...)
}

func (b *boundProvider) Stream(
	ctx context.Context, in []*schema.Message, opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return b.base.Stream(ctx, in, opts...)
}

func (b *boundProvider) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return &boundProvider{base: b.base, tools: tools}, nil
}

// extractor returns a model view that consumes the structured-response queue.
// The slot extractor asks for JSON once per turn and is its only caller, so its
// requests are recorded separately from the planning rounds.
func (p *scriptedProvider) extractor() model.BaseChatModel {
	return &extractorModel{provider: p}
}

type extractorModel struct{ provider *scriptedProvider }

var _ model.BaseChatModel = (*extractorModel)(nil)

func (m *extractorModel) Generate(
	ctx context.Context, in []*schema.Message, _ ...model.Option,
) (*schema.Message, error) {
	if err := m.provider.wait(ctx); err != nil {
		return nil, err
	}
	p := m.provider
	p.mu.Lock()
	defer p.mu.Unlock()
	p.structuredReqs = append(p.structuredReqs, domainchat.StructuredRequest{
		Messages: toDomainMessages(in).Messages,
	})
	if p.structuredErr != nil {
		return nil, p.structuredErr
	}
	if len(p.structuredResps) == 0 {
		return nil, errors.New("scripted provider: no more structured responses queued")
	}
	resp := p.structuredResps[0]
	p.structuredResps = p.structuredResps[1:]
	return schema.AssistantMessage(resp.Content, nil), nil
}

func (m *extractorModel) Stream(
	context.Context, []*schema.Message, ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("scripted provider: the extractor does not stream")
}

// responseToSchema maps a scripted domain response onto the model interface's
// message shape. It is the only conversion in this fake: everything the tests
// assert about requests goes through toDomainMessages instead.
func responseToSchema(resp domainchat.ChatResponse) *schema.Message {
	msg := &schema.Message{
		Role:       schema.RoleType(resp.Message.Role),
		Content:    resp.Message.Content,
		Name:       resp.Message.Name,
		ToolCallID: resp.Message.ToolCallID,
	}
	for _, call := range resp.Message.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, schemaToolCall(call))
	}
	msg.ResponseMeta = &schema.ResponseMeta{FinishReason: string(resp.FinishReason)}
	if resp.Usage.TotalTokens != 0 || resp.Usage.InputTokens != 0 || resp.Usage.OutputTokens != 0 {
		msg.ResponseMeta.Usage = &schema.TokenUsage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		}
	}
	return msg
}

func chunkToSchema(chunk domainchat.ChatChunk) *schema.Message {
	msg := &schema.Message{
		Role:             schema.Assistant,
		Content:          chunk.Delta,
		ReasoningContent: chunk.Reasoning,
	}
	for _, call := range chunk.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, schemaToolCall(call))
	}
	msg.ResponseMeta = &schema.ResponseMeta{FinishReason: string(chunk.FinishReason)}
	if chunk.Usage != nil {
		msg.ResponseMeta.Usage = &schema.TokenUsage{
			PromptTokens:     chunk.Usage.InputTokens,
			CompletionTokens: chunk.Usage.OutputTokens,
			TotalTokens:      chunk.Usage.TotalTokens,
		}
	}
	return msg
}

func schemaToolCall(call domaintool.ToolCall) schema.ToolCall {
	return schema.ToolCall{
		ID:       call.ID,
		Type:     "function",
		Function: schema.FunctionCall{Name: call.Name, Arguments: string(call.Arguments)},
	}
}

// toDomainMessages records a request in the DTO the tests assert against. The
// conversion lives in the fake, not in production code: production nodes pass
// schema.Message straight to the model.
func toDomainMessages(in []*schema.Message) domainchat.ChatRequest {
	msgs := make([]domainchat.ChatMessage, 0, len(in))
	for _, msg := range in {
		if msg == nil {
			continue
		}
		out := domainchat.ChatMessage{
			Role:       domainchat.Role(msg.Role),
			Content:    msg.Content,
			Name:       msg.Name,
			ToolCallID: msg.ToolCallID,
		}
		for _, call := range msg.ToolCalls {
			out.ToolCalls = append(out.ToolCalls, domaintool.ToolCall{
				ID:        call.ID,
				Name:      call.Function.Name,
				Arguments: json.RawMessage(call.Function.Arguments),
			})
		}
		msgs = append(msgs, out)
	}
	return domainchat.ChatRequest{Messages: msgs}
}

func toDomainSpecs(infos []*schema.ToolInfo) []domaintool.ToolSpec {
	specs := make([]domaintool.ToolSpec, 0, len(infos))
	for _, info := range infos {
		spec := domaintool.ToolSpec{Name: info.Name, Description: info.Desc}
		if info.ParamsOneOf != nil {
			if js, err := info.ParamsOneOf.ToJSONSchema(); err == nil {
				if raw, err := json.Marshal(js); err == nil {
					spec.Parameters = raw
				}
			}
		}
		specs = append(specs, spec)
	}
	return specs
}

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
		PlanModel:   p,
		AnswerModel: p,
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
	// Tool capability is now a configuration fact rather than something the
	// model interface reveals: an OpenAI-compatible model implements WithTools
	// whether or not the endpoint honours it, so the application reports it.
	provider := &scriptedProvider{supportTools: false}
	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 3}, agent.Deps{
		PlanModel:        provider,
		AnswerModel:      provider,
		ToolsUnsupported: true,
		Registry:         reg,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

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
