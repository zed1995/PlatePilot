// Package app assembles configuration, adapters, and transports into a runnable
// application. It is the only place that knows about concrete implementations.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"github.com/cloudwego/hertz/pkg/app/server"

	adminapp "github.com/zed1995/platepilot/chat-service/internal/admin"
	"github.com/zed1995/platepilot/chat-service/internal/agent"
	"github.com/zed1995/platepilot/chat-service/internal/agent/audit"
	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
	"github.com/zed1995/platepilot/chat-service/internal/agent/tools"
	"github.com/zed1995/platepilot/chat-service/internal/config"
	"github.com/zed1995/platepilot/chat-service/internal/httpapi"
	"github.com/zed1995/platepilot/chat-service/internal/retrieval"
	"github.com/zed1995/platepilot/shared/chat"
	"github.com/zed1995/platepilot/shared/chat/openai"
	sharedcfg "github.com/zed1995/platepilot/shared/config"
	"github.com/zed1995/platepilot/shared/embedding"
	"github.com/zed1995/platepilot/shared/embedding/fake"
	"github.com/zed1995/platepilot/shared/embedding/ollama"
	"github.com/zed1995/platepilot/shared/rerank"
	"github.com/zed1995/platepilot/shared/store"
	"github.com/zed1995/platepilot/shared/store/postgres"
)

// Deps holds the ports the application is assembled from.
//
// The repositories are injected rather than constructed here so the read path
// can be assembled against an in-memory double in a test and against the real
// store in production, without the transport knowing which it got. Postgres is
// the exception: it is wired below because nothing else should know how to
// build one.
type Deps struct {
	Chat          chat.ChatProvider
	ToolCalling   chat.ToolCallingProvider
	Structured    chat.StructuredOutputProvider
	Embedding     embedding.EmbeddingProvider
	Rerank        rerank.RerankProvider
	Restaurants   store.RestaurantRepository
	Knowledge     store.KnowledgeRepository
	Conversations store.ConversationRepository
	Memories      store.MemoryRepository
	Runs          store.RunRepository
	Admin         store.AdminStore
}

// App owns the assembled runtime.
type App struct {
	cfg    config.Config
	logger *slog.Logger
	deps   Deps
	search *retrieval.Service
	agent  *agent.Runner
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

	// The agent runner is assembled only when a chat provider was supplied.
	// Without one the HTTP search/evidence API still runs; the conversational
	// turn path simply stays unwired, mirroring the embedding degradation.
	if deps.Chat != nil {
		registry := toolreg.New(cfg.Agent.ToolTimeout)
		if err := registry.Register(tools.SearchRestaurantsEntry(searchService)); err != nil {
			return nil, fmt.Errorf("register search_restaurants tool: %w", err)
		}
		if err := registry.Register(tools.RestaurantEvidenceEntry(searchService)); err != nil {
			return nil, fmt.Errorf("register get_restaurant_evidence tool: %w", err)
		}
		agentDeps := agent.Deps{
			Chat:          deps.Chat,
			ToolCalling:   deps.ToolCalling,
			Registry:      registry,
			Conversations: deps.Conversations,
			Memories:      deps.Memories,
		}
		if deps.Runs != nil {
			agentDeps.Auditor = audit.New(deps.Runs, logger, audit.Options{
				ModelProvider: cfg.Chat.Provider,
				ModelName:     cfg.Chat.Model,
			})
		} else {
			logger.Warn("run audit repository not configured; agent turns run without audit trail")
		}
		runner, err := agent.NewRunner(agent.Config{
			MaxToolRounds: cfg.Agent.MaxToolRounds,
			ModelName:     cfg.Chat.Model,
		}, agentDeps)
		if err != nil {
			return nil, fmt.Errorf("build agent runner: %w", err)
		}
		app.agent = runner
		logger.Info("agent runner assembled",
			slog.String("model", cfg.Chat.Model),
			slog.Int("max_tool_rounds", cfg.Agent.MaxToolRounds),
			slog.Duration("tool_timeout", cfg.Agent.ToolTimeout))
	}

	// The conversational surface needs the conversation store; the runner is
	// optional inside it so thread/memory APIs keep working in a chat-less
	// deployment.
	var chatService httpapi.ChatService
	if deps.Conversations != nil {
		chatService = newChatService(app.agent, deps.Conversations, deps.Memories)
	}

	// The administration application service is assembled only when a store
	// was supplied; with it absent the router simply leaves /admin/v1
	// unregistered even if the switch is on.
	var adminService httpapi.AdminService
	if deps.Admin != nil {
		adminService = adminapp.NewService(deps.Admin, adminapp.Config{
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
		Chat:             chatService,
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
		deps.Admin = postgres.NewAdminStore(client)
		deps.Runs = postgres.NewRunRepository(client)
		deps.Conversations = postgres.NewConversationRepository(client)
		deps.Memories = postgres.NewMemoryRepository(client)
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

	if cfg.Chat.Enabled() {
		// Unlike the embedding side, a configured chat provider that fails to
		// build is a startup failure: from M4 on the chat model is the primary
		// capability, and silently serving without it would surface later as
		// confusing runtime errors instead of a clear configuration fault.
		chatClient, err := buildChatClient(cfg.Chat)
		if err != nil {
			return nil, fmt.Errorf("build chat provider: %w", err)
		}
		// One client satisfies all three chat ports; the agent decides which
		// capability a turn needs.
		deps.Chat = chatClient
		deps.ToolCalling = chatClient
		deps.Structured = chatClient
		logger.Info("chat provider assembled",
			slog.String("provider", cfg.Chat.Provider),
			slog.String("model", cfg.Chat.Model),
			slog.Bool("tools", cfg.Chat.SupportsTools),
			slog.Bool("parallel_tools", cfg.Chat.SupportsParallelTools),
			slog.Bool("json_schema", cfg.Chat.SupportsJSONSchema))
	}

	return New(cfg, logger, deps, version)
}

// providerOpenAICompatible is the only chat adapter shipped in M4. The value
// is part of the environment contract documented in .env.example.
const providerOpenAICompatible = "openai_compatible"

// buildChatClient maps chat configuration onto the OpenAI-compatible adapter.
func buildChatClient(cfg config.ChatConfig) (*openai.Client, error) {
	if cfg.Provider != providerOpenAICompatible {
		return nil, fmt.Errorf("unsupported CHAT_PROVIDER %q (supported value: %q)",
			cfg.Provider, providerOpenAICompatible)
	}
	client, err := openai.New(openai.Options{
		BaseURL:      cfg.BaseURL,
		APIKey:       cfg.APIKey,
		Model:        cfg.Model,
		ExtraHeaders: cfg.ExtraHeaders,
		Capabilities: openai.Capabilities{
			Tools:         cfg.SupportsTools,
			ParallelTools: cfg.SupportsParallelTools,
			JSONSchema:    cfg.SupportsJSONSchema,
			Streaming:     true,
			ContextTokens: cfg.ContextTokens,
		},
		Timeout:    cfg.Timeout,
		MaxRetries: cfg.MaxRetries,
	})
	if err != nil {
		return nil, err
	}
	return client, nil
}

// buildEmbedding resolves the configured embedding provider.
//
// The fake provider is wired for the same reason the vector channel is skipped
// without one: a search must be runnable before a model has been pulled, and a
// hard failure here would mean no search at all.
func buildEmbedding(cfg sharedcfg.EmbeddingConfig) (embedding.EmbeddingProvider, error) {
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
