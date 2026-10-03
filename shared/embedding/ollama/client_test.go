package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zed1995/platepilot/shared/domain/errs"
)

// vector builds a deterministic vector of the requested width.
func vector(dim int, seed float32) []float32 {
	out := make([]float32, dim)
	for i := range out {
		out[i] = seed + float32(i)*0.001
	}
	return out
}

// roundTripperFunc adapts a function to http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// jsonResponse builds an *http.Response carrying body as an HTTP 200 JSON
// payload, without a server or a socket.
func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

// offlineClient builds a Client whose transport is roundTripperFunc, so the
// request/response contract is exercised without binding a port.
//
// This exists because the sandbox this repository is developed in forbids
// listen() on any address, which made every httptest-based case in this file
// unrunnable -- the whole M2-01 transport contract (retry classification,
// batch ordering, error mapping) was unverifiable in CI. A RoundTripper is the
// seam the http.Client option already had; the tests were simply not using it.
//
// It also tests more precisely than a real server can: a handler can assert on
// the exact request, and a canned response can pin an exact body.
func offlineClient(t *testing.T, dim int, rt http.RoundTripper) *Client {
	t.Helper()
	client, err := NewWithOptions(Options{
		BaseURL:     "http://ollama.invalid",
		Model:       "qwen3-embedding:0.6b",
		Dimensions:  dim,
		MaxRetries:  2,
		BaseBackoff: time.Millisecond,
		HTTPClient:  &http.Client{Transport: rt},
		Sleep:       func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	return client
}

// echoTripper answers /api/embed with one vector per input, like echoServer.
func echoTripper(t *testing.T, dim int, calls *atomic.Int64) http.RoundTripper {
	t.Helper()
	return roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if calls != nil {
			calls.Add(1)
		}
		if r.URL.Path != "/api/embed" {
			t.Errorf("path = %q, want /api/embed", r.URL.Path)
		}
		var req embedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		out := embedResponse{Embeddings: make([][]float32, len(req.Input))}
		for i := range req.Input {
			out.Embeddings[i] = vector(dim, float32(i))
		}
		raw, err := json.Marshal(out)
		if err != nil {
			t.Errorf("encode response: %v", err)
		}
		return jsonResponse(http.StatusOK, string(raw)), nil
	})
}

