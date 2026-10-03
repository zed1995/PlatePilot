package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	chatport "github.com/zed1995/platepilot/shared/chat"
	"github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/tool"
)

// Defaults for the tunables an operator is not forced to set.
const (
	DefaultTimeout    = 60 * time.Second
	DefaultMaxRetries = 2
	DefaultBackoff    = time.Second
	userAgent         = "platepilot-chat"
	maxErrorBodyBytes = 2048
)

// Options tunes one Client. The zero value is not valid (base URL, key, and
// model are required); tests override the transport and the backoff wait to
// stay fast and deterministic.
type Options struct {
	// BaseURL is the endpoint root, e.g. "https://openrouter.ai/api/v1".
	BaseURL string
	// APIKey is sent as a Bearer token.
	APIKey string
	// Model is the default model id; a request may override it per call.
	Model string
	// ExtraHeaders are added to every request (e.g. OpenRouter's X-Title).
	ExtraHeaders map[string]string
	// Capabilities declares what Model supports; see Capabilities.
	Capabilities Capabilities
	// Timeout bounds one unary HTTP attempt. Streaming calls are bounded by
	// the caller context instead, since a token stream legitimately outlasts
	// a unary timeout.
	Timeout time.Duration
	// MaxRetries is the number of *additional* attempts after the first one.
	// Zero means no retries and is honoured literally; callers that want the
	// shipped default set it to DefaultMaxRetries.
	MaxRetries int
	// BaseBackoff is the first retry delay; it doubles per attempt and is
	// superseded by a Retry-After header when the server gives one.
	BaseBackoff time.Duration
	// HTTPClient overrides the transport, for tests.
	HTTPClient *http.Client
	// Sleep overrides the backoff wait, for tests.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Client is a chatport provider backed by an OpenAI-compatible endpoint. It is
// safe for concurrent use and is created once and shared.
type Client struct {
	baseURL      string
	apiKey       string
	model        string
	extraHeaders map[string]string
	caps         Capabilities
	timeout      time.Duration
	maxRetries   int
	baseBackoff  time.Duration
	unaryHTTP    *http.Client
	streamHTTP   *http.Client
	sleep        func(ctx context.Context, d time.Duration) error
}

// Compile-time proof that one Client satisfies all three chat ports.
var (
	_ chatport.ChatProvider             = (*Client)(nil)
	_ chatport.ToolCallingProvider      = (*Client)(nil)
	_ chatport.StructuredOutputProvider = (*Client)(nil)
)

// New builds a Client from explicit options.
func New(opts Options) (*Client, error) {
	baseURL := strings.TrimSpace(opts.BaseURL)
	if baseURL == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "openai: base url is required")
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		return nil, errs.Newf(errs.CodeInvalidArgument,
			"openai: base url must start with http:// or https:// (got %q)", opts.BaseURL)
	}
	if strings.TrimSpace(opts.APIKey) == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "openai: api key is required")
	}
	if strings.TrimSpace(opts.Model) == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "openai: model is required")
	}

	caps := opts.Capabilities
	if caps.ContextTokens == 0 {
		caps.ContextTokens = DefaultCapabilities().ContextTokens
	}
	if caps.ContextTokens < 0 {
		return nil, errs.Newf(errs.CodeInvalidArgument,
			"openai: context tokens must be > 0 (got %d)", caps.ContextTokens)
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	maxRetries := opts.MaxRetries
	if maxRetries < 0 {
		return nil, errs.Newf(errs.CodeInvalidArgument,
			"openai: max retries must be >= 0 (got %d)", maxRetries)
	}
	baseBackoff := opts.BaseBackoff
	if baseBackoff <= 0 {
		baseBackoff = DefaultBackoff
	}
	unaryHTTP := opts.HTTPClient
	if unaryHTTP == nil {
		unaryHTTP = &http.Client{}
	}
	// The stream client shares the transport (connection pool, dial timeouts)
	// but never carries a client-wide timeout: a stream lives as long as the
	// caller's context.
	streamHTTP := &http.Client{Transport: unaryHTTP.Transport}
	sleepFn := opts.Sleep
	if sleepFn == nil {
		sleepFn = sleepCtx
	}

	return &Client{
		baseURL:      baseURL,
		apiKey:       strings.TrimSpace(opts.APIKey),
		model:        strings.TrimSpace(opts.Model),
		extraHeaders: opts.ExtraHeaders,
		caps:         caps,
		timeout:      timeout,
		maxRetries:   maxRetries,
		baseBackoff:  baseBackoff,
		unaryHTTP:    unaryHTTP,
		streamHTTP:   streamHTTP,
		sleep:        sleepFn,
	}, nil
}

