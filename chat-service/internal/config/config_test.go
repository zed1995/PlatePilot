package config

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	sharedcfg "github.com/zed/platepilot/shared/config"
)

func baseConfig() Config {
	return Config{
		App: sharedcfg.AppConfig{Env: sharedcfg.EnvDev},
		Log: sharedcfg.LogConfig{Level: "info"},
		HTTP: HTTPConfig{
			Addr:            ":8080",
			ReadTimeout:     10 * time.Second,
			WriteTimeout:    30 * time.Second,
			ShutdownTimeout: 10 * time.Second,
		},
		Mongo: sharedcfg.MongoConfig{Database: "platepilot", Timeout: 10 * time.Second},
		Embedding: sharedcfg.EmbeddingConfig{
			BaseURL:    "http://localhost:11434",
			Model:      "qwen3-embedding:0.6b",
			Dimensions: 1024,
		},
	}
}

func TestValidateAcceptsM0Defaults(t *testing.T) {
	if err := baseConfig().Validate(); err != nil {
		t.Fatalf("default config should be valid, got %v", err)
	}
}

func TestValidateReportsEveryProblemWithVariableName(t *testing.T) {
	cfg := baseConfig()
	cfg.HTTP.Addr = "::::"
	cfg.Log.Level = "loud"
	cfg.App.Env = "staging"

	err := cfg.Validate()
	var verr *sharedcfg.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("want *ValidationError, got %T", err)
	}
	for _, want := range []string{"HTTP_ADDR", "LOG_LEVEL", "APP_ENV"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name %s", err.Error(), want)
		}
	}
}

func TestValidateRequiresChatFieldsOnlyWhenProviderConfigured(t *testing.T) {
	cfg := baseConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("chat disabled should not require chat fields: %v", err)
	}

	cfg.Chat.Provider = "openai_compatible"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("want error when CHAT_PROVIDER set without base_url/key/model")
	}
	for _, want := range []string{"CHAT_BASE_URL", "CHAT_API_KEY", "CHAT_MODEL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name %s", err.Error(), want)
		}
	}
}

func TestValidateRequiresEmbeddingDimensions(t *testing.T) {
	cfg := baseConfig()
	cfg.Embedding.Provider = "ollama"
	cfg.Embedding.Dimensions = 0
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "EMBEDDING_DIMENSIONS") {
		t.Fatalf("want EMBEDDING_DIMENSIONS problem, got %v", err)
	}
}

func TestLoadAppliesDefaultsAndOverrides(t *testing.T) {
	t.Setenv("HTTP_ADDR", ":9090")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTP.Addr != ":9090" {
		t.Errorf("HTTP_ADDR override not applied: %q", cfg.HTTP.Addr)
	}
	if cfg.Embedding.Dimensions != 1024 {
		t.Errorf("default embedding dimensions = %d, want 1024", cfg.Embedding.Dimensions)
	}
}

func TestLoadRejectsMalformedValues(t *testing.T) {
	t.Setenv("REQUEST_TIMEOUT", "not-a-duration")
	if _, err := Load(); err == nil {
		t.Fatal("want error for malformed REQUEST_TIMEOUT")
	}

	t.Setenv("REQUEST_TIMEOUT", "15s")
	t.Setenv("EMBEDDING_DIMENSIONS", "many")
	if _, err := Load(); err == nil {
		t.Fatal("want error for malformed EMBEDDING_DIMENSIONS")
	}
}

func TestLoadParsesExtraHeadersJSON(t *testing.T) {
	t.Setenv("CHAT_EXTRA_HEADERS_JSON", `{"X-Title":"PlatePilot"}`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Chat.ExtraHeaders["X-Title"] != "PlatePilot" {
		t.Fatalf("extra headers not parsed: %v", cfg.Chat.ExtraHeaders)
	}
}

func TestRedactedRemovesSecrets(t *testing.T) {
	cfg := baseConfig()
	cfg.Chat.APIKey = "super-secret"
	cfg.Chat.ExtraHeaders = map[string]string{"Authorization": "Bearer super-secret"}
	cfg.Mongo.URI = "mongodb+srv://user:pass@cluster.example/"

	redacted := cfg.Redacted()
	if redacted.Chat.APIKey != "***" {
		t.Errorf("API key not redacted: %q", redacted.Chat.APIKey)
	}
	if redacted.Chat.ExtraHeaders["Authorization"] != "***" {
		t.Errorf("extra headers not redacted: %v", redacted.Chat.ExtraHeaders)
	}
	if strings.Contains(redacted.Mongo.URI, "pass") {
		t.Errorf("mongo credentials not redacted: %q", redacted.Mongo.URI)
	}
	if cfg.Chat.APIKey != "super-secret" {
		t.Error("Redacted mutated the receiver")
	}
}

func TestSummaryOmitsSecrets(t *testing.T) {
	cfg := baseConfig()
	cfg.Chat.APIKey = "super-secret"
	cfg.Mongo.URI = "mongodb+srv://user:pass@cluster.example/"
	rendered := fmt.Sprint(cfg.Redacted().Summary())
	if strings.Contains(rendered, "super-secret") || strings.Contains(rendered, "pass") {
		t.Fatalf("summary leaked a secret: %s", rendered)
	}
}
