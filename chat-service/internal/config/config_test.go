package config

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	sharedcfg "github.com/zed1995/platepilot/shared/config"
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
			MaxToolRounds:        defaultAgentMaxToolRounds,
			ToolTimeout:          defaultAgentToolTimeout,
			SlotExtractTimeout:   defaultAgentSlotExtractTimeout,
			MaxClarifications:    defaultAgentMaxClarifications,
			ResolveMinSimilarity: defaultAgentResolveMinSimilarity,
			ResolveAmbiguityGap:  defaultAgentResolveAmbiguityGap,
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
	if cfg.Agent.SlotExtractTimeout != defaultAgentSlotExtractTimeout {
		t.Fatalf("slot extract timeout = %s, want %s",
			cfg.Agent.SlotExtractTimeout, defaultAgentSlotExtractTimeout)
	}
	if cfg.Agent.MaxClarifications != defaultAgentMaxClarifications {
		t.Fatalf("max clarifications = %d, want %d",
			cfg.Agent.MaxClarifications, defaultAgentMaxClarifications)
	}
	if cfg.Agent.ResolveMinSimilarity != defaultAgentResolveMinSimilarity {
		t.Fatalf("resolve min similarity = %g, want %g",
			cfg.Agent.ResolveMinSimilarity, defaultAgentResolveMinSimilarity)
	}
	if cfg.Agent.ResolveAmbiguityGap != defaultAgentResolveAmbiguityGap {
		t.Fatalf("resolve ambiguity gap = %g, want %g",
			cfg.Agent.ResolveAmbiguityGap, defaultAgentResolveAmbiguityGap)
	}
	if _, ok := cfg.Summary()["agent_max_tool_rounds"]; !ok {
		t.Fatal("summary missing agent_max_tool_rounds")
	}
	// A knob that is not in the startup summary is a knob nobody knows is set.
	for _, key := range []string{
		"agent_slot_extract_timeout", "agent_max_clarifications",
		"agent_resolve_min_similarity", "agent_resolve_ambiguity_gap",
	} {
		if _, ok := cfg.Summary()[key]; !ok {
			t.Errorf("summary missing %s", key)
		}
	}
}

func TestLoadParsesAgentOverrides(t *testing.T) {
	t.Setenv("AGENT_MAX_TOOL_ROUNDS", "8")
	t.Setenv("AGENT_TOOL_TIMEOUT", "2500ms")
	t.Setenv("AGENT_SLOT_EXTRACT_TIMEOUT", "4s")
	t.Setenv("AGENT_MAX_CLARIFICATIONS", "5")
	t.Setenv("AGENT_RESOLVE_MIN_SIMILARITY", "0.7")
	t.Setenv("AGENT_RESOLVE_AMBIGUITY_GAP", "0.2")
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
	if cfg.Agent.SlotExtractTimeout != 4*time.Second {
		t.Fatalf("slot extract timeout = %s, want 4s", cfg.Agent.SlotExtractTimeout)
	}
	if cfg.Agent.MaxClarifications != 5 {
		t.Fatalf("max clarifications = %d, want 5", cfg.Agent.MaxClarifications)
	}
	if cfg.Agent.ResolveMinSimilarity != 0.7 {
		t.Fatalf("resolve min similarity = %g, want 0.7", cfg.Agent.ResolveMinSimilarity)
	}
	if cfg.Agent.ResolveAmbiguityGap != 0.2 {
		t.Fatalf("resolve ambiguity gap = %g, want 0.2", cfg.Agent.ResolveAmbiguityGap)
	}
}

func TestValidateRejectsNonPositiveAgentKnobs(t *testing.T) {
	cfg := baseConfig()
	cfg.Agent.MaxToolRounds = 0
	cfg.Agent.ToolTimeout = 0
	cfg.Agent.SlotExtractTimeout = 0
	cfg.Agent.MaxClarifications = 0
	cfg.Agent.ResolveMinSimilarity = 0
	cfg.Agent.ResolveAmbiguityGap = -1
	err := cfg.Validate()
	if err == nil {
		t.Fatal("want validation error for non-positive agent knobs")
	}
	for _, want := range []string{
		"AGENT_MAX_TOOL_ROUNDS", "AGENT_TOOL_TIMEOUT", "AGENT_SLOT_EXTRACT_TIMEOUT",
		"AGENT_MAX_CLARIFICATIONS", "AGENT_RESOLVE_MIN_SIMILARITY", "AGENT_RESOLVE_AMBIGUITY_GAP",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name %s", err.Error(), want)
		}
	}
}