// ModelID returns the configured default model id (for audit records).
func (c *Client) ModelID() string { return c.model }

// Capabilities returns the declared model capabilities.
func (c *Client) Capabilities() Capabilities { return c.caps }

// SupportsTools reports whether the configured model supports tool calling.
func (c *Client) SupportsTools() bool { return c.caps.Tools }

// SupportsParallelTools reports whether the model supports parallel tool calls.
func (c *Client) SupportsParallelTools() bool { return c.caps.ParallelTools }

// SupportsJSONSchema reports whether strict json_schema output is enabled.
func (c *Client) SupportsJSONSchema() bool { return c.caps.JSONSchema }

// Complete performs a plain (non-streaming) completion.
func (c *Client) Complete(ctx context.Context, req chat.ChatRequest) (chat.ChatResponse, error) {
	resp, err := c.unary(ctx, req, nil, nil)
	if err != nil {
		return chat.ChatResponse{}, err
	}
	return c.toChatResponse(resp), nil
}

// ChatWithTools performs a completion that may return tool calls. When the
// configured model does not declare tool support the request is rejected
// locally: no HTTP call is made, because the failure mode would be a prose
// answer where the agent expects a structured call.
func (c *Client) ChatWithTools(ctx context.Context, req chat.ChatRequest, tools []tool.ToolSpec) (chat.ToolCallResponse, error) {
	if !c.caps.Tools {
		return chat.ToolCallResponse{}, errs.Newf(errs.CodeInvalidArgument,
			"openai: configured model %q does not support tools; set CHAT_SUPPORTS_TOOLS=true "+
				"and choose a function-calling model", c.model)
	}
	if len(tools) == 0 {
		return chat.ToolCallResponse{}, errs.New(errs.CodeInvalidArgument,
			"openai: ChatWithTools requires at least one tool")
	}
	resp, err := c.unary(ctx, req, tools, nil)
	if err != nil {
		return chat.ToolCallResponse{}, err
	}
	out := c.toChatResponse(resp)
	return chat.ToolCallResponse{
		Message:      out.Message,
		FinishReason: out.FinishReason,
		Usage:        out.Usage,
		Model:        out.Model,
	}, nil
}

