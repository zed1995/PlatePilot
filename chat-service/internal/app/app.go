// Package app assembles configuration, adapters, and transports into a runnable
// application. It is the only place that knows about concrete implementations.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"github.com/cloudwego/hertz/pkg/app/server"

	"github.com/zed/platepilot/chat-service/internal/config"
	inspectapp "github.com/zed/platepilot/chat-service/internal/inspect"
	"github.com/zed/platepilot/chat-service/internal/retrieval"
	"github.com/zed/platepilot/chat-service/internal/transport/httpapi"
	"github.com/zed/platepilot/shared/adapter/embedding/fake"
	"github.com/zed/platepilot/shared/adapter/embedding/ollama"
	"github.com/zed/platepilot/shared/adapter/repository/postgres"
	sharedcfg "github.com/zed/platepilot/shared/config"
	"github.com/zed/platepilot/shared/port"
)

// Deps holds the ports the application is assembled from.
//
// The repositories are injected rather than constructed here so the read path
// can be assembled against an in-memory double in a test and against the real
// store in production, without the transport knowing which it got. Postgres is
// the exception: it is wired below because nothing else should know how to
// build one.
type Deps struct {
	Chat          port.ChatProvider
	ToolCalling   port.ToolCallingProvider
	Structured    port.StructuredOutputProvider
	Embedding     port.EmbeddingProvider
	Rerank        port.RerankProvider
	Restaurants   port.RestaurantRepository
	Knowledge     port.KnowledgeRepository
	Conversations port.ConversationRepository
	Memories      port.MemoryRepository
	Runs          port.RunRepository
	Inspect        port.InspectStore
}

// App owns the assembled runtime.
type App struct {
	cfg    config.Config
	logger *slog.Logger
	deps   Deps
	search *retrieval.Service
	http   *server.Hertz
	pool   *postgres.Client
}

// New assembles the application from configuration and adapters.
func New(cfg config.Config, logger *slog.Logger, deps Deps, version string) (*App, error) {
	if logger == nil {
		logger = slog.Default()
	}

	app := &App{cfg: cfg, logger: logger, deps: deps}

	// The retrieval service is assembled here because it is the one read-path
	// component with no port of its own: it coordinates the repository, the
	// embedding provider, and the optional reranker.
	searchService, err := retrieval.NewService(retrievalConfig(cfg), retrieval.Deps{
		Restaurants: deps.Restaurants,
		Knowledge:   deps.Knowledge,
		Embedding:   deps.Embedding,
		Rerank:      deps.Rerank,
		Logger:      logger,
	})
	if err != nil {
		return nil, fmt.Errorf("build retrieval service: %w", err)
	}
	app.search = searchService

	// The administration application service is assembled only when a store
	// was supplied; with it absent the router simply leaves /admin/v1
	// unregistered even if the switch is on.
	var adminService httpapi.AdminService
	if deps.Inspect != nil {
		adminService = inspectapp.NewService(deps.Inspect, inspectapp.Config{
			DefaultPageSize:     cfg.Admin.DefaultPageSize,
			MaxPageSize:         cfg.Admin.MaxPageSize,
			MaxRejections:       cfg.Admin.MaxRejections,
			EmbeddingModel:      cfg.Embedding.Model,
			EmbeddingDimensions: cfg.Embedding.Dimensions,
		})
		logger.Info("admin console assembled",
			slog.Bool("enabled", cfg.Admin.Enabled))
	}
	if cfg.Admin.Enabled {
		warnNonLoopbackBind(logger, cfg.HTTP.Addr)
	}

	router := httpapi.NewRouter(httpapi.Config{
		Addr:             cfg.HTTP.Addr,
		ReadTimeout:      cfg.HTTP.ReadTimeout,
		WriteTimeout:     cfg.HTTP.WriteTimeout,
		CORSAllowOrigins: cfg.HTTP.CORSAllowOrigins,
		Version:          version,
		Logger:           logger,
		Search:           searchService,
		Evidence:         httpapi.NewEvidenceService(searchService),
		Admin:            adminService,
		AdminEnabled:     cfg.Admin.Enabled,
	})
	app.http = router
	return app, nil
}

