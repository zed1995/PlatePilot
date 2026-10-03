package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/errs"
)

func drain(t *testing.T, stream interface {
	Recv() (chat.ChatChunk, error)
	Close() error
}) ([]chat.ChatChunk, error) {
	t.Helper()
	var chunks []chat.ChatChunk
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return chunks, nil
		}
		if err != nil {
			return chunks, err
		}
		chunks = append(chunks, chunk)
	}
}

func TestStreamTextFrames(t *testing.T) {
	const frames = ": keepalive\n\n" +
		"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\" world\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]," +
		"\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n" +
		"data: [DONE]\n\n"

	c := newOfflineClient(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		return streamResponse(frames), nil
	}))
	stream, err := c.Stream(context.Background(), chat.ChatRequest{
		Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	chunks, err := drain(t, stream)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3", len(chunks))
	}
	if chunks[0].Delta+chunks[1].Delta != "Hello world" {
		t.Errorf("text = %q%q", chunks[0].Delta, chunks[1].Delta)
	}
	if chunks[2].FinishReason != chat.FinishReasonStop || chunks[2].Usage == nil ||
		chunks[2].Usage.TotalTokens != 5 {
		t.Errorf("terminal chunk wrong: %+v", chunks[2])
	}
}

func TestStreamAccumulatesToolCallFragments(t *testing.T) {
	// Fragments follow the OpenAI protocol: index 0 first gets id/name, then
	// arguments arrive in pieces.
	const frames = "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0," +
		"\"id\":\"call_42\",\"type\":\"function\",\"function\":{\"name\":\"search_restaurants\",\"arguments\":\"\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0," +
		"\"function\":{\"arguments\":\"{\\\"borough\\\":\\\"\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0," +
		"\"function\":{\"arguments\":\"manhattan\\\"}\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"

	c := newOfflineClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return streamResponse(frames), nil
	}))
	stream, err := c.Stream(context.Background(), chat.ChatRequest{
		Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "find food"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	chunks, err := drain(t, stream)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	last := chunks[len(chunks)-2] // tool fragment before finish frame
	if last.ToolCalls == nil || len(last.ToolCalls) != 1 {
		t.Fatalf("expected accumulated tool call, chunks = %+v", chunks)
	}
	tc := last.ToolCalls[0]
	if tc.ID != "call_42" || tc.Name != "search_restaurants" {
		t.Errorf("tool identity = %+v", tc)
	}
	if string(tc.Arguments) != `{"borough":"manhattan"}` {
		t.Errorf("arguments = %q", tc.Arguments)
	}
	if chunks[len(chunks)-1].FinishReason != chat.FinishReasonToolCalls {
		t.Errorf("finish = %q", chunks[len(chunks)-1].FinishReason)
	}
}

func TestStreamRejectsMalformedFinalArguments(t *testing.T) {
	const frames = "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0," +
		"\"id\":\"c1\",\"function\":{\"name\":\"t\",\"arguments\":\"oops\"}}]}}]}\n\n" +
		"data: [DONE]\n\n"
	c := newOfflineClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return streamResponse(frames), nil
	}))
	stream, err := c.Stream(context.Background(), chat.ChatRequest{
		Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "x"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if _, err := drain(t, stream); err == nil || errs.CodeOf(err) != errs.CodeProviderUnavailable {
		t.Fatalf("want provider_unavailable for malformed args, got %v", err)
	}
}

func TestStreamSurfacesErrorFrame(t *testing.T) {
	const frames = "data: {\"error\":{\"message\":\"upstream overloaded\",\"code\":503}}\n\n"
	c := newOfflineClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return streamResponse(frames), nil
	}))
	stream, err := c.Stream(context.Background(), chat.ChatRequest{
		Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "x"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if _, err := drain(t, stream); err == nil || errs.CodeOf(err) != errs.CodeProviderUnavailable {
		t.Fatalf("want provider_unavailable, got %v", err)
	}
}

func TestStreamNon200IsClassified(t *testing.T) {
	c := newOfflineClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusUnauthorized, `{"error":{"message":"bad key"}}`), nil
	}))
	_, err := c.Stream(context.Background(), chat.ChatRequest{
		Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "x"}},
	})
	if err == nil || errs.CodeOf(err) != errs.CodeUnauthorized {
		t.Fatalf("want unauthorized, got %v", err)
	}
}

func TestStreamCloseIsIdempotent(t *testing.T) {
	c := newOfflineClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return streamResponse("data: [DONE]\n\n"), nil
	}))
	stream, err := c.Stream(context.Background(), chat.ChatRequest{
		Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "x"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