// CompleteStructured performs a JSON-validated completion.
//
// With strict schema support the request uses response_format=json_schema.
// Without it the request degrades to json_object plus an in-prompt schema
// instruction. In both modes a body that is not valid JSON triggers exactly
// one temperature-zero retry with a correction note; a second failure is
// returned as validation_failed rather than as a transport error.
func (c *Client) CompleteStructured(ctx context.Context, req chat.StructuredRequest, schema json.RawMessage) (chat.StructuredResponse, error) {
	if len(schema) == 0 || !json.Valid(schema) {
		return chat.StructuredResponse{}, errs.New(errs.CodeInvalidArgument,
			"openai: structured output requires a valid JSON Schema")
	}

	base := chat.ChatRequest{
		ThreadID:        req.ThreadID,
		Messages:        req.Messages,
		Model:           req.Model,
		Metadata:        req.Metadata,
		ProviderOptions: nil,
	}

	var rf *responseFormat
	messages := req.Messages
	if c.caps.JSONSchema {
		rf = &responseFormat{
			Type: "json_schema",
			JSONSchema: &jsonSchemaSpec{
				Name:   "structured_result",
				Strict: true,
				Schema: schema,
			},
		}
	} else {
		rf = &responseFormat{Type: "json_object"}
		messages = withSchemaInstruction(messages, schema, "")
	}
	base.Messages = messages

	resp, err := c.unary(ctx, base, nil, rf)
	if err != nil {
		return chat.StructuredResponse{}, err
	}
	content := extractJSONObject(resp.Choices[0].Message.Content)
	if content != nil {
		return c.toStructuredResponse(resp, content), nil
	}

	// One corrective retry: deterministic temperature and an explicit note that
	// the previous body was unusable. The failed output is not sent back to
	// the model verbatim to avoid echoing ever-larger malformed payloads.
	retry := base
	zero := 0.0
	retry.Temperature = &zero
	if !c.caps.JSONSchema {
		retry.Messages = withSchemaInstruction(base.Messages, schema,
			"Your previous response was not valid JSON. Respond again with the JSON object only, no prose and no code fences.")
	}
	resp, err = c.unary(ctx, retry, nil, rf)
	if err != nil {
		return chat.StructuredResponse{}, err
	}
	content = extractJSONObject(resp.Choices[0].Message.Content)
	if content == nil {
		return chat.StructuredResponse{}, errs.Newf(errs.CodeValidationFailed,
			"openai: model %q returned content that is not valid JSON after one retry", c.model)
	}
	return c.toStructuredResponse(resp, content), nil
}

// Stream opens a streaming completion. The returned stream must be closed by
// the caller.
func (c *Client) Stream(ctx context.Context, req chat.ChatRequest) (chatport.ChatStream, error) {
	payload, err := c.buildPayload(req, nil, nil, true)
	if err != nil {
		return nil, err
	}
	httpReq, err := c.newRequest(ctx, payload, req.ProviderOptions)
	if err != nil {
		return nil, err
	}
	resp, err := c.streamHTTP.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, errs.Wrap(errs.CodeProviderTimeout, "openai: stream cancelled by caller", ctx.Err())
		}
		return nil, errs.Wrap(errs.CodeProviderUnavailable, "openai: stream request failed", err)
	}
	if resp.StatusCode != http.StatusOK {
		// Drain and classify exactly like the unary path, but a failed
		// pre-flight is never retried: nothing has been streamed yet but the
		// request that failed may be non-idempotent at the billing layer.
		_, err := c.classifyError(resp)
		_ = resp.Body.Close()
		return nil, err
	}
	return newSSEStream(resp.Body), nil
}

// unary performs the bounded-retry HTTP loop for a completion.
func (c *Client) unary(ctx context.Context, req chat.ChatRequest, tools []tool.ToolSpec, rf *responseFormat) (completionResponse, error) {
	payload, err := c.buildPayload(req, tools, rf, false)
	if err != nil {
		return completionResponse{}, err
	}

	backoff := c.baseBackoff
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			if err := c.sleep(ctx, backoff); err != nil {
				return completionResponse{}, errs.Wrap(errs.CodeProviderTimeout,
					"openai: cancelled while backing off", err)
			}
			backoff *= 2
		}

		retryable, wait, resp, err := c.attempt(ctx, payload, req.ProviderOptions)
		if err == nil {
			return resp, nil
		}
		if wait >= 0 {
			// The server named its own wait (0 means "retry immediately");
			// honour it for the next loop.
			backoff = wait
		}
		if !retryable || attempt >= c.maxRetries {
			return completionResponse{}, err
		}
	}
}

