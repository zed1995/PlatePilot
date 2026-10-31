package llm

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

	"github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/errs"
)

// Defaults for the tunables an operator is not forced to set.
const (
	DefaultTimeout    = 60 * time.Second
	DefaultMaxRetries = 2
	DefaultBackoff    = time.Second
	userAgent         = "platepilot-llm"
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
	// Timeout bounds one HTTP attempt.
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

// Client is a Completer backed by an OpenAI-compatible endpoint. It is safe
// for concurrent use and is created once and shared.
type Client struct {
	baseURL     string
	apiKey      string
	model       string
	timeout     time.Duration
	maxRetries  int
	baseBackoff time.Duration
	httpClient  *http.Client
	sleep       func(ctx context.Context, d time.Duration) error
}

// Compile-time proof that one Client satisfies the completion port.
var _ Completer = (*Client)(nil)

// New builds a Client from explicit options.
func New(opts Options) (*Client, error) {
	baseURL := strings.TrimSpace(opts.BaseURL)
	if baseURL == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "llm: base url is required")
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		return nil, errs.Newf(errs.CodeInvalidArgument,
			"llm: base url must start with http:// or https:// (got %q)", opts.BaseURL)
	}
	if strings.TrimSpace(opts.APIKey) == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "llm: api key is required")
	}
	if strings.TrimSpace(opts.Model) == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "llm: model is required")
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	maxRetries := opts.MaxRetries
	if maxRetries < 0 {
		return nil, errs.Newf(errs.CodeInvalidArgument,
			"llm: max retries must be >= 0 (got %d)", opts.MaxRetries)
	}
	baseBackoff := opts.BaseBackoff
	if baseBackoff <= 0 {
		baseBackoff = DefaultBackoff
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	sleepFn := opts.Sleep
	if sleepFn == nil {
		sleepFn = sleepCtx
	}

	return &Client{
		baseURL:     baseURL,
		apiKey:      strings.TrimSpace(opts.APIKey),
		model:       strings.TrimSpace(opts.Model),
		timeout:     timeout,
		maxRetries:  maxRetries,
		baseBackoff: baseBackoff,
		httpClient:  httpClient,
		sleep:       sleepFn,
	}, nil
}

// ModelID returns the configured default model id (for audit records).
func (c *Client) ModelID() string { return c.model }

// Complete performs one non-streaming completion.
func (c *Client) Complete(ctx context.Context, req chat.ChatRequest) (chat.ChatResponse, error) {
	resp, err := c.unary(ctx, req)
	if err != nil {
		return chat.ChatResponse{}, err
	}
	return c.toChatResponse(resp), nil
}

// unary performs the bounded-retry HTTP loop for a completion.
func (c *Client) unary(ctx context.Context, req chat.ChatRequest) (completionResponse, error) {
	payload, err := c.buildPayload(req)
	if err != nil {
		return completionResponse{}, err
	}

	backoff := c.baseBackoff
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			if err := c.sleep(ctx, backoff); err != nil {
				return completionResponse{}, errs.Wrap(errs.CodeProviderTimeout,
					"llm: cancelled while backing off", err)
			}
			backoff *= 2
		}

		retryable, wait, resp, err := c.attempt(ctx, payload)
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

