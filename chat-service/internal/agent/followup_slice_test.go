package agent_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/conversation"
	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/testkit"

	"github.com/zed1995/platepilot/chat-service/internal/agent"
	"github.com/zed1995/platepilot/chat-service/internal/agent/slots"
	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
	"github.com/zed1995/platepilot/chat-service/internal/agent/tools"
	"github.com/zed1995/platepilot/chat-service/internal/retrieval"
)

// snapshotTime is the observation date every fixture document shares, so nothing
// in these tests depends on when they run.
var snapshotTime = time.Date(2021, 9, 1, 12, 0, 0, 0, time.UTC)

// ---- evidence that can be traced back to its restaurant --------------------

// followUpEvidence answers an evidence request with two documents about every
// restaurant it was asked for, and keeps the requests.
//
// It is a fake rather than the retrieval service because the assertion these
// tests make is about *which* restaurant a follow-up asked about, not about how
// recall ranked the documents. The document ids encode their restaurant, so a
// citation in the final answer can be traced to the restaurant it claims to be
// about without reaching into turn state no test can see.
type followUpEvidence struct {
	mu   sync.Mutex
	reqs []retrieval.EvidenceRequest
}

// evidenceIDFor builds a document id that carries its restaurant.
func evidenceIDFor(restaurantID int64, n int) int64 { return restaurantID*100 + int64(n) }

// restaurantOfEvidenceID reads the restaurant back out of a document id.
func restaurantOfEvidenceID(id int64) int64 { return id / 100 }

func (f *followUpEvidence) Evidence(
	_ context.Context, req retrieval.EvidenceRequest,
) (retrieval.EvidenceResult, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()

	var items []evidence.Evidence
	for _, id := range req.RestaurantIDs {
		items = append(items,
			evidence.Evidence{
				EvidenceID:   evidenceIDFor(id, 1),
				RestaurantID: id,
				DocType:      evidence.DocTypeRestaurantProfile,
				Content:      "这家店以安静著称。",
				Source:       "yelp_review",
				SnapshotAt:   snapshotTime,
			},
			evidence.Evidence{
				EvidenceID:   evidenceIDFor(id, 2),
				RestaurantID: id,
				DocType:      evidence.DocTypeRestaurantRepresentativeReviews,
				Content:      "「晚上很安静，适合聊天。」",
				Source:       "yelp_review",
				SnapshotAt:   snapshotTime,
			})
	}
	return retrieval.EvidenceResult{Evidence: items}, nil
}

// ---- a five-restaurant recommendation, then a follow-up --------------------

