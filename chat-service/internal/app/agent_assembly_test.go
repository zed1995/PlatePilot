package app

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/zed1995/platepilot/chat-service/internal/config"
	"github.com/zed1995/platepilot/shared/testkit"
)

func assemblyConfig() config.Config {
	return config.Config{
		Chat: config.ChatConfig{Model: "vendor/test-model", SupportsTools: true},
		Agent: config.AgentConfig{
			MaxToolRounds: 5,
			ToolTimeout:   15 * time.Second,
		},
	}
}

func TestNewAssemblesAgentRunnerWhenChatProviderPresent(t *testing.T) {
	chatModel := &fakeChatModel{name: "test-model"}
	application, err := New(assemblyConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)), Deps{
		PlanModel:    chatModel,
		AnswerModel:  chatModel,
		ExtractModel: chatModel,
		Restaurants:  testkit.NewRestaurantRepository(),
		Knowledge:    testkit.NewKnowledgeRepository(),
	}, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if application.agent == nil {
		t.Fatal("agent runner should be assembled with a chat provider")
	}
}

func TestNewSkipsAgentRunnerWithoutChatProvider(t *testing.T) {
	application, err := New(assemblyConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)), Deps{
		Restaurants: testkit.NewRestaurantRepository(),
		Knowledge:   testkit.NewKnowledgeRepository(),
	}, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if application.agent != nil {
		t.Fatal("agent runner should stay unwired without a chat provider")
	}
}
