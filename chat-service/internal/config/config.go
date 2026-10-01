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

	sharedcfg "github.com/zed/platepilot/shared/config"
)

// DotEnvFile is the optional local development configuration file.
const DotEnvFile = ".env"

// Config is the fully resolved chat-service configuration.
type Config struct {
	App       sharedcfg.AppConfig
	HTTP      HTTPConfig
	Log       sharedcfg.LogConfig
	Postgres  sharedcfg.PostgresConfig
	Chat      ChatConfig
	Embedding sharedcfg.EmbeddingConfig
	Retrieval sharedcfg.RetrievalConfig
	Rerank    RerankConfig
	Timeout   sharedcfg.TimeoutConfig
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

// ChatConfig holds the OpenAI-compatible chat provider settings (used from M4).
type ChatConfig struct {
	Provider     string
	BaseURL      string
	APIKey       string
	Model        string
	ExtraHeaders map[string]string
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
			Provider:     l.String("CHAT_PROVIDER", ""),
			BaseURL:      l.String("CHAT_BASE_URL", ""),
			APIKey:       l.String("CHAT_API_KEY", ""),
			Model:        l.String("CHAT_MODEL", ""),
			ExtraHeaders: l.StringMap("CHAT_EXTRA_HEADERS_JSON"),
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
		c.Retrieval.Validate(),
		c.Rerank.validate(),
	)
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
		"app_env":                 c.App.Env,
		"http_addr":               c.HTTP.Addr,
		"http_read_timeout":       c.HTTP.ReadTimeout.String(),
		"http_write_timeout":      c.HTTP.WriteTimeout.String(),
		"http_shutdown_timeout":   c.HTTP.ShutdownTimeout.String(),
		"cors_allow_origins":      c.HTTP.CORSAllowOrigins,
		"log_level":               c.Log.Level,
		"postgres_enabled":        c.Postgres.Enabled(),
		"postgres_database":       c.Postgres.Database,
		"chat_provider":           c.Chat.Provider,
		"chat_model":              c.Chat.Model,
		"embedding_provider":      c.Embedding.Provider,
		"embedding_model":         c.Embedding.Model,
		"embedding_dimensions":    c.Embedding.Dimensions,
		"retrieval_top_k":         c.Retrieval.TopK,
		"retrieval_enable_vector": c.Retrieval.EnableVector,
		"retrieval_oversample":    c.Retrieval.Oversample,
		"weight_structured":       c.Retrieval.Weights.Structured,
		"weight_keyword":          c.Retrieval.Weights.Keyword,
		"weight_vector":           c.Retrieval.Weights.Vector,
		"weight_quality":          c.Retrieval.Weights.Quality,
		"rerank_provider":         c.Rerank.Provider,
		"rerank_model":            c.Rerank.Model,
	}
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
