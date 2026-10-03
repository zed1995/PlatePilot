// Package errs defines PlatePilot's transport-agnostic error model.
//
// Domain, application, and adapter code all return *errs.Error so that every
// layer shares one stable, machine-readable error code. The HTTP layer is the
// only place that maps these codes onto status codes and a response envelope.
package errs

import (
	"errors"
	"fmt"
	"net/http"
)

// Code is a stable, machine-readable error identifier. New codes are appended;
// existing values must never change meaning.
type Code string

const (
	CodeInvalidArgument     Code = "invalid_argument"
	CodeUnauthorized        Code = "unauthorized"
	CodeNotFound            Code = "not_found"
	CodeConflict            Code = "conflict"
	CodeValidationFailed    Code = "validation_failed"
	CodeProviderTimeout     Code = "provider_timeout"
	CodeProviderUnavailable Code = "provider_unavailable"
	CodeInternal            Code = "internal"

	// Embedding codes cover the M2 vector pipeline. Transport-level failures
	// deliberately reuse CodeProviderTimeout and CodeProviderUnavailable above;
	// these exist only for the cases that are specific to embedding output,
	// where the distinction between "the provider was down" and "the provider
	// answered with something unusable" decides whether a retry can help.
	CodeEmbeddingEmpty             Code = "embedding_empty"
	CodeEmbeddingDimensionMismatch Code = "embedding_dimension_mismatch"
	CodeEmbeddingNaN               Code = "embedding_nan"
	CodeEmbeddingInf               Code = "embedding_inf"
	CodeEmbeddingZeroVector        Code = "embedding_zero_vector"
	CodeEmbeddingDuplicate         Code = "embedding_duplicate"
	CodeEmbeddingModelMismatch     Code = "embedding_model_mismatch"

	// Retrieval codes cover the M3 read path. They exist because a client can
	// fix these itself: an empty query and a misspelled borough are both
	// request defects, and reporting them as internal errors would tell an
	// operator to go looking for a fault that is in the request.
	//
	// Degradations that the service handles by design — a channel that failed
	// but was survivable — deliberately have no code: they are recorded in the
	// retrieval trace, not raised as errors. An error code for "a channel was
	// slow" would train callers to retry a search that already succeeded.
	CodeRetrievalEmptyQuery     Code = "retrieval_empty_query"
	CodeRetrievalInvalidFilter  Code = "retrieval_invalid_filter"
	CodeRetrievalNoScope        Code = "retrieval_no_scope"
	CodeRetrievalBudgetExceeded Code = "retrieval_budget_exceeded"
	CodeRetrievalQueryTooShort  Code = "retrieval_query_too_short"

	// Agent codes cover the M4 reasoning loop. A citation violation means the
	// model cited evidence that was not part of the turn's evidence set even
	// after one corrective retry; the answer is withheld rather than shipped
	// with an unverifiable reference. It is 422 rather than 500 because the
	// request itself was answerable — the generated content just failed a
	// hard output contract and must not be retried blindly by callers.
	CodeAgentCitationViolation Code = "agent_citation_violation"

	// M5 confirmation and memory codes.
	//
	// CodeAgentNoPendingAction is a 400 rather than a 404: the thread exists
	// and the route is correct, but this thread has nothing waiting to be
	// confirmed. A confirm with no pending action is a client sequencing
	// mistake, and answering it as "not found" would send a caller looking for
	// a missing resource instead of a missing prerequisite.
	CodeAgentNoPendingAction Code = "agent_no_pending_action"

	// CodeMemoryWriteNotRequested guards the product rule that a memory is only
	// written when the user explicitly asked for it. The tool carries a
	// quoted_user_text argument that must be a substring of this turn's user
	// message; when it is not, the model invented the condition, and storing it
	// would pollute every later turn. The check is an input-validation rule
	// rather than a prompt instruction precisely because a prompt cannot fail.
	CodeMemoryWriteNotRequested Code = "memory_write_not_requested"

	// CodeReservationUnavailable covers a slot that cannot hold the party:
	// capacity is exhausted, or a hold expired before it was confirmed. It is a
	// 409 because the request is well-formed and would have succeeded earlier —
	// the state it depended on changed, which is exactly what conflict means.
	CodeReservationUnavailable Code = "reservation_unavailable"
)

var httpStatusByCode = map[Code]int{
	CodeInvalidArgument:     http.StatusBadRequest,
	CodeUnauthorized:        http.StatusUnauthorized,
	CodeNotFound:            http.StatusNotFound,
	CodeConflict:            http.StatusConflict,
	CodeValidationFailed:    http.StatusUnprocessableEntity,
	CodeProviderTimeout:     http.StatusGatewayTimeout,
	CodeProviderUnavailable: http.StatusBadGateway,
	CodeInternal:            http.StatusInternalServerError,

	CodeEmbeddingEmpty:             http.StatusBadRequest,
	CodeEmbeddingDimensionMismatch: http.StatusBadRequest,
	CodeEmbeddingNaN:               http.StatusUnprocessableEntity,
	CodeEmbeddingInf:               http.StatusUnprocessableEntity,
	CodeEmbeddingZeroVector:        http.StatusUnprocessableEntity,
	CodeEmbeddingDuplicate:         http.StatusUnprocessableEntity,
	CodeEmbeddingModelMismatch:     http.StatusConflict,

	CodeRetrievalEmptyQuery:     http.StatusBadRequest,
	CodeRetrievalInvalidFilter:  http.StatusBadRequest,
	CodeRetrievalNoScope:        http.StatusBadRequest,
	CodeRetrievalBudgetExceeded: http.StatusBadRequest,
	CodeRetrievalQueryTooShort:  http.StatusBadRequest,

	CodeAgentCitationViolation: http.StatusUnprocessableEntity,

	CodeAgentNoPendingAction:    http.StatusBadRequest,
	CodeMemoryWriteNotRequested: http.StatusBadRequest,
	CodeReservationUnavailable:  http.StatusConflict,
}

