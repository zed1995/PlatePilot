package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"

	"github.com/zed/platepilot/chat-service/internal/httperr"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testRouter(origins ...string) *server.Hertz {
	return NewRouter(Config{
		Addr:             ":0",
		Version:          "test",
		CORSAllowOrigins: origins,
		Logger:           quietLogger(),
	})
}

func decodeError(t *testing.T, body []byte) httperr.Response {
	t.Helper()
	var resp httperr.Response
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode error response %q: %v", body, err)
	}
	return resp
}

func TestHealthEndpoint(t *testing.T) {
	h := testRouter()
	w := ut.PerformRequest(h.Engine, http.MethodGet, "/healthz", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var body HealthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if body.Status != "ok" || body.Version != "test" {
		t.Fatalf("unexpected health payload: %+v", body)
	}
	if body.RequestID == "" {
		t.Fatal("health response must echo a request id")
	}
	if got := w.Header().Get("X-Request-ID"); got != body.RequestID {
		t.Fatalf("X-Request-ID header %q != body %q", got, body.RequestID)
	}
	if got := w.Header().Get("X-Trace-ID"); got == "" {
		t.Fatal("X-Trace-ID header must be set")
	}
}

func TestRequestIDIsPreservedFromClient(t *testing.T) {
	h := testRouter()
	w := ut.PerformRequest(h.Engine, http.MethodGet, "/healthz", nil,
		ut.Header{Key: "X-Request-ID", Value: "client-supplied-123"},
	)
	if got := w.Header().Get("X-Request-ID"); got != "client-supplied-123" {
		t.Fatalf("X-Request-ID = %q, want the client value", got)
	}
}

func TestUnknownRouteReturnsCanonicalError(t *testing.T) {
	h := testRouter()
	w := ut.PerformRequest(h.Engine, http.MethodGet, "/does-not-exist", nil)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	resp := decodeError(t, w.Body.Bytes())
	if resp.Error.Code != "not_found" {
		t.Fatalf("error code = %q, want not_found", resp.Error.Code)
	}
	if resp.Error.RequestID == "" {
		t.Fatal("error response must carry the request id")
	}
}

func TestRecoveryTurnsPanicIntoInternalError(t *testing.T) {
	h := testRouter()
	h.GET("/panic", func(_ context.Context, _ *app.RequestContext) {
		panic("boom")
	})

	w := ut.PerformRequest(h.Engine, http.MethodGet, "/panic", nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	resp := decodeError(t, w.Body.Bytes())
	if resp.Error.Code != "internal" {
		t.Fatalf("error code = %q, want internal", resp.Error.Code)
	}
	if resp.Error.Message == "boom" {
		t.Fatal("internal panic details must not leak to the client")
	}
}

func TestCORSPreflightIsAnswered(t *testing.T) {
	h := testRouter("http://allowed.example")
	w := ut.PerformRequest(h.Engine, http.MethodOptions, "/healthz", nil,
		ut.Header{Key: "Origin", Value: "http://allowed.example"},
		ut.Header{Key: "Access-Control-Request-Method", Value: "GET"},
	)

	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204 (body %s)", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://allowed.example" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Fatal("preflight must advertise allowed methods")
	}
}

func TestCORSDoesNotReflectDisallowedOrigin(t *testing.T) {
	h := testRouter("http://allowed.example")
	w := ut.PerformRequest(h.Engine, http.MethodGet, "/healthz", nil,
		ut.Header{Key: "Origin", Value: "http://evil.example"},
	)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("disallowed origin must not be reflected, got %q", got)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("non-preflight request should still be served, status = %d", w.Code)
	}
}

func TestCORSDisabledWithoutAllowlist(t *testing.T) {
	h := testRouter()
	w := ut.PerformRequest(h.Engine, http.MethodGet, "/healthz", nil,
		ut.Header{Key: "Origin", Value: "http://localhost:3000"},
	)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("cross-origin access must be disabled by default, got %q", got)
	}
}
