package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	domainchat "github.com/zed/platepilot/shared/domain/chat"
	"github.com/zed/platepilot/shared/domain/errs"
	domainretrieval "github.com/zed/platepilot/shared/domain/retrieval"
	"github.com/zed/platepilot/shared/domain/run"
	domaintool "github.com/zed/platepilot/shared/domain/tool"
	"github.com/zed/platepilot/shared/idgen"

	"github.com/zed/platepilot/chat-service/internal/agent/answer"
	"github.com/zed/platepilot/chat-service/internal/agent/audit"
	"github.com/zed/platepilot/chat-service/internal/agent/einomodel"
	"github.com/zed/platepilot/chat-service/internal/agent/tools"
	"github.com/zed/platepilot/chat-service/internal/retrieval"
)

// ingress initializes the turn state and opens the event stream.
func (r *Runner) ingress(ctx context.Context, in TurnInput) (*TurnState, error) {
	meta := runMetaFromContext(ctx)
	if meta.runID == "" {
		// Defensive fallback for direct node invocations outside Run.
		meta = runMeta{runID: idgen.NewUUID(), traceID: in.TraceID, startedAt: time.Now()}
	}
	st := &TurnState{
		RunID:     meta.runID,
		TraceID:   meta.traceID,
		ThreadID:  in.ThreadID,
		UserID:    in.UserID,
		UserInput: in.UserInput,
		Intent:    IntentUnknown,
		StartedAt: meta.startedAt,
	}
	r.loadConversationContext(ctx, st, in)
	r.loadMemoryContext(ctx, st)

	messages := make([]domainchat.ChatMessage, 0, len(in.History)+len(st.replayedHistory)+2)
	messages = append(messages, in.History...)
	messages = append(messages, st.replayedHistory...)
	messages = append(messages, domainchat.ChatMessage{
		Role:    domainchat.RoleUser,
		Content: in.UserInput,
	})
	st.Messages = messages
	emitFromContext(ctx, Event{Type: EventStart, RunID: meta.runID, ThreadID: in.ThreadID})
	return st, nil
}

// plan calls the tools-bound model and decides whether another tool round is
// pending.
func (r *Runner) plan(ctx context.Context, st *TurnState) (*TurnState, error) {
	messages := make([]domainchat.ChatMessage, 0, len(st.Messages)+2)
	messages = append(messages, domainchat.ChatMessage{
		Role:    domainchat.RoleSystem,
		Content: r.planSystemPrompt(),
	})
	if st.MemoryContext != "" {
		messages = append(messages, domainchat.ChatMessage{
			Role:    domainchat.RoleSystem,
			Content: st.MemoryContext,
		})
	}
	messages = append(messages, st.Messages...)

	resp, err := r.planModel.Generate(ctx, einomodel.ToEinoMessages(messages))
	if err != nil {
		return nil, err
	}
	assistant := einomodel.FromEinoMessage(resp)
	if assistant.Role == "" {
		assistant.Role = domainchat.RoleAssistant
	}
	st.Messages = append(st.Messages, assistant)
	st.FinishReason = einomodel.EinoFinishReason(resp)
	usage := einomodel.EinoUsage(resp)
	st.Usage.InputTokens += usage.InputTokens
	st.Usage.OutputTokens += usage.OutputTokens
	st.Usage.TotalTokens += usage.TotalTokens

	st.PendingToolCalls = len(assistant.ToolCalls) > 0
	if st.PendingToolCalls {
		st.Intent = IntentRestaurantQA
		if st.ToolRounds >= r.cfg.MaxToolRounds {
			st.PendingToolCalls = false
			st.RoundsCapped = true
			st.Warnings = appendUnique(st.Warnings, fmt.Sprintf(
				"已达到单轮最大工具调用次数 %d，基于已有信息作答", r.cfg.MaxToolRounds))
		}
	} else if st.Intent == IntentUnknown {
		st.Intent = IntentChitChat
	}
	return st, nil
}

