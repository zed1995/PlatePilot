package app

// The transport's half of the streaming contract.
//
// The agent package proves that RunLive forwards events while the graph is
// still running. This proves the SSE adapter actually uses it — and that is not
// a detail: SendMessage could go back to draining a finished turn and every test
// in the agent package would stay green, while the answer silently arrived in
// one lump at the end of the request. The property being pinned is about this
// adapter, so it is pinned here.

import (
	"context"
	"errors"
	"testing"
	"time"

	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	domaintool "github.com/zed1995/platepilot/shared/domain/tool"
	"github.com/zed1995/platepilot/shared/testkit"

	"github.com/zed1995/platepilot/chat-service/internal/agent"
	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
	"github.com/zed1995/platepilot/chat-service/internal/httpapi"
)

// gatedChatProvider holds the turn's model rounds until the test opens the gate.
//
// A blocking model round is what makes "the frame arrived before the turn
// finished" observable: with an instant provider the turn is over before the
// assertions run, and live forwarding cannot be told from an end-of-turn
// replay.
//
// Both entry points are held rather than only the tool-calling one: which of
// them a plan round reaches depends on what the registry offers, and the
// assertion here is about the adapter's forwarding, not about that dispatch.
type gatedChatProvider struct {
	*testkit.MockChatProvider
	gate chan struct{}
}

func (p *gatedChatProvider) wait(ctx context.Context) error {
	select {
	case <-p.gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *gatedChatProvider) ChatWithTools(
	ctx context.Context, req domainchat.ChatRequest, specs []domaintool.ToolSpec,
) (domainchat.ToolCallResponse, error) {
	if err := p.wait(ctx); err != nil {
		return domainchat.ToolCallResponse{}, err
	}
	return p.MockChatProvider.ChatWithTools(ctx, req, specs)
}

func (p *gatedChatProvider) Complete(
	ctx context.Context, req domainchat.ChatRequest,
) (domainchat.ChatResponse, error) {
	if err := p.wait(ctx); err != nil {
		return domainchat.ChatResponse{}, err
	}
	return p.MockChatProvider.Complete(ctx, req)
}

func TestSendMessageForwardsFramesBeforeTheTurnFinishes(t *testing.T) {
	gate := make(chan struct{})
	provider := &gatedChatProvider{
		MockChatProvider: &testkit.MockChatProvider{Tools: true},
		gate:             gate,
	}
	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 2}, agent.Deps{
		Chat:        provider,
		ToolCalling: provider,
		Registry:    toolreg.New(0),
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	service := newChatService(chatServiceDeps{Runner: runner})

	emitted := make(chan httpapi.StreamEvent, 8)
	done := make(chan error, 1)
	go func() {
		done <- service.SendMessage(context.Background(), httpapi.SendMessageInput{
			ThreadID: "th-live", UserID: "u1", Content: "你好",
		}, func(ev httpapi.StreamEvent) error {
			emitted <- ev
			return nil
		})
	}()

	select {
	case ev := <-emitted:
		if ev.Type != httpapi.StreamStart {
			t.Fatalf("the first frame is %q, want the run's start", ev.Type)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the transport emitted nothing while the turn was running")
	}

	// The model round is still held, so the turn cannot have finished. If the
	// adapter drained the run instead of forwarding it, no frame could have
	// reached the client yet and the select above would have timed out.
	select {
	case err := <-done:
		t.Fatalf("SendMessage returned while the model round was still held: %v", err)
	default:
	}

	close(gate)
	if err := <-done; err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
}

// TestSendMessageStopsTheRunWhenTheClientDisconnects covers the other half: a
// write that fails is a client that is gone, and the turn has to stop instead
// of finishing into a socket nobody is reading.
//
// The gate is never opened, so the model round can only end one way — by being
// cancelled. That makes the assertion a statement about cancellation reaching
// the model call rather than about how quickly the adapter gives up.
func TestSendMessageStopsTheRunWhenTheClientDisconnects(t *testing.T) {
	provider := &gatedChatProvider{
		MockChatProvider: &testkit.MockChatProvider{Tools: true},
		gate:             make(chan struct{}),
	}
	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 2}, agent.Deps{
		Chat:        provider,
		ToolCalling: provider,
		Registry:    toolreg.New(0),
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	service := newChatService(chatServiceDeps{Runner: runner})

	refused := errors.New("connection closed")
	done := make(chan error, 1)
	go func() {
		done <- service.SendMessage(context.Background(), httpapi.SendMessageInput{
			ThreadID: "th-dead", UserID: "u1", Content: "你好",
		}, func(httpapi.StreamEvent) error { return refused })
	}()

	select {
	case err := <-done:
		if err != refused {
			t.Fatalf("err = %v, want the transport's own error to reach the caller", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the run kept waiting on the model after the client's write failed")
	}
}
