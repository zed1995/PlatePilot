package middleware

import (
	"context"
	"net"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/zed1995/platepilot/chat-service/internal/httperr"
	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/requestctx"
)

// LoopbackOnly guards the administration console: requests whose peer address
// is on the local host continue, every other request is refused.
//
// Only the actual transport peer address is considered. Forwarded headers are
// deliberately ignored: honoring them would let a remote caller claim a local
// origin. The guard returns 403 (the request reached the process but must not
// reach the routes) with the canonical unauthorized error envelope.
func LoopbackOnly() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		if !isLoopbackRemote(c.RemoteAddr()) {
			rc, _ := requestctx.FromContext(ctx)
			c.JSON(http.StatusForbidden, httperr.Response{
				Error: httperr.Body{
					Code:      string(errs.CodeUnauthorized),
					Message:   "administration console accepts local-host requests only",
					RequestID: rc.RequestID,
				},
			})
			c.Abort()
			return
		}
		c.Next(ctx)
	}
}

// isLoopbackRemote reports whether addr belongs to the local host. A value
// that cannot be parsed as an IP is rejected rather than assumed local.
func isLoopbackRemote(addr net.Addr) bool {
	if addr == nil {
		return false
	}
	host := addr.String()
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