// TestValidateRejectsOutOfRangeSimilarity keeps a knob whose scale is a
// similarity inside its own domain: a value above 1 would make every name match
// fail, which reads as "no such restaurant" rather than as a misconfiguration.
func TestValidateRejectsOutOfRangeSimilarity(t *testing.T) {
	cfg := baseConfig()
	cfg.Agent.ResolveMinSimilarity = 1.5
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "AGENT_RESOLVE_MIN_SIMILARITY") {
		t.Fatalf("want AGENT_RESOLVE_MIN_SIMILARITY out of range, got %v", err)
	}
}

// The memory write path defaults to on. It is the only way a memory is ever
// created, and its guard is an input check rather than a confirmation step, so
// switching it off removes a capability rather than closing a hole.
func TestMemoryWritesAreEnabledByDefault(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Agent.MemoryWriteEnabled {
		t.Fatal("memory writes are off without AGENT_MEMORY_WRITE_ENABLED")
	}
}

func TestLoadParsesTheMemoryWriteSwitch(t *testing.T) {
	t.Setenv("AGENT_MEMORY_WRITE_ENABLED", "false")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Agent.MemoryWriteEnabled {
		t.Fatal("AGENT_MEMORY_WRITE_ENABLED=false was ignored")
	}
	if got := fmt.Sprint(cfg.Summary()["agent_memory_write_enabled"]); got != "false" {
		t.Fatalf("summary reports agent_memory_write_enabled=%s", got)
	}
}

