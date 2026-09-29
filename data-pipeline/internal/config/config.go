// Package config loads and validates data-pipeline configuration.
//
// The data pipeline is a batch, CLI-driven service: it shares the logging,
// Mongo, and embedding settings with the chat service and adds its own batch
// knobs.
package config

import (
	"fmt"
	"strings"

	sharedcfg "github.com/zed/platepilot/shared/config"
)

// DotEnvFile is the optional local development configuration file.
const DotEnvFile = ".env"

// DefaultDataDir is where the raw Google Local files are expected.
const DefaultDataDir = "data/raw/google_local"

// Defaults for the curation knobs.
const (
	DefaultMinReviewChars = 20
	DefaultDemoTarget     = 3000
)

// Config is the fully resolved data-pipeline configuration.
type Config struct {
	App       sharedcfg.AppConfig
	Log       sharedcfg.LogConfig
	Mongo     sharedcfg.MongoConfig
	Embedding sharedcfg.EmbeddingConfig
	Timeout   sharedcfg.TimeoutConfig
	Pipeline  PipelineConfig
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
		Mongo:     l.Mongo(),
		Embedding: l.Embedding(),
		Timeout:   l.Timeout(),
		Pipeline: PipelineConfig{
			DataDir:        l.String("PIPELINE_DATA_DIR", DefaultDataDir),
			BatchSize:      l.Int("PIPELINE_BATCH_SIZE", 1000),
			Workers:        l.Int("PIPELINE_WORKERS", 4),
			MinReviewChars: l.Int("PIPELINE_MIN_REVIEW_CHARS", DefaultMinReviewChars),
			DemoTarget:     l.Int("PIPELINE_DEMO_TARGET", DefaultDemoTarget),
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
		c.Mongo.Validate(),
		c.Embedding.Validate(),
		c.Pipeline.validate(),
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

// Redacted returns a copy with secrets replaced so it is safe to log.
func (c Config) Redacted() Config {
	c.Mongo.URI = sharedcfg.RedactURI(c.Mongo.URI)
	return c
}

// Summary returns a log-friendly, secret-free view of the configuration.
func (c Config) Summary() map[string]any {
	return map[string]any{
		"app_env":                   c.App.Env,
		"log_level":                 c.Log.Level,
		"mongo_enabled":             c.Mongo.Enabled(),
		"mongo_database":            c.Mongo.Database,
		"mongo_timeout":             c.Mongo.Timeout.String(),
		"embedding_provider":        c.Embedding.Provider,
		"embedding_model":           c.Embedding.Model,
		"embedding_dimensions":      c.Embedding.Dimensions,
		"request_timeout":           c.Timeout.Request.String(),
		"pipeline_data_dir":         c.Pipeline.DataDir,
		"pipeline_batch_size":       c.Pipeline.BatchSize,
		"pipeline_workers":          c.Pipeline.Workers,
		"pipeline_min_review_chars": c.Pipeline.MinReviewChars,
		"pipeline_demo_target":      c.Pipeline.DemoTarget,
	}
}
