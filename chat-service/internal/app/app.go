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
	"github.com/zed1995/platepilot/chat-service/internal/agent/slots"
	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
	"github.com/zed1995/platepilot/chat-service/internal/agent/tools"
	"github.com/zed1995/platepilot/chat-service/internal/config"
	"github.com/zed1995/platepilot/chat-service/internal/hitl"
	"github.com/zed1995/platepilot/chat-service/internal/httpapi"
	"github.com/zed1995/platepilot/chat-service/internal/memorywrite"
	"github.com/zed1995/platepilot/chat-service/internal/reservation"
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
	Reservations  store.ReservationRepository
	Admin         store.AdminStore
}

// App owns the assembled runtime.
type App struct {
	cfg       config.Config
	logger    *slog.Logger
	deps      Deps
	search    *retrieval.Service
	extractor *slots.Extractor
	agent     *agent.Runner
	// registry is the tool registry the runner and the confirmation service
	// share. It is kept so a test can ask what the model was offered without
	// reaching through the compiled graph.
	registry *toolreg.Registry
	http     *server.Hertz
	pool     *postgres.Client
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

	// The slot extractor is assembled unconditionally, including in a
	// deployment with no chat provider. That is not a convenience: understanding
	// a sentence is a local parse when there is no model to ask, and the whole
	// conversation path is designed to work from a rule-derived plan rather than
	// to refuse the turn.
	extractor := slots.New(slots.Deps{
		Structured: deps.Structured,
		Model:      cfg.Chat.ExtractModel,
		Config: slots.Config{
			MaxClarifications:    cfg.Agent.MaxClarifications,
			ResolveMinSimilarity: cfg.Agent.ResolveMinSimilarity,
			ResolveAmbiguityGap:  cfg.Agent.ResolveAmbiguityGap,
			Timeout:              cfg.Agent.SlotExtractTimeout,
		},
	})
	app.extractor = extractor

	// The memory write policy is assembled outside the chat block because the
	// memory management routes need it even in a deployment with no model: a
	// user who cannot chat can still list, edit and delete what they asked to
	// be remembered.
	var memoryWrite *memorywrite.Service
	if deps.Memories != nil {
		memoryWrite, err = memorywrite.NewService(deps.Memories)
		if err != nil {
			return nil, fmt.Errorf("build memory write service: %w", err)
		}
	}

	// The agent runner is assembled only when a chat provider was supplied.
	// Without one the HTTP search/evidence API still runs; the conversational
	// turn path simply stays unwired, mirroring the embedding degradation.
	//
	// The confirmation service is built from the same registry, so it is
	// declared out here: the conversational surface below has to know whether
	// the write path exists at all.
	var confirmation *hitl.Service
	var summarize ApprovalSummarizer

	if deps.Chat != nil {
		registry := toolreg.New(cfg.Agent.ToolTimeout)
		app.registry = registry
		if err := registry.Register(tools.SearchRestaurantsEntry(searchService)); err != nil {
			return nil, fmt.Errorf("register search_restaurants tool: %w", err)
		}
		if err := registry.Register(tools.RestaurantEvidenceEntry(searchService)); err != nil {
			return nil, fmt.Errorf("register get_restaurant_evidence tool: %w", err)
		}
		// resolve_restaurant goes to the restaurant repository, not to the
		// retrieval service: turning a name into an id is a lookup, and routing
		// it through retrieval would let a name lookup acquire the soft
		// channels' opinions. It is registered only when a restaurant store
		// exists, because the tool's whole job is to read one.
		if deps.Restaurants != nil {
			entry := tools.ResolveRestaurantEntry(deps.Restaurants, tools.ResolveConfig{
				MinSimilarity: cfg.Agent.ResolveMinSimilarity,
				AmbiguityGap:  cfg.Agent.ResolveAmbiguityGap,
			})
			if err := registry.Register(entry); err != nil {
				return nil, fmt.Errorf("register resolve_restaurant tool: %w", err)
			}
		}
		// save_memory is registered only when there is a memory store to write
		// to and the switch is on. Both conditions matter: without the store the
		// tool could only fail, and the switch exists precisely so an operator
		// can present a model that has no way to remember anything.
		if memoryWrite != nil && cfg.Agent.MemoryWriteEnabled {
			if err := registry.Register(tools.SaveMemoryEntry(memoryWrite)); err != nil {
				return nil, fmt.Errorf("register save_memory tool: %w", err)
			}
			logger.Info("memory write path assembled")
		} else if cfg.Agent.MemoryWriteEnabled {
			logger.Warn("AGENT_MEMORY_WRITE_ENABLED is set but no memory store is configured; " +
				"save_memory is not offered")
		}
		// The mock reservation capability, when switched on, contributes the
		// only two tools that touch inventory. They are registered here rather
		// than always-registered-and-failing so the switch removes the
		// capability from the model's view instead of presenting a tool that
		// can only say no.
		if cfg.Reservation.Enabled {
			if deps.Reservations == nil {
				return nil, fmt.Errorf("RESERVATION_ENABLED is set but no reservation store is configured")
			}
			reservations, err := reservation.NewService(reservation.Config{
				HoldTTL:       cfg.Reservation.HoldTTL,
				PolicyVersion: cfg.Reservation.PolicyVersion,
			}, reservation.Deps{
				Reservations: deps.Reservations,
				Restaurants:  deps.Restaurants,
			})
			if err != nil {
				return nil, fmt.Errorf("build reservation service: %w", err)
			}
			if err := registry.Register(tools.GetAvailabilityEntry(reservations)); err != nil {
				return nil, fmt.Errorf("register get_availability tool: %w", err)
			}
			if err := registry.Register(tools.RequestReservationEntry(reservations)); err != nil {
				return nil, fmt.Errorf("register request_reservation tool: %w", err)
			}
			// The confirmation service needs somewhere to record the pending
			// request. Without a conversation store there is no thread to park
			// it on, so the capability degrades to availability-only rather
			// than offering a booking nobody could approve.
			if deps.Conversations != nil {
				confirmation, err = hitl.NewService(hitl.Config{
					Checkpoints: deps.Conversations,
					Tools:       registry,
				})
				if err != nil {
					return nil, fmt.Errorf("build confirmation service: %w", err)
				}
				summarize = registry.ApprovalSummary
			} else {
				logger.Warn("reservation capability enabled without a conversation store; " +
					"bookings cannot be confirmed on a thread")
			}
			logger.Info("mock reservation capability assembled",
				slog.Duration("hold_ttl", cfg.Reservation.HoldTTL),
				slog.String("policy_version", cfg.Reservation.PolicyVersion),
				slog.Bool("confirmable", confirmation != nil))
		}
		agentDeps := agent.Deps{
			Chat:          deps.Chat,
			ToolCalling:   deps.ToolCalling,
			Registry:      registry,
			Extractor:     extractor,
			Conversations: deps.Conversations,
			Memories:      deps.Memories,
		}
		if deps.Runs != nil {
			agentDeps.Auditor = audit.New(deps.Runs, logger, audit.Options{
				ModelProvider: cfg.Chat.Provider,
				// The run row carries one model name and a turn may use three.
				// It records the planning model: that is the one whose decisions
				// the row's node spans describe. The other two are in the startup
				// summary, and with no per-node overrides all three are equal, so
				// nothing changes for a deployment that does not use them.
				ModelName: cfg.Chat.PlanModel,
			})
		} else {
			logger.Warn("run audit repository not configured; agent turns run without audit trail")
		}
		runner, err := agent.NewRunner(agent.Config{
			MaxToolRounds:     cfg.Agent.MaxToolRounds,
			ModelName:         cfg.Chat.PlanModel,
			AnswerModel:       cfg.Chat.AnswerModel,
			MaxClarifications: cfg.Agent.MaxClarifications,
			AnswerStreaming:   cfg.Agent.AnswerStreaming,
			PhaseEvents:       cfg.Agent.PhaseEvents,
		}, agentDeps)
		if err != nil {
			return nil, fmt.Errorf("build agent runner: %w", err)
		}
		app.agent = runner
		logger.Info("agent runner assembled",
			slog.String("plan_model", cfg.Chat.PlanModel),
			slog.String("answer_model", cfg.Chat.AnswerModel),
			slog.String("extract_model", cfg.Chat.ExtractModel),
			slog.Int("max_tool_rounds", cfg.Agent.MaxToolRounds),
			slog.Int("max_clarifications", cfg.Agent.MaxClarifications),
			slog.Bool("answer_streaming", cfg.Agent.AnswerStreaming),
			slog.Duration("tool_timeout", cfg.Agent.ToolTimeout))
	}

	// The conversational surface needs the conversation store; the runner is
	// optional inside it so thread/memory APIs keep working in a chat-less
	// deployment.
	var chatService httpapi.ChatService
	if deps.Conversations != nil {
		chatService = newChatService(chatServiceDeps{
			Runner:        app.agent,
			Conversations: deps.Conversations,
			Memories:      deps.Memories,
			Runs:          deps.Runs,
			Confirmation:  confirmation,
			Summarize:     summarize,
			MemoryWrite:   memoryWrite,
		})
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
			Reservations:        deps.Reservations,
		})
		logger.Info("admin console assembled",
			slog.Bool("enabled", cfg.Admin.Enabled))
	}
	if cfg.Admin.Enabled {
		warnNonLoopbackBind(logger, cfg.HTTP.Addr)
	}

	// The replay surface needs both stores: the run rows to answer with and the
	// conversation row to answer "does this thread exist" from. Neither alone is
	// enough, so it is assembled only when both are present rather than serving
	// a page that cannot distinguish an empty thread from a missing one.
	var runReadService httpapi.RunService
	if deps.Runs != nil && deps.Conversations != nil {
		runReadService = newRunService(deps.Conversations, deps.Runs)
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
		Interpret:        newSlotInterpreter(extractor),
		Chat:             chatService,
		Runs:             runReadService,
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
		// A service whose schema is behind its binary must not start. The
		// alternative is not "it works anyway": the audit path swallows write
		// errors by design, so a missing column costs the trail silently, and
		// the read path reports it as an upstream outage. Failing here is the
		// one place the fault is cheap to see. The check only reads — applying
		// migrations belongs to the pipeline.
		if err := client.VerifySchema(ctx); err != nil {
			_ = client.Close(context.WithoutCancel(ctx))
			return nil, fmt.Errorf("verify postgres schema: %w", err)
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
		deps.Reservations = postgres.NewReservationRepository(client)
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
