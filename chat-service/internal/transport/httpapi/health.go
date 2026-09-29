// Package httpapi wires the Hertz HTTP server: routes, health checks, and the
// canonical error responses shared with the middleware chain.
package httpapi

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"github.com/zed/platepilot/shared/domain/requestctx"
)

// HealthResponse is the payload returned by GET /healthz.
type HealthResponse struct {
	Status    string `json:"status"`
	Version   string `json:"version"`
	RequestID string `json:"request_id,omitempty"`
}

// HealthHandler reports liveness. It deliberately has no external dependencies
// so it stays valid while Atlas and models are unreachable.
func HealthHandler(version string) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		rc, _ := requestctx.FromContext(ctx)
		c.JSON(consts.StatusOK, HealthResponse{
			Status:    "ok",
			Version:   version,
			RequestID: rc.RequestID,
		})
	}
}