// newTestClient points a Client at srv and removes retry latency.
func newTestClient(t *testing.T, srv *httptest.Server, dim int) *Client {
	t.Helper()
	client, err := NewWithOptions(Options{
		BaseURL:     srv.URL,
		Model:       "qwen3-embedding:0.6b",
		Dimensions:  dim,
		MaxRetries:  2,
		BaseBackoff: time.Millisecond,
		Sleep:       func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	return client
}

// echoServer answers /api/embed with one vector per input.
func echoServer(t *testing.T, dim int, calls *atomic.Int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			calls.Add(1)
		}
		if r.URL.Path != "/api/embed" {
			t.Errorf("path = %q, want /api/embed", r.URL.Path)
		}
		var req embedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		out := embedResponse{Embeddings: make([][]float32, len(req.Input))}
		for i := range req.Input {
			out.Embeddings[i] = vector(dim, float32(i))
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(out); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNewRejectsUnusableConfig(t *testing.T) {
	cases := []struct {
		name string
		opts Options
	}{
		{"no model", Options{Dimensions: 8}},
		{"zero dimensions", Options{Model: "m", Dimensions: 0}},
		{"negative dimensions", Options{Model: "m", Dimensions: -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewWithOptions(tc.opts); err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func TestNewAppliesDefaults(t *testing.T) {
	client, err := NewWithOptions(Options{Model: "m", Dimensions: 4})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	if client.ModelID() != "m" || client.Dimensions() != 4 {
		t.Errorf("model/dims = %q/%d, want m/4", client.ModelID(), client.Dimensions())
	}
	if client.timeout != DefaultTimeout {
		t.Errorf("timeout = %v, want %v", client.timeout, DefaultTimeout)
	}
}

func TestEmbedDocumentsPreservesOrderAndLength(t *testing.T) {
	const dim = 16
	srv := echoServer(t, dim, nil)
	client := newTestClient(t, srv, dim)

	inputs := []string{"first", "second", "third"}
	got, err := client.EmbedDocuments(context.Background(), inputs)
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	if len(got) != len(inputs) {
		t.Fatalf("got %d vectors, want %d", len(got), len(inputs))
	}
	for i, vec := range got {
		if len(vec) != dim {
			t.Fatalf("vector %d has %d dims, want %d", i, len(vec), dim)
		}
		// echoServer seeds each vector with its input index, so a reordering
		// would show up as a mismatched first component.
		if vec[0] != float32(i) {
			t.Errorf("vector %d starts at %v, want %v", i, vec[0], float32(i))
		}
	}
}

func TestEmbedDocumentsEmptyInputIsNotAnError(t *testing.T) {
	srv := echoServer(t, 8, nil)
	client := newTestClient(t, srv, 8)

	got, err := client.EmbedDocuments(context.Background(), nil)
	if err != nil {
		t.Fatalf("EmbedDocuments(nil): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d vectors, want 0", len(got))
	}
}

func TestEmbedQueryUsesTheSamePath(t *testing.T) {
	const dim = 16
	srv := echoServer(t, dim, nil)
	client := newTestClient(t, srv, dim)

	vec, err := client.EmbedQuery(context.Background(), "quiet italian restaurant")
	if err != nil {
		t.Fatalf("EmbedQuery: %v", err)
	}
	if len(vec) != dim {
		t.Errorf("got %d dims, want %d", len(vec), dim)
	}
}

func TestRetriesServerErrorsThenSucceeds(t *testing.T) {
	const dim = 8
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(embedResponse{Embeddings: [][]float32{vector(dim, 1)}})
	}))
	defer srv.Close()

	client := newTestClient(t, srv, dim)
	if _, err := client.EmbedDocuments(context.Background(), []string{"a"}); err != nil {
		t.Fatalf("EmbedDocuments after retries: %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("calls = %d, want 3 (two failures then success)", got)
	}
}

func TestDoesNotRetryClientErrors(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		// A 404 is what a model that was never pulled looks like.
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := newTestClient(t, srv, 8)
	if _, err := client.EmbedDocuments(context.Background(), []string{"a"}); err == nil {
		t.Fatal("want error, got nil")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("calls = %d, want 1 (404 must not be retried)", got)
	}
}

func TestGivesUpAfterMaxRetries(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client := newTestClient(t, srv, 8) // MaxRetries: 2
	_, err := client.EmbedDocuments(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if errs.CodeOf(err) != errs.CodeProviderUnavailable {
		t.Errorf("code = %q, want %q", errs.CodeOf(err), errs.CodeProviderUnavailable)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("calls = %d, want 3 (initial + 2 retries)", got)
	}
}

func TestMissingModelIsReportedAsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Ollama answers 200 with an error field for an unloaded model.
		_ = json.NewEncoder(w).Encode(embedResponse{Error: `model "qwen3-embedding:0.6b" not found`})
	}))
	defer srv.Close()

	client := newTestClient(t, srv, 8)
	_, err := client.EmbedDocuments(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if errs.CodeOf(err) != errs.CodeProviderUnavailable {
		t.Errorf("code = %q, want %q", errs.CodeOf(err), errs.CodeProviderUnavailable)
	}
	if !strings.Contains(err.Error(), "ollama pull") {
		t.Errorf("error should tell the operator to pull the model: %v", err)
	}
}

func TestDimensionMismatchNamesTheActualWidth(t *testing.T) {
	const wantDim = 16
	srv := echoServer(t, 512, nil) // server returns the wrong width
	client := newTestClient(t, srv, wantDim)

	_, err := client.EmbedDocuments(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if got := errs.CodeOf(err); got != errs.CodeEmbeddingDimensionMismatch {
		t.Errorf("code = %q, want %q", got, errs.CodeEmbeddingDimensionMismatch)
	}
	if !strings.Contains(err.Error(), "512") {
		t.Errorf("error should name the actual dimension: %v", err)
	}
}

// encoding/json cannot represent NaN or Inf: marshalling one fails outright,
// so no conformant server can put one on the wire. The checks in validate
// therefore cannot fire over this transport - they are kept because
// EmbeddingProvider is provider-agnostic and a future adapter may not use
// JSON. This test pins the reason they are unreachable rather than leaving a
// reader to assume they are live.
func TestNaNAndInfCannotCrossTheJSONTransport(t *testing.T) {
	if _, err := json.Marshal(vector(4, 1)); err != nil {
		t.Fatalf("sanity: finite vector should marshal: %v", err)
	}
	for name, value := range map[string]float64{
		"NaN": math.NaN(),
		"Inf": math.Inf(1),
	} {
		if _, err := json.Marshal([]float32{float32(value)}); err == nil {
			t.Errorf("%s marshalled without error; the adapter's %s check may now be reachable", name, name)
		}
	}
}

// A null component is the JSON-legal way to smuggle a non-number past the
// decoder: it silently becomes 0.0 rather than failing. A vector whose every
// component is null therefore arrives as a zero vector, which is exactly the
// condition validate must catch downstream.
func TestNullComponentsDecodeToZero(t *testing.T) {
	var out embedResponse
	if err := json.Unmarshal([]byte(`{"embeddings":[[1,null,3]]}`), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := out.Embeddings[0]
	if len(got) != 3 || got[1] != 0 {
		t.Errorf("decoded = %v, want [1 0 3]", got)
	}

	// validate rejects it on dimension, not on the zero, which is caught by
	// the quality gate rather than here.
	if err := validate(got, 3); err != nil {
		t.Errorf("validate(3 dims) = %v, want nil", err)
	}
	if err := validate([]float32{0, 0, 0}, 3); err != nil {
		t.Errorf("validate(zero vector) = %v, want nil (zero is the quality gate's job)", err)
	}
}

// validate is provider-agnostic defence even though JSON cannot deliver NaN,
// so it is exercised directly.
func TestValidateRejectsNonFiniteAndEmpty(t *testing.T) {
	cases := []struct {
		name string
		vec  []float32
		want errs.Code
	}{
		{"empty", nil, errs.CodeEmbeddingEmpty},
		{"nan", []float32{1, float32(math.NaN()), 3}, errs.CodeEmbeddingNaN},
		{"inf", []float32{1, float32(math.Inf(1)), 3}, errs.CodeEmbeddingInf},
		{"negative inf", []float32{1, float32(math.Inf(-1)), 3}, errs.CodeEmbeddingInf},
		{"wrong width", []float32{1, 2}, errs.CodeEmbeddingDimensionMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := errs.CodeOf(validate(tc.vec, 3)); got != tc.want {
				t.Errorf("code = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEmptyEmbeddingIsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(embedResponse{Embeddings: [][]float32{{}}})
	}))
	defer srv.Close()

	client := newTestClient(t, srv, 8)
	_, err := client.EmbedDocuments(context.Background(), []string{"a"})
	if got := errs.CodeOf(err); got != errs.CodeEmbeddingEmpty {
		t.Errorf("code = %q, want %q", got, errs.CodeEmbeddingEmpty)
	}
}

func TestCountMismatchIsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Two inputs, one vector back.
		_ = json.NewEncoder(w).Encode(embedResponse{Embeddings: [][]float32{vector(8, 1)}})
	}))
	defer srv.Close()

	client := newTestClient(t, srv, 8)
	_, err := client.EmbedDocuments(context.Background(), []string{"a", "b"})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "2 inputs") {
		t.Errorf("error should state both counts: %v", err)
	}
}

func TestCallerCancellationIsNotRetried(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := newTestClient(t, srv, 8)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.EmbedDocuments(ctx, []string{"a"})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if got := errs.CodeOf(err); got != errs.CodeProviderTimeout {
		t.Errorf("code = %q, want %q", got, errs.CodeProviderTimeout)
	}
	if got := calls.Load(); got > 1 {
		t.Errorf("calls = %d, want at most 1 (cancellation must not retry)", got)
	}
}

func TestBackoffGrowsBetweenAttempts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	var waits []time.Duration
	client, err := NewWithOptions(Options{
		BaseURL:     srv.URL,
		Model:       "m",
		Dimensions:  8,
		MaxRetries:  3,
		BaseBackoff: 10 * time.Millisecond,
		Sleep: func(_ context.Context, d time.Duration) error {
			waits = append(waits, d)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	_, _ = client.EmbedDocuments(context.Background(), []string{"a"})

	want := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond}
	if len(waits) != len(want) {
		t.Fatalf("waits = %v, want %v", waits, want)
	}
	for i := range want {
		if waits[i] != want[i] {
			t.Errorf("wait %d = %v, want %v", i, waits[i], want[i])
		}
	}
}

func TestSleepStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("sleepCtx err = %v, want context.Canceled", err)
	}
}

