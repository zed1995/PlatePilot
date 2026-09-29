package httpapi

import (
	"context"
	"log/slog"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/hlog"

	"github.com/zed/platepilot/chat-service/internal/transport/httpapi/middleware"
	"github.com/zed/platepilot/chat-service/internal/transport/httperr"
	"github.com/zed/platepilot/shared/domain/errs"
)

// Config configures the HTTP engine.
type Config struct {
	Addr             string
	ReadTimeout      time.Duration
	WriteTimeout     time.Duration
	CORSAllowOrigins []string
	Version          string
	Logger           *slog.Logger
}

// NewRouter builds the Hertz engine with the global middleware chain in the
// order request-id -> logging -> recovery -> CORS, then registers routes.
func NewRouter(cfg Config) *server.Hertz {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// Route Hertz's own logs through slog so all output is structured JSON, and
	// install the Validatable-based validator used by BindAndValidate.
	hlog.SetLogger(newHertzLogger(logger))

	h := server.Default(
		server.WithHostPorts(cfg.Addr),
		server.WithReadTimeout(cfg.ReadTimeout),
		server.WithWriteTimeout(cfg.WriteTimeout),
		server.WithCustomValidatorFunc(validatorFunc),
	)

	h.Use(
		middleware.RequestID(),
		middleware.Logging(logger),
		middleware.Recovery(logger),
		middleware.CORS(middleware.CORSConfig{AllowOrigins: cfg.CORSAllowOrigins}),
	)

	// Liveness probe. Business routes are versioned under /v1 and arrive in M3/M5.
	h.GET("/healthz", HealthHandler(cfg.Version))

	h.NoRoute(func(ctx context.Context, c *app.RequestContext) {
		httperr.Write(ctx, c, errs.New(errs.CodeNotFound, "route not found"))
	})

	return h
}
