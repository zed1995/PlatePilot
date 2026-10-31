package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/eino-contrib/jsonschema"

	"github.com/zed1995/platepilot/chat-service/internal/agent/slots"
	"github.com/zed1995/platepilot/chat-service/internal/config"
)

// providerOpenAICompatible is the only chat adapter shipped in M4. The value
// is part of the environment contract documented in .env.example.
const providerOpenAICompatible = "openai_compatible"

// Defaults applied when the operator left a chat tunable unset.
const (
	defaultChatModelTimeout = 60 * time.Second
	defaultRetryBackoff     = time.Second
	maxRetryBackoff         = 30 * time.Second
)

// buildChatModels builds one model per job a turn asks of the chat endpoint:
// planning (with tools), answering, and slot extraction.
//
// They share a transport rather than a model object because the roles can be
// different model ids on the same endpoint. Binding tools to the planning model
// happens in the runner, which owns the registry; extraction differs in
// configuration instead — it is built with a fixed response format, because the
// request a structured-output call carries no longer names its own schema.
func buildChatModels(cfg config.ChatConfig) (plan, answer, extract model.BaseChatModel, err error) {
	if cfg.Provider != providerOpenAICompatible {
		return nil, nil, nil, fmt.Errorf("unsupported CHAT_PROVIDER %q (supported value: %q)",
			cfg.Provider, providerOpenAICompatible)
	}
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		return nil, nil, nil, errors.New("CHAT_BASE_URL is required")
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		return nil, nil, nil, fmt.Errorf("CHAT_BASE_URL must start with http:// or https:// (got %q)", cfg.BaseURL)
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, nil, nil, errors.New("CHAT_API_KEY is required")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, nil, nil, errors.New("CHAT_MODEL is required")
	}
	if cfg.MaxRetries < 0 {
		return nil, nil, nil, fmt.Errorf("CHAT_MAX_RETRIES must be >= 0 (got %d)", cfg.MaxRetries)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultChatModelTimeout
	}

	// The eino-ext model ignores its own Timeout when an HTTPClient is supplied,
	// so the unary timeout, the retries, and the extra headers all live here.
	client := &http.Client{Transport: &retryTransport{
		base:        http.DefaultTransport,
		headers:     cfg.ExtraHeaders,
		maxRetries:  cfg.MaxRetries,
		timeout:     timeout,
		baseBackoff: defaultRetryBackoff,
	}}

	newModel := func(name string, format *einoopenai.ChatCompletionResponseFormat) (*einoopenai.ChatModel, error) {
		return einoopenai.NewChatModel(context.Background(), &einoopenai.ChatModelConfig{
			BaseURL:        baseURL,
			APIKey:         cfg.APIKey,
			Model:          name,
			HTTPClient:     client,
			ResponseFormat: format,
		})
	}

	planModel, err := newModel(cfg.PlanModel, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("build plan model: %w", err)
	}
	answerModel, err := newModel(cfg.AnswerModel, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("build answer model: %w", err)
	}
	extractFormat, err := extractionResponseFormat(cfg.SupportsJSONSchema)
	if err != nil {
		return nil, nil, nil, err
	}
	extractModel, err := newModel(cfg.ExtractModel, extractFormat)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("build extract model: %w", err)
	}
	return planModel, answerModel, extractModel, nil
}

