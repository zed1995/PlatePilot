// Package einomodel adapts PlatePilot's vendor-neutral chat ports onto Eino's
// model.ToolCallingChatModel interface.
//
// It is one of the only two packages in the codebase allowed to name Eino
// types (the other is agent/toolreg). Every mapping between schema.Message and
// the domain chat DTOs lives here so the rest of the agent can program against
// project-owned types.
package einomodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	chatport "github.com/zed/platepilot/shared/chat"
	domainchat "github.com/zed/platepilot/shared/domain/chat"
	"github.com/zed/platepilot/shared/domain/errs"
	domaintool "github.com/zed/platepilot/shared/domain/tool"
)

// Deps are the chat ports the adapter delegates to. ToolCalling may be nil when
// the runtime never binds tools; a call on a tool-bound copy then fails loudly
// instead of silently degrading to plain completion.
type Deps struct {
	Chat        chatport.ChatProvider
	ToolCalling chatport.ToolCallingProvider
	// Model overrides the provider's default model name on every request.
	Model string
}

// New builds a tool-unbound Eino chat model over the project ports.
func New(deps Deps) (*Model, error) {
	if deps.Chat == nil {
		return nil, errors.New("einomodel: Chat provider is required")
	}
	return &Model{deps: deps}, nil
}

// Model implements model.ToolCallingChatModel. The zero value of Model is not
// usable; construct it with New.
type Model struct {
	deps  Deps
	specs []domaintool.ToolSpec
}

// WithTools returns a new model instance with the given Eino tools bound. The
// Eino tool descriptions are converted back into the project's tool specs, so
// the provider adapter never sees schema.ToolInfo.
func (m *Model) WithTools(infos []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	specs := make([]domaintool.ToolSpec, 0, len(infos))
	for _, info := range infos {
		if info == nil {
			continue
		}
		spec := domaintool.ToolSpec{
			Name:        info.Name,
			Description: info.Desc,
			ReadOnly:    true,
		}
		if info.ParamsOneOf != nil {
			jsonSchema, err := info.ParamsOneOf.ToJSONSchema()
			if err != nil {
				return nil, fmt.Errorf("einomodel: convert schema of tool %q: %w", info.Name, err)
			}
			raw, err := json.Marshal(jsonSchema)
			if err != nil {
				return nil, fmt.Errorf("einomodel: marshal schema of tool %q: %w", info.Name, err)
			}
			spec.Parameters = raw
		}
		specs = append(specs, spec)
	}
	return &Model{deps: m.deps, specs: specs}, nil
}

// Generate produces one complete response.
func (m *Model) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	req := m.request(in, opts)
	if len(m.specs) > 0 {
		if m.deps.ToolCalling == nil || !m.deps.ToolCalling.SupportsTools() {
			return nil, errs.Newf(errs.CodeInvalidArgument,
				"model was called with %d tools but the configured chat provider does not support tool calling", len(m.specs))
		}
		resp, err := m.deps.ToolCalling.ChatWithTools(ctx, req, m.specs)
		if err != nil {
			return nil, err
		}
		return toEinoMessage(resp.Message, resp.FinishReason, resp.Usage), nil
	}
	resp, err := m.deps.Chat.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	return toEinoMessage(resp.Message, resp.FinishReason, resp.Usage), nil
}

// Stream produces a message stream backed by the project ChatStream. Text and
// tool-call deltas are forwarded as they arrive; the terminating chunk carries
// finish reason and usage even when its delta is empty.
func (m *Model) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	stream, err := m.deps.Chat.Stream(ctx, m.request(in, opts))
	if err != nil {
		return nil, err
	}
	reader, writer := schema.Pipe[*schema.Message](0)
	go func() {
		defer func() { _ = stream.Close() }()
		for {
			chunk, recvErr := stream.Recv()
			if errors.Is(recvErr, io.EOF) {
				writer.Close()
				return
			}
			if recvErr != nil {
				writer.Send(nil, recvErr)
				writer.Close()
				return
			}
			// Send returns true when the consumer stopped receiving; in that
			// case there is nobody to flush to, so stop pumping.
			if writer.Send(chunkToMessage(chunk), nil) {
				return
			}
		}
	}()
	return reader, nil
}

// ToEinoMessages maps domain messages onto Eino messages. It is exported for
// the graph nodes, which program in project DTOs until they cross the model
// boundary.
func ToEinoMessages(messages []domainchat.ChatMessage) []*schema.Message {
	out := make([]*schema.Message, 0, len(messages))
	for _, msg := range messages {
		out = append(out, toEinoMessage(msg, "", domainchat.TokenUsage{}))
	}
	return out
}

// FromEinoMessage maps an Eino response message onto a domain message.
func FromEinoMessage(msg *schema.Message) domainchat.ChatMessage {
	if msg == nil {
		return domainchat.ChatMessage{}
	}
	domainMessages := toDomainMessages([]*schema.Message{msg})
	if len(domainMessages) == 0 {
		return domainchat.ChatMessage{}
	}
	return domainMessages[0]
}

