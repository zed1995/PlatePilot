// Package app assembles configuration, adapters, and transports into a runnable
// application. It is the only place that knows about concrete implementations.
package app

import (
	"context"
	"log/slog"

	"github.com/cloudwego/hertz/pkg/app/server"

	"github.com/zed/platepilot/chat-service/internal/config"
	"github.com/zed/platepilot/chat-service/internal/transport/httpapi"
	"github.com/zed/platepilot/shared/port"
)

// Deps holds the ports the application is assembled from. M0 leaves them nil:
// the Mongo repository arrives in M1, the Ollama embedding adapter in M2, and
// the OpenAI-compatible chat adapter in M4.
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
}

// App owns the assembled runtime.
type App struct {
	cfg    config.Config
	logger *slog.Logger
	deps   Deps
	http   *server.Hertz
}

// New assembles the application from configuration and adapters.
func New(cfg config.Config, logger *slog.Logger, deps Deps, version string) (*App, error) {
	if logger == nil {
		logger = slog.Default()
	}
	router := httpapi.NewRouter(httpapi.Config{
		Addr:             cfg.HTTP.Addr,
		ReadTimeout:      cfg.HTTP.ReadTimeout,
		WriteTimeout:     cfg.HTTP.WriteTimeout,
		CORSAllowOrigins: cfg.HTTP.CORSAllowOrigins,
		Version:          version,
		Logger:           logger,
	})
	return &App{cfg: cfg, logger: logger, deps: deps, http: router}, nil
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
