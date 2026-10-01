package middleware

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
)

// CORSConfig configures the CORS middleware. An empty AllowOrigins list disables
// cross-origin access entirely: origins are never wildcarded by default.
type CORSConfig struct {
	AllowOrigins     []string
	AllowMethods     []string
	AllowHeaders     []string
	ExposeHeaders    []string
	AllowCredentials bool
	MaxAge           time.Duration
}

func (c CORSConfig) withDefaults() CORSConfig {
	if len(c.AllowMethods) == 0 {
		c.AllowMethods = []string{
			http.MethodGet, http.MethodPost, http.MethodPut,
			http.MethodPatch, http.MethodDelete, http.MethodOptions,
		}
	}
	if len(c.AllowHeaders) == 0 {
		c.AllowHeaders = []string{"Content-Type", "Authorization", HeaderRequestID, HeaderTraceID}
	}
	if len(c.ExposeHeaders) == 0 {
		c.ExposeHeaders = []string{HeaderRequestID, HeaderTraceID}
	}
	return c
}

// CORS applies configurable cross-origin headers and answers preflight requests.
func CORS(cfg CORSConfig) app.HandlerFunc {
	cfg = cfg.withDefaults()
	return func(ctx context.Context, c *app.RequestContext) {
		origin := strings.TrimSpace(string(c.GetHeader("Origin")))
		if origin == "" || !originAllowed(cfg, origin) {
			c.Next(ctx)
			return
		}

		// Take a pointer: ResponseHeader embeds a no-copy lock and must not be copied.
		header := &c.Response.Header
		if isWildcard(cfg.AllowOrigins) && !cfg.AllowCredentials {
			header.Set("Access-Control-Allow-Origin", "*")
		} else {
			header.Set("Access-Control-Allow-Origin", origin)
			header.Set("Vary", "Origin")
		}
		if cfg.AllowCredentials {
			header.Set("Access-Control-Allow-Credentials", "true")
		}
		if len(cfg.ExposeHeaders) > 0 {
			header.Set("Access-Control-Expose-Headers", strings.Join(cfg.ExposeHeaders, ", "))
		}

		if string(c.Method()) == http.MethodOptions && len(c.GetHeader("Access-Control-Request-Method")) > 0 {
			header.Set("Access-Control-Allow-Methods", strings.Join(cfg.AllowMethods, ", "))
			header.Set("Access-Control-Allow-Headers", strings.Join(cfg.AllowHeaders, ", "))
			if cfg.MaxAge > 0 {
				header.Set("Access-Control-Max-Age", strconv.Itoa(int(cfg.MaxAge.Seconds())))
			}
			c.SetStatusCode(http.StatusNoContent)
			c.Abort()
			return
		}

		c.Next(ctx)
	}
}

func originAllowed(cfg CORSConfig, origin string) bool {
	for _, allowed := range cfg.AllowOrigins {
		if allowed == "*" || strings.EqualFold(strings.TrimSpace(allowed), origin) {
			return true
		}
	}
	return false
}

func isWildcard(origins []string) bool {
	for _, origin := range origins {
		if origin == "*" {
			return true
		}
	}
	return false
}
