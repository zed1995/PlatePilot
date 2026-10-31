package app

// The assembly's half of per-node model allocation.
//
// The agent package proves the runner sends plan to Deps.PlanModel and answers
// through Deps.AnswerModel. What only the application can prove is that the
// three model instances it is given are handed to the right job at all — an
// assembly that wired one instance to two jobs would leave every agent test
// green while the deployment silently ignored its configured models.

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/zed1995/platepilot/chat-service/internal/agent"
	"github.com/zed1995/platepilot/shared/testkit"
)

func TestTheAssembledAppUsesTheInjectedModels(t *testing.T) {
	planner := &fakeChatModel{
		name:  "planner",
		resps: []*schema.Message{schema.AssistantMessage("你好。", nil)},
	}
	writer := &fakeChatModel{name: "writer"}
	reader := &fakeChatModel{
		name: "reader",
		resps: []*schema.Message{
			schema.AssistantMessage(`{"intent":"discover","query":"有什么推荐"}`, nil),
		},
	}

	application, err := New(assemblyConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)), Deps{
		PlanModel:     planner,
		AnswerModel:   writer,
		ExtractModel:  reader,
		Restaurants:   testkit.NewRestaurantRepository(),
		Knowledge:     testkit.NewKnowledgeRepository(),
		Conversations: testkit.NewConversationRepository(),
	}, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if application.agent == nil {
		t.Fatal("agent runner should be assembled with a chat model")
	}

	_, events := application.agent.Run(context.Background(), agent.TurnInput{
		ThreadID:  "th-node-models",
		UserID:    "u1",
		UserInput: "有什么推荐",
	})
	for range events {
	}

	// Extraction runs once at ingress, before any planning round.
	if reader.calls != 1 {
		t.Errorf("extraction reached the extract model %d times, want 1", reader.calls)
	}
	// The planning round is the turn's decision and the only model call a
	// non-grounding turn makes.
	if planner.calls == 0 {
		t.Fatal("the planning round never reached the plan model")
	}
	// Nothing was retrieved, so the turn answered from the plan model's own
	// text and the answer model stayed untouched.
	if writer.calls != 0 {
		t.Errorf("the answer model was called %d times on a non-grounding turn", writer.calls)
	}
}
