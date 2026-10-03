package agent_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/conversation"
	domaintool "github.com/zed1995/platepilot/shared/domain/tool"
	"github.com/zed1995/platepilot/shared/store/memory"
	"github.com/zed1995/platepilot/shared/testkit"

	"github.com/zed1995/platepilot/chat-service/internal/agent"
	"github.com/zed1995/platepilot/chat-service/internal/agent/answer"
	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
	"github.com/zed1995/platepilot/chat-service/internal/agent/tools"
)

// emptyEvidenceEntry stands in for get_restaurant_evidence returning nothing.
//
// The handler is a stub rather than the real tool because this test is about
// what the turn does with an empty evidence set, not about how the set became
// empty. The tool name is the real one: absorbToolData dispatches on it, and a
// stub under a private name would exercise a path no deployment has.
func emptyEvidenceEntry() toolreg.Entry {
	return toolreg.Entry{
		Spec: domaintool.ToolSpec{
			Name:        tools.RestaurantEvidenceToolName,
			Description: "test stub that recalls no evidence",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			ReadOnly:    true,
		},
		Handler: func(context.Context, json.RawMessage) (domaintool.ToolResult, error) {
			// An empty EvidenceResult: the shape the real tool returns when the
			// recall finds nothing.
			return domaintool.ToolResult{
				Status:  domaintool.ToolStatusOK,
				Content: "没有可用证据。",
				Data:    json.RawMessage(`{}`),
			}, nil
		},
	}
}

// seedPriorTurn leaves a thread that already answered a question with two
// citations, exactly as a previous successful turn would have left it.
func seedPriorTurn(t *testing.T, repo *memory.ConversationRepository, threadID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2021, 9, 1, 12, 0, 0, 0, time.UTC)
	if err := repo.Upsert(ctx, conversation.Conversation{
		ThreadID:     threadID,
		CurrentState: conversation.StateIdle,
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("seed thread: %v", err)
	}
	if err := repo.AppendMessage(ctx, conversation.Message{
		ThreadID: threadID, Role: conversation.RoleUser,
		Content: "曼哈顿有什么安静的意大利餐厅？", CreatedAt: now,
	}); err != nil {
		t.Fatalf("seed user message: %v", err)
	}
	if err := repo.AppendMessage(ctx, conversation.Message{
		ThreadID: threadID, Role: conversation.RoleAssistant,
		Content: "以下是两家[^99][^100]。", EvidenceIDs: []int64{99, 100}, CreatedAt: now,
	}); err != nil {
		t.Fatalf("seed assistant message: %v", err)
	}
	if err := repo.SaveCheckpoint(ctx, conversation.Checkpoint{
		ThreadID:    threadID,
		Version:     1,
		State:       conversation.StateIdle,
		EvidenceIDs: []int64{99, 100},
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}
}

// The citation rule is a rule about this turn, and the checkpoint's evidence
// ids are a recovery snapshot rather than a citation allowance.
//
// The failure this guards against is the quietest one in the milestone: a
// follow-up turn recalls nothing, the model reaches for the two sources it
// remembered from the previous answer, and the answer ships with citations that
// resolve to nothing in this turn's evidence set. The turn has to refuse
// instead, and the persisted checkpoint must not keep carrying the old ids as
// though they were still live.
func TestATurnWithNoNewEvidenceRefusesRatherThanReusingLastTurnCitations(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewConversationRepository()
	seedPriorTurn(t, repo, "th-carryover")

	provider := &scriptedProvider{
		supportTools: true,
		toolResps: []domainchat.ToolCallResponse{
			toolCallResponse("c1", tools.RestaurantEvidenceToolName, `{"restaurant_id":42}`),
			// The model answers from memory, citing last turn's ids.
			assistantText("这家店服务很好[^99][^100]。\nFOLLOWUPS: []"),
		},
	}
	registry := toolreg.New(0)
	if err := registry.Register(emptyEvidenceEntry()); err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 3}, agent.Deps{
		Chat:          provider,
		ToolCalling:   provider,
		Registry:      registry,
		Conversations: repo,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	result, events := runner.Run(ctx, agent.TurnInput{
		ThreadID:  "th-carryover",
		UserInput: "那它服务怎么样？",
	})
	drain(events)

	if result == nil || result.Answer == nil {
		t.Fatal("the turn must produce an answer")
	}
	if result.Answer.Text != answer.RefusalAnswer {
		t.Fatalf("answer = %q, want the fixed refusal", result.Answer.Text)
	}
	if len(result.Answer.Citations) != 0 {
		t.Fatalf("citations = %v, want none: the previous turn's ids are not evidence "+
			"for this one", result.Answer.Citations)
	}

	// The persisted state has to agree with the answer. A checkpoint that kept
	// the old ids would make the next turn believe evidence exists when it does
	// not.
	checkpoint, err := repo.LoadCheckpoint(ctx, "th-carryover")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if len(checkpoint.EvidenceIDs) != 0 {
		t.Fatalf("checkpoint evidence ids = %v, want none after a refusal",
			checkpoint.EvidenceIDs)
	}

	messages, err := repo.ListMessages(ctx, "th-carryover", 10, "")
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	last := messages[len(messages)-1]
	if last.Role != conversation.RoleAssistant {
		t.Fatalf("last message role = %q", last.Role)
	}
	if last.Content != answer.RefusalAnswer {
		t.Fatalf("persisted answer = %q, want the refusal", last.Content)
	}
	if len(last.EvidenceIDs) != 0 {
		t.Fatalf("persisted evidence ids = %v, want none", last.EvidenceIDs)
	}
}
