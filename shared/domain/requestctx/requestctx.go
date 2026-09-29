// Package requestctx carries per-request identity (request ID, trace ID, user
// ID) through context.Context without depending on any HTTP framework.
package requestctx

import "context"

type contextKey struct{}

// RequestContext is the transport-agnostic identity of a single request.
type RequestContext struct {
	RequestID string
	TraceID   string
	UserID    string
}

// New builds a RequestContext. When traceID is empty it falls back to requestID
// so that every request always has a correlation identifier for logs.
func New(requestID, traceID string) RequestContext {
	if traceID == "" {
		traceID = requestID
	}
	return RequestContext{RequestID: requestID, TraceID: traceID}
}

// WithUserID returns a copy carrying the authenticated user ID.
func (rc RequestContext) WithUserID(userID string) RequestContext {
	rc.UserID = userID
	return rc
}

// WithContext stores the RequestContext in ctx.
func (rc RequestContext) WithContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, contextKey{}, rc)
}

// FromContext extracts a RequestContext stored by WithContext.
func FromContext(ctx context.Context) (RequestContext, bool) {
	if ctx == nil {
		return RequestContext{}, false
	}
	rc, ok := ctx.Value(contextKey{}).(RequestContext)
	return rc, ok
}
