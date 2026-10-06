package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
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

// Reasoning is a channel of its own, not answer text. A reasoning-only frame
// must survive the empty-frame skip and must never land in Delta — the answer
// the user keeps is built from Delta alone.
func TestStreamKeepsReasoningOutOfTheAnswer(t *testing.T) {
	const frames = "data: {\"choices\":[{\"delta\":{\"reasoning\":\"We need answer\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\" in Chinese\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"你好\"}}]}\n\n" +
		"data: [DONE]\n\n"

	c := newOfflineClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
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
	var reasoning, answer string
	for _, chunk := range chunks {
		reasoning += chunk.Reasoning
		answer += chunk.Delta
	}
	if reasoning != "We need answer in Chinese" {
		t.Errorf("reasoning = %q", reasoning)
	}
	if answer != "你好" {
		t.Errorf("answer = %q, want just the content delta", answer)
	}
	if len(chunks) != 3 {
		t.Fatalf("chunks = %d, want 3 (two reasoning frames kept, not skipped)", len(chunks))
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

// An overloaded route answers the SSE request with an error frame before the
// first token. Nothing has reached the caller yet, so the dial is retried and
// the caller only ever sees the successful attempt.
func TestStreamRetriesBeforeFirstChunk(t *testing.T) {
	var calls atomic.Int64
	const good = "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n" +
		"data: [DONE]\n\n"
	c := newOfflineClient(t, countingTransport(&calls, func(r *http.Request) (*http.Response, error) {
		// Every attempt must carry the full payload: the request is rebuilt per
		// attempt precisely because its body is a one-shot reader.
		body := decodeRequest(t, r)
		if _, ok := body["messages"]; !ok {
			t.Errorf("attempt %d sent a body without messages: %v", calls.Load(), body)
		}
		if body["stream"] != true {
			t.Errorf("attempt %d: stream = %v, want true", calls.Load(), body["stream"])
		}
		if calls.Load() == 1 {
			return streamResponse("data: {\"error\":{\"message\":\"upstream overloaded\",\"code\":503}}\n\n"), nil
		}
		return streamResponse(good), nil
	}))
	stream, err := c.Stream(context.Background(), chat.ChatRequest{
		Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "x"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	chunks, err := drain(t, stream)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(chunks) != 1 || chunks[0].Delta != "Hello" {
		t.Fatalf("chunks = %+v, want the second attempt's text", chunks)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", calls.Load())
	}
}

func TestStreamErrorBeforeFirstChunkFailsAfterRetries(t *testing.T) {
	var calls atomic.Int64
	c := newOfflineClient(t, countingTransport(&calls, func(*http.Request) (*http.Response, error) {
		return streamResponse("data: {\"error\":{\"message\":\"upstream overloaded\",\"code\":503}}\n\n"), nil
	}))
	_, err := c.Stream(context.Background(), chat.ChatRequest{
		Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "x"}},
	})
	if err == nil || errs.CodeOf(err) != errs.CodeProviderUnavailable {
		t.Fatalf("want provider_unavailable, got %v", err)
	}
	if !strings.Contains(err.Error(), "upstream overloaded") {
		t.Errorf("error hides the upstream reason: %v", err)
	}
	if calls.Load() != 3 { // first + 2 retries
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}

// Once a chunk is in the caller's hands a failure is final: replaying the turn
// would duplicate text the user already has.
func TestStreamErrorAfterFirstChunkIsNotRetried(t *testing.T) {
	var calls atomic.Int64
	const frames = "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n" +
		"data: {\"error\":{\"message\":\"upstream overloaded\",\"code\":503}}\n\n"
	c := newOfflineClient(t, countingTransport(&calls, func(*http.Request) (*http.Response, error) {
		return streamResponse(frames), nil
	}))
	stream, err := c.Stream(context.Background(), chat.ChatRequest{
		Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "x"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	chunks, err := drain(t, stream)
	if err == nil || errs.CodeOf(err) != errs.CodeProviderUnavailable {
		t.Fatalf("want provider_unavailable, got %v", err)
	}
	if len(chunks) != 1 || chunks[0].Delta != "Hel" {
		t.Errorf("chunks = %+v, want the partial text", chunks)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want exactly 1", calls.Load())
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
