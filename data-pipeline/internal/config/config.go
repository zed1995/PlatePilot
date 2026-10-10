// Package config loads and validates data-pipeline configuration.
//
// The data pipeline is a batch, CLI-driven service: it shares the logging,
// PostgreSQL, and embedding settings with the chat service and adds its own batch
// knobs.
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/zed1995/platepilot/data-pipeline/internal/pipeline/curate"
	"github.com/zed1995/platepilot/data-pipeline/internal/pipeline/knowledge"
	sharedcfg "github.com/zed1995/platepilot/shared/config"
)

// DotEnvFile is the optional local development configuration file.
const DotEnvFile = ".env"

// DefaultDataDir is where the raw Google Local files are expected.
const DefaultDataDir = "data/raw/google_local"

// Defaults for the curation knobs.
const (
	DefaultMinReviewChars = 20
	DefaultDemoTarget     = 3000
	// DefaultBoundaryFile is the borough geometry used for borough_guess.
	DefaultBoundaryFile = curate.DefaultBoundaryFile

	// DefaultServiceArea is the NYC five-borough bounding box,
	// "south,west,north,east". The shipped meta file is US-wide.
	DefaultServiceArea = "40.49,-74.26,40.93,-73.68"

	// Defaults for the review-digest stage. The timeout is deliberately generous
	// because one LLM call reads an 8k–20k token input bundle; the concurrency
	// and rate stay low so a full run looks like background traffic to the
	// digest provider.
	DefaultDigestTimeout       = 120 * time.Second
	DefaultDigestConcurrency   = 2
	DefaultDigestRateQPS       = 1.0
	DefaultDigestBatchSize     = 100
	DefaultDigestMaxRetries    = 2
	DefaultDigestPromptVersion = knowledge.DigestLLMVersion
)

// Config is the fully resolved data-pipeline configuration.
type Config struct {
	App       sharedcfg.AppConfig
	Log       sharedcfg.LogConfig
	Postgres  sharedcfg.PostgresConfig
	Embedding sharedcfg.EmbeddingConfig
	Timeout   sharedcfg.TimeoutConfig
	Pipeline  PipelineConfig
	Digest    DigestConfig
}

// PipelineConfig holds batch-processing settings.
type PipelineConfig struct {
	DataDir string
	// BatchSize is the number of documents written per bulk upsert.
	BatchSize int
	// Workers reserved for parallel stages (unused by the streaming stages).
	Workers int
	// MinReviewChars is the shortest review text kept as usable evidence.
	MinReviewChars int
	// DemoTarget is the target number of demo restaurants (clamped to
	// [2000, 5000] when selecting).
	DemoTarget int
	// ServiceArea is the ingestion bounding box as "south,west,north,east".
	// The source file is US-wide, so this is what keeps the corpus local.
	// Empty means the default NYC box.
	ServiceArea string
	// BoundaryFile is the administrative boundary geometry used to label each
	// place with its borough. Empty disables boundary labelling and leaves
	// borough_guess empty. A path that does not exist falls back to approximate
	// bounding boxes unless RequireBoundaries is set.
	BoundaryFile string
	// RequireBoundaries makes a missing or altered boundary file a hard error
	// instead of a silent fallback to approximate labels.
	RequireBoundaries bool
}