// attempt performs one unary HTTP round-trip. The bool reports whether the
// failure is worth retrying; the duration carries a Retry-After hint.
func (c *Client) attempt(ctx context.Context, payload []byte, providerOpts map[string]any) (bool, time.Duration, completionResponse, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	httpReq, err := c.newRequest(attemptCtx, payload, providerOpts)
	if err != nil {
		return false, 0, completionResponse{}, err
	}
	resp, err := c.unaryHTTP.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return false, 0, completionResponse{},
				errs.Wrap(errs.CodeProviderTimeout, "openai: request cancelled by caller", ctx.Err())
		}
		if attemptCtx.Err() != nil {
			return true, 0, completionResponse{},
				errs.Wrap(errs.CodeProviderTimeout, "openai: request timed out", err)
		}
		return true, 0, completionResponse{},
			errs.Wrap(errs.CodeProviderUnavailable, "openai: request failed", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		retryable, err := c.classifyError(resp)
		return retryable, retryAfter(resp), completionResponse{}, err
	}

	var out completionResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&out); err != nil {
		return true, 0, completionResponse{},
			errs.Wrap(errs.CodeProviderUnavailable, "openai: decode completion response", err)
	}
	if len(out.Choices) == 0 {
		return false, 0, completionResponse{}, errs.Newf(errs.CodeProviderUnavailable,
			"openai: model %q returned no choices", c.model)
	}
	if _, err := toDomainMessage(out.Choices[0].Message); err != nil {
		// The model answered with malformed tool arguments. This is a broken
		// provider response and not worth retrying blindly.
		return false, 0, completionResponse{}, err
	}
	return false, 0, out, nil
}

// classifyError maps a non-2xx response onto the project error model. It reads
// a bounded slice of the body so the upstream message (model id typos, quota,
// region denial) is visible to the operator — status code alone is not enough
// to act on an OpenRouter failure.
func (c *Client) classifyError(resp *http.Response) (bool, error) {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	detail := strings.TrimSpace(string(snippet))
	var apiErr apiError
	if json.Unmarshal(snippet, &apiErr) == nil && strings.TrimSpace(apiErr.Error.Message) != "" {
		detail = strings.TrimSpace(apiErr.Error.Message)
	}
	if detail == "" {
		detail = resp.Status
	} else {
		detail = fmt.Sprintf("%s: %s", resp.Status, detail)
	}

	switch {
	case resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode == http.StatusRequestTimeout,
		resp.StatusCode >= 500:
		return true, errs.Newf(errs.CodeProviderUnavailable,
			"openai: chat request failed (model %q): %s", c.model, detail)
	case resp.StatusCode == http.StatusBadRequest:
		return false, errs.Newf(errs.CodeInvalidArgument,
			"openai: chat request rejected (model %q): %s", c.model, detail)
	case resp.StatusCode == http.StatusUnauthorized,
		resp.StatusCode == http.StatusForbidden:
		return false, errs.Newf(errs.CodeUnauthorized,
			"openai: chat request not authorized (model %q): %s", c.model, detail)
	case resp.StatusCode == http.StatusPaymentRequired,
		resp.StatusCode == http.StatusNotFound:
		// 402: out of credit. 404 on OpenRouter means the model id has no
		// endpoint. Both are configuration faults the operator must fix.
		return false, errs.Newf(errs.CodeProviderUnavailable,
			"openai: chat model unavailable (model %q): %s", c.model, detail)
	default:
		return false, errs.Newf(errs.CodeProviderUnavailable,
			"openai: chat request failed (model %q): %s", c.model, detail)
	}
}

// retryAfter extracts a Retry-After delay. A non-negative duration (including
// zero = retry immediately) means the header was present and valid; -1 means
// the caller should fall back to exponential backoff.
func retryAfter(resp *http.Response) time.Duration {
	v := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if v == "" {
		return -1
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
		return 0
	}
	return -1
}