// --- Socket-free equivalents -------------------------------------------------
//
// Each test below mirrors one httptest-based case above, but drives the client
// through a RoundTripper. They exist so the M2-01 transport contract stays
// covered in environments where listen() is not permitted; where a socket is
// available both versions run, and they must agree.

func TestOfflineEmbedDocumentsPreservesOrderAndLength(t *testing.T) {
	client := offlineClient(t, 8, echoTripper(t, 8, nil))
	docs := []string{"a", "b", "c"}
	got, err := client.EmbedDocuments(context.Background(), docs)
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	if len(got) != len(docs) {
		t.Fatalf("got %d vectors, want %d", len(got), len(docs))
	}
	// Order is what the write-back depends on: vector i must belong to doc i.
	for i := range docs {
		if want := vector(8, float32(i)); !slicesEqual(got[i], want) {
			t.Errorf("vector %d = %v, want %v", i, got[i], want)
		}
	}
}

// One request must carry the whole batch. Ollama embeds an array server-side
// in parallel, so a client that loops per document is slower for the same work
// and defeats the batching the batch size exists to control.
func TestOfflineEmbedDocumentsSendsOneRequestPerBatch(t *testing.T) {
	var calls atomic.Int64
	client := offlineClient(t, 4, echoTripper(t, 4, &calls))
	if _, err := client.EmbedDocuments(context.Background(), []string{"a", "b", "c", "d"}); err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("provider calls = %d, want 1 for a four-document batch", got)
	}
}

