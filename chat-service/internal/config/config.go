// Package config loads and validates chat-service configuration.
//
// Configuration comes from environment variables, with a local .env layered
// underneath for development. Secrets are read only from the environment and are
// redacted before they are ever logged.
package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	sharedcfg "github.com/zed1995/platepilot/shared/config"
)

// DotEnvFile is the optional local development configuration file.
const DotEnvFile = ".env"

// Config is the fully resolved chat-service configuration.
type Config struct {
	App         sharedcfg.AppConfig
	HTTP        HTTPConfig
	Log         sharedcfg.LogConfig
	Postgres    sharedcfg.PostgresConfig
	Chat        ChatConfig
	Agent       AgentConfig
	Embedding   sharedcfg.EmbeddingConfig
	Retrieval   sharedcfg.RetrievalConfig
	Rerank      RerankConfig
	Admin       AdminConfig
	Reservation ReservationConfig
	Timeout     sharedcfg.TimeoutConfig
}

// Agent defaults bound the tool-use loop so a model that keeps requesting
// tools cannot turn one turn into an unbounded chain of provider calls.
const (
	defaultAgentMaxToolRounds = 5
	defaultAgentToolTimeout   = 15 * time.Second

	// Interpretation defaults. They are the same values the slot layer falls
	// back to, repeated here because configuration is where an operator looks
	// for them and a default that exists in two places must at least agree.
	defaultAgentSlotExtractTimeout   = 10 * time.Second
	defaultAgentMaxClarifications    = 3
	defaultAgentResolveMinSimilarity = 0.55
	defaultAgentResolveAmbiguityGap  = 0.10

	// defaultAgentMemoryWriteEnabled keeps the user-requested memory path on by
	// default. It is the only way a memory is ever created, and it is guarded by
	// its own input check rather than by a confirmation step, so switching it
	// off removes a capability the user asked for rather than closing a hole.
	defaultAgentMemoryWriteEnabled = true

	// defaultAgentAnswerStreaming publishes the composed answer as it is
	// generated. It is on by default because the first-token latency it removes
	// is the user-visible cost of the whole turn; the switch exists so a client
	// that cannot handle a mid-answer replacement can opt out.
	defaultAgentAnswerStreaming = true
)

// AgentConfig holds the agent runtime knobs. They stay effective even when no
// chat provider is configured, so wiring the runner later cannot silently fall
// back to zero/unlimited values.
type AgentConfig struct {
	// MaxToolRounds bounds how many plan->tools cycles one turn may run before
	// the model is forced to answer.
	MaxToolRounds int
	// ToolTimeout bounds one tool invocation.
	ToolTimeout time.Duration

	// SlotExtractTimeout bounds the once-per-turn structured extraction call.
	// It is shorter than the chat timeout because extraction is an optional
	// refinement: a turn can proceed on a rule-derived plan, and a user waiting
	// on a model that is still deciding what a sentence meant should get the
	// rules answer instead.
	SlotExtractTimeout time.Duration
	// MaxClarifications bounds how many turns may be spent asking the user to
	// disambiguate before the agent proceeds on its best assumption. Without a
	// bound, a model that keeps finding two plausible restaurants can keep the
	// thread asking forever.
	MaxClarifications int
	// ResolveMinSimilarity is the name-match similarity at or above which a
	// named restaurant counts as found.
	ResolveMinSimilarity float64
	// ResolveAmbiguityGap is the top1/top2 similarity difference below which a
	// name match is ambiguous rather than resolved.
	ResolveAmbiguityGap float64

	// MemoryWriteEnabled registers the save_memory tool. It is on by default:
	// the tool is the only way a long-term memory is created, and it is already
	// guarded by an input check that refuses anything the user did not ask for.
	// Switching it off removes the capability rather than closing a hole, which
	// is why it is a separate switch from anything about retrieval.
	MemoryWriteEnabled bool

	// AnswerStreaming publishes the composed answer incrementally instead of as
	// one message.delta. Turning it off restores the single-delta behaviour and
	// is the documented fallback for a client that does not understand
	// message.replace, which is the only event streaming introduces.
	AnswerStreaming bool
}

