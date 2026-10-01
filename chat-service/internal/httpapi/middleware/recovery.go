package middleware

import (
	"context"
	"log/slog"
	"runtime/debug"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/zed/platepilot/chat-service/internal/httperr"
	"github.com/zed/platepilot/shared/domain/errs"
)

// Recovery converts a panic in the handler chain into a canonical 500 response
// and logs the stack, keeping the process alive.
func Recovery(logger *slog.Logger) app.HandlerFunc {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, c *app.RequestContext) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error("panic recovered",
					slog.Any("panic", recovered),
					slog.String("stack", string(debug.Stack())),
				)
				httperr.Write(ctx, c, errs.New(errs.CodeInternal, "internal server error"))
				c.Abort()
			}
		}()
		c.Next(ctx)
	}
}