// Error is the canonical error type used across PlatePilot.
//
// Code and Message are safe to expose to callers; the wrapped cause is kept for
// logs only and is never serialized (see the struct tags).
type Error struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
	cause   error
}

// New builds an Error with the given code and message.
func New(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// Newf builds an Error with a formatted message.
func Newf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap builds an Error that keeps cause for inspection via errors.Unwrap.
func Wrap(code Code, message string, cause error) *Error {
	return &Error{Code: code, Message: message, cause: cause}
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap returns the wrapped cause, if any.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Is reports whether target shares the same error code. This lets callers write
// errors.Is(err, errs.ErrNotFound) regardless of the message or wrapping.
func (e *Error) Is(target error) bool {
	if e == nil {
		return false
	}
	var t *Error
	if !errors.As(target, &t) || t == nil {
		return false
	}
	return e.Code == t.Code
}

// HTTPStatus returns the status code mapped from this error.
func (e *Error) HTTPStatus() int {
	return HTTPStatusOf(e)
}

// As extracts an *Error from err.
func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) && e != nil {
		return e, true
	}
	return nil, false
}

// CodeOf returns the code carried by err, defaulting to CodeInternal.
func CodeOf(err error) Code {
	if e, ok := As(err); ok {
		return e.Code
	}
	return CodeInternal
}

// HTTPStatusOf maps an error onto an HTTP status code. Unclassified errors map
// to 500 so that unknown failures never look like client errors.
func HTTPStatusOf(err error) int {
	if err == nil {
		return http.StatusOK
	}
	if e, ok := As(err); ok {
		if status, found := httpStatusByCode[e.Code]; found {
			return status
		}
	}
	return http.StatusInternalServerError
}

// Sentinels for errors.Is comparisons. Two *Error values match when their codes
// are equal, so wrapping a sentinel preserves IS semantics.
var (
	ErrInvalidArgument     = New(CodeInvalidArgument, "invalid argument")
	ErrUnauthorized        = New(CodeUnauthorized, "unauthorized")
	ErrNotFound            = New(CodeNotFound, "resource not found")
	ErrConflict            = New(CodeConflict, "conflict")
	ErrValidationFailed    = New(CodeValidationFailed, "validation failed")
	ErrProviderTimeout     = New(CodeProviderTimeout, "provider timeout")
	ErrProviderUnavailable = New(CodeProviderUnavailable, "provider unavailable")
	ErrInternal            = New(CodeInternal, "internal error")

	ErrEmbeddingEmpty             = New(CodeEmbeddingEmpty, "embedding is empty")
	ErrEmbeddingDimensionMismatch = New(CodeEmbeddingDimensionMismatch, "embedding dimension mismatch")
	ErrEmbeddingNaN               = New(CodeEmbeddingNaN, "embedding contains NaN")
	ErrEmbeddingInf               = New(CodeEmbeddingInf, "embedding contains Inf")
	ErrEmbeddingZeroVector        = New(CodeEmbeddingZeroVector, "embedding is a zero vector")
	ErrEmbeddingDuplicate         = New(CodeEmbeddingDuplicate, "embedding duplicates an earlier vector")
	ErrEmbeddingModelMismatch     = New(CodeEmbeddingModelMismatch, "embedding model does not match stored documents")

	ErrRetrievalEmptyQuery     = New(CodeRetrievalEmptyQuery, "retrieval request carries neither text nor filters")
	ErrRetrievalInvalidFilter  = New(CodeRetrievalInvalidFilter, "retrieval filter is not valid")
	ErrRetrievalNoScope        = New(CodeRetrievalNoScope, "retrieval requires a scope that was not provided")
	ErrRetrievalBudgetExceeded = New(CodeRetrievalBudgetExceeded, "retrieval budget is too small to hold any result")
	ErrRetrievalQueryTooShort  = New(CodeRetrievalQueryTooShort, "retrieval text is too short to match")

	ErrAgentCitationViolation = New(CodeAgentCitationViolation, "answer cites evidence that is not part of this turn")

	ErrAgentNoPendingAction    = New(CodeAgentNoPendingAction, "thread has no pending action to confirm")
	ErrMemoryWriteNotRequested = New(CodeMemoryWriteNotRequested, "memory write was not requested by the user")
	ErrReservationUnavailable  = New(CodeReservationUnavailable, "reservation slot is not available")
)
