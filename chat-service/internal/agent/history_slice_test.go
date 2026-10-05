package agent_test

import (
	"context"
	"strings"
	"testing"

	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/testkit"

	"github.com/zed1995/platepilot/chat-service/internal/agent"
	"github.com/zed1995/platepilot/chat-service/internal/agent/slots"
	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
	"github.com/zed1995/platepilot/chat-service/internal/agent/tools"
)

// TestAnIncrementalConditionIsReadAgainstTheLastAnswer is the slice's
// acceptance test for history injection, and it runs two turns that differ in
// exactly one respect: the second one's meaning lives entirely in the first.
//
// "便宜一点的" names no cuisine, no borough, and no price — the only thing that
// makes it a request is what was already said. The plan model has always been
// able to read that from the replayed transcript, so the second search does
// narrow (four candidates become two). What M-OPT-03 adds is the other half: the
// composer now sees the previous answer too, which is what stops it from
// restating a conclusion the user has moved past.
//
// Three things are asserted, and they are the three that could each be true
// independently of the others:
//
//  1. the filter really changed — the corpus is fixed and the two searches
//     returned different lists, so this is the search service's answer rather
//     than the script's;
//  2. the composer's second prompt carried the first turn's question and answer
//     and declared them non-citable;
//  3. the second answer cites only the second turn's documents, so the first
//     turn's sources were not recycled — and could not have been, because their
//     markers were stripped before the model saw them.
func TestAnIncrementalConditionIsReadAgainstTheLastAnswer(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewConversationRepository()
	reader := &followUpEvidence{}

	service := seededSearchService(t,
		rankedDetail(31, "Trattoria Uno", "manhattan", []string{"italian"}, 3, 4.8),
		rankedDetail(32, "Trattoria Due", "manhattan", []string{"italian"}, 3, 4.7),
		rankedDetail(33, "Trattoria Tre", "manhattan", []string{"italian"}, 1, 4.5),
		rankedDetail(34, "Osteria Quattro", "manhattan", []string{"italian"}, 2, 4.3),
	)

	provider := &scriptedProvider{supportTools: true}
	registry := toolreg.New(0)
	register(t, registry, tools.SearchRestaurantsEntry(service))
	register(t, registry, tools.RestaurantEvidenceEntry(reader))

	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 4}, agent.Deps{
		Chat:          provider,
		ToolCalling:   provider,
		Registry:      registry,
		Extractor:     slots.New(slots.Deps{}),
		Conversations: repo,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	turn := func(userInput string) *agent.TurnResult {
		t.Helper()
		result, stream := runner.Run(ctx, agent.TurnInput{
			ThreadID: "th-incremental", UserID: "u1", UserInput: userInput,
		})
		drain(stream)
		return result
	}

	// ---- turn one: four 4-star Italian restaurants in Manhattan -------------

	provider.toolResps = append(provider.toolResps,
		toolCallResponse("c1", tools.SearchRestaurantsToolName,
			`{"cuisine":"italian","borough":"manhattan","min_rating":4}`),
		// The two the answer will actually stand behind. The rest of the list
		// stays recommendable without evidence, which is exactly the state the
		// second turn's "便宜一点的" has to narrow.
		toolCallResponse("c2", tools.RestaurantEvidenceToolName,
			`{"restaurant_ids":[31,32],"query":"哪家值得推荐"}`),
		assistantText("资料已取到。"))
	const firstAnswer = "Trattoria Uno 与 Trattoria Due 都是曼哈顿 4 星以上的意大利菜[^3101][^3201]。" +
		"\nFOLLOWUPS: [\"这两家能订位吗?\"]"
	provider.completeResps = append(provider.completeResps, domainchat.ChatResponse{
		Message:      domainchat.ChatMessage{Role: domainchat.RoleAssistant, Content: firstAnswer},
		FinishReason: domainchat.FinishReasonStop,
	})

	first := turn("曼哈顿 4 星以上的意大利菜")
	if first == nil || first.Answer == nil {
		t.Fatal("the first turn must produce an answer")
	}

	broad, err := repo.ListCandidates(ctx, "th-incremental")
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(broad) != 4 {
		t.Fatalf("the 4-star filter returned %d candidates, want all 4", len(broad))
	}
	if got := requestsOf(t, reader); len(got) != 1 || len(got[0]) != 2 {
		t.Fatalf("turn one grounded on %v, want two restaurants", got)
	}

	// A first turn has nothing to be read against.
	if userMessageContaining(t, composerPromptOf(t, provider, 0), "<recent_turns>") != "" {
		t.Fatal("the first turn's composer must not be given a history block")
	}

	// ---- turn two: the same corpus, narrowed by "便宜一点的" ----------------

	provider.toolResps = append(provider.toolResps,
		toolCallResponse("c3", tools.SearchRestaurantsToolName,
			`{"cuisine":"italian","borough":"manhattan","min_rating":4,"price_levels":[1,2]}`),
		toolCallResponse("c4", tools.RestaurantEvidenceToolName,
			`{"restaurant_ids":[33,34],"query":"哪家便宜"}`),
		assistantText("资料已取到。"))
	const secondAnswer = "更便宜的两家是 Trattoria Tre 与 Osteria Quattro[^3301][^3401]。\nFOLLOWUPS: []"
	provider.completeResps = append(provider.completeResps, domainchat.ChatResponse{
		Message:      domainchat.ChatMessage{Role: domainchat.RoleAssistant, Content: secondAnswer},
		FinishReason: domainchat.FinishReasonStop,
	})

	second := turn("便宜一点的")
	if second == nil || second.Answer == nil {
		t.Fatal("the follow-up must produce an answer")
	}

	// (1) The filter changed, and the change is visible in what the real search
	// service returned: the same four restaurants, two of them now excluded.
	narrow, err := repo.ListCandidates(ctx, "th-incremental")
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(narrow) != 2 {
		t.Fatalf("the narrowed filter returned %d candidates, want 2", len(narrow))
	}
	for i, candidate := range narrow {
		if candidate.RestaurantID != int64(33+i) {
			t.Fatalf("candidate %d = %d, want the two cheapest first",
				i+1, candidate.RestaurantID)
		}
	}
	grounded := requestsOf(t, reader)
	if len(grounded) != 2 {
		t.Fatalf("evidence requests = %d, want one per turn", len(grounded))
	}
	if grounded[0][0] == grounded[1][0] || grounded[0][1] == grounded[1][1] {
		t.Fatalf("both turns grounded on the same restaurants: %v", grounded)
	}

	// (2) And the composer was told what the previous turn said.
	prompt := composerPromptOf(t, provider, 1)
	block := userMessageContaining(t, prompt, "<recent_turns>")
	if block == "" {
		t.Fatalf("the follow-up's composer never saw the conversation:\n%s", prompt)
	}
	for _, want := range []string{"曼哈顿 4 星以上的意大利菜", "Trattoria Uno 与 Trattoria Due"} {
		if !strings.Contains(block, want) {
			t.Fatalf("the history block is missing %q:\n%s", want, block)
		}
	}
	if !strings.Contains(block, "不是可引用资料") {
		t.Fatalf("the history block must declare itself non-citable:\n%s", block)
	}
	// The previous turn's markers are stripped, which is what makes (3) a
	// property of the prompt rather than a hope about the model.
	if strings.Contains(block, "[^3101]") {
		t.Fatalf("a previous turn's citation marker reached the model:\n%s", block)
	}

	// (3) The new answer stands on the new documents and nothing else.
	if len(second.Answer.Citations) == 0 {
		t.Fatal("the follow-up's answer carries no citations")
	}
	for _, id := range second.Answer.Citations {
		if got := restaurantOfEvidenceID(id); got != 33 && got != 34 {
			t.Fatalf("citation %d is about restaurant %d, want one of this turn's two",
				id, got)
		}
	}
}

