package httpapi

import (
	"context"
	"log/slog"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/hlog"

	"github.com/zed/platepilot/chat-service/internal/httpapi/middleware"
	"github.com/zed/platepilot/chat-service/internal/httperr"
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
	// Search and Evidence are the retrieval service. They are optional so the
	// router can be built before the read path exists; with them nil the /v1
	// routes are simply not registered rather than failing every request with a
	// nil dereference. Evidence is separate because it has its own precondition —
	// a recall without restaurants is refused — and a router that registered the
	// route without the service would answer 404 for a request that is merely
	// unscoped, which reads as a wrong URL rather than a wrong request.
	Search   SearchService
	Evidence EvidenceService
	// Admin is the read-only administration application service. When
	// AdminEnabled is true, the /admin/v1 group is mounted behind the
	// local-host guard; with it false (the default) no administration path
	// exists and requests hit NoRoute.
	Admin        AdminService
	AdminEnabled bool
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

	// Liveness probe.
	h.GET("/healthz", HealthHandler(cfg.Version))

	// Read-only retrieval routes. They are versioned under /v1 because they are
	// a client contract: the response shape carries candidate explanations that
	// a UI reads, so changing them is a breaking change rather than a refactor.
	if cfg.Search != nil {
		v1 := h.Group("/v1")
		v1.POST("/restaurants/search", SearchHandler(cfg.Search))
	}
	if cfg.Evidence != nil {
		v1 := h.Group("/v1")
		// The two evidence routes are ordered so the literal one is registered
		// first. Hertz would match /restaurants/search against
		// /restaurants/{id}/evidence only if the shapes overlapped, and they do
		// not — but registering the specific route before the parameterised one
		// keeps the table readable in the order a caller meets it.
		v1.POST("/restaurants/evidence", EvidenceHandler(cfg.Evidence))
		v1.POST("/restaurants/:id/evidence", RestaurantEvidenceHandler(cfg.Evidence))
	}

	// Administration console. Mounted only when explicitly enabled: the guard
	// alone would not be enough for a surface that most deployments never use,
	// so when the switch is off the routes do not exist at all.
	if cfg.AdminEnabled && cfg.Admin != nil {
		registerAdminRoutes(h, cfg.Admin, cfg.Search, cfg.Evidence)
	}

	h.NoRoute(func(ctx context.Context, c *app.RequestContext) {
		httperr.Write(ctx, c, errs.New(errs.CodeNotFound, "route not found"))
	})

	return h
}