// warnNonLoopbackBind reminds the operator that enabling the console does not
// relax the guard: on a concrete non-loopback bind address the routes exist
// there but non-local peers are still refused.
func warnNonLoopbackBind(logger *slog.Logger, addr string) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return
	}
	if host == "" {
		return // bound to every interface; the guard decides per peer
	}
	if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
		logger.Warn("admin console enabled while HTTP binds a non-loopback address; "+
			"non-local peers remain blocked by the loopback guard",
			slog.String("addr", addr))
	}
}

// retrievalConfig projects the service configuration onto the read path.
func retrievalConfig(cfg config.Config) retrieval.ServiceConfig {
	shared := cfg.Retrieval
	return retrieval.ServiceConfig{
		Weights: retrieval.Weights{
			Structured: shared.Weights.Structured,
			Keyword:    shared.Weights.Keyword,
			Vector:     shared.Weights.Vector,
			Quality:    shared.Weights.Quality,
		},
		Oversample:       shared.Oversample,
		TopK:             shared.TopK,
		EnableStructured: shared.EnableStructured,
		EnableKeyword:    shared.EnableKeyword,
		EnableVector:     shared.EnableVector,
		EmbeddingTimeout: shared.EmbeddingTimeout,
	}
}

// Connect opens the stores the read path needs and returns the assembled app.
//
// It is separate from New so a configuration error and a connection failure are
// reported differently: a bad environment variable should not look like an
// unreachable database.
func Connect(ctx context.Context, cfg config.Config, logger *slog.Logger, version string) (*App, error) {
	deps := Deps{}

	if cfg.Postgres.Enabled() {
		client, err := postgres.Connect(ctx, postgres.ConfigFromPostgres(cfg.Postgres))
		if err != nil {
			return nil, fmt.Errorf("connect to postgres: %w", err)
		}
		// Both read-side repositories come off the same pool. They are separate
		// types because they answer different questions -- hard-filtered
		// restaurants versus scoped documents -- but one connection is enough
		// for both, and a second pool would only add a way to exhaust connections.
		deps.Restaurants = postgres.NewRestaurantSearchRepository(client)
		deps.Knowledge = postgres.NewKnowledgeReadRepository(client)
		deps.Inspect = postgres.NewInspectStore(client)
		logger.Info("postgres read store connected",
			slog.String("database", client.DatabaseName()))
	}

	embedding, err := buildEmbedding(cfg.Embedding)
	if err != nil {
		// An unusable embedding provider is a degradation, not a failure: the
		// search still answers from the structured and keyword channels.
		logger.Warn("embedding provider unavailable; retrieval runs without the vector channel",
			slog.String("error", err.Error()))
	} else if embedding != nil {
		deps.Embedding = embedding
	}

	return New(cfg, logger, deps, version)
}

// buildEmbedding resolves the configured embedding provider.
//
// The fake provider is wired for the same reason the vector channel is skipped
// without one: a search must be runnable before a model has been pulled, and a
// hard failure here would mean no search at all.
func buildEmbedding(cfg sharedcfg.EmbeddingConfig) (port.EmbeddingProvider, error) {
	if !cfg.Enabled() {
		return nil, nil
	}
	switch cfg.Provider {
	case sharedcfg.ProviderFake:
		return fake.New(cfg.Dimensions), nil
	default:
		return ollama.New(cfg)
	}
}

// Run starts the HTTP server and blocks until the server stops.
func (a *App) Run() error {
	a.logger.Info("http server starting", slog.String("addr", a.cfg.HTTP.Addr))
	return a.http.Run()
}

// Shutdown gracefully stops the HTTP server.
func (a *App) Shutdown(ctx context.Context) error {
	return a.http.Shutdown(ctx)
}