// composerPromptOf returns the messages the composer sent on the turn whose
// model call is at index turn.
//
// The composer is the only caller of Complete on a tools-bound turn — the plan
// goes through ChatWithTools and the slot extractor is rules-only here — so the
// completion log is exactly the composer's log.
func composerPromptOf(t *testing.T, provider *scriptedProvider, turn int) []domainchat.ChatMessage {
	t.Helper()
	if turn >= len(provider.completeReqs) {
		t.Fatalf("the composer called the model %d times, want at least %d",
			len(provider.completeReqs), turn+1)
	}
	return provider.completeReqs[turn].Messages
}

// userMessageContaining returns the content of the first user message that
// carries tag, or "" when none does. The role check matters: the system
// instruction names <recent_turns> too, so matching on the tag alone would
// return the instruction and make the assertion vacuous.
func userMessageContaining(t *testing.T, messages []domainchat.ChatMessage, tag string) string {
	t.Helper()
	for _, message := range messages {
		if message.Role == domainchat.RoleUser && strings.Contains(message.Content, tag) {
			return message.Content
		}
	}
	return ""
}

// requestsOf returns the restaurant ids each evidence request asked about, in
// order.
func requestsOf(t *testing.T, reader *followUpEvidence) [][]int64 {
	t.Helper()
	reader.mu.Lock()
	defer reader.mu.Unlock()
	out := make([][]int64, 0, len(reader.reqs))
	for _, req := range reader.reqs {
		out = append(out, append([]int64(nil), req.RestaurantIDs...))
	}
	return out
}
