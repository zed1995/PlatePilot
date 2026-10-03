package app

import (
	"context"
	"time"

	"github.com/zed/platepilot/chat-service/internal/agent"
	"github.com/zed/platepilot/chat-service/internal/httpapi"
	"github.com/zed/platepilot/shared/domain/conversation"
	"github.com/zed/platepilot/shared/domain/errs"
	domainmemory "github.com/zed/platepilot/shared/domain/memory"
	"github.com/zed/platepilot/shared/idgen"
	"github.com/zed/platepilot/shared/store"
)

// chatService adapts the agent runner and the conversation/memory stores onto
// the transport's httpapi.ChatService contract. It is the composition point
// that knows both worlds; neither the agent nor the stores know about SSE.
type chatService struct {
	runner        *agent.Runner
	conversations store.ConversationRepository
	memories      store.MemoryRepository
}

// newChatService builds the adapter. runner may be nil when no chat provider
// is configured: thread and memory routes still work, and sending a message
// returns provider_unavailable.
func newChatService(
	runner *agent.Runner,
	conversations store.ConversationRepository,
	memories store.MemoryRepository,
) *chatService {
	return &chatService{runner: runner, conversations: conversations, memories: memories}
}

func (s *chatService) CreateThread(ctx context.Context, userID, title string) (conversation.Conversation, error) {
	now := time.Now().UTC()
	conv := conversation.Conversation{
		ThreadID:     idgen.NewUUID(),
		UserID:       userID,
		Title:        title,
		CurrentState: conversation.StateIdle,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.conversations.Upsert(ctx, conv); err != nil {
		return conversation.Conversation{}, err
	}
	return conv, nil
}

func (s *chatService) GetThread(ctx context.Context, threadID string) (httpapi.ThreadDetail, error) {
	conv, err := s.conversations.Get(ctx, threadID)
	if err != nil {
		return httpapi.ThreadDetail{}, err
	}
	detail := httpapi.ThreadDetail{Conversation: conv}
	checkpoint, err := s.conversations.LoadCheckpoint(ctx, threadID)
	if err == nil {
		detail.Checkpoint = &checkpoint
	} else if errs.CodeOf(err) != errs.CodeNotFound {
		return httpapi.ThreadDetail{}, err
	}
	return detail, nil
}

func (s *chatService) ListMessages(
	ctx context.Context, threadID string, limit int, beforeID string,
) (httpapi.MessagePage, error) {
	messages, err := s.conversations.ListMessages(ctx, threadID, limit, beforeID)
	if err != nil {
		return httpapi.MessagePage{}, err
	}
	return httpapi.MessagePage{Messages: messages}, nil
}

func (s *chatService) ListMemories(ctx context.Context, userID string) (httpapi.MemoryPage, error) {
	memories, err := s.memories.List(ctx, userID)
	if err != nil {
		return httpapi.MemoryPage{}, err
	}
	views := make([]httpapi.MemoryView, 0, len(memories))
	for _, mem := range memories {
		views = append(views, toMemoryView(mem))
	}
	return httpapi.MemoryPage{Memories: views}, nil
}

func (s *chatService) DeleteMemory(ctx context.Context, userID, memoryID string) error {
	return s.memories.Delete(ctx, userID, memoryID)
}

func toMemoryView(mem domainmemory.Memory) httpapi.MemoryView {
	return httpapi.MemoryView{
		ID:         mem.ID,
		Type:       string(mem.Type),
		Content:    mem.Content,
		Source:     mem.Source,
		Confidence: mem.Confidence,
		CreatedAt:  mem.CreatedAt,
		UpdatedAt:  mem.UpdatedAt,
	}
}

// SendMessage runs one turn and forwards its events to emit in order.
//
// Runner.Run blocks for the whole turn while publishing onto a buffered
// channel, so the run executes in a goroutine that forwards onto an
// unbuffered channel this method drains: this keeps backpressure honest — a
// slow SSE client throttles the run instead of letting 128 buffered events
// pile up.
//
// The run runs on a derived, cancelable context. A dead sink (a failed emit)
// cancels it even when the transport does not notice the disconnect itself,
// so model calls and tool rounds stop as soon as there is no client left.
func (s *chatService) SendMessage(
	ctx context.Context, in httpapi.SendMessageInput, emit func(httpapi.StreamEvent) error,
) error {
	if s.runner == nil {
		return errs.New(errs.CodeProviderUnavailable, "chat provider is not configured")
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	forwarded := make(chan agent.Event)
	go func() {
		defer close(forwarded)
		_, events := s.runner.Run(ctx, agent.TurnInput{
			TraceID:   in.TraceID,
			ThreadID:  in.ThreadID,
			UserID:    in.UserID,
			UserInput: in.Content,
		})
		for ev := range events {
			select {
			case forwarded <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()

	for ev := range forwarded {
		if err := emit(toStreamEvent(ev)); err != nil {
			cancel()
			return err
		}
	}
	return nil
}

// toStreamEvent maps an agent event onto the transport-neutral stream event.
// The two share event-name strings by contract, but never share a type.
func toStreamEvent(ev agent.Event) httpapi.StreamEvent {
	out := httpapi.StreamEvent{
		Type:         httpapi.StreamEventType(ev.Type),
		RunID:        ev.RunID,
		ThreadID:     ev.ThreadID,
		CallID:       ev.CallID,
		Tool:         ev.Tool,
		LatencyMS:    ev.LatencyMS,
		Delta:        ev.Delta,
		EvidenceIDs:  ev.Citations,
		FinishReason: ev.FinishReason,
		Usage:        ev.Usage,
		Warnings:     ev.Warnings,
		Code:         ev.Code,
		Message:      ev.Message,
	}
	if ev.Type == agent.EventToolFinish {
		if ev.OK {
			out.ToolStatus = "succeeded"
		} else {
			out.ToolStatus = "failed"
		}
	}
	return out
}
