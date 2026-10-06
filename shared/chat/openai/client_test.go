package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/tool"
)

const successBody = `{
  "id": "chatcmpl-1",
  "model": "vendor/test-model",
  "choices": [{"index": 0, "message": {"role": "assistant", "content": "hello back"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18}
}`

func TestNewValidatesOptions(t *testing.T) {
	cases := []func(Options) Options{
		func(o Options) Options { o.BaseURL = ""; return o },
		func(o Options) Options { o.APIKey = ""; return o },
		func(o Options) Options { o.Model = ""; return o },
		func(o Options) Options { o.BaseURL = "ftp://nope"; return o },
		func(o Options) Options { o.MaxRetries = -1; return o },
		func(o Options) Options {
			o.Capabilities.ContextTokens = -1
			return o
		},
	}
	base := Options{BaseURL: "https://x.example/v1", APIKey: "k", Model: "m"}
	for i, mutate := range cases {
		if _, err := New(mutate(base)); err == nil {
			t.Errorf("case %d: want construction error", i)
		}
	}
}

func TestCompleteRequestShapeAndResponseMapping(t *testing.T) {
	var gotReq *http.Request
	var gotBody map[string]any
	c := newOfflineClient(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		gotReq = r
		gotBody = decodeRequest(t, r)
		return jsonResponse(http.StatusOK, successBody), nil
	}))

	resp, err := c.Complete(context.Background(), chat.ChatRequest{
		Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Message.Content != "hello back" || resp.FinishReason != chat.FinishReasonStop {
		t.Errorf("response mapping: %+v", resp)
	}
	if resp.Usage.TotalTokens != 18 || resp.Model != "vendor/test-model" {
		t.Errorf("usage/model mapping: %+v", resp.Usage)
	}
	if gotReq.URL.Path != "/api/v1/chat/completions" {
		t.Errorf("path = %q", gotReq.URL.Path)
	}
	if gotReq.Header.Get("Authorization") != "Bearer test-key" {
		t.Errorf("auth header = %q", gotReq.Header.Get("Authorization"))
	}
	if gotReq.Header.Get("User-Agent") != userAgent {
		t.Errorf("user-agent = %q", gotReq.Header.Get("User-Agent"))
	}
	if gotBody["model"] != "vendor/test-model" {
		t.Errorf("body model = %v", gotBody["model"])
	}
	if stream, present := gotBody["stream"]; present && stream != false {
		t.Errorf("unary request must not request streaming, got %v", stream)
	}
}

func TestPerRequestModelOverridesDefault(t *testing.T) {
	var body map[string]any
	c := newOfflineClient(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		body = decodeRequest(t, r)
		return jsonResponse(http.StatusOK, successBody), nil
	}))
	_, err := c.Complete(context.Background(), chat.ChatRequest{
		Model:    "vendor/other-model",
		Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if body["model"] != "vendor/other-model" {
		t.Errorf("model override = %v", body["model"])
	}
}

func TestChatWithToolsSerializesAndMapsToolCalls(t *testing.T) {
	var body map[string]any
	const toolBody = `{
	  "model": "vendor/test-model",
	  "choices": [{"index": 0, "message": {"role": "assistant", "content": null,
	    "tool_calls": [{"id": "call_9", "type": "function",
	      "function": {"name": "search_restaurants", "arguments": "{\"borough\":\"manhattan\",\"top_k\":3}"}}]},
	    "finish_reason": "tool_calls"}],
	  "usage": {"prompt_tokens": 20, "completion_tokens": 5, "total_tokens": 25}
	}`
	c := newOfflineClient(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		body = decodeRequest(t, r)
		return jsonResponse(http.StatusOK, toolBody), nil
	}))

	resp, err := c.ChatWithTools(context.Background(),
		chat.ChatRequest{Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "find dinner"}}},
		[]tool.ToolSpec{{
			Name:        "search_restaurants",
			Description: "find restaurants",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"borough":{"type":"string"}}}`),
		}})
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if resp.FinishReason != chat.FinishReasonToolCalls || len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("response = %+v", resp)
	}
	tc := resp.Message.ToolCalls[0]
	if tc.ID != "call_9" || tc.Name != "search_restaurants" ||
		string(tc.Arguments) != `{"borough":"manhattan","top_k":3}` {
		t.Errorf("tool call mapping: %+v", tc)
	}

	tools := body["tools"].([]any)
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "search_restaurants" || body["tool_choice"] != "auto" {
		t.Errorf("request tools/choice wrong: %v", body)
	}
	// Parallel calls default off.
	if body["parallel_tool_calls"] != false {
		t.Errorf("parallel_tool_calls = %v, want false", body["parallel_tool_calls"])
	}
}

func TestParallelToolsFlag(t *testing.T) {
	caps := DefaultCapabilities()
	caps.ParallelTools = true
	c, err := New(Options{
		BaseURL:      "https://x.example/v1",
		APIKey:       "k",
		Model:        "m",
		Capabilities: caps,
		HTTPClient: &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, successBody), nil
		})},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var body map[string]any
	rt := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		body = decodeRequest(t, r)
		return jsonResponse(http.StatusOK, successBody), nil
	})
	c.unaryHTTP = &http.Client{Transport: rt}
	_, err = c.ChatWithTools(context.Background(),
		chat.ChatRequest{Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "x"}}},
		[]tool.ToolSpec{{Name: "t"}})
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if _, present := body["parallel_tool_calls"]; present {
		t.Errorf("parallel_tool_calls should be omitted when enabled, got %v", body["parallel_tool_calls"])
	}
}

func TestCapabilityRejectsToolsWithoutHTTP(t *testing.T) {
	var calls atomic.Int64
	caps := DefaultCapabilities()
	caps.Tools = false
	c, err := New(Options{
		BaseURL:      "https://x.example/v1",
		APIKey:       "k",
		Model:        "m",
		Capabilities: caps,
		HTTPClient: &http.Client{Transport: countingTransport(&calls, func(*http.Request) (*http.Response, error) {
			t.Error("no HTTP request must be made when tools are unsupported")
			return nil, nil
		})},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.ChatWithTools(context.Background(),
		chat.ChatRequest{Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "x"}}},
		[]tool.ToolSpec{{Name: "t"}})
	if err == nil || errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("want invalid_argument, got %v", err)
	}
	if calls.Load() != 0 {
		t.Errorf("transport called %d times", calls.Load())
	}
}

func TestHTTPStatusMapping(t *testing.T) {
	cases := []struct {
		status int
		body   string
		code   errs.Code
	}{
		{http.StatusBadRequest, `{"error":{"message":"bad model"}}`, errs.CodeInvalidArgument},
		{http.StatusUnauthorized, `{"error":{"message":"bad key"}}`, errs.CodeUnauthorized},
		{http.StatusForbidden, `{"error":{"message":"region denied"}}`, errs.CodeUnauthorized},
		{http.StatusPaymentRequired, `{"error":{"message":"no credit"}}`, errs.CodeProviderUnavailable},
		{http.StatusNotFound, `{"error":{"message":"model has no endpoint"}}`, errs.CodeProviderUnavailable},
	}
	for _, tc := range cases {
		c := newOfflineClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return jsonResponse(tc.status, tc.body), nil
		}))
		_, err := c.Complete(context.Background(), chat.ChatRequest{
			Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "hi"}},
		})
		if err == nil || errs.CodeOf(err) != tc.code {
			t.Errorf("status %d: want code %s, got %v", tc.status, tc.code, err)
		}
	}
}

func TestRetryOn429ThenSucceeds(t *testing.T) {
	var calls atomic.Int64
	var slept time.Duration
	rt := countingTransport(&calls, func(*http.Request) (*http.Response, error) {
		if calls.Load() == 1 {
			resp := jsonResponse(http.StatusTooManyRequests, `{"error":{"message":"slow down"}}`)
			resp.Header.Set("Retry-After", "0")
			return resp, nil
		}
		return jsonResponse(http.StatusOK, successBody), nil
	})
	c, err := New(Options{
		BaseURL:     "https://x.example/v1",
		APIKey:      "k",
		Model:       "m",
		MaxRetries:  DefaultMaxRetries,
		HTTPClient:  &http.Client{Transport: rt},
		Sleep:       func(_ context.Context, d time.Duration) error { slept = d; return nil },
		BaseBackoff: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := c.Complete(context.Background(), chat.ChatRequest{
		Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", calls.Load())
	}
	if resp.Message.Content != "hello back" {
		t.Errorf("unexpected content %q", resp.Message.Content)
	}
	if slept != 0 {
		t.Errorf("Retry-After: 0 should override backoff, slept %s", slept)
	}
}

func TestRetryExhaustedOn5xx(t *testing.T) {
	var calls atomic.Int64
	c := newOfflineClient(t, countingTransport(&calls, func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusBadGateway, "bad gateway"), nil
	}))
	_, err := c.Complete(context.Background(), chat.ChatRequest{
		Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "hi"}},
	})
	if err == nil || errs.CodeOf(err) != errs.CodeProviderUnavailable {
		t.Fatalf("want provider_unavailable, got %v", err)
	}
	if calls.Load() != 3 { // first + 2 retries
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}

// OpenRouter reports an overloaded upstream as HTTP 200 with an error object
// and no choices. Before this was handled the operator saw only "returned no
// choices" and the turn died on the first attempt.
func TestGatewayErrorIn200BodyIsSurfacedAndRetried(t *testing.T) {
	var calls atomic.Int64
	const body = `{"id":"gen-1","error":{"message":"Upstream error from Nvidia: Service temporarily overloaded","code":503,"metadata":{"error_type":"provider_overloaded"}}}`
	c := newOfflineClient(t, countingTransport(&calls, func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, body), nil
	}))
	_, err := c.Complete(context.Background(), chat.ChatRequest{
		Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "hi"}},
	})
	if err == nil || errs.CodeOf(err) != errs.CodeProviderUnavailable {
		t.Fatalf("want provider_unavailable, got %v", err)
	}
	if !strings.Contains(err.Error(), "Service temporarily overloaded") ||
		!strings.Contains(err.Error(), "vendor/test-model") {
		t.Errorf("error hides the upstream reason or the model: %v", err)
	}
	if calls.Load() != 3 { // first + 2 retries
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}

// A 4xx inside the body is a fault the operator must fix, so it is reported
// once rather than retried.
func TestGatewayClientErrorIn200BodyIsNotRetried(t *testing.T) {
	var calls atomic.Int64
	const body = `{"id":"gen-2","error":{"message":"No endpoints found for this model","code":400}}`
	c := newOfflineClient(t, countingTransport(&calls, func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, body), nil
	}))
	_, err := c.Complete(context.Background(), chat.ChatRequest{
		Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "hi"}},
	})
	if err == nil || errs.CodeOf(err) != errs.CodeProviderUnavailable {
		t.Fatalf("want provider_unavailable, got %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want exactly 1", calls.Load())
	}
}

func TestEmptyChoicesIsRetried(t *testing.T) {
	var calls atomic.Int64
	c := newOfflineClient(t, countingTransport(&calls, func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"id":"gen-3","model":"vendor/test-model","choices":[]}`), nil
	}))
	_, err := c.Complete(context.Background(), chat.ChatRequest{
		Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "hi"}},
	})
	if err == nil || errs.CodeOf(err) != errs.CodeProviderUnavailable {
		t.Fatalf("want provider_unavailable, got %v", err)
	}
	if !strings.Contains(err.Error(), "returned no choices") {
		t.Errorf("unexpected message: %v", err)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}