// TestAFollowUpReadsTheSecondRecommendationAndStaysOnIt is the slice's
// acceptance test, and it runs the whole loop through the database rather than
// through memory: turn one recommends five restaurants, turn two says "第二家安静吗",
// and every later stage — the plan, the evidence request, the citations — has to
// name the same one of the five.
//
// The chain is what makes it worth asserting end to end. An ordinal is only
// decidable against a snapshot that survived the request that produced it, and a
// citation is only honest if it came from the restaurant the ordinal chose; a
// test that checked the id alone would pass on an implementation that pinned the
// right restaurant and then answered from somebody else's reviews.
func TestAFollowUpReadsTheSecondRecommendationAndStaysOnIt(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewConversationRepository()

	service := seededSearchService(t,
		rankedDetail(11, "Trattoria Uno", "manhattan", []string{"italian"}, 2, 4.9),
		rankedDetail(12, "Trattoria Due", "manhattan", []string{"italian"}, 2, 4.8),
		rankedDetail(13, "Trattoria Tre", "manhattan", []string{"italian"}, 2, 4.7),
		rankedDetail(14, "Trattoria Quattro", "manhattan", []string{"italian"}, 2, 4.6),
		rankedDetail(15, "Trattoria Cinque", "manhattan", []string{"italian"}, 2, 4.5),
	)
	reader := &followUpEvidence{}

	// Turn one: the model searches with arguments it read off the sentence. The
	// plan is deliberately not asked for a filter here — M5-01/M5-02 own that
	// behaviour, and this test is about what happens *after* a list exists.
	provider := &scriptedProvider{
		supportTools: true,
		toolResps: []domainchat.ToolCallResponse{
			toolCallResponse("c1", tools.SearchRestaurantsToolName,
				`{"cuisine":"italian","borough":"manhattan"}`),
			assistantText("为你找到这几家意大利餐厅。"),
		},
		// Turn one has candidates but no review evidence; the fact route
		// composes a grounded answer instead of passing the tool-round text
		// through.
		completeResps: []domainchat.ChatResponse{factAnswerResponse(
			"为你找到这 5 家曼哈顿意大利餐厅（据 Google Local 2021 年快照）。")},
	}
	registry := toolreg.New(0)
	register(t, registry, tools.SearchRestaurantsEntry(service))
	register(t, registry, tools.RestaurantEvidenceEntry(reader))

	// The extractor runs without a model on purpose: the ordinal has to be
	// decided by rules, so a structured provider here would hide whether the
	// persistence is doing any work.
	newRunner := func(t *testing.T) *agent.Runner {
		t.Helper()
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
		return runner
	}

	const threadID = "th-follow-up"
	if _, stream := newRunner(t).Run(ctx, agent.TurnInput{
		ThreadID:  threadID,
		UserID:    "u1",
		UserInput: "曼哈顿的意大利餐厅",
	}); stream != nil {
		drain(stream)
	}

	// The list the recommendation produced, in the order the user was shown it.
	candidates, err := repo.ListCandidates(ctx, threadID)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(candidates) != 5 {
		t.Fatalf("persisted %d candidates, want the 5 the turn recommended", len(candidates))
	}
	for i, candidate := range candidates {
		if candidate.Position != i+1 {
			t.Fatalf("candidate %d sits at position %d, want %d",
				i, candidate.Position, i+1)
		}
	}

	// Turn two. The evidence call deliberately passes no restaurant ids: the
	// only thing that can put a restaurant in it is the resolved reference.
	target := candidates[1]
	composed := fmt.Sprintf(
		"%s 挺安静的，晚上适合聊天[^%d][^%d]。",
		target.Name, evidenceIDFor(target.RestaurantID, 1), evidenceIDFor(target.RestaurantID, 2))
	provider.toolResps = append(provider.toolResps,
		toolCallResponse("c2", tools.RestaurantEvidenceToolName, `{"query":"这家安静吗"}`),
		assistantText(composed+"\nFOLLOWUPS: []"),
	)
	provider.completeResps = append(provider.completeResps, domainchat.ChatResponse{
		Message:      domainchat.ChatMessage{Role: domainchat.RoleAssistant, Content: composed + "\nFOLLOWUPS: []"},
		FinishReason: domainchat.FinishReasonStop,
	})

	result, stream := newRunner(t).Run(ctx, agent.TurnInput{
		ThreadID:  threadID,
		UserID:    "u1",
		UserInput: "第二家安静吗",
	})
	events := drain(stream)

	if result == nil || result.Answer == nil {
		t.Fatal("the follow-up must produce an answer")
	}
	// Nothing is parked: the ordinal answered the only open question.
	if got := awaitingInputEvents(events); len(got) != 0 {
		t.Fatalf("a resolved follow-up must not ask anything: %+v", got)
	}

	checkpoint, err := repo.LoadCheckpoint(ctx, threadID)
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if checkpoint.SelectedRestaurantID != target.RestaurantID {
		t.Fatalf("pinned restaurant = %d, want the second candidate %d",
			checkpoint.SelectedRestaurantID, target.RestaurantID)
	}

	// The evidence request is the first place the pin becomes a fact rather
	// than a field: this is what the turn actually asked about.
	if len(reader.reqs) != 1 {
		t.Fatalf("evidence requests = %d, want exactly 1", len(reader.reqs))
	}
	if got := reader.reqs[0].RestaurantIDs; len(got) != 1 || got[0] != target.RestaurantID {
		t.Fatalf("evidence requested for %v, want only %d", got, target.RestaurantID)
	}

	// And the citations have to agree with it. A document id carries its
	// restaurant, so this is a real scope check rather than a count.
	if len(result.Answer.Citations) != 2 {
		t.Fatalf("citations = %v, want both documents of the pinned restaurant",
			result.Answer.Citations)
	}
	for _, id := range result.Answer.Citations {
		if got := restaurantOfEvidenceID(id); got != target.RestaurantID {
			t.Fatalf("citation %d is about restaurant %d, want the pinned %d",
				id, got, target.RestaurantID)
		}
	}
}

