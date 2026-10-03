package testkit

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/search"
)

func TestMockChatProviderRecordsAndReturns(t *testing.T) {
	provider := &MockChatProvider{Response: chat.ChatResponse{Message: chat.ChatMessage{Role: chat.RoleAssistant, Content: "hi"}}}
	resp, err := provider.Complete(context.Background(), chat.ChatRequest{ThreadID: "t1"})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resp.Message.Content != "hi" {
		t.Fatalf("content = %q", resp.Message.Content)
	}
	if len(provider.CompleteCalls) != 1 || provider.CompleteCalls[0].ThreadID != "t1" {
		t.Fatalf("call not recorded: %+v", provider.CompleteCalls)
	}
}

func TestMockChatProviderStreamsThenEOF(t *testing.T) {
	provider := &MockChatProvider{Chunks: []chat.ChatChunk{{Delta: "he"}, {Delta: "llo"}}}
	stream, err := provider.Stream(context.Background(), chat.ChatRequest{})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	var got string
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		got += chunk.Delta
	}
	if got != "hello" {
		t.Fatalf("streamed %q, want hello", got)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("closed stream should return io.EOF, got %v", err)
	}
}

func TestMockChatProviderPropagatesError(t *testing.T) {
	sentinel := errors.New("upstream down")
	provider := &MockChatProvider{Err: sentinel}
	if _, err := provider.Complete(context.Background(), chat.ChatRequest{}); !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel error, got %v", err)
	}
	if _, err := provider.Stream(context.Background(), chat.ChatRequest{}); !errors.Is(err, sentinel) {
		t.Fatalf("stream want sentinel error, got %v", err)
	}
}

func TestMockEmbeddingProviderIsDeterministicAndSized(t *testing.T) {
	provider := &MockEmbeddingProvider{Dim: 16}
	first, err := provider.EmbedQuery(context.Background(), "pizza")
	if err != nil {
		t.Fatalf("embed query: %v", err)
	}
	if len(first) != 16 {
		t.Fatalf("dimension = %d, want 16", len(first))
	}
	if provider.Dimensions() != 16 {
		t.Fatalf("Dimensions() = %d", provider.Dimensions())
	}
	second, _ := provider.EmbedQuery(context.Background(), "pizza")
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("vectors must be deterministic at index %d", i)
		}
	}
	if len(provider.QueryCalls) != 2 {
		t.Fatalf("query calls = %d", len(provider.QueryCalls))
	}
}

func TestMockEmbeddingProviderDefaultDimension(t *testing.T) {
	provider := &MockEmbeddingProvider{}
	if provider.Dimensions() != 8 {
		t.Fatalf("default dimension = %d, want 8", provider.Dimensions())
	}
	if provider.ModelID() != "mock-embedding" {
		t.Fatalf("default model = %q", provider.ModelID())
	}
}

func TestMockRerankProviderReverses(t *testing.T) {
	provider := &MockRerankProvider{}
	in := []search.RestaurantCandidate{{RestaurantID: 1}, {RestaurantID: 2}, {RestaurantID: 3}}
	out, err := provider.Rerank(context.Background(), "quiet", in)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if out[0].RestaurantID != 3 || out[2].RestaurantID != 1 {
		t.Fatalf("unexpected order: %+v", out)
	}
	if provider.Calls != 1 {
		t.Fatalf("calls = %d", provider.Calls)
	}
}
