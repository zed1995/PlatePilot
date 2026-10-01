// Package ollama implements embedding.EmbeddingProvider against a local Ollama
// server using the /api/embed endpoint.
//
// The endpoint takes a batch of inputs and embeds them server-side in
// parallel, which is the reason this adapter never issues one request per
// document: local inference is the bottleneck, and N round-trips would pay the
// HTTP and tokenisation cost N times over for a single unit of work.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/zed/platepilot/shared/config"
	"github.com/zed/platepilot/shared/domain/errs"
)

// Defaults for the tunables that config does not force an operator to set.
const (
	DefaultTimeout    = 30 * time.Second
	DefaultMaxRetries = 3
	DefaultBaseURL    = "http://localhost:11434"
)

// Options tunes one Client. The zero value is valid and means "use the
// defaults", which is what production uses; tests override the endpoints and
// the retry backoff to stay fast and deterministic.
type Options struct {
	// BaseURL is the Ollama server root, e.g. "http://localhost:11434".
	BaseURL string
	// Model is the embedding model id, e.g. "qwen3-embedding:0.6b".
	Model string
	// Dimensions is the expected vector length.
	Dimensions int
	// Timeout bounds a single HTTP attempt. Retries are separate: one attempt
	// timing out does not consume the caller's whole budget.
	Timeout time.Duration
	// MaxRetries is the number of *additional* attempts after the first one.
	MaxRetries int
	// BaseBackoff is the first retry delay; it doubles per attempt.
	BaseBackoff time.Duration
	// HTTPClient overrides the transport, for tests.
	HTTPClient *http.Client
	// Sleep overrides the backoff wait, for tests.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Client is a embedding.EmbeddingProvider backed by Ollama. It is safe for
// concurrent use and is meant to be created once and shared: the embedded
// http.Client keeps connections alive across calls.
type Client struct {
	baseURL     string
	model       string
	dimensions  int
	timeout     time.Duration
	maxRetries  int
	baseBackoff time.Duration
	http        *http.Client
	sleep       func(ctx context.Context, d time.Duration) error
}

// compile-time proof that the adapter satisfies the port.
var _ interface {
	ModelID() string
	Dimensions() int
	EmbedDocuments(ctx context.Context, docs []string) ([][]float32, error)
	EmbedQuery(ctx context.Context, query string) ([]float32, error)
} = (*Client)(nil)

// embedRequest is the /api/embed request body. Input is a slice so one call
// carries a whole batch.
type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// embedResponse is the /api/embed response body.
type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
	// Ollama reports a load failure here rather than in the body when the
	// model was never pulled.
	Error string `json:"error"`
}

// New builds a Client from shared config.
func New(cfg config.EmbeddingConfig) (*Client, error) {
	return NewWithOptions(Options{
		BaseURL:    cfg.BaseURL,
		Model:      cfg.Model,
		Dimensions: cfg.Dimensions,
	})
}

// NewWithOptions builds a Client from explicit options.
func NewWithOptions(opts Options) (*Client, error) {
	baseURL := strings.TrimSpace(opts.BaseURL)
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")

	model := strings.TrimSpace(opts.Model)
	if model == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "ollama: embedding model is required")
	}
	// A non-positive dimension is rejected here rather than at write time,
	// where it would surface as an opaque pgvector serialisation error that
	// points at the database instead of at the configuration.
	if opts.Dimensions <= 0 {
		return nil, errs.Newf(errs.CodeInvalidArgument,
			"ollama: embedding dimensions must be > 0 (got %d)", opts.Dimensions)
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	maxRetries := opts.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	baseBackoff := opts.BaseBackoff
	if baseBackoff <= 0 {
		baseBackoff = time.Second
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}

	return &Client{
		baseURL:     baseURL,
		model:       model,
		dimensions:  opts.Dimensions,
		timeout:     timeout,
		maxRetries:  maxRetries,
		baseBackoff: baseBackoff,
		http:        httpClient,
		sleep:       sleep,
	}, nil
}

// ModelID returns the configured model id.
func (c *Client) ModelID() string { return c.model }

// Dimensions returns the configured vector length.
func (c *Client) Dimensions() int { return c.dimensions }