// ---- clarification resolved by an ordinal ----------------------------------

// parkOnAmbiguousName runs one turn that stops on an ambiguous restaurant name
// and returns the runner that did it, so a second turn can answer the question.
func parkOnAmbiguousName(
	t *testing.T, provider *scriptedProvider, repo *testkit.ConversationRepository, threadID string,
) *agent.Runner {
	t.Helper()
	provider.supportTools = true
	provider.toolResps = append(provider.toolResps,
		toolCallResponse("c1", tools.ResolveRestaurantToolName, `{"name":"Joe's Pizza"}`))

	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 4}, agent.Deps{
		Chat:          provider,
		ToolCalling:   provider,
		Registry:      ambiguousRegistry(t),
		Extractor:     slots.New(slots.Deps{}),
		Conversations: repo,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	_, stream := runner.Run(context.Background(), agent.TurnInput{
		ThreadID:  threadID,
		UserID:    "u1",
		UserInput: "Joes Pizza 怎么样？",
	})
	drain(stream)

	checkpoint, err := repo.LoadCheckpoint(context.Background(), threadID)
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if checkpoint.State != conversation.StateAwaitingClarification {
		t.Fatalf("the ambiguous turn must park: state = %q", checkpoint.State)
	}
	return runner
}

// answeringRegistry is the ambiguity registry plus a real evidence tool, which
// is what the continuation turn needs: it must be able to act on the answer.
func answeringRegistry(t *testing.T, reader *followUpEvidence) *toolreg.Registry {
	t.Helper()
	registry := ambiguousRegistry(t)
	register(t, registry, tools.RestaurantEvidenceEntry(reader))
	return registry
}

// TestAnOrdinalAnswersAClarificationAndContinuesThePendingAction finishes the
// conversation the clarification started: the thread parked waiting for a
// restaurant, the user answered "第二家", and the same turn must go on to read
// that restaurant's evidence and answer — not ask again.
//
// "Continues the original PendingAction" is what the assertions check: the
// pending action is gone, the pin is the second option the question offered,
// and the turn really did run the work the parked action existed for.
func TestAnOrdinalAnswersAClarificationAndContinuesThePendingAction(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewConversationRepository()
	reader := &followUpEvidence{}

	provider := &scriptedProvider{}
	parkOnAmbiguousName(t, provider, repo, "th-continue")
	// The parked turn persisted its options as the candidate snapshot; that is
	// what "第二家" will be read against.
	candidates, err := repo.ListCandidates(ctx, "th-continue")
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("the question offered %d options, want 2", len(candidates))
	}
	target := candidates[1]

	composed := fmt.Sprintf(
		"%s（布鲁克林）可以[^%d][^%d]。",
		target.Name, evidenceIDFor(target.RestaurantID, 1), evidenceIDFor(target.RestaurantID, 2))
	provider.toolResps = append(provider.toolResps,
		toolCallResponse("c2", tools.RestaurantEvidenceToolName, `{"query":"这家怎么样"}`),
		assistantText(composed+"\nFOLLOWUPS: []"),
	)
	provider.completeResps = append(provider.completeResps, domainchat.ChatResponse{
		Message:      domainchat.ChatMessage{Role: domainchat.RoleAssistant, Content: composed + "\nFOLLOWUPS: []"},
		FinishReason: domainchat.FinishReasonStop,
	})

	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 4}, agent.Deps{
		Chat:          provider,
		ToolCalling:   provider,
		Registry:      answeringRegistry(t, reader),
		Extractor:     slots.New(slots.Deps{}),
		Conversations: repo,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	result, stream := runner.Run(ctx, agent.TurnInput{
		ThreadID:  "th-continue",
		UserID:    "u1",
		UserInput: "第二家",
	})
	events := drain(stream)

	if got := awaitingInputEvents(events); len(got) != 0 {
		t.Fatalf("the answer resolved the question, yet the turn asked again: %+v", got)
	}
	if result == nil || result.Answer == nil {
		t.Fatal("the continuation must produce an answer")
	}
	if len(reader.reqs) != 1 || len(reader.reqs[0].RestaurantIDs) != 1 ||
		reader.reqs[0].RestaurantIDs[0] != target.RestaurantID {
		t.Fatalf("the continuation acted on %+v, want the second option %d",
			reader.reqs, target.RestaurantID)
	}

	checkpoint, err := repo.LoadCheckpoint(ctx, "th-continue")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if checkpoint.SelectedRestaurantID != target.RestaurantID {
		t.Fatalf("pinned = %d, want %d", checkpoint.SelectedRestaurantID, target.RestaurantID)
	}
	// The thread is free again, and the clarification budget is back: a counter
	// that never reset would make the next ambiguity unreportable.
	if checkpoint.State != conversation.StateIdle {
		t.Fatalf("state = %q, want idle once the question was answered", checkpoint.State)
	}
	if checkpoint.PendingAction != "" || len(checkpoint.MissingSlots) != 0 {
		t.Fatalf("the parked work must be cleared: action=%q slots=%v",
			checkpoint.PendingAction, checkpoint.MissingSlots)
	}
	if checkpoint.ClarificationCount != 0 {
		t.Fatalf("clarification count = %d, want 0", checkpoint.ClarificationCount)
	}
}