// DigestConfig holds the offline review-digest stage settings. The chat
// endpoint is deliberately separate from the chat-service CHAT_* settings so
// the online conversation model and the offline batch model can differ in
// vendor, model, key, and rate budget.
type DigestConfig struct {
	// Enabled gates the build-digests stage. It is off by default because the
	// stage calls a paid API; every other stage must run without it.
	Enabled bool
	// ChatBaseURL is the OpenAI-compatible endpoint root for digest generation.
	ChatBaseURL string
	// ChatAPIKey authenticates against that endpoint. It must never reach a
	// log line: Redacted masks it and Summary omits it.
	ChatAPIKey string
	// ChatModel is the digest model id. A long-context, Chinese-summarising
	// model is wanted; tool-calling ability is not.
	ChatModel string
	// Timeout bounds one completion attempt.
	Timeout time.Duration
	// Concurrency is how many restaurants are generated in parallel.
	Concurrency int
	// RateQPS caps completed generation requests per second across workers.
	RateQPS float64
	// BatchSize is the restaurant page size for the paginated run.
	BatchSize int
	// MaxRetries is the number of additional attempts for retryable provider
	// errors (429/5xx/transport), matching the online client's default.
	MaxRetries int
	// PromptVersion selects the generator: digest:llm:v1 (model-backed) or
	// digest:rules:v1 (zero-model eval baseline). Changing it changes the
	// cache key, so a switch regenerates instead of reusing.
	PromptVersion string
}

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
		Timeout:   l.Timeout(),
		Pipeline: PipelineConfig{
			DataDir:           l.String("PIPELINE_DATA_DIR", DefaultDataDir),
			BatchSize:         l.Int("PIPELINE_BATCH_SIZE", 1000),
			Workers:           l.Int("PIPELINE_WORKERS", 4),
			MinReviewChars:    l.Int("PIPELINE_MIN_REVIEW_CHARS", DefaultMinReviewChars),
			DemoTarget:        l.Int("PIPELINE_DEMO_TARGET", DefaultDemoTarget),
			ServiceArea:       l.String("PIPELINE_BBOX", DefaultServiceArea),
			BoundaryFile:      l.String("PIPELINE_BOUNDARY_FILE", curate.DefaultBoundaryFile),
			RequireBoundaries: l.Bool("PIPELINE_REQUIRE_BOUNDARIES", false),
		},
		Digest: DigestConfig{
			Enabled:       l.Bool("DIGEST_ENABLED", false),
			ChatBaseURL:   l.String("DIGEST_CHAT_BASE_URL", ""),
			ChatAPIKey:    l.String("DIGEST_CHAT_API_KEY", ""),
			ChatModel:     l.String("DIGEST_CHAT_MODEL", ""),
			Timeout:       l.Duration("DIGEST_TIMEOUT", DefaultDigestTimeout),
			Concurrency:   l.Int("DIGEST_CONCURRENCY", DefaultDigestConcurrency),
			RateQPS:       l.Float("DIGEST_RATE_QPS", DefaultDigestRateQPS),
			BatchSize:     l.Int("DIGEST_BATCH_SIZE", DefaultDigestBatchSize),
			MaxRetries:    l.Int("DIGEST_MAX_RETRIES", DefaultDigestMaxRetries),
			PromptVersion: l.String("DIGEST_PROMPT_VERSION", DefaultDigestPromptVersion),
		},
	}
	if err := l.Err(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks the configuration required to run the data pipeline.
func (c Config) Validate() error {
	return sharedcfg.Combine(
		c.App.Validate(),
		c.Log.Validate(),
		c.Postgres.Validate(),
		c.Embedding.Validate(),
		c.Pipeline.validate(),
		c.Digest.validate(),
	)
}

func (c PipelineConfig) validate() []string {
	var problems []string
	if strings.TrimSpace(c.DataDir) == "" {
		problems = append(problems, "PIPELINE_DATA_DIR: must not be empty")
	}
	if c.BatchSize <= 0 {
		problems = append(problems, fmt.Sprintf("PIPELINE_BATCH_SIZE: must be > 0 (got %d)", c.BatchSize))
	}
	if c.Workers <= 0 {
		problems = append(problems, fmt.Sprintf("PIPELINE_WORKERS: must be > 0 (got %d)", c.Workers))
	}
	if c.MinReviewChars < 0 {
		problems = append(problems, fmt.Sprintf("PIPELINE_MIN_REVIEW_CHARS: must be >= 0 (got %d)", c.MinReviewChars))
	}
	if c.DemoTarget < 0 {
		problems = append(problems, fmt.Sprintf("PIPELINE_DEMO_TARGET: must be >= 0 (got %d)", c.DemoTarget))
	}
	return problems
}

// digest prompt-version prefixes. A rules-prefixed version needs no model; an
// llm-prefixed one does. Checking the prefix rather than the exact string lets
// a future digest:llm:v2 land without touching this validation.
const (
	digestRulesPrefix = "digest:rules:"
	digestLLMPrefix   = "digest:llm:"
)

func (c DigestConfig) validate() []string {
	// The stage is gated off by default and every other command must run with
	// an unconfigured digest section, so its values are only checked when the
	// stage can actually be used.
	if !c.Enabled {
		return nil
	}
	var problems []string
	version := strings.TrimSpace(c.PromptVersion)
	switch {
	case version == "":
		problems = append(problems, "DIGEST_PROMPT_VERSION: must not be empty when DIGEST_ENABLED is true")
	case strings.HasPrefix(version, digestRulesPrefix):
		// The rules generator is a pure function; no chat endpoint is read.
	case strings.HasPrefix(version, digestLLMPrefix):
		if strings.TrimSpace(c.ChatBaseURL) == "" {
			problems = append(problems, "DIGEST_CHAT_BASE_URL: required when DIGEST_ENABLED is true and the prompt version is an llm version")
		}
		if strings.TrimSpace(c.ChatAPIKey) == "" {
			problems = append(problems, "DIGEST_CHAT_API_KEY: required when DIGEST_ENABLED is true and the prompt version is an llm version")
		}
		if strings.TrimSpace(c.ChatModel) == "" {
			problems = append(problems, "DIGEST_CHAT_MODEL: required when DIGEST_ENABLED is true and the prompt version is an llm version")
		}
	default:
		problems = append(problems, fmt.Sprintf(
			"DIGEST_PROMPT_VERSION: must start with %q or %q (got %q)",
			digestRulesPrefix, digestLLMPrefix, version))
	}
	if c.Timeout <= 0 {
		problems = append(problems, fmt.Sprintf("DIGEST_TIMEOUT: must be > 0 (got %s)", c.Timeout))
	}
	if c.Concurrency <= 0 {
		problems = append(problems, fmt.Sprintf("DIGEST_CONCURRENCY: must be > 0 (got %d)", c.Concurrency))
	}
	if c.RateQPS <= 0 {
		problems = append(problems, fmt.Sprintf("DIGEST_RATE_QPS: must be > 0 (got %g)", c.RateQPS))
	}
	if c.BatchSize <= 0 {
		problems = append(problems, fmt.Sprintf("DIGEST_BATCH_SIZE: must be > 0 (got %d)", c.BatchSize))
	}
	if c.MaxRetries < 0 {
		problems = append(problems, fmt.Sprintf("DIGEST_MAX_RETRIES: must be >= 0 (got %d)", c.MaxRetries))
	}
	return problems
}

// Redacted returns a copy with secrets replaced so it is safe to log.
func (c Config) Redacted() Config {
	c.Postgres.DSN = sharedcfg.RedactURI(c.Postgres.DSN)
	c.Digest.ChatAPIKey = sharedcfg.Redact(c.Digest.ChatAPIKey)
	return c
}

// Summary returns a log-friendly, secret-free view of the configuration.
func (c Config) Summary() map[string]any {
	return map[string]any{
		"app_env":                     c.App.Env,
		"log_level":                   c.Log.Level,
		"postgres_enabled":            c.Postgres.Enabled(),
		"postgres_database":           c.Postgres.Database,
		"postgres_timeout":            c.Postgres.Timeout.String(),
		"postgres_connect_timeout":    c.Postgres.ConnectTimeout.String(),
		"postgres_max_pool_size":      c.Postgres.MaxPoolSize,
		"postgres_min_pool_size":      c.Postgres.MinPoolSize,
		"embedding_provider":          c.Embedding.Provider,
		"embedding_model":             c.Embedding.Model,
		"embedding_dimensions":        c.Embedding.Dimensions,
		"request_timeout":             c.Timeout.Request.String(),
		"pipeline_data_dir":           c.Pipeline.DataDir,
		"pipeline_batch_size":         c.Pipeline.BatchSize,
		"pipeline_workers":            c.Pipeline.Workers,
		"pipeline_min_review_chars":   c.Pipeline.MinReviewChars,
		"pipeline_demo_target":        c.Pipeline.DemoTarget,
		"pipeline_service_area":       c.Pipeline.ServiceArea,
		"pipeline_boundary_file":      c.Pipeline.BoundaryFile,
		"pipeline_require_boundaries": c.Pipeline.RequireBoundaries,
		"digest_enabled":              c.Digest.Enabled,
		// The api key itself never appears here; only whether one is set, so an
		// operator can tell a missing key from a configured one without the
		// value leaking into a log.
		"digest_chat_api_key_set": c.Digest.ChatAPIKey != "",
		"digest_chat_base_url":    c.Digest.ChatBaseURL,
		"digest_chat_model":       c.Digest.ChatModel,
		"digest_timeout":          c.Digest.Timeout.String(),
		"digest_concurrency":      c.Digest.Concurrency,
		"digest_rate_qps":         c.Digest.RateQPS,
		"digest_batch_size":       c.Digest.BatchSize,
		"digest_max_retries":      c.Digest.MaxRetries,
		"digest_prompt_version":   c.Digest.PromptVersion,
	}
}