func TestZeroRetriesMeansSingleAttempt(t *testing.T) {
	var calls atomic.Int64
	c, err := New(Options{
		BaseURL:    "https://x.example/v1",
		APIKey:     "k",
		Model:      "m",
		MaxRetries: 0,
		HTTPClient: &http.Client{Transport: countingTransport(&calls,
			func(*http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusBadGateway, "bad gateway"), nil
			})},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.Complete(context.Background(), chat.ChatRequest{
		Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("want error")
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want exactly 1 with MaxRetries=0", calls.Load())
	}
}

func TestTransportErrorRetriesThenFails(t *testing.T) {
	var calls atomic.Int64
	c := newOfflineClient(t, countingTransport(&calls, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	}))
	_, err := c.Complete(context.Background(), chat.ChatRequest{
		Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "hi"}},
	})
	if err == nil || errs.CodeOf(err) != errs.CodeProviderUnavailable {
		t.Fatalf("want provider_unavailable, got %v", err)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}

func TestCancelledContextIsNotRetried(t *testing.T) {
	var calls atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := newOfflineClient(t, countingTransport(&calls, func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))
	_, err := c.Complete(ctx, chat.ChatRequest{
		Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "hi"}},
	})
	if err == nil || errs.CodeOf(err) != errs.CodeProviderTimeout {
		t.Fatalf("want provider_timeout, got %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("cancelled request retried %d times", calls.Load())
	}
}