// ---- the state reset is conditional ----------------------------------------

// TestASecondClarificationCountsOnTheFirst is the regression the unconditional
// reset used to cause.
//
// The counter is what makes the clarification cap reachable, and a thread that
// parked a question has not finished the conversation — it is waiting. Zeroing
// the count at the end of every turn — what persistTurn did before M5 — would
// restart the budget on every park, so a user who never answers could be asked
// forever and the cap would never fire. The count therefore has to survive the
// parked turn, and this is the smallest pair of turns that shows it.
func TestASecondClarificationCountsOnTheFirst(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewConversationRepository()

	provider := &scriptedProvider{}
	parkOnAmbiguousName(t, provider, repo, "th-count")

	// The second turn is ambiguous again — the user still did not pick — so the
	// thread parks a second time.
	provider.toolResps = append(provider.toolResps,
		toolCallResponse("c2", tools.ResolveRestaurantToolName, `{"name":"Joes Pizza"}`))
	if _, stream := transportRun(t, provider, repo, "th-count", "还是不确定"); stream != nil {
		drain(stream)
	}

	checkpoint, err := repo.LoadCheckpoint(ctx, "th-count")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if checkpoint.State != conversation.StateAwaitingClarification {
		t.Fatalf("state = %q, want the thread still asking", checkpoint.State)
	}
	if checkpoint.ClarificationCount != 2 {
		t.Fatalf("clarification count = %d, want 2: the budget must survive the park",
			checkpoint.ClarificationCount)
	}
}

// transportRun runs one turn on an existing thread with the ambiguity registry
// and the rules-only extractor. It is the continuation half of
// parkOnAmbiguousName, which is why it takes the already-running provider.
func transportRun(
	t *testing.T, provider *scriptedProvider, repo *testkit.ConversationRepository,
	threadID, userInput string,
) (*agent.TurnResult, <-chan agent.Event) {
	t.Helper()
	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 4}, agent.Deps{
		Chat:          provider,
		ToolCalling:   provider,
		Registry:      ambiguousRegistry(t),
		Extractor:     slots.New(slots.Deps{}),
		Conversations: repo,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner.Run(context.Background(), agent.TurnInput{
		ThreadID: threadID, UserID: "u1", UserInput: userInput,
	})
}

// ---- what the model is told about the thread -------------------------------