// buildPayload turns a domain request into the wire JSON body.
func (c *Client) buildPayload(req chat.ChatRequest, tools []tool.ToolSpec, rf *responseFormat, stream bool) (json.RawMessage, error) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = c.model
	}
	body := completionRequest{
		Model:       model,
		Messages:    toWireMessages(req.Messages),
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      stream,
	}
	if len(tools) > 0 {
		body.Tools = toWireTools(tools)
		body.ToolChoice = "auto"
		// Explicitly disable parallel calls only when the model does not
		// declare support for them; omitting the field keeps the server
		// default for models that do.
		if !c.caps.ParallelTools {
			parallel := false
			body.ParallelToolCalls = &parallel
		}
	}
	if rf != nil {
		body.ResponseFormat = rf
	}
	return mergeExtraBody(body, asStringMap(req.ProviderOptions, "extra_body"))
}

// newRequest builds the POST request with auth and configured headers.
// Authorization is applied last so a ProviderOptions header cannot replace
// the credential.
func (c *Client) newRequest(ctx context.Context, payload []byte, providerOpts map[string]any) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+chatCompletionsPath, bytes.NewReader(payload))
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, "openai: build request", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", userAgent)
	for key, value := range c.extraHeaders {
		httpReq.Header.Set(key, value)
	}
	for key, value := range asHeaderMap(providerOpts) {
		httpReq.Header.Set(key, value)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	return httpReq, nil
}

func (c *Client) toChatResponse(in completionResponse) chat.ChatResponse {
	// toDomainMessage is already validated on the unary path; the stream path
	// builds chunks directly.
	msg, _ := toDomainMessage(in.Choices[0].Message)
	return chat.ChatResponse{
		Message:      msg,
		FinishReason: mapFinishReason(in.Choices[0].FinishReason),
		Usage:        toUsage(in.Usage),
		Model:        in.Model,
	}
}

func (c *Client) toStructuredResponse(in completionResponse, content json.RawMessage) chat.StructuredResponse {
	return chat.StructuredResponse{
		Content: string(content),
		Usage:   toUsage(in.Usage),
		Model:   in.Model,
	}
}

// withSchemaInstruction appends a system message describing the required JSON
// shape for the json_object fallback mode. It appends rather than replacing
// the caller's system prompt so persona/instructions survive.
func withSchemaInstruction(messages []chat.ChatMessage, schema json.RawMessage, correction string) []chat.ChatMessage {
	instruction := fmt.Sprintf(
		"Respond with a single JSON object that conforms to this JSON Schema. "+
			"Output the JSON object only: no prose, no markdown, no code fences.\n\nSchema:\n%s",
		string(schema))
	if correction != "" {
		instruction = correction + "\n\n" + instruction
	}
	out := make([]chat.ChatMessage, 0, len(messages)+1)
	out = append(out, messages...)
	out = append(out, chat.ChatMessage{Role: chat.RoleSystem, Content: instruction})
	return out
}

// extractJSONObject pulls a JSON object out of model text, tolerating the
// common small-model habit of wrapping it in ```json fences or surrounding it
// with a sentence. It returns nil when nothing usable was found.
func extractJSONObject(s string) json.RawMessage {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return nil
	}
	if json.Valid([]byte(trimmed)) {
		return json.RawMessage(trimmed)
	}
	// Strip a leading/trailing code fence.
	if strings.HasPrefix(trimmed, "```") {
		trimmed = strings.TrimPrefix(trimmed, "```json")
		trimmed = strings.TrimPrefix(trimmed, "```")
		trimmed = strings.TrimSpace(trimmed)
		trimmed = strings.TrimSuffix(trimmed, "```")
		trimmed = strings.TrimSpace(trimmed)
		if json.Valid([]byte(trimmed)) {
			return json.RawMessage(trimmed)
		}
	}
	// Fall back to the outermost {...} span.
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start >= 0 && end > start {
		candidate := strings.TrimSpace(s[start : end+1])
		if json.Valid([]byte(candidate)) {
			return json.RawMessage(candidate)
		}
	}
	return nil
}

// sleepCtx waits for d unless the context ends first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