func TestProviderOptionsExtraBodyAndHeaders(t *testing.T) {
	var req *http.Request
	var body map[string]any
	c := newOfflineClient(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		req = r
		body = decodeRequest(t, r)
		return jsonResponse(http.StatusOK, successBody), nil
	}))
	_, err := c.Complete(context.Background(), chat.ChatRequest{
		Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "hi"}},
		ProviderOptions: map[string]any{
			"extra_body":    map[string]any{"provider": map[string]any{"allow_fallbacks": false}},
			"extra_headers": map[string]string{"X-Title": "PlatePilot", "Authorization": "Bearer hijack"},
			"ignored_key":   42,
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if req.Header.Get("X-Title") != "PlatePilot" {
		t.Errorf("X-Title = %q", req.Header.Get("X-Title"))
	}
	if req.Header.Get("Authorization") != "Bearer test-key" {
		t.Errorf("Authorization must not be overridable, got %q", req.Header.Get("Authorization"))
	}
	if body["provider"] == nil {
		t.Errorf("extra_body not merged: %v", body)
	}
	if _, present := body["ignored_key"]; present {
		t.Errorf("unknown provider option leaked into body")
	}
}

func TestCompleteStructuredStrictSchema(t *testing.T) {
	var body map[string]any
	c := newOfflineClient(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		body = decodeRequest(t, r)
		return jsonResponse(http.StatusOK, `{
		  "model": "vendor/test-model",
		  "choices": [{"message": {"role":"assistant","content":"{\"intent\":\"search\"}"}, "finish_reason": "stop"}],
		  "usage": {"prompt_tokens": 5, "completion_tokens": 5, "total_tokens": 10}
		}`), nil
	}))
	resp, err := c.CompleteStructured(context.Background(),
		chat.StructuredRequest{Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "classify"}}},
		json.RawMessage(`{"type":"object","properties":{"intent":{"type":"string"}}}`))
	if err != nil {
		t.Fatalf("CompleteStructured: %v", err)
	}
	if resp.Content != `{"intent":"search"}` {
		t.Errorf("content = %q", resp.Content)
	}
	rf := body["response_format"].(map[string]any)
	if rf["type"] != "json_schema" {
		t.Errorf("response_format = %v", rf)
	}
}

