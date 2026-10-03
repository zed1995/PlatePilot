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
		Postgres: sharedcfg.PostgresConfig{Database: "platepilot", Timeout: 10 * time.Second},
		Agent: AgentConfig{
			MaxToolRounds: defaultAgentMaxToolRounds,
			ToolTimeout:   defaultAgentToolTimeout,
		},
		Retrieval: sharedcfg.RetrievalConfig{
			Weights:          sharedcfg.DefaultRetrievalWeights,
			Oversample:       2,
			TopK:             5,
			EnableStructured: true,
			EnableKeyword:    true,
			EnableVector:     true,
			EmbeddingTimeout: 5 * time.Second,
		},
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

func chatEnabledConfig() Config {
	cfg := baseConfig()
	cfg.Chat = ChatConfig{
		Provider:              "openai_compatible",
		BaseURL:               "https://openrouter.ai/api/v1",
		APIKey:                "test-key",
		Model:                 "vendor/test-model",
		Timeout:               defaultChatTimeout,
		MaxRetries:            defaultChatMaxRetries,
		SupportsTools:         defaultChatSupportsTools,
		SupportsParallelTools: defaultChatSupportsParallel,
		SupportsJSONSchema:    defaultChatSupportsJSONSchema,
		ContextTokens:         defaultChatContextTokens,
	}
	return cfg
}

func TestValidateAcceptsChatDefaultsWhenEnabled(t *testing.T) {
	if err := chatEnabledConfig().Validate(); err != nil {
		t.Fatalf("chat-enabled config with defaults should be valid: %v", err)
	}
}

func TestValidateChatRuntimeBounds(t *testing.T) {
	cases := map[string]func(*ChatConfig){
		"CHAT_TIMEOUT":        func(c *ChatConfig) { c.Timeout = 0 },
		"CHAT_MAX_RETRIES":    func(c *ChatConfig) { c.MaxRetries = -1 },
		"CHAT_CONTEXT_TOKENS": func(c *ChatConfig) { c.ContextTokens = 0 },
	}
	for wantVar, mutate := range cases {
		cfg := chatEnabledConfig()
		mutate(&cfg.Chat)
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), wantVar) {
			t.Errorf("want validation error naming %s, got %v", wantVar, err)
		}
	}
}

func TestLoadAppliesChatDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Chat.Timeout != defaultChatTimeout {
		t.Errorf("CHAT_TIMEOUT default = %s", cfg.Chat.Timeout)
	}
	if cfg.Chat.MaxRetries != defaultChatMaxRetries {
		t.Errorf("CHAT_MAX_RETRIES default = %d", cfg.Chat.MaxRetries)
	}
	if !cfg.Chat.SupportsTools || cfg.Chat.SupportsParallelTools || cfg.Chat.SupportsJSONSchema {
		t.Errorf("capability defaults wrong: %+v", cfg.Chat)
	}
	if cfg.Chat.ContextTokens != defaultChatContextTokens {
		t.Errorf("CHAT_CONTEXT_TOKENS default = %d", cfg.Chat.ContextTokens)
	}
}

func TestLoadParsesChatOverrides(t *testing.T) {
	t.Setenv("CHAT_TIMEOUT", "12s")
	t.Setenv("CHAT_MAX_RETRIES", "5")
	t.Setenv("CHAT_SUPPORTS_TOOLS", "false")
	t.Setenv("CHAT_SUPPORTS_PARALLEL_TOOLS", "true")
	t.Setenv("CHAT_SUPPORTS_JSON_SCHEMA", "true")
	t.Setenv("CHAT_CONTEXT_TOKENS", "200000")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := ChatConfig{
		Timeout:               12 * time.Second,
		MaxRetries:            5,
		SupportsTools:         false,
		SupportsParallelTools: true,
		SupportsJSONSchema:    true,
		ContextTokens:         200000,
	}
	if cfg.Chat.Timeout != want.Timeout ||
		cfg.Chat.MaxRetries != want.MaxRetries ||
		cfg.Chat.SupportsTools != want.SupportsTools ||
		cfg.Chat.SupportsParallelTools != want.SupportsParallelTools ||
		cfg.Chat.SupportsJSONSchema != want.SupportsJSONSchema ||
		cfg.Chat.ContextTokens != want.ContextTokens {
		t.Errorf("chat overrides not applied: %+v", cfg.Chat)
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
	cfg.Postgres.DSN = "postgres://platepilot:hunter2@localhost:55432/platepilot"

	redacted := cfg.Redacted()
	if redacted.Chat.APIKey != "***" {
		t.Errorf("API key not redacted: %q", redacted.Chat.APIKey)
	}
	if redacted.Chat.ExtraHeaders["Authorization"] != "***" {
		t.Errorf("extra headers not redacted: %v", redacted.Chat.ExtraHeaders)
	}
	if strings.Contains(redacted.Postgres.DSN, "hunter2") {
		t.Errorf("postgres credentials not redacted: %q", redacted.Postgres.DSN)
	}
	if cfg.Chat.APIKey != "super-secret" {
		t.Error("Redacted mutated the receiver")
	}
}

func TestLoadAppliesAgentDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Agent.MaxToolRounds != defaultAgentMaxToolRounds {
		t.Fatalf("max tool rounds = %d, want %d", cfg.Agent.MaxToolRounds, defaultAgentMaxToolRounds)
	}
	if cfg.Agent.ToolTimeout != defaultAgentToolTimeout {
		t.Fatalf("tool timeout = %s, want %s", cfg.Agent.ToolTimeout, defaultAgentToolTimeout)
	}
	if _, ok := cfg.Summary()["agent_max_tool_rounds"]; !ok {
		t.Fatal("summary missing agent_max_tool_rounds")
	}
}

func TestLoadParsesAgentOverrides(t *testing.T) {
	t.Setenv("AGENT_MAX_TOOL_ROUNDS", "8")
	t.Setenv("AGENT_TOOL_TIMEOUT", "2500ms")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Agent.MaxToolRounds != 8 {
		t.Fatalf("max tool rounds = %d, want 8", cfg.Agent.MaxToolRounds)
	}
	if cfg.Agent.ToolTimeout != 2500*time.Millisecond {
		t.Fatalf("tool timeout = %s, want 2500ms", cfg.Agent.ToolTimeout)
	}
}

func TestValidateRejectsNonPositiveAgentKnobs(t *testing.T) {
	cfg := baseConfig()
	cfg.Agent.MaxToolRounds = 0
	cfg.Agent.ToolTimeout = 0
	err := cfg.Validate()
	if err == nil {
		t.Fatal("want validation error for non-positive agent knobs")
	}
	for _, want := range []string{"AGENT_MAX_TOOL_ROUNDS", "AGENT_TOOL_TIMEOUT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name %s", err.Error(), want)
		}
	}
}

func TestSummaryOmitsSecrets(t *testing.T) {
	cfg := baseConfig()
	cfg.Chat.APIKey = "super-secret"
	cfg.Postgres.DSN = "postgres://platepilot:hunter2@localhost:55432/platepilot"
	rendered := fmt.Sprint(cfg.Redacted().Summary())
	if strings.Contains(rendered, "super-secret") || strings.Contains(rendered, "hunter2") {
		t.Fatalf("summary leaked a secret: %s", rendered)
	}
}
