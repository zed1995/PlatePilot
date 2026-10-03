package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// roundTripperFunc adapts a function to http.RoundTripper, the seam used by
// the ollama adapter tests as well: this repository's sandbox forbids
// listen(), so the request/response contract is exercised without a socket.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

func streamResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
}

func noSleep(context.Context, time.Duration) error { return nil }

// newOfflineClient builds a Client pointed at an invalid host with the given
// transport. Capabilities default to the shipped conservative set with
// JSONSchema flipped on so both structured modes are testable.
func newOfflineClient(t *testing.T, rt http.RoundTripper) *Client {
	t.Helper()
	caps := DefaultCapabilities()
	caps.JSONSchema = true
	c, err := New(Options{
		BaseURL:      "https://openrouter.invalid/api/v1",
		APIKey:       "test-key",
		Model:        "vendor/test-model",
		Capabilities: caps,
		Timeout:      5 * time.Second,
		MaxRetries:   2,
		BaseBackoff:  time.Millisecond,
		HTTPClient:   &http.Client{Transport: rt},
		Sleep:        noSleep,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// decodeRequest reads and parses the request body inside a tripper.
func decodeRequest(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode request body %s: %v", string(raw), err)
	}
	return body
}

// countingTransport records how many attempts were made.
func countingTransport(calls *atomic.Int64, rt roundTripperFunc) roundTripperFunc {
	return func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return rt(r)
	}
}