// EinoFinishReason extracts the finish reason from a response message meta.
func EinoFinishReason(msg *schema.Message) domainchat.FinishReason {
	if msg != nil && msg.ResponseMeta != nil {
		return domainchat.FinishReason(msg.ResponseMeta.FinishReason)
	}
	return ""
}

// EinoUsage extracts token usage from a response message meta.
func EinoUsage(msg *schema.Message) domainchat.TokenUsage {
	if msg != nil && msg.ResponseMeta != nil && msg.ResponseMeta.Usage != nil {
		usage := msg.ResponseMeta.Usage
		total := usage.TotalTokens
		if total == 0 {
			total = usage.PromptTokens + usage.CompletionTokens
		}
		return domainchat.TokenUsage{
			InputTokens:  usage.PromptTokens,
			OutputTokens: usage.CompletionTokens,
			TotalTokens:  total,
		}
	}
	return domainchat.TokenUsage{}
}

// request maps Eino messages and call options onto a domain chat request.
func (m *Model) request(in []*schema.Message, opts []model.Option) domainchat.ChatRequest {
	req := domainchat.ChatRequest{
		Model:    m.deps.Model,
		Messages: toDomainMessages(in),
	}
	common := model.GetCommonOptions(nil, opts...)
	if common != nil {
		if common.Temperature != nil {
			t := float64(*common.Temperature)
			req.Temperature = &t
		}
		if common.MaxTokens != nil {
			req.MaxTokens = *common.MaxTokens
		}
	}
	return req
}

// toDomainMessages maps Eino messages onto the domain DTOs.
func toDomainMessages(in []*schema.Message) []domainchat.ChatMessage {
	messages := make([]domainchat.ChatMessage, 0, len(in))
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
		if len(msg.ToolCalls) > 0 {
			out.ToolCalls = make([]domaintool.ToolCall, 0, len(msg.ToolCalls))
			for _, call := range msg.ToolCalls {
				out.ToolCalls = append(out.ToolCalls, domaintool.ToolCall{
					ID:        call.ID,
					Name:      call.Function.Name,
					Arguments: json.RawMessage(call.Function.Arguments),
				})
			}
		}
		messages = append(messages, out)
	}
	return messages
}

// toEinoMessage maps one complete domain response back onto an Eino message.
func toEinoMessage(msg domainchat.ChatMessage, finish domainchat.FinishReason, usage domainchat.TokenUsage) *schema.Message {
	out := &schema.Message{
		Role:       schema.RoleType(msg.Role),
		Content:    msg.Content,
		Name:       msg.Name,
		ToolCallID: msg.ToolCallID,
	}
	if len(msg.ToolCalls) > 0 {
		out.ToolCalls = make([]schema.ToolCall, 0, len(msg.ToolCalls))
		for i, call := range msg.ToolCalls {
			idx := i
			out.ToolCalls = append(out.ToolCalls, schema.ToolCall{
				Index: &idx,
				ID:    call.ID,
				Type:  "function",
				Function: schema.FunctionCall{
					Name:      call.Name,
					Arguments: string(call.Arguments),
				},
			})
		}
	}
	if finish != "" || usage.TotalTokens > 0 {
		out.ResponseMeta = &schema.ResponseMeta{
			FinishReason: string(finish),
			Usage: &schema.TokenUsage{
				PromptTokens:     usage.InputTokens,
				CompletionTokens: usage.OutputTokens,
				TotalTokens:      usage.TotalTokens,
			},
		}
	}
	return out
}

// chunkToMessage maps one streamed chunk. The chat adapter accumulates
// tool-call deltas into snapshots, so each chunk's ToolCalls is forwarded as a
// complete call list.
func chunkToMessage(chunk domainchat.ChatChunk) *schema.Message {
	out := &schema.Message{
		Role:    schema.Assistant,
		Content: chunk.Delta,
	}
	if len(chunk.ToolCalls) > 0 {
		out.ToolCalls = make([]schema.ToolCall, 0, len(chunk.ToolCalls))
		for i, call := range chunk.ToolCalls {
			idx := i
			out.ToolCalls = append(out.ToolCalls, schema.ToolCall{
				Index: &idx,
				ID:    call.ID,
				Type:  "function",
				Function: schema.FunctionCall{
					Name:      call.Name,
					Arguments: string(call.Arguments),
				},
			})
		}
	}
	if chunk.FinishReason != "" || chunk.Usage != nil {
		meta := &schema.ResponseMeta{FinishReason: string(chunk.FinishReason)}
		if chunk.Usage != nil {
			meta.Usage = &schema.TokenUsage{
				PromptTokens:     chunk.Usage.InputTokens,
				CompletionTokens: chunk.Usage.OutputTokens,
				TotalTokens:      chunk.Usage.TotalTokens,
			}
		}
		out.ResponseMeta = meta
	}
	return out
}
