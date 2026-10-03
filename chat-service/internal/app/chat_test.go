package app

import (
	"strings"
	"testing"
	"time"

	"github.com/zed/platepilot/chat-service/internal/config"
)

func validChatConfig() config.ChatConfig {
	return config.ChatConfig{
		Provider:              providerOpenAICompatible,
		BaseURL:               "https://openrouter.ai/api/v1",
		APIKey:                "test-key",
		Model:                 "vendor/test-model",
		Timeout:               60 * time.Second,
		MaxRetries:            2,
		SupportsTools:         true,
		SupportsParallelTools: false,
		SupportsJSONSchema:    false,
		ContextTokens:         32768,
	}
}

func TestBuildChatClient(t *testing.T) {
	client, err := buildChatClient(validChatConfig())
	if err != nil {
		t.Fatalf("buildChatClient: %v", err)
	}
	if client == nil || client.ModelID() != "vendor/test-model" {
		t.Fatalf("unexpected client: %+v", client)
	}
	if !client.SupportsTools() || client.SupportsParallelTools() || client.SupportsJSONSchema() {
		t.Errorf("capability mapping wrong: tools=%v parallel=%v json=%v",
			client.SupportsTools(), client.SupportsParallelTools(), client.SupportsJSONSchema())
	}
}

func TestBuildChatClientRejectsUnknownProvider(t *testing.T) {
	cfg := validChatConfig()
	cfg.Provider = "some-other-vendor"
	_, err := buildChatClient(cfg)
	if err == nil || !strings.Contains(err.Error(), "CHAT_PROVIDER") {
		t.Fatalf("want CHAT_PROVIDER error, got %v", err)
	}
}