// EmbedDocuments embeds a batch of texts, preserving input order.
//
// An empty input is a short-circuit rather than an error: the worker pool
// hands over whatever the batch query returned, and an empty page is normal,
// not a failure worth an HTTP round-trip.
func (c *Client) EmbedDocuments(ctx context.Context, docs []string) ([][]float32, error) {
	if len(docs) == 0 {
		return [][]float32{}, nil
	}
	payload, err := json.Marshal(embedRequest{Model: c.model, Input: docs})
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, "ollama: encode embed request", err)
	}

	var out embedResponse
	if err := c.post(ctx, "/api/embed", payload, &out); err != nil {
		return nil, err
	}
	if out.Error != "" {
		// Ollama answers 200 with an error field when the model is missing.
		// That is a permanent configuration fault, so it must not be retried.
		return nil, errs.Newf(errs.CodeProviderUnavailable,
			"ollama: %s (model %q; run `ollama pull %s`)", out.Error, c.model, c.model)
	}
	if len(out.Embeddings) != len(docs) {
		return nil, errs.Newf(errs.CodeProviderUnavailable,
			"ollama: returned %d embeddings for %d inputs", len(out.Embeddings), len(docs))
	}
	for i, vec := range out.Embeddings {
		// validate already returns an *errs.Error carrying the precise code
		// and a message naming the offending component, so the batch index is
		// prepended while the code is preserved.
		if err := validate(vec, c.dimensions); err != nil {
			return nil, errs.Newf(errs.CodeOf(err), "ollama: input %d: %s", i, err)
		}
	}
	return out.Embeddings, nil
}

// EmbedQuery embeds a single query.
//
// It deliberately goes through the same request path as EmbedDocuments: a
// query vector computed by different code than the document vectors is the
// fastest way to make every retrieval score meaningless.
func (c *Client) EmbedQuery(ctx context.Context, query string) ([]float32, error) {
	vecs, err := c.EmbedDocuments(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	if len(vecs) != 1 {
		return nil, errs.Newf(errs.CodeProviderUnavailable,
			"ollama: query embedding returned %d vectors", len(vecs))
	}
	return vecs[0], nil
}

// validate rejects a vector the caller cannot use. The dimension check is the
// first line of defence against a model whose output width disagrees with
// EMBEDDING_DIMENSIONS; without it the mismatch only appears at write time as
// a database error.
func validate(vec []float32, wantDim int) error {
	if len(vec) == 0 {
		return errs.New(errs.CodeEmbeddingEmpty, "ollama: returned an empty embedding")
	}
	if len(vec) != wantDim {
		return errs.Newf(errs.CodeEmbeddingDimensionMismatch,
			"ollama: embedding has %d dimensions, want %d", len(vec), wantDim)
	}
	// NaN and Inf are checked here so the failure is attributed to the model
	// rather than to pgvector, which rejects them at the type level with a
	// message that says nothing about the cause.
	for i, v := range vec {
		switch {
		case math.IsNaN(float64(v)):
			return errs.Newf(errs.CodeEmbeddingNaN, "ollama: embedding component %d is NaN", i)
		case math.IsInf(float64(v), 0):
			return errs.Newf(errs.CodeEmbeddingInf, "ollama: embedding component %d is Inf", i)
		}
	}
	return nil
}

// post sends the payload with bounded retries and returns the decoded body.
//
// Retry policy: only 429, 5xx, and transport errors are retried, with
// exponential backoff. Everything else - notably 404 for a model that was
// never pulled - is permanent, and retrying it only delays the error message
// the operator needs to see.
func (c *Client) post(ctx context.Context, path string, payload []byte, out *embedResponse) error {
	backoff := c.baseBackoff

	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			if err := c.sleep(ctx, backoff); err != nil {
				return errs.Wrap(errs.CodeProviderTimeout, "ollama: cancelled while backing off", err)
			}
			backoff *= 2
		}

		retryable, err := c.attempt(ctx, path, payload, out)
		if err == nil {
			return nil
		}
		if !retryable || attempt >= c.maxRetries {
			return err
		}
	}
}

// attempt performs one HTTP round-trip. The bool reports whether the failure
// is worth another try.
func (c *Client) attempt(ctx context.Context, path string, payload []byte, out *embedResponse) (bool, error) {
	// The per-attempt timeout is derived from the caller's context, so a
	// cancelled caller stops immediately while an attempt timeout stays
	// retryable.
	attemptCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return false, errs.Wrap(errs.CodeInternal, "ollama: build embed request", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		// A caller-initiated cancellation is not a provider fault and must
		// not be retried; an attempt deadline is.
		if ctx.Err() != nil {
			return false, errs.Wrap(errs.CodeProviderTimeout, "ollama: request cancelled by caller", ctx.Err())
		}
		return true, errs.Wrap(errs.CodeProviderUnavailable, "ollama: request failed", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// The body is read with a bound so a misbehaving server cannot make
		// the pipeline buffer without limit while it is trying to report an
		// error.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		switch {
		case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
			return true, errs.Newf(errs.CodeProviderUnavailable,
				"ollama: %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
		default:
			return false, errs.Newf(errs.CodeProviderUnavailable,
				"ollama: %s: %s", resp.Status, strings.TrimSpace(string(snippet)))
		}
	}

	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(out); err != nil {
		return true, errs.Wrap(errs.CodeProviderUnavailable, "ollama: decode embed response", err)
	}
	return false, nil
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
