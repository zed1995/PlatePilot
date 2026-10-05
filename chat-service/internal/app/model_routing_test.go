package app

// The assembly's half of per-node model allocation.
//
// The config package proves the environment resolves to three model names and
// the agent package proves the runner sends the two it owns. Neither would
// catch the application handing the same name to all three constructors — which
// is what the code did before this item, and is exactly the failure mode of a
// change whose whole value is that the names differ. So this drives a real
// assembled application and reads the model off each request.

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/zed1995/platepilot/chat-service/internal/agent"
	"github.com/zed1995/platepilot/chat-service/internal/config"
	"github.com/zed1995/platepilot/shared/testkit"
)

func TestTheAssembledAppRoutesEachNodeToItsConfiguredModel(t *testing.T) {
	provider := &testkit.MockChatProvider{Tools: true}

	cfg := assemblyConfig()
	cfg.Chat = config.ChatConfig{
		Provider:     "openai_compatible",
		Model:        "vendor/base-model",
		PlanModel:    "vendor/planner",
		AnswerModel:  "vendor/writer",
		ExtractModel: "vendor/reader",
	}

	application, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), Deps{
		Chat:          provider,
		ToolCalling:   provider,
		Structured:    provider,
		Restaurants:   testkit.NewRestaurantRepository(),
		Knowledge:     testkit.NewKnowledgeRepository(),
		Conversations: testkit.NewConversationRepository(),
	}, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if application.agent == nil {
		t.Fatal("agent runner should be assembled with a chat provider")
	}

	_, events := application.agent.Run(context.Background(), agent.TurnInput{
		ThreadID:  "th-node-models",
		UserID:    "u1",
		UserInput: "曼哈顿有什么安静的意大利餐厅？",
	})
	for range events {
	}

	// The planning rounds are the turn's decisions, and they are the requests
	// that carry the tool specs.
	if len(provider.ToolCalls) == 0 {
		t.Fatal("the planning round never reached the provider")
	}
	if got := provider.ToolCalls[0].Model; got != "vendor/planner" {
		t.Errorf("planning asked for %q, want the plan model", got)
	}

	// Extraction runs once, at ingress, before any planning round.
	if len(provider.StructuredCalls) == 0 {
		t.Fatal("the slot extraction never reached the provider")
	}
	if got := provider.StructuredCalls[0].Model; got != "vendor/reader" {
		t.Errorf("extraction asked for %q, want the extract model", got)
	}
}
