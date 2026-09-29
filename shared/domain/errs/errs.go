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
)
