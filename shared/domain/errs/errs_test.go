package errs

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestCodeOfAndHTTPStatusOf(t *testing.T) {
	cases := []struct {
		err        error
		wantCode   Code
		wantStatus int
	}{
		{New(CodeInvalidArgument, "bad"), CodeInvalidArgument, http.StatusBadRequest},
		{New(CodeNotFound, "missing"), CodeNotFound, http.StatusNotFound},
		{New(CodeConflict, "dup"), CodeConflict, http.StatusConflict},
		{New(CodeValidationFailed, "invalid"), CodeValidationFailed, http.StatusUnprocessableEntity},
		{New(CodeProviderTimeout, "slow"), CodeProviderTimeout, http.StatusGatewayTimeout},
		{New(CodeProviderUnavailable, "down"), CodeProviderUnavailable, http.StatusBadGateway},
		{New(CodeInternal, "boom"), CodeInternal, http.StatusInternalServerError},
		{errors.New("plain"), CodeInternal, http.StatusInternalServerError},
		{fmt.Errorf("wrapped: %w", New(CodeNotFound, "missing")), CodeNotFound, http.StatusNotFound},
	}
	for _, tc := range cases {
		if got := CodeOf(tc.err); got != tc.wantCode {
			t.Errorf("CodeOf(%v) = %q, want %q", tc.err, got, tc.wantCode)
		}
		if got := HTTPStatusOf(tc.err); got != tc.wantStatus {
			t.Errorf("HTTPStatusOf(%v) = %d, want %d", tc.err, got, tc.wantStatus)
		}
	}
}

func TestUnknownCodeFallsBackToInternal(t *testing.T) {
	err := New(Code("weird"), "unknown")
	if got := HTTPStatusOf(err); got != http.StatusInternalServerError {
		t.Errorf("unknown code status = %d, want 500", got)
	}
}

func TestErrorsIsMatchesByCode(t *testing.T) {
	wrapped := fmt.Errorf("outer: %w", Newf(CodeNotFound, "restaurant %q not found", "abc"))
	if !errors.Is(wrapped, ErrNotFound) {
		t.Error("wrapped not_found should match ErrNotFound")
	}
	if errors.Is(wrapped, ErrConflict) {
		t.Error("not_found must not match ErrConflict")
	}
}

func TestWrapPreservesCause(t *testing.T) {
	cause := errors.New("connection refused")
	err := Wrap(CodeProviderUnavailable, "chat provider unreachable", cause)
	if !errors.Is(err, cause) {
		t.Error("Wrap must preserve the cause chain")
	}
	if got := CodeOf(err); got != CodeProviderUnavailable {
		t.Errorf("CodeOf = %q", got)
	}
}

func TestErrorSerializesCodeAndMessageOnly(t *testing.T) {
	// The cause is unexported on purpose; guard against accidental leakage.
	err := Wrap(CodeInternal, "failed", errors.New("secret detail"))
	if err.Code != CodeInternal || err.Message != "failed" {
		t.Fatalf("unexpected error fields: %+v", err)
	}
}

// A code without an httpStatusByCode entry silently degrades to 500, which
// turns a client error into a server error. Every embedding code must have an
// explicit mapping.
func TestEveryEmbeddingCodeHasHTTPStatus(t *testing.T) {
	codes := []Code{
		CodeEmbeddingEmpty,
		CodeEmbeddingDimensionMismatch,
		CodeEmbeddingNaN,
		CodeEmbeddingInf,
		CodeEmbeddingZeroVector,
		CodeEmbeddingDuplicate,
		CodeEmbeddingModelMismatch,
	}
	for _, code := range codes {
		if _, ok := httpStatusByCode[code]; !ok {
			t.Errorf("embedding code %q has no HTTP status mapping", code)
		}
		if HTTPStatusOf(New(code, "x")) == http.StatusInternalServerError {
			t.Errorf("embedding code %q maps to 500", code)
		}
	}
}

// The same guard applies to the retrieval codes. They are all client-fixable,
// so none of them may degrade to a 500: a 500 tells an operator the service is
// broken when in fact the request was.
func TestEveryRetrievalCodeHasHTTPStatus(t *testing.T) {
	pairs := []struct {
		code     Code
		sentinel error
	}{
		{CodeRetrievalEmptyQuery, ErrRetrievalEmptyQuery},
		{CodeRetrievalInvalidFilter, ErrRetrievalInvalidFilter},
		{CodeRetrievalNoScope, ErrRetrievalNoScope},
		{CodeRetrievalBudgetExceeded, ErrRetrievalBudgetExceeded},
		{CodeRetrievalQueryTooShort, ErrRetrievalQueryTooShort},
	}
	for _, pair := range pairs {
		if _, ok := httpStatusByCode[pair.code]; !ok {
			t.Errorf("retrieval code %q has no HTTP status mapping", pair.code)
		}
		if status := HTTPStatusOf(New(pair.code, "x")); status != http.StatusBadRequest {
			t.Errorf("retrieval code %q maps to %d, want 400", pair.code, status)
		}
		// A code without a sentinel cannot be matched with errors.Is, which is
		// how callers branch on a retrieval failure.
		if !errors.Is(New(pair.code, "x"), pair.sentinel) {
			t.Errorf("retrieval code %q does not match its sentinel", pair.code)
		}
	}
}
