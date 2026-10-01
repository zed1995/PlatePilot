package middleware

import (
	"context"
	"log/slog"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/zed/platepilot/shared/observability/logging"
	"github.com/zed/platepilot/shared/requestctx"
)

// Logging attaches a request-scoped logger to the context and records one
// structured access line per request once the handler chain returns.
func Logging(base *slog.Logger) app.HandlerFunc {
	if base == nil {
		base = slog.Default()
	}
	return func(ctx context.Context, c *app.RequestContext) {
		rc, _ := requestctx.FromContext(ctx)
		logger := logging.WithRequest(base, rc)
		ctx = logging.WithLogger(ctx, logger)

		start := time.Now()
		c.Next(ctx)
		logger.Info("http_request",
			slog.String("method", string(c.Method())),
			slog.String("path", string(c.Path())),
			slog.Int("status", c.Response.StatusCode()),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
		)
	}
}