// runTools executes every tool call from the latest assistant message, in
// order. Tool errors are fed back to the model as tool messages; they never
// abort the turn.
func (r *Runner) runTools(ctx context.Context, st *TurnState) (*TurnState, error) {
	if len(st.Messages) == 0 {
		return nil, errs.New(errs.CodeInternal, "tools node ran without a transcript")
	}
	calls := st.Messages[len(st.Messages)-1].ToolCalls
	for _, call := range calls {
		emitFromContext(ctx, Event{
			Type:     EventToolStart,
			RunID:    st.RunID,
			ThreadID: st.ThreadID,
			CallID:   call.ID,
			Tool:     call.Name,
		})
		started := time.Now()
		result := r.deps.Registry.Invoke(ctx, domaintool.ToolCall{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: call.Arguments,
		})
		latency := time.Since(started)

		st.UsedTools = true
		r.absorbToolData(st, call.Name, result)
		r.deps.Auditor.ToolCall(ctx, audit.ToolEvent{
			RunID:         st.RunID,
			CallID:        call.ID,
			Name:          call.Name,
			Arguments:     call.Arguments,
			ResultSummary: toolMessageContent(result),
			Status:        string(result.Status),
			LatencyMS:     latency.Milliseconds(),
		})

		emitFromContext(ctx, Event{
			Type:      EventToolFinish,
			RunID:     st.RunID,
			ThreadID:  st.ThreadID,
			CallID:    call.ID,
			Tool:      call.Name,
			OK:        result.Status == domaintool.ToolStatusOK,
			LatencyMS: latency.Milliseconds(),
			Error:     errorText(result),
		})

		st.Messages = append(st.Messages, domainchat.ChatMessage{
			Role:       domainchat.RoleTool,
			ToolCallID: call.ID,
			Name:       call.Name,
			Content:    toolMessageContent(result),
		})
	}
	st.ToolRounds++
	return st, nil
}

// absorbToolData folds structured tool payloads into the turn state.
func (r *Runner) absorbToolData(st *TurnState, toolName string, result domaintool.ToolResult) {
	if len(result.Data) == 0 || result.Status != domaintool.ToolStatusOK {
		return
	}
	switch toolName {
	case tools.SearchRestaurantsToolName:
		var payload domainretrieval.SearchResult
		if err := json.Unmarshal(result.Data, &payload); err == nil {
			st.Candidates = append(st.Candidates, payload.Candidates...)
			if len(payload.Candidates) == 0 {
				st.RetrievalEmpty = true
			}
			if payload.Trace != nil {
				st.Warnings = appendUnique(st.Warnings, payload.Trace.Warnings...)
			}
		}
	case tools.RestaurantEvidenceToolName:
		var payload retrieval.EvidenceResult
		if err := json.Unmarshal(result.Data, &payload); err == nil {
			st.Evidence = append(st.Evidence, payload.Evidence...)
			if len(payload.Evidence) == 0 {
				st.RetrievalEmpty = true
			}
			if payload.Trace != nil {
				st.Warnings = appendUnique(st.Warnings, payload.Trace.Warnings...)
			}
		}
	}
}

// answerNode produces the final answer. When evidence was gathered it runs the
// citation-validated composer; when the tools found nothing citable it returns
// the fixed refusal; otherwise it passes the model's own final text through.
func (r *Runner) answerNode(ctx context.Context, st *TurnState) (*TurnState, error) {
	switch {
	case len(st.Evidence) > 0:
		composed, err := r.composer.Compose(ctx, answer.Input{
			Question: st.UserInput,
			Evidence: st.Evidence,
		})
		if err != nil {
			return nil, err
		}
		st.FinalAnswer = &composed
		r.emitAnswer(ctx, st, composed)
	case st.RetrievalEmpty:
		// A restaurant question for which the retrieval tools found neither
		// candidates nor evidence: refuse from a fixed template rather than
		// regenerating against an empty context.
		final := domainchat.Answer{Text: answer.RefusalAnswer}
		st.FinalAnswer = &final
		r.emitAnswer(ctx, st, final)
	default:
		text := lastAssistantText(st)
		if strings.TrimSpace(text) == "" {
			// The model spent the whole turn calling tools and the round cap
			// ended the loop before it wrote an answer.
			text = answer.RefusalAnswer
			st.RetrievalEmpty = true
		}
		final := domainchat.Answer{Text: text}
		st.FinalAnswer = &final
		r.emitAnswer(ctx, st, final)
	}
	return st, nil
}