// extractionResponseFormat fixes the shape of a slot-extraction response.
//
// With strict schema support the wire schema is the extraction schema itself.
// Without it the model is asked for a JSON object and the instruction prompt
// carries the field vocabulary — the best a provider without json_schema can
// offer, and still parsed and validated on this side.
func extractionResponseFormat(strict bool) (*einoopenai.ChatCompletionResponseFormat, error) {
	if !strict {
		return &einoopenai.ChatCompletionResponseFormat{
			Type: einoopenai.ChatCompletionResponseFormatTypeJSONObject,
		}, nil
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(slots.ExtractionSchema(), &schema); err != nil {
		return nil, fmt.Errorf("decode slot extraction schema: %w", err)
	}
	return &einoopenai.ChatCompletionResponseFormat{
		Type: einoopenai.ChatCompletionResponseFormatTypeJSONSchema,
		JSONSchema: &einoopenai.ChatCompletionResponseFormatJSONSchema{
			Name:        "slot_extraction",
			Description: "This turn's intent and slot extraction.",
			JSONSchema:  &schema,
			Strict:      true,
		},
	}, nil
}

// retryTransport adds the chat endpoint's transport policy to every model
// request: per-attempt headers, a bounded retry for transient failures, and a
// unary timeout that streaming requests are exempt from.
//
// It is a RoundTripper rather than a wrapper around the model because the three
// roles share one connection pool and one policy; wrapping each model would
// duplicate the policy three times and let it drift.
type retryTransport struct {
	base        http.RoundTripper
	headers     map[string]string
	maxRetries  int
	timeout     time.Duration
	baseBackoff time.Duration
}

// RoundTrip implements http.RoundTripper.
func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := drainRequestBody(req)
	if err != nil {
		return nil, err
	}
	streaming := requestIsStreaming(body)

	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	backoff := t.baseBackoff
	if backoff <= 0 {
		backoff = defaultRetryBackoff
	}

	var lastErr error
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			if err := sleepContext(req.Context(), backoff); err != nil {
				return nil, err
			}
			backoff *= 2
			if backoff > maxRetryBackoff {
				backoff = maxRetryBackoff
			}
		}

		attemptReq, cancel, err := t.prepareRequest(req, body, streaming)
		if err != nil {
			return nil, err
		}
		resp, err := base.RoundTrip(attemptReq)
		if err != nil {
			cancel()
			if ctxErr := req.Context().Err(); ctxErr != nil {
				return nil, ctxErr
			}
			lastErr = err
			if attempt >= t.maxRetries {
				return nil, lastErr
			}
			continue
		}

		if retryableStatus(resp.StatusCode) && attempt < t.maxRetries {
			delay := retryAfterDelay(resp)
			drainResponse(resp)
			cancel()
			if ctxErr := req.Context().Err(); ctxErr != nil {
				return nil, ctxErr
			}
			if delay > 0 {
				backoff = delay
			}
			continue
		}

		// A non-streaming body must finish within the attempt timeout, so the
		// context is cancelled when the body is closed or read to EOF. Cancelling
		// when RoundTrip returned would cut the response off before the caller
		// could read it.
		resp.Body = &cancelOnCloseBody{ReadCloser: resp.Body, cancel: cancel}
		return resp, nil
	}
}

// prepareRequest rebuilds one attempt from the buffered body.
func (t *retryTransport) prepareRequest(
	req *http.Request, body []byte, streaming bool,
) (*http.Request, context.CancelFunc, error) {
	ctx := req.Context()
	cancel := context.CancelFunc(func() {})
	if !streaming && t.timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, t.timeout)
	}
	attempt := req.Clone(ctx)
	attempt.Body = nil
	attempt.ContentLength = 0
	if body != nil {
		attempt.Body = io.NopCloser(bytes.NewReader(body))
		attempt.ContentLength = int64(len(body))
	}
	for key, value := range t.headers {
		if strings.EqualFold(key, "Authorization") {
			continue
		}
		attempt.Header.Set(key, value)
	}
	return attempt, cancel, nil
}

// drainRequestBody reads the request body once so every attempt can replay it.
// The original body is closed; the caller owns nothing after RoundTrip starts.
func drainRequestBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	if err := req.Body.Close(); err != nil {
		return nil, err
	}
	return body, nil
}

// requestIsStreaming reports whether the request asked for SSE. Streaming
// responses outlive a unary timeout, so the timeout is skipped for them.
func requestIsStreaming(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var payload struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	return payload.Stream
}

func retryableStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}

// retryAfterDelay honours a server's Retry-After when it gives one, capped so a
// broken upstream cannot stall a turn indefinitely.
func retryAfterDelay(resp *http.Response) time.Duration {
	value := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		delay := time.Duration(seconds) * time.Second
		if delay > maxRetryBackoff {
			delay = maxRetryBackoff
		}
		return delay
	}
	if at, err := http.ParseTime(value); err == nil {
		delay := time.Until(at)
		if delay <= 0 {
			return 0
		}
		if delay > maxRetryBackoff {
			delay = maxRetryBackoff
		}
		return delay
	}
	return 0
}

func drainResponse(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2048))
	_ = resp.Body.Close()
}

type cancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnCloseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if errors.Is(err, io.EOF) {
		b.cancel()
	}
	return n, err
}

func (b *cancelOnCloseBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
