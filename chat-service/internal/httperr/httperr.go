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

// Write renders err as the canonical error response. Internal errors are logged
// with their cause but never leak it to the client.
func Write(ctx context.Context, c *app.RequestContext, err error) {
	code := errs.CodeOf(err)
	status := errs.HTTPStatusOf(err)
	message := err.Error()

	if code == errs.CodeInternal {
		logging.FromContext(ctx).Error("request failed", slog.String("error", err.Error()))
		message = "internal server error"
	}

	rc, _ := requestctx.FromContext(ctx)
	c.JSON(status, Response{Error: Body{
		Code:      string(code),
		Message:   message,
		RequestID: rc.RequestID,
	}})
}