// AdminConfig holds the administration console settings. The console is off by
// default; when enabled it is additionally restricted to loopback peers by the
// transport, so enabling it never exposes the surface beyond the local host.
type AdminConfig struct {
	Enabled         bool
	DefaultPageSize int
	MaxPageSize     int
	MaxRejections   int
}

// Reservation defaults. The TTL and the policy version are repeated here rather
// than imported from the reservation package so configuration stays a leaf, and
// a value that exists in two places must at least agree; the service falls back
// to the same numbers when it is handed a zero.
const (
	defaultReservationHoldTTL      = 10 * time.Minute
	defaultReservationPolicyName   = "mock-v1"
	defaultReservationEnabledState = false
)

// ReservationConfig holds the mock reservation capability's settings.
//
// The capability is off by default and that is a decision, not an oversight:
// these two tools are the only ones in the registry that can write outside the
// conversation, and a write path should be switched on deliberately. Turning it
// off removes the tools from the registry entirely rather than leaving them
// registered and failing, so a deployment without it presents a model that
// cannot even ask for a table.
type ReservationConfig struct {
	// Enabled registers get_availability and request_reservation.
	Enabled bool
	// HoldTTL bounds how long a hold keeps its seats before the TTL sweep
	// returns them. It must be long enough to read a summary and answer.
	HoldTTL time.Duration
	// PolicyVersion identifies the rules a booking was made under. It is
	// recorded on every slot and quoted in the approval summary, so a user is
	// told which policy they are agreeing to.
	PolicyVersion string
}

// RerankConfig holds the optional reranking settings. An empty provider name
// means the stage is switched off, which is a supported way to run.
type RerankConfig struct {
	Provider string
	Model    string
	Timeout  time.Duration
}

// Enabled reports whether a rerank provider was configured.
func (c RerankConfig) Enabled() bool { return c.Provider != "" }

// HTTPConfig holds HTTP server settings.
type HTTPConfig struct {
	Addr             string
	ReadTimeout      time.Duration
	WriteTimeout     time.Duration
	ShutdownTimeout  time.Duration
	CORSAllowOrigins []string
}

// Chat defaults. They mirror the conservative capability assumptions of the
// OpenAI-compatible adapter: a model that genuinely supports parallel tool
// calls or strict JSON schema is opted in explicitly through the environment.
const (
	defaultChatTimeout            = 60 * time.Second
	defaultChatMaxRetries         = 2
	defaultChatSupportsTools      = true
	defaultChatSupportsParallel   = false
	defaultChatSupportsJSONSchema = false
	defaultChatContextTokens      = 32768
)

// ChatConfig holds the OpenAI-compatible chat provider settings (used from M4).
type ChatConfig struct {
	Provider string
	BaseURL  string
	APIKey   string
	Model    string
	// PlanModel, AnswerModel and ExtractModel put different models behind the
	// three jobs a turn asks of one: deciding what to do next, writing the
	// answer, and reading the sentence into slots. They are the cheapest form
	// of model routing — a small model for extraction and a large one for the
	// answer, with no router component in between.
	//
	// Empty means "the same as Model". The fallback is resolved at load time
	// rather than at each use, so nothing downstream has to know it exists and
	// an operator reading the startup summary sees the model each job really
	// uses.
	PlanModel    string
	AnswerModel  string
	ExtractModel string

	ExtraHeaders map[string]string

	Timeout time.Duration
	// MaxRetries is the number of retries after the first attempt for
	// transient failures (429/408/5xx/transport).
	MaxRetries int
	// Capability flags declare what the configured endpoint supports. They
	// are operator assertions, not runtime probes: unknown models default to
	// the safe subset (tools on, parallel tools and strict JSON schema off).
	SupportsTools         bool
	SupportsParallelTools bool
	SupportsJSONSchema    bool
	ContextTokens         int
}