func TestOfflineRetriesServerErrorsThenSucceeds(t *testing.T) {
	var calls atomic.Int64
	client := offlineClient(t, 4, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return jsonResponse(http.StatusInternalServerError, `{"error":"boom"}`), nil
		}
		return jsonResponse(http.StatusOK, `{"embeddings":[[0,0,0,0]]}`), nil
	}))
	if _, err := client.EmbedDocuments(context.Background(), []string{"a"}); err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("attempts = %d, want 2 (one failure then success)", got)
	}
}

func TestOfflineDoesNotRetryClientErrors(t *testing.T) {
	var calls atomic.Int64
	client := offlineClient(t, 4, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return jsonResponse(http.StatusBadRequest, `{"error":"bad model"}`), nil
	}))
	if _, err := client.EmbedDocuments(context.Background(), []string{"a"}); err == nil {
		t.Fatal("want an error for a 400 response")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1: a 400 is the caller's fault and cannot fix itself", got)
	}
}

func TestOfflineGivesUpAfterMaxRetries(t *testing.T) {
	// The cap is low and the call is made on a context with a deadline, so an
	// unbounded retry loop fails this test in milliseconds instead of hanging
	// until the package timeout -- a hanging test is a test nobody waits for.
	const maxCalls = 16
	var calls atomic.Int64
	client := offlineClient(t, 4, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) > maxCalls {
			t.Errorf("attempts exceeded %d: the retry loop is not bounded", maxCalls)
			return nil, context.Canceled
		}
		return jsonResponse(http.StatusServiceUnavailable, `{"error":"down"}`), nil
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := client.EmbedDocuments(ctx, []string{"a"}); err == nil {
		t.Fatal("want an error when every attempt fails")
	}
	// One initial attempt plus MaxRetries, and no more: retrying forever is how
	// a dead provider turns one bad night into a stuck pipeline.
	if got := calls.Load(); got != 3 {
		t.Errorf("attempts = %d, want 3 (1 initial + 2 retries)", got)
	}
}

func TestOfflineCallerCancellationIsNotRetried(t *testing.T) {
	var calls atomic.Int64
	client := offlineClient(t, 4, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, context.Canceled
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.EmbedDocuments(ctx, []string{"a"}); err == nil {
		t.Fatal("want an error for a cancelled caller")
	}
	if got := calls.Load(); got > 1 {
		t.Errorf("attempts = %d, want at most 1: a cancelled caller must not be retried", got)
	}
}

func TestOfflineCountMismatchIsRejected(t *testing.T) {
	client := offlineClient(t, 4, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"embeddings":[[0,0,0,0]]}`), nil
	}))
	if _, err := client.EmbedDocuments(context.Background(), []string{"a", "b"}); err == nil {
		t.Fatal("want an error when the provider returns fewer vectors than inputs")
	}
}

func TestOfflineDimensionMismatchNamesTheActualWidth(t *testing.T) {
	client := offlineClient(t, 4, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"embeddings":[[0,0]]}`), nil
	}))
	_, err := client.EmbedDocuments(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("want an error for a wrong-width vector")
	}
	if !strings.Contains(err.Error(), "2") {
		t.Errorf("error %q should name the width the provider actually returned", err.Error())
	}
}

// slicesEqual compares two vectors exactly. The adapter has no tolerance
// because it never does arithmetic on the values it transports: it forwards
// what the provider produced, and a rounding difference here would mean the
// JSON codec changed a number, which is worth failing on.
func slicesEqual(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