func TestCompleteStructuredFallbackModeRetriesOnce(t *testing.T) {
	var calls atomic.Int64
	var lastBody map[string]any
	caps := DefaultCapabilities() // JSONSchema false → fallback mode
	c, err := New(Options{
		BaseURL:      "https://x.example/v1",
		APIKey:       "k",
		Model:        "m",
		Capabilities: caps,
		Timeout:      5 * time.Second,
		HTTPClient: &http.Client{Transport: countingTransport(&calls, func(r *http.Request) (*http.Response, error) {
			lastBody = decodeRequest(t, r)
			if calls.Load() == 1 {
				return jsonResponse(http.StatusOK, `{
				  "choices": [{"message": {"role":"assistant","content":"sure, the answer is..."}, "finish_reason":"stop"}]}`,
				), nil
			}
			return jsonResponse(http.StatusOK, `{
			  "choices": [{"message": {"role":"assistant","content":"{\"intent\":\"small_talk\"}"}, "finish_reason":"stop"}]}`,
			), nil
		})},
		Sleep: noSleep,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := c.CompleteStructured(context.Background(),
		chat.StructuredRequest{Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "classify"}}},
		json.RawMessage(`{"type":"object"}`))
	if err != nil {
		t.Fatalf("CompleteStructured: %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want corrective retry", calls.Load())
	}
	if resp.Content != `{"intent":"small_talk"}` {
		t.Errorf("content = %q", resp.Content)
	}
	rf := lastBody["response_format"].(map[string]any)
	if rf["type"] != "json_object" {
		t.Errorf("fallback response_format = %v", rf)
	}
	msgs := lastBody["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "system" || !strings.Contains(last["content"].(string), "JSON Schema") {
		t.Errorf("schema instruction missing: %v", last)
	}
}

func TestCompleteStructuredFailsAfterTwoInvalidBodies(t *testing.T) {
	caps := DefaultCapabilities()
	c, err := New(Options{
		BaseURL:      "https://x.example/v1",
		APIKey:       "k",
		Model:        "m",
		Capabilities: caps,
		HTTPClient: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return jsonResponse(http.StatusOK, `{
			  "choices": [{"message": {"role":"assistant","content":"still prose"}, "finish_reason":"stop"}]}`), nil
		})},
		Sleep: noSleep,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.CompleteStructured(context.Background(),
		chat.StructuredRequest{Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "classify"}}},
		json.RawMessage(`{"type":"object"}`))
	if err == nil || errs.CodeOf(err) != errs.CodeValidationFailed {
		t.Fatalf("want validation_failed after retry, got %v", err)
	}
}

func TestMalformedToolArgumentsRejected(t *testing.T) {
	c := newOfflineClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{
		  "choices": [{"message": {"role":"assistant",
		    "tool_calls":[{"id":"c1","type":"function",
		      "function":{"name":"t","arguments":"oops-not-json"}}]},
		    "finish_reason":"tool_calls"}]}`), nil
	}))
	_, err := c.ChatWithTools(context.Background(),
		chat.ChatRequest{Messages: []chat.ChatMessage{{Role: chat.RoleUser, Content: "x"}}},
		[]tool.ToolSpec{{Name: "t"}})
	if err == nil || errs.CodeOf(err) != errs.CodeProviderUnavailable {
		t.Fatalf("want provider_unavailable, got %v", err)
	}
}