// Enabled reports whether a chat provider has been configured.
func (c ChatConfig) Enabled() bool { return c.Provider != "" }

// Load reads configuration from the environment, layering .env underneath when
// present. Malformed values are reported as an error.
func Load() (Config, error) {
	if err := sharedcfg.LoadDotEnv(DotEnvFile); err != nil {
		return Config{}, fmt.Errorf("load .env: %w", err)
	}
	l := sharedcfg.NewLoader()
	// Read once and reuse: it is the default for all three per-node overrides,
	// and reading it three times would let the values drift apart if the
	// loader ever acquired state.
	chatModel := l.String("CHAT_MODEL", "")
	cfg := Config{
		App:       l.App(),
		Log:       l.Log(),
		Postgres:  l.Postgres(),
		Embedding: l.Embedding(),
		Retrieval: l.Retrieval(),
		Rerank: RerankConfig{
			Provider: l.String("RERANK_PROVIDER", ""),
			Model:    l.String("RERANK_MODEL", ""),
			Timeout:  l.Duration("RERANK_TIMEOUT", 3*time.Second),
		},
		Timeout: l.Timeout(),
		HTTP: HTTPConfig{
			Addr:             l.String("HTTP_ADDR", ":8080"),
			ReadTimeout:      l.Duration("HTTP_READ_TIMEOUT", 10*time.Second),
			WriteTimeout:     l.Duration("HTTP_WRITE_TIMEOUT", 30*time.Second),
			ShutdownTimeout:  l.Duration("HTTP_SHUTDOWN_TIMEOUT", 10*time.Second),
			CORSAllowOrigins: l.List("HTTP_CORS_ALLOW_ORIGINS"),
		},
		Chat: ChatConfig{
			Provider:              l.String("CHAT_PROVIDER", ""),
			BaseURL:               l.String("CHAT_BASE_URL", ""),
			APIKey:                l.String("CHAT_API_KEY", ""),
			Model:                 chatModel,
			PlanModel:             modelOverride(l, "CHAT_MODEL_PLAN", chatModel),
			AnswerModel:           modelOverride(l, "CHAT_MODEL_ANSWER", chatModel),
			ExtractModel:          modelOverride(l, "CHAT_MODEL_EXTRACT", chatModel),
			ExtraHeaders:          l.StringMap("CHAT_EXTRA_HEADERS_JSON"),
			Timeout:               l.Duration("CHAT_TIMEOUT", defaultChatTimeout),
			MaxRetries:            l.Int("CHAT_MAX_RETRIES", defaultChatMaxRetries),
			SupportsTools:         l.Bool("CHAT_SUPPORTS_TOOLS", defaultChatSupportsTools),
			SupportsParallelTools: l.Bool("CHAT_SUPPORTS_PARALLEL_TOOLS", defaultChatSupportsParallel),
			SupportsJSONSchema:    l.Bool("CHAT_SUPPORTS_JSON_SCHEMA", defaultChatSupportsJSONSchema),
			ContextTokens:         l.Int("CHAT_CONTEXT_TOKENS", defaultChatContextTokens),
		},
		Agent: AgentConfig{
			MaxToolRounds:        l.Int("AGENT_MAX_TOOL_ROUNDS", defaultAgentMaxToolRounds),
			ToolTimeout:          l.Duration("AGENT_TOOL_TIMEOUT", defaultAgentToolTimeout),
			SlotExtractTimeout:   l.Duration("AGENT_SLOT_EXTRACT_TIMEOUT", defaultAgentSlotExtractTimeout),
			MaxClarifications:    l.Int("AGENT_MAX_CLARIFICATIONS", defaultAgentMaxClarifications),
			ResolveMinSimilarity: l.Float("AGENT_RESOLVE_MIN_SIMILARITY", defaultAgentResolveMinSimilarity),
			ResolveAmbiguityGap:  l.Float("AGENT_RESOLVE_AMBIGUITY_GAP", defaultAgentResolveAmbiguityGap),
			MemoryWriteEnabled:   l.Bool("AGENT_MEMORY_WRITE_ENABLED", defaultAgentMemoryWriteEnabled),
			AnswerStreaming:      l.Bool("PLATEPILOT_ANSWER_STREAMING", defaultAgentAnswerStreaming),
		},
		Admin: AdminConfig{
			Enabled:         l.Bool("ADMIN_ENABLED", false),
			DefaultPageSize: l.Int("ADMIN_DEFAULT_PAGE_SIZE", 25),
			MaxPageSize:     l.Int("ADMIN_MAX_PAGE_SIZE", 100),
			MaxRejections:   l.Int("ADMIN_MAX_REJECTIONS", 200),
		},
		Reservation: ReservationConfig{
			Enabled:       l.Bool("RESERVATION_ENABLED", defaultReservationEnabledState),
			HoldTTL:       l.Duration("RESERVATION_HOLD_TTL", defaultReservationHoldTTL),
			PolicyVersion: l.String("RESERVATION_POLICY_VERSION", defaultReservationPolicyName),
		},
	}
	if err := l.Err(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks the configuration required to run the chat service. Only the
// HTTP and logging settings are required in M0; PostgreSQL, chat, and embedding
// settings are validated as soon as they are configured.
func (c Config) Validate() error {
	return sharedcfg.Combine(
		c.App.Validate(),
		c.Log.Validate(),
		c.HTTP.validate(),
		c.Postgres.Validate(),
		c.Embedding.Validate(),
		c.Chat.validate(),
		c.Agent.validate(),
		c.Retrieval.Validate(),
		c.Rerank.validate(),
		c.Admin.validate(),
		c.Reservation.validate(),
	)
}

// validate reserves its checks for the enabled capability. A switched-off
// reservation path has no hold and no policy to get wrong, and refusing to
// start because an unused knob is nonsensical would block the deployments that
// never touch it.
func (c ReservationConfig) validate() []string {
	if !c.Enabled {
		return nil
	}
	var problems []string
	if c.HoldTTL <= 0 {
		problems = append(problems, fmt.Sprintf(
			"RESERVATION_HOLD_TTL: must be > 0 (got %s)", c.HoldTTL))
	}
	if strings.TrimSpace(c.PolicyVersion) == "" {
		problems = append(problems, "RESERVATION_POLICY_VERSION: must not be empty")
	}
	return problems
}

func (c AdminConfig) validate() []string {
	if !c.Enabled {
		return nil
	}
	var problems []string
	if c.DefaultPageSize <= 0 {
		problems = append(problems, fmt.Sprintf(
			"ADMIN_DEFAULT_PAGE_SIZE: must be > 0 (got %d)", c.DefaultPageSize))
	}
	if c.MaxPageSize <= 0 {
		problems = append(problems, fmt.Sprintf(
			"ADMIN_MAX_PAGE_SIZE: must be > 0 (got %d)", c.MaxPageSize))
	}
	if c.DefaultPageSize > c.MaxPageSize {
		problems = append(problems, fmt.Sprintf(
			"ADMIN_DEFAULT_PAGE_SIZE %d must not exceed ADMIN_MAX_PAGE_SIZE %d",
			c.DefaultPageSize, c.MaxPageSize))
	}
	if c.MaxRejections <= 0 {
		problems = append(problems, fmt.Sprintf(
			"ADMIN_MAX_REJECTIONS: must be > 0 (got %d)", c.MaxRejections))
	}
	return problems
}

func (c HTTPConfig) validate() []string {
	if err := validateAddr(c.Addr); err != nil {
		return []string{fmt.Sprintf("HTTP_ADDR: %v (got %q)", err, c.Addr)}
	}
	return nil
}

func (c ChatConfig) validate() []string {
	if !c.Enabled() {
		return nil
	}
	var problems []string
	if strings.TrimSpace(c.BaseURL) == "" {
		problems = append(problems, "CHAT_BASE_URL: required when CHAT_PROVIDER is set")
	}
	if strings.TrimSpace(c.APIKey) == "" {
		problems = append(problems, "CHAT_API_KEY: required when CHAT_PROVIDER is set")
	}
	if strings.TrimSpace(c.Model) == "" {
		problems = append(problems, "CHAT_MODEL: required when CHAT_PROVIDER is set")
	}
	if c.Timeout <= 0 {
		problems = append(problems, fmt.Sprintf(
			"CHAT_TIMEOUT: must be > 0 (got %s)", c.Timeout))
	}
	if c.MaxRetries < 0 {
		problems = append(problems, fmt.Sprintf(
			"CHAT_MAX_RETRIES: must be >= 0 (got %d)", c.MaxRetries))
	}
	if c.ContextTokens <= 0 {
		problems = append(problems, fmt.Sprintf(
			"CHAT_CONTEXT_TOKENS: must be > 0 (got %d)", c.ContextTokens))
	}
	return problems
}

func (c AgentConfig) validate() []string {
	var problems []string
	if c.MaxToolRounds <= 0 {
		problems = append(problems, fmt.Sprintf(
			"AGENT_MAX_TOOL_ROUNDS: must be > 0 (got %d)", c.MaxToolRounds))
	}
	if c.ToolTimeout <= 0 {
		problems = append(problems, fmt.Sprintf(
			"AGENT_TOOL_TIMEOUT: must be > 0 (got %s)", c.ToolTimeout))
	}
	if c.SlotExtractTimeout <= 0 {
		problems = append(problems, fmt.Sprintf(
			"AGENT_SLOT_EXTRACT_TIMEOUT: must be > 0 (got %s)", c.SlotExtractTimeout))
	}
	if c.MaxClarifications <= 0 {
		problems = append(problems, fmt.Sprintf(
			"AGENT_MAX_CLARIFICATIONS: must be > 0 (got %d)", c.MaxClarifications))
	}
	if c.ResolveMinSimilarity <= 0 || c.ResolveMinSimilarity > 1 {
		problems = append(problems, fmt.Sprintf(
			"AGENT_RESOLVE_MIN_SIMILARITY: must be in (0,1] (got %g)", c.ResolveMinSimilarity))
	}
	if c.ResolveAmbiguityGap < 0 || c.ResolveAmbiguityGap > 1 {
		problems = append(problems, fmt.Sprintf(
			"AGENT_RESOLVE_AMBIGUITY_GAP: must be in [0,1] (got %g)", c.ResolveAmbiguityGap))
	}
	return problems
}

func (c RerankConfig) validate() []string {
	if !c.Enabled() {
		return nil
	}
	if strings.TrimSpace(c.Model) == "" {
		return []string{"RERANK_MODEL: required when RERANK_PROVIDER is set"}
	}
	if c.Timeout <= 0 {
		return []string{fmt.Sprintf("RERANK_TIMEOUT: must be > 0 (got %s)", c.Timeout)}
	}
	return nil
}

// Redacted returns a copy with secrets replaced so it is safe to log.
func (c Config) Redacted() Config {
	c.Chat.APIKey = sharedcfg.Redact(c.Chat.APIKey)
	if len(c.Chat.ExtraHeaders) > 0 {
		headers := make(map[string]string, len(c.Chat.ExtraHeaders))
		for key := range c.Chat.ExtraHeaders {
			headers[key] = "***"
		}
		c.Chat.ExtraHeaders = headers
	}
	c.Postgres.DSN = sharedcfg.RedactURI(c.Postgres.DSN)
	return c
}

// Summary returns a log-friendly, secret-free view of the configuration.
func (c Config) Summary() map[string]any {
	return map[string]any{
		"app_env":                      c.App.Env,
		"http_addr":                    c.HTTP.Addr,
		"http_read_timeout":            c.HTTP.ReadTimeout.String(),
		"http_write_timeout":           c.HTTP.WriteTimeout.String(),
		"http_shutdown_timeout":        c.HTTP.ShutdownTimeout.String(),
		"cors_allow_origins":           c.HTTP.CORSAllowOrigins,
		"log_level":                    c.Log.Level,
		"postgres_enabled":             c.Postgres.Enabled(),
		"postgres_database":            c.Postgres.Database,
		"chat_provider":                c.Chat.Provider,
		"chat_model":                   c.Chat.Model,
		"chat_model_plan":              c.Chat.PlanModel,
		"chat_model_answer":            c.Chat.AnswerModel,
		"chat_model_extract":           c.Chat.ExtractModel,
		"chat_timeout":                 c.Chat.Timeout.String(),
		"chat_max_retries":             c.Chat.MaxRetries,
		"chat_supports_tools":          c.Chat.SupportsTools,
		"chat_supports_parallel_tools": c.Chat.SupportsParallelTools,
		"chat_supports_json_schema":    c.Chat.SupportsJSONSchema,
		"chat_context_tokens":          c.Chat.ContextTokens,
		"agent_max_tool_rounds":        c.Agent.MaxToolRounds,
		"agent_tool_timeout":           c.Agent.ToolTimeout.String(),
		"agent_slot_extract_timeout":   c.Agent.SlotExtractTimeout.String(),
		"agent_max_clarifications":     c.Agent.MaxClarifications,
		"agent_resolve_min_similarity": c.Agent.ResolveMinSimilarity,
		"agent_resolve_ambiguity_gap":  c.Agent.ResolveAmbiguityGap,
		"agent_memory_write_enabled":   c.Agent.MemoryWriteEnabled,
		"agent_answer_streaming":       c.Agent.AnswerStreaming,
		"embedding_provider":           c.Embedding.Provider,
		"embedding_model":              c.Embedding.Model,
		"embedding_dimensions":         c.Embedding.Dimensions,
		"retrieval_top_k":              c.Retrieval.TopK,
		"retrieval_enable_vector":      c.Retrieval.EnableVector,
		"retrieval_oversample":         c.Retrieval.Oversample,
		"weight_structured":            c.Retrieval.Weights.Structured,
		"weight_keyword":               c.Retrieval.Weights.Keyword,
		"weight_vector":                c.Retrieval.Weights.Vector,
		"weight_quality":               c.Retrieval.Weights.Quality,
		"rerank_provider":              c.Rerank.Provider,
		"rerank_model":                 c.Rerank.Model,
		"admin_enabled":                c.Admin.Enabled,
		"admin_default_page_size":      c.Admin.DefaultPageSize,
		"admin_max_page_size":          c.Admin.MaxPageSize,
		"reservation_enabled":          c.Reservation.Enabled,
		"reservation_hold_ttl":         c.Reservation.HoldTTL.String(),
		"reservation_policy_version":   c.Reservation.PolicyVersion,
	}
}

// modelOverride resolves one per-node model variable.
//
// An override with content wins; anything else — the variable unset, or set to
// nothing — means "use the base model". That is the same rule the loader's
// other helpers apply to an empty value, and it is why the resolution happens
// here rather than at each use: `CHAT_MODEL_PLAN=` must mean "no override", not
// "ask the provider for whatever its default model is".
func modelOverride(l *sharedcfg.Loader, key, base string) string {
	if value := strings.TrimSpace(l.String(key, "")); value != "" {
		return value
	}
	return base
}

func validateAddr(addr string) error {
	if strings.TrimSpace(addr) == "" {
		return fmt.Errorf("must not be empty")
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("must be in host:port form")
	}
	if port == "" {
		return fmt.Errorf("port must not be empty")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("port must be an integer in 1..65535")
	}
	return nil
}
