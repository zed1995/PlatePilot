package agent_test

// The runner's half of per-node model allocation.
//
// Which model each job uses is decided by configuration (chat-service/internal/
// config) and carried by the model instance the application builds. The runner
// receives them as Deps.PlanModel and Deps.AnswerModel, so the property worth
// asserting is which instance a job reaches — not a name on a request, because
// a model instance carries its own identity and the request carries none.

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
// two different model instances and checks which one each job reached.
//
// A turn is used rather than a constructor call because the two jobs are not
// distinguishable from the outside otherwise: both are "the chat model", and an
// implementation that passed the plan model to the composer would look correct
// until a deployment tried to route them differently.
func TestThePlannerAndTheComposerUseDifferentModels(t *testing.T) {
	ctx := context.Background()
	service := seededSearchService(t,
		rankedDetail(21, "Trattoria Ventuno", "manhattan", []string{"italian"}, 2, 4.7),
	)
	reader := &followUpEvidence{}

	// The answer cites the document the stub recalls, so the composer's
	// citation check passes and the composed text is what the turn publishes.
	citation := evidenceIDFor(21, 1)
	planner := &scriptedProvider{
		name:         "planner",
		supportTools: true,
		toolResps: []domainchat.ToolCallResponse{
			toolCallResponse("c1", tools.SearchRestaurantsToolName,
				`{"cuisine":"italian","borough":"manhattan"}`),
			toolCallResponse("c2", tools.RestaurantEvidenceToolName,
				`{"restaurant_ids":[21],"query":"安静吗"}`),
			assistantText("这家很安静。"),
		},
	}
	writer := &scriptedProvider{
		name: "writer",
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

	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 4}, agent.Deps{
		PlanModel:   planner,
		AnswerModel: writer,
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
	if len(planner.toolReqs) == 0 {
		t.Fatal("the planning round never reached the plan model")
	}
	if len(planner.completeReqs) != 0 {
		t.Fatalf("the plan model answered %d times; answering belongs to the answer model",
			len(planner.completeReqs))
	}
	if len(writer.completeReqs) == 0 {
		t.Fatal("the composed answer never reached the answer model")
	}
	if len(writer.toolReqs) != 0 {
		t.Fatalf("the answer model planned %d rounds; planning belongs to the plan model",
			len(writer.toolReqs))
	}
}

// An unset AnswerModel means "the same instance as PlanModel", so a caller that
// names one model keeps getting one model — the override is an addition, not a
// second thing every caller must now set.
func TestAnUnsetAnswerModelFollowsThePlanModel(t *testing.T) {
	ctx := context.Background()
	service := seededSearchService(t,
		rankedDetail(21, "Trattoria Ventuno", "manhattan", []string{"italian"}, 2, 4.7),
	)
	reader := &followUpEvidence{}
	citation := evidenceIDFor(21, 1)

	provider := &scriptedProvider{
		name:         "only-model",
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

	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 4}, agent.Deps{
		PlanModel: provider,
		Registry:  registry,
	})
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
		t.Fatal("the composed answer never reached the one configured model")
	}
}
