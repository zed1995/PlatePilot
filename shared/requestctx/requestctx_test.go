package requestctx

import (
	"context"
	"testing"
)

func TestContextRoundTrip(t *testing.T) {
	rc := New("req-1", "trace-1").WithUserID("user-1")
	ctx := rc.WithContext(context.Background())

	got, ok := FromContext(ctx)
	if !ok {
		t.Fatal("expected a RequestContext in the context")
	}
	if got != rc {
		t.Fatalf("round trip mismatch: got %+v want %+v", got, rc)
	}
}

func TestTraceIDFallsBackToRequestID(t *testing.T) {
	rc := New("req-only", "")
	if rc.TraceID != "req-only" {
		t.Fatalf("trace id = %q, want fallback to request id", rc.TraceID)
	}
}

func TestFromContextMissing(t *testing.T) {
	if _, ok := FromContext(context.Background()); ok {
		t.Fatal("empty context should not report a RequestContext")
	}
	if _, ok := FromContext(nil); ok {
		t.Fatal("nil context should not panic or report a RequestContext")
	}
}