// systemMessagesOf flattens the system messages the model was given on the
// turn whose model call is at index turn.
//
// The system messages are the observable for what the runtime told the model
// about the thread. Nothing else carries it: the prompt is assembled per turn
// and never stored.
func systemMessagesOf(t *testing.T, reqs []toolCallReq, turn int) string {
	t.Helper()
	if turn >= len(reqs) {
		t.Fatalf("the turn made %d model calls, want at least %d", len(reqs), turn+1)
	}
	var out []string
	for _, msg := range reqs[turn].req.Messages {
		if msg.Role == domainchat.RoleSystem {
			out = append(out, msg.Content)
		}
	}
	return strings.Join(out, "\n")
}

// TestAPinSurvivesAFollowUpAndIsReleasedByANewSearch covers the lifetime of the
// thread's scope, which is what makes a pronoun mean the same restaurant on
// consecutive requests.
//
// Three rules, and they are three aspects of one idea — the pin describes the
// list the user is looking at. A turn that resolves a reference owns the pin
// outright. A turn that names no restaurant inherits it, but only as a default,
// because the sentence may be the user changing the subject. A turn whose
// search returns a list the pinned restaurant is not in releases it.
func TestAPinSurvivesAFollowUpAndIsReleasedByANewSearch(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewConversationRepository()

	service := seededSearchService(t,
		rankedDetail(21, "Trattoria Uno", "manhattan", []string{"italian"}, 2, 4.9),
		rankedDetail(22, "Trattoria Due", "manhattan", []string{"italian"}, 2, 4.8),
		rankedDetail(23, "Bistro Bleu", "brooklyn", []string{"french"}, 3, 4.5),
	)
	provider := &scriptedProvider{
		supportTools: true,
		// Turns one and three run a fresh search and end with candidate facts,
		// so the grounded composer needs one completion each; turn two calls
		// no retrieval tool and passes the model text through.
		completeResps: []domainchat.ChatResponse{
			factAnswerResponse("找到两家曼哈顿意大利餐厅（据 Google Local 2021 年快照）。"),
			factAnswerResponse("找到一家布鲁克林法餐厅（据 Google Local 2021 年快照）。"),
		},
	}
	registry := toolreg.New(0)
	register(t, registry, tools.SearchRestaurantsEntry(service))

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
	turn := func(userInput string) {
		t.Helper()
		if _, stream := runner.Run(ctx, agent.TurnInput{
			ThreadID: "th-pin", UserID: "u1", UserInput: userInput,
		}); stream != nil {
			drain(stream)
		}
	}

	// Turn one: a search, and therefore no pin at all.
	provider.toolResps = append(provider.toolResps,
		toolCallResponse("c1", tools.SearchRestaurantsToolName,
			`{"cuisine":"italian","borough":"manhattan"}`),
		assistantText("找到两家。"))
	turn("曼哈顿的意大利餐厅")
	afterSearch := len(provider.toolReqs)

	candidates, err := repo.ListCandidates(ctx, "th-pin")
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("persisted %d candidates, want 2", len(candidates))
	}
	pinned := candidates[1].RestaurantID
	if strings.Contains(systemMessagesOf(t, provider.toolReqs, 0), "restaurant_id=") {
		t.Fatal("a turn whose pin came from nowhere must not claim one")
	}

	// Turn two: the ordinal decides the pin, and the model is told so as an
	// instruction it may not wander from.
	provider.toolResps = append(provider.toolResps, assistantText("好的。"))
	turn("第二家安静吗")
	follow := systemMessagesOf(t, provider.toolReqs, afterSearch)
	if !strings.Contains(follow, fmt.Sprintf("本轮已锁定 restaurant_id=%d", pinned)) {
		t.Fatalf("the follow-up must tell the model which restaurant it is about:\n%s", follow)
	}

	// Turn three: a search whose results do not contain the pinned restaurant
	// releases it, in the state and in what the model is told.
	provider.toolResps = append(provider.toolResps,
		toolCallResponse("c3", tools.SearchRestaurantsToolName,
			`{"cuisine":"french","borough":"brooklyn"}`),
		assistantText("找到一家法餐。"))
	turn("那布鲁克林的法餐呢")

	reopened := systemMessagesOf(t, provider.toolReqs, afterSearch+1)
	if strings.Contains(reopened, "本轮已锁定") {
		t.Fatalf("a turn that reopened the search must not instruct the model to stay on the"+
			" previous restaurant:\n%s", reopened)
	}
	if !strings.Contains(reopened, fmt.Sprintf("restaurant_id=%d", pinned)) {
		t.Fatalf("the thread's previous restaurant must still be stated, as a default:\n%s", reopened)
	}
	// And once the search came back without it, the pin is gone from the state
	// too — assertion on the *second* round of this turn, which runs after the
	// tool.
	if got := systemMessagesOf(t, provider.toolReqs, afterSearch+2); strings.Contains(got, "restaurant_id=") {
		t.Fatalf("the released pin must not be restated after the search:\n%s", got)
	}
	checkpoint, err := repo.LoadCheckpoint(ctx, "th-pin")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if checkpoint.SelectedRestaurantID != 0 {
		t.Fatalf("pinned = %d, want released once the list no longer contains it",
			checkpoint.SelectedRestaurantID)
	}
}

