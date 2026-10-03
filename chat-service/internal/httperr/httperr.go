// Package httperr renders PlatePilot's canonical HTTP error envelope.
//
// All handlers and middleware funnel errors through Write so that clients see a
// single, stable shape and a stable error code.
package httperr

import (
	"context"
	"log/slog"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/observability/logging"
	"github.com/zed/platepilot/shared/requestctx"
)

// Body is the inner error object.
type Body struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

// Response is the canonical error envelope.
type Response struct {
	Error Body `json:"error"`
}

// ClientMessage returns the error prose that is safe to show to a caller.
// Internal failures collapse to a generic sentence so implementation details
// never leak; other errors render their Error() text. It is shared by the
// JSON envelope and the in-stream SSE error frame so both channels stay
// consistent.
func ClientMessage(err error) string {
	if errs.CodeOf(err) == errs.CodeInternal {
		return "internal server error"
	}
	return err.Error()
}

// Write renders err as the canonical error response. Internal errors are logged
// with their cause but never leak it to the client.
func Write(ctx context.Context, c *app.RequestContext, err error) {
	code := errs.CodeOf(err)
	status := errs.HTTPStatusOf(err)

	if code == errs.CodeInternal {
		logging.FromContext(ctx).Error("request failed", slog.String("error", err.Error()))
	}

	rc, _ := requestctx.FromContext(ctx)
	c.JSON(status, Response{Error: Body{
		Code:      string(code),
		Message:   ClientMessage(err),
		RequestID: rc.RequestID,
	}})
}
