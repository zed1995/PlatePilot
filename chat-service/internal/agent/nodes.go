package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/conversation"
	"github.com/zed1995/platepilot/shared/domain/errs"
	domainretrieval "github.com/zed1995/platepilot/shared/domain/retrieval"
	"github.com/zed1995/platepilot/shared/domain/run"
	"github.com/zed1995/platepilot/shared/domain/search"
	domaintool "github.com/zed1995/platepilot/shared/domain/tool"
	"github.com/zed1995/platepilot/shared/idgen"

	"github.com/zed1995/platepilot/chat-service/internal/agent/answer"
	"github.com/zed1995/platepilot/chat-service/internal/agent/audit"
	"github.com/zed1995/platepilot/chat-service/internal/agent/einomodel"
	"github.com/zed1995/platepilot/chat-service/internal/agent/slots"
	"github.com/zed1995/platepilot/chat-service/internal/agent/tools"
	"github.com/zed1995/platepilot/chat-service/internal/memorywrite"
	"github.com/zed1995/platepilot/chat-service/internal/retrieval"
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
	r.interpret(ctx, st)

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
//
// It first checks whether the turn has to stop and ask the user something. That
// check has to happen before the model call, not after: once a tool has reported
// that a name matches two restaurants equally well, asking the model to continue
// would let it pick one, and the pick would look like a resolution rather than a
// guess.
func (r *Runner) plan(ctx context.Context, st *TurnState) (*TurnState, error) {
	if st.needsClarification() {
		r.settleClarificationCap(st)
	}
	if st.needsClarification() {
		return st, nil
	}
	// A turn that parked a write has nothing left to plan. The tools node routes
	// back here like any other round, and returning without a model call is what
	// keeps the parked request off the wire: the assistant message that asked
	// for the write carries tool_calls with no matching tool result, and a chat
	// API rejects a transcript that ends that way. It would also be the wrong
	// question to ask — the model just asked to book, and the only thing left is
	// for the user to answer.
	if st.ConfirmationRequired {
		st.PendingToolCalls = false
		return st, nil
	}

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
	if context := referencePrompt(st); context != "" {
		messages = append(messages, domainchat.ChatMessage{
			Role:    domainchat.RoleSystem,
			Content: context,
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
//
// Calls to a tool that changes something outside the conversation are the one
// exception: they are not executed at all. They are parked on the thread and the
// round stops, because continuing would either run a write the user has not
// approved or act on the outcome of one — and the rest of the round has no way
// to know which.
func (r *Runner) runTools(ctx context.Context, st *TurnState) (*TurnState, error) {
	if len(st.Messages) == 0 {
		return nil, errs.New(errs.CodeInternal, "tools node ran without a transcript")
	}
	calls := st.Messages[len(st.Messages)-1].ToolCalls
	// The turn's plan rides on the tool context so a tool can fill in what the
	// model left out. It is attached here rather than in ingress because the
	// plan belongs to the tool call, and a tool invoked outside a turn simply
	// finds none.
	toolCtx := slots.WithPlan(ctx, st.Plan)
	// The user's own message and identity ride along for the same reason, and
	// for one more: the memory guard checks a quote against the message, so the
	// check has to be against the server's copy of it rather than against
	// anything the model supplied.
	toolCtx = memorywrite.WithTurn(toolCtx, memorywrite.Turn{
		UserID:  st.UserID,
		Message: st.UserInput,
	})
	for _, call := range calls {
		if r.deps.Registry.RequiresConfirmation(call.Name) {
			parked, err := r.parkForConfirmation(ctx, st, call)
			if err != nil {
				return nil, err
			}
			if parked {
				// The write is on the thread now, and this turn's remaining
				// work is the question. The loop stops rather than skipping to
				// the next call: a checkpoint holds one pending action, so a
				// second write in the same round could not be recorded.
				st.PendingToolCalls = false
				break
			}
			// The call was described to nobody because it could not be
			// described at all. Its error is already in the transcript for the
			// model to correct.
			continue
		}
		emitFromContext(ctx, Event{
			Type:     EventToolStart,
			RunID:    st.RunID,
			ThreadID: st.ThreadID,
			CallID:   call.ID,
			Tool:     call.Name,
		})
		started := time.Now()
		result := r.deps.Registry.Invoke(toolCtx, domaintool.ToolCall{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: call.Arguments,
		})
		latency := time.Since(started)

		st.UsedTools = true
		r.absorbToolData(ctx, st, call.Name, result)
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

// parkForConfirmation records a write the user has not approved yet.
//
// It returns false when the call could not be described, in which case nothing
// is parked and the reason is appended to the transcript as a tool result. The
// asymmetric outcome is deliberate: asking the user to approve "some call with
// these arguments" teaches them to approve without reading, which defeats the
// gate. A call whose description cannot be produced is a call the model got
// wrong, and the model is the one who can fix it.
//
// No tool.start/tool.finish pair and no tool_calls audit row is written for a
// parked call. The tool did not run, and the audit trail's subject is tools that
// ran; the request itself is recorded where it matters — on the thread's
// checkpoint and in the confirmation event.
func (r *Runner) parkForConfirmation(
	ctx context.Context, st *TurnState, call domaintool.ToolCall,
) (bool, error) {
	summary, err := r.deps.Registry.ApprovalSummary(ctx, call.Name, call.Arguments)
	if err != nil {
		st.Messages = append(st.Messages, domainchat.ChatMessage{
			Role:       domainchat.RoleTool,
			ToolCallID: call.ID,
			Name:       call.Name,
			Content: "无法生成该操作的确认摘要：" + err.Error() +
				"。请检查参数后重试，用户无法在不了解内容的情况下确认。",
		})
		st.Warnings = appendUnique(st.Warnings,
			"需要确认的操作无法生成摘要，本轮未挂起："+call.Name)
		return false, nil
	}

	st.State = conversation.StateAwaitingConfirmation
	st.PendingAction = call.Name
	st.PendingToolCallID = idgen.NewUUID()
	st.PendingArguments = call.Arguments
	st.ConfirmationSummary = summary
	st.ConfirmationRequired = true
	// The checkpoint is saved here rather than at finalize so the approval has
	// something to read: the confirmation arrives as a later request, possibly
	// after a restart, and it can only replay a call the thread stored.
	if err := r.persistPendingCheckpoint(ctx, st); err != nil {
		return false, err
	}
	return true, nil
}

// absorbToolData folds structured tool payloads into the turn state and emits
// whatever the payload is an event about.
//
// It takes the context because a payload can be an occurrence rather than a
// fact: a saved memory is something that happened to the user's account, and
// the stream is where the client learns about it.
func (r *Runner) absorbToolData(
	ctx context.Context, st *TurnState, toolName string, result domaintool.ToolResult,
) {
	if len(result.Data) == 0 || result.Status != domaintool.ToolStatusOK {
		return
	}
	switch toolName {
	case tools.SearchRestaurantsToolName:
		var payload domainretrieval.SearchResult
		if err := json.Unmarshal(result.Data, &payload); err == nil {
			st.Candidates = append(st.Candidates, payload.Candidates...)
			dropStalePin(st)
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
	case tools.ResolveRestaurantToolName:
		var payload tools.ResolveResult
		if err := json.Unmarshal(result.Data, &payload); err == nil {
			r.absorbResolve(st, payload)
		}
	case tools.SaveMemoryToolName:
		r.emitMemorySaved(ctx, st, result.Data)
	}
}

// emitMemorySaved reports a memory the turn wrote.
//
// The event is built here rather than inside the tool because tools return data
// and the graph owns the event stream: a tool that could publish would have to
// know about the transport's vocabulary, and this one is deliberately unaware of
// everything except whether the write was allowed.
func (r *Runner) emitMemorySaved(ctx context.Context, st *TurnState, data json.RawMessage) {
	var payload tools.SavedMemory
	if err := json.Unmarshal(data, &payload); err != nil {
		// The write already happened; a payload this turn cannot read is a
		// reporting problem, not a reason to fail the turn.
		st.Warnings = appendUnique(st.Warnings, "记忆已保存，但保存事件无法解析")
		return
	}
	emitFromContext(ctx, Event{
		Type:            EventMemorySaved,
		RunID:           st.RunID,
		ThreadID:        st.ThreadID,
		MemoryID:        payload.MemoryID,
		MemoryType:      payload.MemoryType,
		MemoryContent:   payload.Content,
		MemoryRefreshed: payload.Refreshed,
	})
}

// absorbResolve folds a name resolution into the turn state.
//
// The three statuses are three different turns: resolved pins the restaurant and
// the conversation can go on to evidence, ambiguous stops it so the user can
// choose, and not_found records why the turn has nothing to answer about. None
// of them is an error, which is why none of them becomes a warning.
func (r *Runner) absorbResolve(st *TurnState, payload tools.ResolveResult) {
	switch payload.Status {
	case tools.ResolveResolved:
		if payload.Restaurant == nil {
			return
		}
		st.SelectedRestaurantID = payload.Restaurant.RestaurantID
		st.Plan.SelectedRestaurantID = payload.Restaurant.RestaurantID
		// The note is what a follow-up turn has to be able to report: the user
		// said a name, and the system has to be able to say which restaurant it
		// took that to mean before it answers about it.
		st.Plan.ReferenceNote = fmt.Sprintf("已按名称确定为「%s」",
			payload.Restaurant.Name)
	case tools.ResolveAmbiguous:
		st.ClarificationOptions = resolveOptionsAsCandidates(payload.Candidates)
		st.Plan.NamedRestaurants = appendUnique(st.Plan.NamedRestaurants, payload.Name)
	case tools.ResolveNotFound:
		st.Plan.NamedRestaurants = appendUnique(st.Plan.NamedRestaurants, payload.Name)
	}
}

// dropStalePin clears a pin the turn's candidate list no longer contains.
//
// A pin is a statement about the list the user is looking at: "第二家" pins a row
// and "这家" then means that row. When a fresh search replaces the list, a pin
// from the previous one stops being a fact about anything the user can see —
// carried forward it would make the next plan tell the model to answer about a
// restaurant the new results do not mention. Keeping the pin when the id is
// still present is what lets a refined search stay on the same restaurant.
func dropStalePin(st *TurnState) {
	if st.SelectedRestaurantID == 0 || len(st.Candidates) == 0 {
		return
	}
	for _, candidate := range st.Candidates {
		if candidate.RestaurantID == st.SelectedRestaurantID {
			return
		}
	}
	st.SelectedRestaurantID = 0
	st.Plan.SelectedRestaurantID = 0
	st.Plan.ReferenceNote = ""
}

// resolveOptionsAsCandidates converts the tool's projection into the candidate
// shape the turn state and the persistence layer already speak.
func resolveOptionsAsCandidates(options []tools.ResolveCandidate) []search.RestaurantCandidate {
	out := make([]search.RestaurantCandidate, 0, len(options))
	for _, option := range options {
		out = append(out, search.RestaurantCandidate{
			RestaurantID: option.RestaurantID,
			Name:         option.Name,
			Address:      option.Address,
			Borough:      option.Borough,
			Cuisines:     option.Cuisines,
			Rating:       option.Rating,
			PriceLevel:   option.PriceLevel,
			Score:        option.Similarity,
		})
	}
	return out
}

// needsClarification reports whether the turn has to ask the user something
// before it can answer.
func (st *TurnState) needsClarification() bool {
	if st.ClarificationAssumed {
		// The cap was reached and the turn is proceeding on a stated
		// assumption. Asking again would be the loop the cap exists to break.
		return false
	}
	return len(st.ClarificationOptions) >= 2 || st.Plan.NeedClarification
}

// settleClarificationCap decides what to do when a thread has already been asked
// as many times as it is allowed to be.
//
// Two outcomes, because the two situations have different ways out. With
// candidates on the table the turn can proceed on the best match, as long as it
// says so. Without any — the plan wanted a slot the user never named — there is
// nothing to assume, so the turn stops asking and lets the answer report the gap
// instead of another question.
func (r *Runner) settleClarificationCap(st *TurnState) {
	if st.ClarificationCount < r.cfg.MaxClarifications {
		return
	}
	if len(st.ClarificationOptions) >= 2 {
		best := st.ClarificationOptions[0]
		st.SelectedRestaurantID = best.RestaurantID
		st.Plan.SelectedRestaurantID = best.RestaurantID
		st.Plan.ReferenceNote = fmt.Sprintf("澄清次数已达上限，按最相似的「%s」继续", best.Name)
		st.Warnings = appendUnique(st.Warnings, fmt.Sprintf(
			"已连续澄清 %d 次仍无法确定餐厅，本轮按最相似的「%s」继续，回答中必须说明这一假设",
			st.ClarificationCount, best.Name))
	}
	st.ClarificationOptions = nil
	st.Plan.NeedClarification = false
	st.ClarificationAssumed = true
}

// clarifyNode parks the turn on a question for the user.
//
// It is terminal rather than an intermediate step: it writes the question as the
// turn's answer, saves the state that keeps the thread waiting, and reports that
// state on the stream. A client that only sees message.end cannot tell a
// finished answer from a question, which is why the awaiting-input event exists.
func (r *Runner) clarifyNode(ctx context.Context, st *TurnState) (*TurnState, error) {
	st.State = conversation.StateAwaitingClarification
	if len(st.ClarificationOptions) >= 2 {
		st.PendingAction = tools.ResolveRestaurantToolName
		st.MissingSlots = []string{slots.SlotRestaurantID}
	} else {
		// Nothing to choose between: the plan knows a slot it needs. Park the
		// slots themselves rather than a made-up action name — the next turn
		// has to know what to fill, not which tool to pretend asked.
		st.PendingAction = ""
		st.MissingSlots = append([]string(nil), st.Plan.MissingSlots...)
		if len(st.MissingSlots) == 0 {
			st.MissingSlots = []string{slots.SlotRestaurantID}
		}
	}
	st.ClarificationCount++

	question := buildClarificationQuestion(st)
	final := domainchat.Answer{Text: question}
	st.FinalAnswer = &final

	// The mid-turn checkpoint is saved before either event is published, so a
	// client that reacts to awaiting_input by immediately reading the thread
	// sees the state it was just told about. Saving first is also what makes a
	// lost race cheap: the turn ends before the question is streamed, rather
	// than after the user has already read a question about a thread that
	// belongs to another turn.
	if err := r.persistPendingCheckpoint(ctx, st); err != nil {
		return nil, err
	}
	emitFromContext(ctx, Event{
		Type:     EventDelta,
		RunID:    st.RunID,
		ThreadID: st.ThreadID,
		Delta:    question,
	})
	emitFromContext(ctx, Event{
		Type:          EventAwaitingInput,
		RunID:         st.RunID,
		ThreadID:      st.ThreadID,
		State:         string(st.State),
		PendingAction: st.PendingAction,
		MissingSlots:  st.MissingSlots,
	})
	return st, nil
}

// buildClarificationQuestion writes the product copy for a clarification.
//
// It describes the candidates by what a person would use to tell them apart — a
// neighbourhood, an address, a cuisine — and never by their database ids. The
// ids are how the system refers to a restaurant; the user has never seen one,
// and a question that asks them to choose between "42" and "77" is not a
// question.
func buildClarificationQuestion(st *TurnState) string {
	if len(st.ClarificationOptions) < 2 {
		if len(st.MissingSlots) > 0 {
			return "我需要再确认一点信息才能继续：" + strings.Join(st.MissingSlots, "、") +
				"。补充之后我再帮你查一次。"
		}
		return "我还需要更多信息才能确定你指的是哪一家餐厅，请补充名称或地址。"
	}

	var b strings.Builder
	b.WriteString("按你说的名字，我找到了几家都对得上的餐厅，你说的是哪一家？\n")
	for i, candidate := range st.ClarificationOptions {
		fmt.Fprintf(&b, "%d. %s", i+1, candidate.Name)
		var distinctions []string
		if candidate.Borough != "" {
			distinctions = append(distinctions, candidate.Borough)
		}
		if candidate.Address != "" {
			distinctions = append(distinctions, candidate.Address)
		}
		if len(candidate.Cuisines) > 0 {
			distinctions = append(distinctions, strings.Join(candidate.Cuisines, "/"))
		}
		if candidate.Rating != nil {
			distinctions = append(distinctions, fmt.Sprintf("评分 %.1f", *candidate.Rating))
		}
		if len(distinctions) > 0 {
			fmt.Fprintf(&b, "（%s）", strings.Join(distinctions, "，"))
		}
		b.WriteString("\n")
	}
	b.WriteString("回复序号或直接说出名称、地址都可以。")
	return b.String()
}

// answerNode produces the final answer. When evidence was gathered it runs the
// citation-validated composer; when the tools found nothing citable it returns
// the fixed refusal; otherwise it passes the model's own final text through.
func (r *Runner) answerNode(ctx context.Context, st *TurnState) (*TurnState, error) {
	switch {
	case st.ConfirmationRequired:
		// The turn parked a write and the answer is the request for approval.
		// It is built in code rather than composed: there is no evidence to
		// ground, nothing to cite, and the text the user approves has to be the
		// same text the tool reported — a model asked to rephrase a summary
		// could describe a booking the user did not agree to.
		final := domainchat.Answer{Text: confirmationAnswer(st)}
		st.FinalAnswer = &final
		r.emitAnswer(ctx, st, final)
		// The event is emitted after the text so a client that renders the
		// message and then reads the next frame has something to show before it
		// is told what to ask.
		emitFromContext(ctx, Event{
			Type:                EventConfirmationRequired,
			RunID:               st.RunID,
			ThreadID:            st.ThreadID,
			State:               string(st.State),
			PendingAction:       st.PendingAction,
			ConfirmationSummary: st.ConfirmationSummary,
		})
	case len(st.Evidence) > 0:
		composed, err := r.composer.Compose(ctx, answer.Input{
			Question: st.UserInput,
			Evidence: st.Evidence,
			// The candidates are passed even though they are not citable: an
			// answer that recommends a restaurant has to be able to say when
			// the data behind it was observed, and that date lives on the
			// candidate rather than on any quotable document.
			Candidates: st.Candidates,
			// The filter is passed as the filter, not as prose: the answer has
			// to account for every condition that was enforced, and the
			// description is rendered from the same value the search used.
			Filters: st.Plan.HardFilters,
			// The soft conditions are passed so the composer can decide, per
			// condition, whether this turn's evidence supports it — a judgement
			// the model cannot make, because only the plan knows that "安静"
			// and the documents tagged "ambience" are the same request.
			SoftConditions: st.Plan.SoftConditions,
			// Missing slots and degradations are the two honest sources of a
			// "cannot confirm" line. Neither is a disclaimer: the ranking really
			// was computed without them.
			MissingSlots: st.Plan.MissingSlots,
			Warnings:     st.Warnings,
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

// confirmationAnswer writes the text a turn that parked a write ends with.
//
// The summary is quoted verbatim and the instruction is fixed, because both are
// the user's side of a contract: they are being asked to approve the exact
// sentence that describes the write, and the reply that will carry it out is
// named so the decision is not a guess about what to type.
func confirmationAnswer(st *TurnState) string {
	return st.ConfirmationSummary + "\n\n请回复「确认」执行，或回复「取消」放弃。"
}

// finalize converts the state into the terminal turn result.
//
// The turn is persisted before it is recorded as succeeded, and that order is
// load-bearing: a turn that lost the race for its thread never durably captured
// its state, so recording success first and only then discovering the conflict
// would leave an audit row claiming a conversation that does not exist.
func (r *Runner) finalize(ctx context.Context, st *TurnState) (*TurnResult, error) {
	if st.FinalAnswer == nil {
		return nil, errs.New(errs.CodeInternal, "turn finished without a final answer")
	}
	finishedAt := time.Now()
	if err := r.persistTurn(ctx, st, finishedAt); err != nil {
		return nil, err
	}
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

// interpret turns the user's message into this turn's plan.
//
// It runs exactly once per turn, before any tool call, so that every tool works
// from one reading of the sentence instead of re-deriving one from whatever
// fragment of it reached that tool. The extraction itself cannot fail: with no
// model, or with a model that answers unusably, it returns a rule-derived plan.
// A canceled context is the only way out, and that is a turn already on its way
// to being abandoned, so the plan is simply left empty.
func (r *Runner) interpret(ctx context.Context, st *TurnState) {
	if r.deps.Extractor == nil {
		return
	}
	plan, err := r.deps.Extractor.Extract(ctx, st.UserInput, slots.ThreadContext{
		Pending:    st.LoadedCheckpoint,
		Candidates: st.LoadedCandidates,
	})
	if err != nil {
		return
	}
	st.Plan = plan
	st.Warnings = appendUnique(st.Warnings, plan.Warnings...)
	// A reference resolved by rules is this turn's reading of the sentence, so
	// it becomes the thread's scope. It is an initial value rather than a
	// guard: a model that goes on to name a restaurant outright is saying
	// something stronger than a pronoun, and absorbResolve is free to move it.
	if plan.SelectedRestaurantID != 0 {
		st.SelectedRestaurantID = plan.SelectedRestaurantID
	}
	// The plan's intent is a routing hint, not an override of what the model
	// decides to do: a turn that names a restaurant may still need a search.
	if st.Intent == IntentUnknown {
		st.Intent = mapPlanIntent(plan.Intent)
	}
}

// mapPlanIntent projects the slot layer's intent onto the runtime's routing
// values.
//
// The two vocabularies overlap but are not the same: the slot layer needs a
// finer distinction between finding and recommending, while the graph only
// needs to know whether restaurant tools are in play. Translating in one place
// keeps that asymmetry visible instead of letting one enum leak into the other.
func mapPlanIntent(intent slots.Intent) Intent {
	switch intent {
	case slots.IntentChitChat:
		return IntentChitChat
	case slots.IntentDiscover, slots.IntentRecommend, slots.IntentRestaurantQA, slots.IntentReservation:
		return IntentRestaurantQA
	default:
		return IntentUnknown
	}
}

// referencePrompt states which restaurant this turn is about, and why.
//
// A follow-up like "第二家安静吗" names no restaurant, so without this the model
// only sees an ordinal it has never been shown the list for — it would either
// ask again or, worse, answer about whichever name it last read. The note is
// produced by the rule-based resolver, not by a generation, so the sentence the
// user reads and the id the tools read come from the same decision.
//
// It is a per-turn system message rather than part of the static instruction:
// the fact expires with the turn, and a model told once that "第二家" is id 7
// would carry that reading into the next conversation on the same thread.
//
// The pin reaches the model in two strengths, and the difference is *who
// decided it*. A pin this turn resolved is an instruction: the user referred to
// a restaurant and the rules say which, so every later stage must stay on it. A
// pin inherited from an earlier turn is only a default — the thread was talking
// about that restaurant, and this message may well be the user changing the
// subject. Stating it as an instruction is the failure mode worth naming: the
// model would answer a Brooklyn question about a Manhattan restaurant it was
// told to keep answering about. So the inherited form says where it came from
// and when to drop it, because deciding whether the sentence is a follow-up is
// exactly the judgement the model can make and the rules cannot.
func referencePrompt(st *TurnState) string {
	if st.SelectedRestaurantID == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<thread_context>\n")
	if st.Plan.ReferenceNote == "" {
		fmt.Fprintf(&b, "本线程此前的追问对象是 restaurant_id=%d。"+
			"如果这一轮用户换了条件或换了问题，请先重新搜索，不要沿用上一轮的这家店。\n",
			st.SelectedRestaurantID)
		b.WriteString("</thread_context>")
		return b.String()
	}
	b.WriteString("用户这一轮用的是指代：" + st.Plan.ReferenceNote + "\n")
	fmt.Fprintf(&b, "本轮已锁定 restaurant_id=%d。", st.SelectedRestaurantID)
	b.WriteString("回答必须围绕这家店：先用 get_restaurant_evidence 取它的证据再作答，" +
		"不要换成候选里的别家，也不要把别家的资料混进来。\n</thread_context>")
	return b.String()
}

// planSystemPrompt describes the tools and the minimum routing policy. It is
// generated from the registry so the prompt cannot drift from registered tools.
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