// Turning the write path off is a supported configuration, so it must not be a
// validation failure: an operator who does not want the capability has to be
// able to say so without the service refusing to start.
func TestTurningMemoryWritesOffIsValidConfiguration(t *testing.T) {
	cfg := baseConfig()
	cfg.Agent.MemoryWriteEnabled = false
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a deployment without memory writes failed validation: %v", err)
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

// The three per-node models are optional. A deployment that names only
// CHAT_MODEL has to keep getting that one model for all three jobs — the
// override is an addition to the environment contract, not a second way to
// configure the model that everyone must now know about.
func TestPerNodeModelsFallBackToChatModel(t *testing.T) {
	t.Setenv("CHAT_MODEL", "vendor/base-model")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for name, got := range map[string]string{
		"CHAT_MODEL":         cfg.Chat.Model,
		"CHAT_MODEL_PLAN":    cfg.Chat.PlanModel,
		"CHAT_MODEL_ANSWER":  cfg.Chat.AnswerModel,
		"CHAT_MODEL_EXTRACT": cfg.Chat.ExtractModel,
	} {
		if got != "vendor/base-model" {
			t.Errorf("%s = %q, want the base model", name, got)
		}
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestLoadParsesPerNodeModelOverrides(t *testing.T) {
	t.Setenv("CHAT_MODEL", "vendor/base-model")
	t.Setenv("CHAT_MODEL_PLAN", "vendor/planner")
	t.Setenv("CHAT_MODEL_ANSWER", "vendor/writer")
	t.Setenv("CHAT_MODEL_EXTRACT", "vendor/reader")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Chat.Model != "vendor/base-model" {
		t.Errorf("CHAT_MODEL = %q, want it left alone by the overrides", cfg.Chat.Model)
	}
	for name, got := range map[string]string{
		"CHAT_MODEL_PLAN":    cfg.Chat.PlanModel,
		"CHAT_MODEL_ANSWER":  cfg.Chat.AnswerModel,
		"CHAT_MODEL_EXTRACT": cfg.Chat.ExtractModel,
	} {
		if got == "" || got == "vendor/base-model" {
			t.Errorf("%s = %q, want its own model", name, got)
		}
	}
	if cfg.Chat.PlanModel != "vendor/planner" ||
		cfg.Chat.AnswerModel != "vendor/writer" ||
		cfg.Chat.ExtractModel != "vendor/reader" {
		t.Errorf("overrides landed in the wrong fields: %+v", cfg.Chat)
	}
}

// Naming one node must not move the others off the base model: routing is the
// point, and a single override that quietly un-routed the two it did not name
// would send them to the provider's default model instead.
func TestAPerNodeOverrideLeavesTheOthersOnTheBaseModel(t *testing.T) {
	t.Setenv("CHAT_MODEL", "vendor/base-model")
	t.Setenv("CHAT_MODEL_ANSWER", "vendor/writer")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Chat.AnswerModel != "vendor/writer" {
		t.Fatalf("CHAT_MODEL_ANSWER = %q", cfg.Chat.AnswerModel)
	}
	if cfg.Chat.PlanModel != "vendor/base-model" || cfg.Chat.ExtractModel != "vendor/base-model" {
		t.Fatalf("unnamed nodes did not stay on the base model: %+v", cfg.Chat)
	}
}

// An override set to nothing is not an override. `CHAT_MODEL_PLAN=` occurs
// naturally when an operator comments out the value but leaves the assignment,
// and it must not mean "let the provider pick", which would silently route the
// planning rounds away from the model the deployment configured.
func TestAnEmptyPerNodeOverrideIsNotAnOverride(t *testing.T) {
	t.Setenv("CHAT_MODEL", "vendor/base-model")
	t.Setenv("CHAT_MODEL_PLAN", "")
	t.Setenv("CHAT_MODEL_ANSWER", "   ")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Chat.PlanModel != "vendor/base-model" {
		t.Errorf("empty CHAT_MODEL_PLAN gave %q, want the base model", cfg.Chat.PlanModel)
	}
	if cfg.Chat.AnswerModel != "vendor/base-model" {
		t.Errorf("blank CHAT_MODEL_ANSWER gave %q, want the base model", cfg.Chat.AnswerModel)
	}
}

// The startup summary is where an operator checks what a deployment actually
// resolved to, so it has to report the effective models rather than the raw
// variables — three identical names are the honest answer for a deployment
// that overrode nothing.
func TestSummaryReportsTheEffectivePerNodeModels(t *testing.T) {
	cfg := baseConfig()
	cfg.Chat.Model = "vendor/base-model"
	cfg.Chat.PlanModel = "vendor/planner"
	cfg.Chat.AnswerModel = "vendor/writer"
	cfg.Chat.ExtractModel = "vendor/reader"
	summary := cfg.Summary()
	for key, want := range map[string]string{
		"chat_model":         "vendor/base-model",
		"chat_model_plan":    "vendor/planner",
		"chat_model_answer":  "vendor/writer",
		"chat_model_extract": "vendor/reader",
	} {
		if got := fmt.Sprint(summary[key]); got != want {
			t.Errorf("summary[%q] = %q, want %q", key, got, want)
		}
	}
}

// The mock reservation capability is off unless an operator says otherwise.
// That default is what keeps the only write path in the service opt-in.
func TestReservationIsDisabledByDefault(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Reservation.Enabled {
		t.Fatal("reservations are enabled without RESERVATION_ENABLED")
	}
	if cfg.Reservation.HoldTTL != 10*time.Minute {
		t.Fatalf("hold ttl = %s, want the default 10m", cfg.Reservation.HoldTTL)
	}
	if cfg.Reservation.PolicyVersion == "" {
		t.Fatal("a booking has no policy version to quote")
	}
}

func TestLoadParsesReservationOverrides(t *testing.T) {
	t.Setenv("RESERVATION_ENABLED", "true")
	t.Setenv("RESERVATION_HOLD_TTL", "90s")
	t.Setenv("RESERVATION_POLICY_VERSION", "mock-v9")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Reservation.Enabled {
		t.Fatal("RESERVATION_ENABLED=true was ignored")
	}
	if cfg.Reservation.HoldTTL != 90*time.Second {
		t.Fatalf("hold ttl = %s, want 90s", cfg.Reservation.HoldTTL)
	}
	if cfg.Reservation.PolicyVersion != "mock-v9" {
		t.Fatalf("policy version = %q, want mock-v9", cfg.Reservation.PolicyVersion)
	}
	if got := fmt.Sprint(cfg.Summary()["reservation_enabled"]); got != "true" {
		t.Fatalf("summary reports reservation_enabled=%s", got)
	}
}

// A hold that never expires keeps its seats forever, so the TTL is checked when
// the capability is on and ignored when it is off: refusing to start because an
// unused knob is nonsensical would block the deployments that never touch it.
func TestValidateChecksTheReservationTTLOnlyWhenEnabled(t *testing.T) {
	off := baseConfig()
	off.Reservation = ReservationConfig{Enabled: false, HoldTTL: 0}
	if err := off.Validate(); err != nil {
		t.Fatalf("a disabled reservation path failed validation: %v", err)
	}

	on := baseConfig()
	on.Reservation = ReservationConfig{Enabled: true, HoldTTL: 0, PolicyVersion: "mock-v1"}
	err := on.Validate()
	if err == nil || !strings.Contains(err.Error(), "RESERVATION_HOLD_TTL") {
		t.Fatalf("want RESERVATION_HOLD_TTL rejected, got %v", err)
	}

	blank := baseConfig()
	blank.Reservation = ReservationConfig{Enabled: true, HoldTTL: time.Minute, PolicyVersion: "  "}
	err = blank.Validate()
	if err == nil || !strings.Contains(err.Error(), "RESERVATION_POLICY_VERSION") {
		t.Fatalf("want RESERVATION_POLICY_VERSION rejected, got %v", err)
	}
}
