package config

import (
	"errors"
	"os"
	"strings"
	"testing"

	sharedcfg "github.com/zed/platepilot/shared/config"
)

// clearEnv unsets the given variables for the duration of the test.
func clearEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			key, value := key, value
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Setenv(key, value) })
		}
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t,
		"PIPELINE_DATA_DIR", "PIPELINE_BATCH_SIZE", "PIPELINE_WORKERS",
		"APP_ENV", "LOG_LEVEL", "POSTGRES_DSN",
	)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Pipeline.DataDir != DefaultDataDir {
		t.Errorf("DataDir = %q, want %q", cfg.Pipeline.DataDir, DefaultDataDir)
	}
	if cfg.Pipeline.BatchSize != 1000 {
		t.Errorf("BatchSize = %d, want 1000", cfg.Pipeline.BatchSize)
	}
	if cfg.Pipeline.Workers != 4 {
		t.Errorf("Workers = %d, want 4", cfg.Pipeline.Workers)
	}
	if cfg.Embedding.Dimensions != 1024 {
		t.Errorf("Embedding.Dimensions = %d, want 1024", cfg.Embedding.Dimensions)
	}
}

func TestLoadAppliesOverrides(t *testing.T) {
	t.Setenv("PIPELINE_BATCH_SIZE", "50")
	t.Setenv("PIPELINE_WORKERS", "8")
	t.Setenv("PIPELINE_DATA_DIR", "/tmp/corpus")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Pipeline.BatchSize != 50 || cfg.Pipeline.Workers != 8 {
		t.Errorf("overrides not applied: %+v", cfg.Pipeline)
	}
	if cfg.Pipeline.DataDir != "/tmp/corpus" {
		t.Errorf("DataDir = %q", cfg.Pipeline.DataDir)
	}
}

func TestLoadRejectsMalformedValues(t *testing.T) {
	t.Setenv("PIPELINE_BATCH_SIZE", "lots")
	if _, err := Load(); err == nil {
		t.Fatal("want error for malformed PIPELINE_BATCH_SIZE")
	}
}

func TestValidateRejectsBadPipelineValues(t *testing.T) {
	cfg := Config{
		App: sharedcfg.AppConfig{Env: sharedcfg.EnvDev},
		Log: sharedcfg.LogConfig{Level: "info"},
		Postgres: sharedcfg.PostgresConfig{
			Database: "platepilot",
		},
		Embedding: sharedcfg.EmbeddingConfig{
			BaseURL: "http://localhost:11434", Model: "qwen3-embedding:0.6b", Dimensions: 1024,
		},
		Pipeline: PipelineConfig{DataDir: "", BatchSize: 0, Workers: -1},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("want validation error")
	}
	var verr *sharedcfg.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("want *ValidationError, got %T", err)
	}
	for _, want := range []string{"PIPELINE_DATA_DIR", "PIPELINE_BATCH_SIZE", "PIPELINE_WORKERS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name %s", err.Error(), want)
		}
	}
}

func TestValidateAcceptsDefaults(t *testing.T) {
	cfg := Config{
		App: sharedcfg.AppConfig{Env: sharedcfg.EnvTest},
		Log: sharedcfg.LogConfig{Level: "debug"},
		Pipeline: PipelineConfig{
			DataDir: DefaultDataDir, BatchSize: 100, Workers: 2,
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults should validate, got %v", err)
	}
}

func TestRedactedHidesPostgresCredentials(t *testing.T) {
	const dsn = "postgres://platepilot:hunter2@localhost:55432/platepilot"
	cfg := Config{Postgres: sharedcfg.PostgresConfig{DSN: dsn}}
	redacted := cfg.Redacted()
	if strings.Contains(redacted.Postgres.DSN, "hunter2") {
		t.Fatalf("credentials not redacted: %q", redacted.Postgres.DSN)
	}
	if cfg.Postgres.DSN != dsn {
		t.Error("Redacted mutated the receiver")
	}
}

// A libpq keyword/value DSN carries the password too, and is the form pgx
// documents, so it must be redacted as well.
func TestRedactedHidesKeywordDSNCredentials(t *testing.T) {
	const dsn = "host=localhost dbname=platepilot user=platepilot password=hunter2"
	cfg := Config{Postgres: sharedcfg.PostgresConfig{DSN: dsn}}
	if strings.Contains(cfg.Redacted().Postgres.DSN, "hunter2") {
		t.Fatalf("keyword/value credentials not redacted: %q", cfg.Redacted().Postgres.DSN)
	}
}