func (r *Runner) emitAnswer(ctx context.Context, st *TurnState, final domainchat.Answer) {
	emitFromContext(ctx, Event{
		Type:     EventDelta,
		RunID:    st.RunID,
		ThreadID: st.ThreadID,
		Delta:    final.Text,
	})
	if len(final.Citations) > 0 {
		emitFromContext(ctx, Event{
			Type:      EventCitation,
			RunID:     st.RunID,
			ThreadID:  st.ThreadID,
			Citations: final.Citations,
		})
	}
}

// finalize converts the state into the terminal turn result.
func (r *Runner) finalize(ctx context.Context, st *TurnState) (*TurnResult, error) {
	if st.FinalAnswer == nil {
		return nil, errs.New(errs.CodeInternal, "turn finished without a final answer")
	}
	finishedAt := time.Now()
	r.deps.Auditor.RunFinish(ctx, run.AgentRun{
		TraceID:     st.TraceID,
		ThreadID:    st.ThreadID,
		RunID:       st.RunID,
		Status:      run.StatusSucceeded,
		StartedAt:   st.StartedAt,
		FinishedAt:  &finishedAt,
		LatencyMS:   finishedAt.Sub(st.StartedAt).Milliseconds(),
		TokenInput:  st.Usage.InputTokens,
		TokenOutput: st.Usage.OutputTokens,
	})
	r.persistTurn(ctx, st, finishedAt)
	return &TurnResult{
		RunID:        st.RunID,
		TraceID:      st.TraceID,
		ThreadID:     st.ThreadID,
		Answer:       st.FinalAnswer,
		FinishReason: st.FinishReason,
		Usage:        st.Usage,
		ToolRounds:   st.ToolRounds,
		Warnings:     st.Warnings,
	}, nil
}

// planSystemPrompt describes the tools and the minimum routing policy. It is
// generated from the registry so the prompt cannot drift from registered
// tools.
func (r *Runner) planSystemPrompt() string {
	var b strings.Builder
	b.WriteString("你是 PlatePilot，纽约餐厅咨询助手。需要餐厅事实信息时按顺序使用工具：\n")
	b.WriteString("1) 先调用 search_restaurants 找到候选餐厅；\n")
	b.WriteString("2) 再调用 get_restaurant_evidence 获取可引用证据后回答；\n")
	b.WriteString("3) 已有足够信息或问题与餐厅无关时，直接用简洁中文回答，不要调用工具。\n")
	if specs := r.deps.Registry.Specs(); len(specs) > 0 {
		b.WriteString("\n可用工具：\n")
		for _, spec := range specs {
			fmt.Fprintf(&b, "- %s: %s\n", spec.Name, spec.Description)
		}
	}
	return b.String()
}

// toolMessageContent renders one ToolResult as the transcript content the
// model sees next.
func toolMessageContent(result domaintool.ToolResult) string {
	if result.Status != domaintool.ToolStatusOK {
		return fmt.Sprintf("工具执行失败：%s", errorText(result))
	}
	if strings.TrimSpace(result.Content) != "" {
		return result.Content
	}
	if len(result.Data) > 0 {
		return string(result.Data)
	}
	return "工具执行完成，无返回内容。"
}

func errorText(result domaintool.ToolResult) string {
	if result.Error != nil {
		return result.Error.Error()
	}
	return ""
}

func lastAssistantText(st *TurnState) string {
	for i := len(st.Messages) - 1; i >= 0; i-- {
		if st.Messages[i].Role == domainchat.RoleAssistant {
			return st.Messages[i].Content
		}
	}
	return ""
}

func appendUnique(existing []string, additions ...string) []string {
	seen := make(map[string]struct{}, len(existing))
	for _, item := range existing {
		seen[item] = struct{}{}
	}
	for _, item := range additions {
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		existing = append(existing, item)
	}
	return existing
}
