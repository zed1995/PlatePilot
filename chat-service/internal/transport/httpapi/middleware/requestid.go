// Package middleware provides the common Hertz middleware chain: request ID,
// structured access logging, panic recovery, and CORS.
package middleware

import (
	"context"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/zed/platepilot/shared/domain/requestctx"
	"github.com/zed/platepilot/shared/idgen"
)

// Correlation headers used by the request ID middleware.
const (
	HeaderRequestID = "X-Request-ID"
	HeaderTraceID   = "X-Trace-ID"
)

// RequestID reads or generates a request ID, echoes it back to the client, and
// stores a RequestContext in the standard context for downstream handlers.
func RequestID() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		requestID := strings.TrimSpace(string(c.GetHeader(HeaderRequestID)))
		if requestID == "" {
			requestID = idgen.NewUUID()
		}
		traceID := strings.TrimSpace(string(c.GetHeader(HeaderTraceID)))
		if traceID == "" {
			traceID = requestID
		}

		rc := requestctx.New(requestID, traceID)
		c.Response.Header.Set(HeaderRequestID, requestID)
		c.Response.Header.Set(HeaderTraceID, traceID)

		c.Next(rc.WithContext(ctx))
	}
}