// ---- the thread survives the process ---------------------------------------

// TestAPendingStateSurvivesAProcessRestart rebuilds the runner between the two
// turns. It is the difference between storing state and *depending* on it: the
// second runner shares no memory with the first, so anything the continuation
// knows about the thread it read from the store.
func TestAPendingStateSurvivesAProcessRestart(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewConversationRepository()
	reader := &followUpEvidence{}

	first := &scriptedProvider{}
	parkOnAmbiguousName(t, first, repo, "th-restart")

	// A different provider object, a newly assembled runner: the only thing the
	// two runs share is the repository.
	second := &scriptedProvider{supportTools: true}
	target := conversation.Candidate{}
	if candidates, err := repo.ListCandidates(ctx, "th-restart"); err != nil {
		t.Fatalf("list candidates: %v", err)
	} else {
		target = candidates[1]
	}
	composed := fmt.Sprintf("为你继续查了 %s。", target.Name)
	// The continuation does not need evidence to prove it resumed — the pin
	// does — so the model answers directly. That keeps the test about recovery
	// rather than about the composer.
	second.toolResps = append(second.toolResps,
		toolCallResponse("c1", tools.RestaurantEvidenceToolName, `{"query":"这家怎么样"}`),
		assistantText(composed+"\nFOLLOWUPS: []"))
	second.completeResps = append(second.completeResps, domainchat.ChatResponse{
		Message:      domainchat.ChatMessage{Role: domainchat.RoleAssistant, Content: composed + "\nFOLLOWUPS: []"},
		FinishReason: domainchat.FinishReasonStop,
	})

	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 4}, agent.Deps{
		Chat:          second,
		ToolCalling:   second,
		Registry:      answeringRegistry(t, reader),
		Extractor:     slots.New(slots.Deps{}),
		Conversations: repo,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	result, stream := runner.Run(ctx, agent.TurnInput{
		ThreadID:  "th-restart",
		UserID:    "u1",
		UserInput: "第二家呢",
	})
	drain(stream)
	if result == nil || result.Answer == nil {
		t.Fatal("the resumed turn must produce an answer")
	}

	if len(reader.reqs) != 1 || len(reader.reqs[0].RestaurantIDs) != 1 ||
		reader.reqs[0].RestaurantIDs[0] != target.RestaurantID {
		t.Fatalf("after the restart the turn acted on %+v, want the second option %d",
			reader.reqs, target.RestaurantID)
	}
	checkpoint, err := repo.LoadCheckpoint(ctx, "th-restart")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if checkpoint.SelectedRestaurantID != target.RestaurantID {
		t.Fatalf("pinned = %d, want %d", checkpoint.SelectedRestaurantID, target.RestaurantID)
	}

	// And the resumed thread has the whole conversation in it, not just the
	// half that survived in memory.
	messages, err := repo.ListMessages(ctx, "th-restart", 20, "")
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 4 {
		t.Fatalf("transcript has %d messages, want 4 (question and answer, twice)", len(messages))
	}
}

// ---- two requests for one thread -------------------------------------------