// attempt performs one HTTP round-trip. The bool reports whether the failure
// is worth retrying; the duration carries a Retry-After hint.
func (c *Client) attempt(ctx context.Context, payload []byte) (bool, time.Duration, completionResponse, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	httpReq, err := c.newRequest(attemptCtx, payload)
	if err != nil {
		return false, 0, completionResponse{}, err
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return false, 0, completionResponse{},
				errs.Wrap(errs.CodeProviderTimeout, "llm: request cancelled by caller", ctx.Err())
		}
		if attemptCtx.Err() != nil {
			return true, 0, completionResponse{},
				errs.Wrap(errs.CodeProviderTimeout, "llm: request timed out", err)
		}
		return true, 0, completionResponse{},
			errs.Wrap(errs.CodeProviderUnavailable, "llm: request failed", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		retryable, err := c.classifyError(resp)
		return retryable, retryAfter(resp), completionResponse{}, err
	}

	var out completionResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&out); err != nil {
		return true, 0, completionResponse{},
			errs.Wrap(errs.CodeProviderUnavailable, "llm: decode completion response", err)
	}
	// A gateway can accept the request with 200 and then fail in the body
	// instead of the status line. Surfacing that message is the difference
	// between an operator reading "the upstream is overloaded" and an
	// unactionable "returned no choices".
	if out.Error != nil && strings.TrimSpace(out.Error.Message) != "" {
		status := codeAsInt(out.Error.Code)
		return status == 0 || retryableStatus(status), 0, completionResponse{},
			errs.Newf(errs.CodeProviderUnavailable,
				"llm: chat request failed (model %q): %s", c.model,
				strings.TrimSpace(out.Error.Message))
	}
	if len(out.Choices) == 0 {
		// An empty 200 is a transient drop on overloaded routes, not a request
		// the provider rejected: retrying is the recovery, so say so.
		return true, 0, completionResponse{}, errs.Newf(errs.CodeProviderUnavailable,
			"llm: model %q returned no choices", c.model)
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
	case retryableStatus(resp.StatusCode):
		return true, errs.Newf(errs.CodeProviderUnavailable,
			"llm: chat request failed (model %q): %s", c.model, detail)
	case resp.StatusCode == http.StatusBadRequest:
		return false, errs.Newf(errs.CodeInvalidArgument,
			"llm: chat request rejected (model %q): %s", c.model, detail)
	case resp.StatusCode == http.StatusUnauthorized,
		resp.StatusCode == http.StatusForbidden:
		return false, errs.Newf(errs.CodeUnauthorized,
			"llm: chat request not authorized (model %q): %s", c.model, detail)
	case resp.StatusCode == http.StatusPaymentRequired,
		resp.StatusCode == http.StatusNotFound:
		// 402: out of credit. 404 on OpenRouter means the model id has no
		// endpoint. Both are configuration faults the operator must fix.
		return false, errs.Newf(errs.CodeProviderUnavailable,
			"llm: chat model unavailable (model %q): %s", c.model, detail)
	default:
		return false, errs.Newf(errs.CodeProviderUnavailable,
			"llm: chat request failed (model %q): %s", c.model, detail)
	}
}

// retryableStatus reports whether a status — a real HTTP one or the status an
// upstream failure carries inside a 200 body — describes a transient problem
// that another attempt can plausibly clear.
func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests ||
		status == http.StatusRequestTimeout ||
		status >= 500
}

// codeAsInt reads the OpenAI-family error code, which arrives as a number from
// the OpenRouter gateway and as a string from providers that follow OpenAI
// literally. Zero means the code was absent or unparseable.
func codeAsInt(code any) int {
	switch v := code.(type) {
	case float64:
		return int(v)
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
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
func (c *Client) buildPayload(req chat.ChatRequest) (json.RawMessage, error) {
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = c.model
	}
	body := completionRequest{
		Model:       model,
		Messages:    toWireMessages(req.Messages),
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, "llm: encode request", err)
	}
	return raw, nil
}

// newRequest builds the POST request with auth and configured headers.
func (c *Client) newRequest(ctx context.Context, payload []byte) (*http.Request, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+chatCompletionsPath, bytes.NewReader(payload))
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, "llm: build request", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", userAgent)
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	return httpReq, nil
}

func (c *Client) toChatResponse(in completionResponse) chat.ChatResponse {
	return chat.ChatResponse{
		Message:      toDomainMessage(in.Choices[0].Message),
		FinishReason: mapFinishReason(in.Choices[0].FinishReason),
		Usage:        toUsage(in.Usage),
		Model:        in.Model,
	}
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
