package agent_test

// The runner's half of per-node model allocation.
//
// Which model each job uses is decided by configuration (chat-service/internal/
// config), and applied by whichever constructor is asked to build that job.
// There are three of them and they are built in two places: the slot extractor
// is assembled by the application, while the planning model and the answer
// composer are both built inside NewRunner. Nothing outside this package can
// read the name the composer was given, so the assertion has to be made where
// it is observable — on the request that reaches the provider.

import (
	"context"
	"fmt"
	"testing"

	domainchat "github.com/zed1995/platepilot/shared/domain/chat"

	"github.com/zed1995/platepilot/chat-service/internal/agent"
	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
	"github.com/zed1995/platepilot/chat-service/internal/agent/tools"
)

// TestThePlannerAndTheComposerUseDifferentModels runs one grounded turn with
// two different models configured and checks which one each request carried.
//
// A turn is used rather than a constructor call because the two jobs are not
// distinguishable from the outside otherwise: both are "the chat provider", and
// an implementation that passed the plan model to the composer would look
// correct until a deployment tried to route them differently.
func TestThePlannerAndTheComposerUseDifferentModels(t *testing.T) {
	ctx := context.Background()
	service := seededSearchService(t,
		rankedDetail(21, "Trattoria Ventuno", "manhattan", []string{"italian"}, 2, 4.7),
	)
	reader := &followUpEvidence{}

	// The answer cites the document the stub recalls, so the composer's
	// citation check passes and the composed text is what the turn publishes.
	citation := evidenceIDFor(21, 1)
	provider := &scriptedProvider{
		supportTools: true,
		toolResps: []domainchat.ToolCallResponse{
			toolCallResponse("c1", tools.SearchRestaurantsToolName,
				`{"cuisine":"italian","borough":"manhattan"}`),
			toolCallResponse("c2", tools.RestaurantEvidenceToolName,
				`{"restaurant_ids":[21],"query":"安静吗"}`),
			assistantText("这家很安静。"),
		},
		completeResps: []domainchat.ChatResponse{{
			Message: domainchat.ChatMessage{
				Role:    domainchat.RoleAssistant,
				Content: fmt.Sprintf("这家店晚上很安静[^%d]。\nFOLLOWUPS: []", citation),
			},
			FinishReason: domainchat.FinishReasonStop,
		}},
	}

	registry := toolreg.New(0)
	register(t, registry, tools.SearchRestaurantsEntry(service))
	register(t, registry, tools.RestaurantEvidenceEntry(reader))

	runner, err := agent.NewRunner(agent.Config{
		MaxToolRounds: 4,
		ModelName:     "vendor/planner",
		AnswerModel:   "vendor/writer",
	}, agent.Deps{
		Chat:        provider,
		ToolCalling: provider,
		Registry:    registry,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	result, events := runner.Run(ctx, agent.TurnInput{
		ThreadID:  "th-models",
		UserID:    "u1",
		UserInput: "曼哈顿有什么安静的意大利餐厅？",
	})
	drain(events)

	if result == nil || result.Answer == nil {
		t.Fatal("the turn produced no answer")
	}
	if len(provider.toolReqs) == 0 {
		t.Fatal("the planning round never reached the provider")
	}
	for i, req := range provider.toolReqs {
		if req.req.Model != "vendor/planner" {
			t.Fatalf("planning round %d asked for %q, want the plan model", i, req.req.Model)
		}
	}
	if len(provider.completeReqs) == 0 {
		t.Fatal("the composed answer never reached the provider")
	}
	if got := provider.completeReqs[0].Model; got != "vendor/writer" {
		t.Fatalf("the answer was composed with %q, want the answer model", got)
	}
}

// An unset AnswerModel means "the same as ModelName", so a caller that names
// one model keeps getting one model — the override is an addition, not a
// second thing every caller must now set.
func TestAnUnsetAnswerModelFollowsThePlanModel(t *testing.T) {
	ctx := context.Background()
	service := seededSearchService(t,
		rankedDetail(21, "Trattoria Ventuno", "manhattan", []string{"italian"}, 2, 4.7),
	)
	reader := &followUpEvidence{}
	citation := evidenceIDFor(21, 1)

	provider := &scriptedProvider{
		supportTools: true,
		toolResps: []domainchat.ToolCallResponse{
			toolCallResponse("c1", tools.SearchRestaurantsToolName, `{"cuisine":"italian"}`),
			toolCallResponse("c2", tools.RestaurantEvidenceToolName,
				`{"restaurant_ids":[21],"query":"安静吗"}`),
			assistantText("这家很安静。"),
		},
		completeResps: []domainchat.ChatResponse{{
			Message: domainchat.ChatMessage{
				Role:    domainchat.RoleAssistant,
				Content: fmt.Sprintf("这家店晚上很安静[^%d]。\nFOLLOWUPS: []", citation),
			},
			FinishReason: domainchat.FinishReasonStop,
		}},
	}

	registry := toolreg.New(0)
	register(t, registry, tools.SearchRestaurantsEntry(service))
	register(t, registry, tools.RestaurantEvidenceEntry(reader))

	runner, err := agent.NewRunner(agent.Config{
		MaxToolRounds: 4,
		ModelName:     "vendor/only-model",
	}, agent.Deps{Chat: provider, ToolCalling: provider, Registry: registry})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	_, events := runner.Run(ctx, agent.TurnInput{
		ThreadID:  "th-one-model",
		UserID:    "u1",
		UserInput: "曼哈顿有什么安静的意大利餐厅？",
	})
	drain(events)

	if len(provider.completeReqs) == 0 {
		t.Fatal("the composed answer never reached the provider")
	}
	if got := provider.completeReqs[0].Model; got != "vendor/only-model" {
		t.Fatalf("the answer was composed with %q, want the one configured model", got)
	}
}
