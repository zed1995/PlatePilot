package agent

import (
	"context"
	"strings"
	"testing"

	chatport "github.com/zed1995/platepilot/shared/chat"
	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/search"

	"github.com/zed1995/platepilot/chat-service/internal/agent/answer"
	"github.com/zed1995/platepilot/chat-service/internal/agent/slots"
)

// nodesFactChat is a non-streaming scripted provider for the answer composer.
type nodesFactChat struct {
	content string
	calls   int
}

func (c *nodesFactChat) Complete(_ context.Context, req domainchat.ChatRequest) (domainchat.ChatResponse, error) {
	c.calls++
	return domainchat.ChatResponse{
		Message:      domainchat.ChatMessage{Role: domainchat.RoleAssistant, Content: c.content},
		FinishReason: domainchat.FinishReasonStop,
	}, nil
}

func (c *nodesFactChat) Stream(context.Context, domainchat.ChatRequest) (chatport.ChatStream, error) {
	return nil, nil
}

// A turn whose search returned candidates but which never recalled evidence
// has to go through the grounded composer: the candidate facts (rating,
// price, cuisine) are answerable material. The pre-fix switch fell through to
// the default branch, which either shipped unprompted model text or the fixed
// refusal when the model wrote nothing.
func TestAnswerNodeGroundsFactOnlyTurnFromCandidates(t *testing.T) {
	em := newEmitter()
	ctx := withEmitter(context.Background(), em)

	rating := 4.8
	st := &TurnState{
		RunID:     "run-facts",
		ThreadID:  "th-facts",
		UserInput: "推荐一家布鲁克林评分4以上的餐厅",
		Plan: slots.Plan{
			HardFilters: search.RestaurantFilter{Borough: "brooklyn", MinRating: &rating},
		},
		Candidates: []search.RestaurantCandidate{{
			RestaurantID: 2059,
			Name:         "Royal Bay Restaurant",
			Borough:      "brooklyn",
			Rating:       &rating,
			RatingCount:  8,
		}},
	}

	chat := &nodesFactChat{content: "推荐 Royal Bay Restaurant，评分 4.8（8 条评论样本），据 Google Local 2021 年快照。\nFOLLOWUPS: []"}
	runner := &Runner{
		cfg:      Config{},
		composer: answer.NewComposer(answer.Deps{Chat: chat, Model: "m"}),
	}

	got, err := runner.answerNode(ctx, st)
	if err != nil {
		t.Fatalf("answerNode: %v", err)
	}
	if got.FinalAnswer == nil {
		t.Fatal("the turn must produce a final answer")
	}
	if got.FinalAnswer.Text == answer.RefusalAnswer {
		t.Fatalf("candidate-only turn must not be refused:\n%s", got.FinalAnswer.Text)
	}
	if !strings.Contains(got.FinalAnswer.Text, "4.8") {
		t.Fatalf("grounded answer must state the candidate rating:\n%s", got.FinalAnswer.Text)
	}
	if chat.calls != 1 {
		t.Fatalf("composer model calls = %d, want 1", chat.calls)
	}
}

// Nothing at all — no candidates and no evidence — is still the fixed
// refusal, without spending a model call. The fact route must not turn an
// empty retrieval into an ungrounded generation.
func TestAnswerNodeRefusesWhenNeitherCandidatesNorEvidence(t *testing.T) {
	em := newEmitter()
	ctx := withEmitter(context.Background(), em)

	st := &TurnState{
		RunID:          "run-empty",
		ThreadID:       "th-empty",
		UserInput:      "随便推荐一家",
		Plan:           slots.Plan{},
		RetrievalEmpty: true,
	}
	chat := &nodesFactChat{content: "不该被调用"}
	runner := &Runner{
		cfg:      Config{},
		composer: answer.NewComposer(answer.Deps{Chat: chat, Model: "m"}),
	}

	got, err := runner.answerNode(ctx, st)
	if err != nil {
		t.Fatalf("answerNode: %v", err)
	}
	if got.FinalAnswer == nil || got.FinalAnswer.Text != answer.RefusalAnswer {
		t.Fatalf("text = %v, want fixed refusal", got.FinalAnswer)
	}
	if chat.calls != 0 {
		t.Fatalf("composer model calls = %d, want 0", chat.calls)
	}
}