// gatedProvider holds every turn at its model round until all the turns under
// test have reached it.
//
// Without it the concurrency test would depend on the scheduler: one turn can
// load its checkpoint, finish, and write before the other has even loaded, and
// no conflict ever arises. Holding both turns after ingress and before the
// write makes the interleaving the test needs deterministic instead of likely.
//
// It gates Complete because the fixture registers no tools, and a tools-bound
// model with an empty spec list is a plain completion. That also fixes the
// arrival count at one per turn — a turn would deadlock a barrier it arrived at
// twice.
type gatedProvider struct {
	*scriptedProvider
	gate *sync.WaitGroup
}

func (p *gatedProvider) Complete(
	ctx context.Context, req domainchat.ChatRequest,
) (domainchat.ChatResponse, error) {
	p.gate.Done()
	p.gate.Wait()
	return p.scriptedProvider.Complete(ctx, req)
}

// TestConcurrentTurnsOnOneThreadConflict pins the concurrency contract: threads
// are claimed optimistically through the checkpoint version, and the request
// that loses is told so.
//
// Both halves matter. Returning the conflict is what makes the client able to
// retry instead of believing a turn was recorded. Not appending the losing
// turn's transcript is what keeps the thread coherent: a transcript carrying
// both turns beside a checkpoint describing one would replay the conversation
// into a state that never existed, and every later "第二家" would be resolved
// against a list half the history never saw.
func TestConcurrentTurnsOnOneThreadConflict(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewConversationRepository()

	const turns = 2
	var gate sync.WaitGroup
	gate.Add(turns)
	provider := &gatedProvider{
		scriptedProvider: &scriptedProvider{
			supportTools: true,
			// One plain completion per turn: the fixture registers no tools, so
			// the plan round is the only model call a turn makes.
			completeResps: []domainchat.ChatResponse{
				{Message: domainchat.ChatMessage{Role: domainchat.RoleAssistant, Content: "好的。"},
					FinishReason: domainchat.FinishReasonStop},
				{Message: domainchat.ChatMessage{Role: domainchat.RoleAssistant, Content: "好的。"},
					FinishReason: domainchat.FinishReasonStop},
			},
		},
		gate: &gate,
	}

	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 2}, agent.Deps{
		Chat:          provider,
		ToolCalling:   provider,
		Conversations: repo,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		results  = make([]*agent.TurnResult, turns)
		conflict = make([]bool, turns)
		otherErr = make([]string, turns)
	)
	wg.Add(turns)
	for i := 0; i < turns; i++ {
		go func(i int) {
			defer wg.Done()
			result, stream := runner.Run(ctx, agent.TurnInput{
				ThreadID: "th-race", UserID: "u1", UserInput: "你好",
			})
			mu.Lock()
			results[i] = result
			mu.Unlock()
			for ev := range stream {
				if ev.Type != agent.EventError {
					continue
				}
				mu.Lock()
				if ev.Code == "conflict" {
					conflict[i] = true
				} else {
					otherErr[i] = ev.Code + ": " + ev.Message
				}
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	var won, lost int
	for i := 0; i < turns; i++ {
		if otherErr[i] != "" {
			t.Errorf("turn %d failed for a reason other than the race: %s", i, otherErr[i])
		}
		switch {
		case results[i] != nil && conflict[i]:
			t.Errorf("turn %d both succeeded and reported a conflict", i)
		case results[i] != nil:
			won++
		case conflict[i]:
			lost++
		default:
			t.Errorf("turn %d neither succeeded nor reported a conflict", i)
		}
	}
	if won != 1 || lost != 1 {
		t.Fatalf("outcome = %d succeeded, %d conflicted; want exactly one of each", won, lost)
	}

	// One turn's transcript, not two: the losing turn wrote nothing.
	messages, err := repo.ListMessages(ctx, "th-race", 20, "")
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("transcript has %d messages, want the winning turn's 2", len(messages))
	}
	checkpoint, err := repo.LoadCheckpoint(ctx, "th-race")
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if checkpoint.Version != 1 {
		t.Fatalf("checkpoint version = %d, want 1: the losing turn must not have advanced it",
			checkpoint.Version)
	}
}
