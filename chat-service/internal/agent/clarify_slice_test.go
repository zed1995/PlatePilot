package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/conversation"
	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/domain/search"
	domaintool "github.com/zed1995/platepilot/shared/domain/tool"
	"github.com/zed1995/platepilot/shared/testkit"

	"github.com/zed1995/platepilot/chat-service/internal/agent"
	"github.com/zed1995/platepilot/chat-service/internal/agent/slots"
	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
	"github.com/zed1995/platepilot/chat-service/internal/agent/tools"
	"github.com/zed1995/platepilot/chat-service/internal/retrieval"
)

// ---- stubs ----------------------------------------------------------------

// fixedResolver answers a name lookup from a fixed list, which is what a
// trigram index would do for a given corpus.
type fixedResolver struct {
	rows []search.RestaurantCandidate
	err  error
}

func (f fixedResolver) MatchByText(
	_ context.Context, _ string, _ int,
) ([]search.RestaurantCandidate, error) {
	return f.rows, f.err
}

func joes(id int64, borough, address string, score float64) search.RestaurantCandidate {
	rating := 4.4
	return search.RestaurantCandidate{
		RestaurantID: id,
		Name:         "Joe's Pizza",
		Borough:      borough,
		Address:      address,
		Cuisines:     []string{"pizza"},
		Rating:       &rating,
		Score:        score,
	}
}

// stubEvidenceEntry stands in for get_restaurant_evidence returning a fixed set.
//
// The handler is a stub because this test is about what a named-restaurant turn
// does with the evidence it is handed, not about how recall produced it. The
// tool name is the real one: absorbToolData dispatches on it.
func stubEvidenceEntry(items []evidence.Evidence) toolreg.Entry {
	return toolreg.Entry{
		Spec: domaintool.ToolSpec{
			Name:        tools.RestaurantEvidenceToolName,
			Description: "test stub returning a fixed evidence set",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			ReadOnly:    true,
		},
		Handler: func(context.Context, json.RawMessage) (domaintool.ToolResult, error) {
			data, err := json.Marshal(retrieval.EvidenceResult{Evidence: items})
			if err != nil {
				return domaintool.ToolResult{}, err
			}
			return domaintool.ToolResult{
				Status:  domaintool.ToolStatusOK,
				Content: "已取到证据。",
				Data:    data,
			}, nil
		},
	}
}

func citable(id, restaurantID int64, docType evidence.DocType, content string) evidence.Evidence {
	return evidence.Evidence{
		EvidenceID:   id,
		RestaurantID: restaurantID,
		DocType:      docType,
		Content:      content,
		Source:       "yelp_review",
		SnapshotAt:   time.Date(2021, 9, 1, 12, 0, 0, 0, time.UTC),
	}
}

func register(t *testing.T, registry *toolreg.Registry, entry toolreg.Entry) {
	t.Helper()
	if err := registry.Register(entry); err != nil {
		t.Fatalf("register %s: %v", entry.Spec.Name, err)
	}
}

// ambiguousRegistry registers the real resolve_restaurant tool over a corpus
// where the name matches two different branches equally well.
func ambiguousRegistry(t *testing.T) *toolreg.Registry {
	t.Helper()
	registry := toolreg.New(0)
	register(t, registry, tools.ResolveRestaurantEntry(fixedResolver{rows: []search.RestaurantCandidate{
		joes(7, "manhattan", "7 Carmine St", 0.92),
		joes(8, "brooklyn", "235 Bedford Ave", 0.90),
	}}, tools.ResolveConfig{}))
	return registry
}

func awaitingInputEvents(events []agent.Event) []agent.Event {
	var out []agent.Event
	for _, ev := range events {
		if ev.Type == agent.EventAwaitingInput {
			out = append(out, ev)
		}
	}
	return out
}

// ---- the clarification path ------------------------------------------------

// A name that matches two branches equally well is not a failure and not a
// guess: the turn stops, asks, and leaves the question in the database so the
// next request finds a thread that is genuinely waiting.
//
// The assertion that matters is the coupling of four things: the text the user
// reads, the stream event a client routes on, the conversation state another
// request reads, and the candidate snapshot the user's answer will be resolved
// against. Any one of them alone would leave the thread in a state nothing else
// agrees with.
func TestAnAmbiguousRestaurantNameParksTheThreadOnAQuestion(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewConversationRepository()

	provider := &scriptedProvider{
		supportTools: true,
		toolResps: []domainchat.ToolCallResponse{
			toolCallResponse("c1", tools.ResolveRestaurantToolName, `{"name":"Joe's Pizza"}`),
		},
	}
	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 3}, agent.Deps{
		Chat:          provider,
		ToolCalling:   provider,
		Registry:      ambiguousRegistry(t),
		Conversations: repo,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	const threadID = "th-ambiguous"
	result, stream := runner.Run(ctx, agent.TurnInput{
		ThreadID:  threadID,
		UserID:    "u1",
		UserInput: "Joes Pizza 怎么样？",
	})
	events := drain(stream)

	// The question a person reads. It has to distinguish the candidates by what
	// a person would use — a neighbourhood, an address — and never by the ids
	// the system happens to store them under.
	question := result.Answer.Text
	if !strings.Contains(question, "你说的是哪一家") {
		t.Fatalf("the turn must ask which branch was meant:\n%s", question)
	}
	for _, want := range []string{"Joe's Pizza", "manhattan", "brooklyn", "7 Carmine St"} {
		if !strings.Contains(question, want) {
			t.Fatalf("the question must describe the candidates by %q:\n%s", want, question)
		}
	}
	if strings.Contains(question, "id=7") || strings.Contains(question, "id=8") {
		t.Fatalf("the question must not list database ids:\n%s", question)
	}

	// The stream event. A client that only sees message.end cannot tell a
	// finished answer from a question.
	parked := awaitingInputEvents(events)
	if len(parked) != 1 {
		t.Fatalf("awaiting-input events = %d, want exactly 1", len(parked))
	}
	if parked[0].State != string(conversation.StateAwaitingClarification) {
		t.Fatalf("event state = %q", parked[0].State)
	}
	if parked[0].PendingAction != tools.ResolveRestaurantToolName {
		t.Fatalf("event pending action = %q, want %q",
			parked[0].PendingAction, tools.ResolveRestaurantToolName)
	}
	if len(parked[0].MissingSlots) != 1 || parked[0].MissingSlots[0] != slots.SlotRestaurantID {
		t.Fatalf("event missing slots = %v, want [%s]", parked[0].MissingSlots, slots.SlotRestaurantID)
	}

	// The conversation row, which is what a different request reads.
	conv, err := repo.Get(ctx, threadID)
	if err != nil {
		t.Fatalf("get conversation: %v", err)
	}
	if conv.CurrentState != conversation.StateAwaitingClarification {
		t.Fatalf("conversation state = %q, want %q",
			conv.CurrentState, conversation.StateAwaitingClarification)
	}

	// The checkpoint, which is what survives a restart.
	checkpoint, err := repo.LoadCheckpoint(ctx, threadID)
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if checkpoint.PendingAction != tools.ResolveRestaurantToolName {
		t.Fatalf("checkpoint pending action = %q", checkpoint.PendingAction)
	}
	if checkpoint.ClarificationCount != 1 {
		t.Fatalf("clarification count = %d, want 1", checkpoint.ClarificationCount)
	}
	if len(checkpoint.MissingSlots) != 1 || checkpoint.MissingSlots[0] != slots.SlotRestaurantID {
		t.Fatalf("checkpoint missing slots = %v", checkpoint.MissingSlots)
	}

	// The candidate snapshot: "第二家" has to mean something on the next request,
	// and it can only mean something against one ordered list.
	candidates, err := repo.ListCandidates(ctx, threadID)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("persisted %d candidates, want the 2 the question offered", len(candidates))
	}
	for i, want := range []struct {
		position int
		id       int64
	}{{1, 7}, {2, 8}} {
		if candidates[i].Position != want.position || candidates[i].RestaurantID != want.id {
			t.Fatalf("candidate %d = (pos %d, id %d), want (pos %d, id %d)",
				i, candidates[i].Position, candidates[i].RestaurantID, want.position, want.id)
		}
		if candidates[i].Name != "Joe's Pizza" {
			t.Fatalf("candidate %d name = %q", i, candidates[i].Name)
		}
	}
}

// The budget is per thread and survives requests, so the turn has to be able to
// count rounds it did not start. With the budget spent, the turn stops asking
// and answers on a stated assumption — the loop the cap exists to break.
func TestTheClarificationCapAnswersOnAStatedAssumption(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewConversationRepository()
	seedWaitingThread(t, repo, "th-capped", 1)

	provider := &scriptedProvider{
		supportTools: true,
		toolResps: []domainchat.ToolCallResponse{
			toolCallResponse("c1", tools.ResolveRestaurantToolName, `{"name":"Joe's Pizza"}`),
			assistantText("按最相似的「Joe's Pizza」（曼哈顿）继续。"),
		},
	}
	runner, err := agent.NewRunner(agent.Config{
		MaxToolRounds:     3,
		MaxClarifications: 1,
	}, agent.Deps{
		Chat:          provider,
		ToolCalling:   provider,
		Registry:      ambiguousRegistry(t),
		Conversations: repo,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	const threadID = "th-capped"
	result, stream := runner.Run(ctx, agent.TurnInput{
		ThreadID:  threadID,
		UserInput: "Joes Pizza 怎么样？",
	})
	events := drain(stream)

	if got := awaitingInputEvents(events); len(got) != 0 {
		t.Fatalf("the cap was already spent, yet the turn asked again: %+v", got)
	}
	if result.Answer == nil || !strings.Contains(result.Answer.Text, "Joe's Pizza") {
		t.Fatalf("answer = %+v, want the model's text on the assumed restaurant", result.Answer)
	}
	if !containsSubstring(result.Warnings, "已达上限") &&
		!containsSubstring(result.Warnings, "最相似的") {
		t.Fatalf("the assumption must be recorded as a warning: %v", result.Warnings)
	}

	checkpoint, err := repo.LoadCheckpoint(ctx, threadID)
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if checkpoint.SelectedRestaurantID != 7 {
		t.Fatalf("assumed restaurant = %d, want the best-scoring 7", checkpoint.SelectedRestaurantID)
	}
	// The turn ended normally, so the thread is free again and the budget starts
	// over: a clarification counter that never reset would make the next
	// ambiguity unreportable.
	if checkpoint.ClarificationCount != 0 {
		t.Fatalf("clarification count = %d, want 0 after the thread went idle",
			checkpoint.ClarificationCount)
	}
	if checkpoint.State != conversation.StateIdle {
		t.Fatalf("checkpoint state = %q, want idle", checkpoint.State)
	}
}

// ---- the named-restaurant answer -------------------------------------------

// A name that resolves cleanly pins the restaurant, reads its evidence, and
// answers with citations that all come from that restaurant. The scope is the
// point: a citation to a document about a different place would be a claim the
// answer cannot support, whatever it said.
func TestANamedRestaurantAnswerCitesOnlyThatRestaurantsEvidence(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewConversationRepository()

	// Two kinds of document, so the evidence is enough to answer without the
	// adequacy caveat, and both about Joe's Pizza at id 42.
	docs := []evidence.Evidence{
		citable(501, 42, evidence.DocTypeRestaurantProfile, "1972 年开业的老店。"),
		citable(502, 42, evidence.DocTypeRestaurantHours, "周一至周日 11:00-23:00。"),
	}
	byID := make(map[int64]int64, len(docs))
	for _, doc := range docs {
		byID[doc.EvidenceID] = doc.RestaurantID
	}

	const composed = "Joe's Pizza 是 1972 年开业的老店[^501]，营业时间每天 11:00-23:00[^502]。"
	provider := &scriptedProvider{
		supportTools: true,
		toolResps: []domainchat.ToolCallResponse{
			toolCallResponse("c1", tools.ResolveRestaurantToolName, `{"name":"Joe's Pizza"}`),
			toolCallResponse("c2", tools.RestaurantEvidenceToolName, `{"restaurant_id":42}`),
			assistantText(composed + "\nFOLLOWUPS: []"),
		},
		// The composer asks the plain completion port, so the grounded answer
		// comes from here. It is scripted to cite both documents: the point of
		// the test is that those two citations are the only ones available.
		completeResps: []domainchat.ChatResponse{{
			Message:      domainchat.ChatMessage{Role: domainchat.RoleAssistant, Content: composed + "\nFOLLOWUPS: []"},
			FinishReason: domainchat.FinishReasonStop,
		}},
	}
	registry := toolreg.New(0)
	register(t, registry, tools.ResolveRestaurantEntry(fixedResolver{rows: []search.RestaurantCandidate{
		joes(42, "manhattan", "7 Carmine St", 0.97),
	}}, tools.ResolveConfig{}))
	register(t, registry, stubEvidenceEntry(docs))

	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 4}, agent.Deps{
		Chat:          provider,
		ToolCalling:   provider,
		Registry:      registry,
		Conversations: repo,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	const threadID = "th-named"
	result, stream := runner.Run(ctx, agent.TurnInput{
		ThreadID:  threadID,
		UserInput: "Joe's Pizza 怎么样？",
	})
	events := drain(stream)

	// A name that resolved is not a question, so nothing may be parked.
	if got := awaitingInputEvents(events); len(got) != 0 {
		t.Fatalf("a resolved name must not ask anything: %+v", got)
	}
	if strings.Contains(result.Answer.Text, "资料不足") {
		t.Fatalf("two kinds of document are enough to answer:\n%s", result.Answer.Text)
	}

	citations := result.Answer.Citations
	if len(citations) != 2 {
		t.Fatalf("citations = %v, want both documents", citations)
	}
	for _, id := range citations {
		restaurantID, ok := byID[id]
		if !ok {
			t.Fatalf("citation %d is not in this turn's evidence set", id)
		}
		if restaurantID != 42 {
			t.Fatalf("citation %d belongs to restaurant %d, want the addressed 42",
				id, restaurantID)
		}
	}

	// The scope has to be persisted, not merely used: a follow-up turn reads the
	// checkpoint to know which restaurant the thread is talking about.
	checkpoint, err := repo.LoadCheckpoint(ctx, threadID)
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if checkpoint.SelectedRestaurantID != 42 {
		t.Fatalf("checkpoint restaurant = %d, want 42", checkpoint.SelectedRestaurantID)
	}
	if len(checkpoint.EvidenceIDs) != 2 {
		t.Fatalf("checkpoint evidence ids = %v, want the two cited", checkpoint.EvidenceIDs)
	}
}

// ---- helpers --------------------------------------------------------------

func containsSubstring(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}

// seedWaitingThread leaves a thread that has already asked once and not been
// answered, which is what a second ambiguous request finds.
func seedWaitingThread(t *testing.T, repo *testkit.ConversationRepository, threadID string, count int) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2021, 9, 1, 12, 0, 0, 0, time.UTC)
	if err := repo.Upsert(ctx, conversation.Conversation{
		ThreadID:     threadID,
		UserID:       "u1",
		CurrentState: conversation.StateAwaitingClarification,
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("seed thread: %v", err)
	}
	if err := repo.SaveCheckpoint(ctx, conversation.Checkpoint{
		ThreadID:           threadID,
		Version:            1,
		State:              conversation.StateAwaitingClarification,
		PendingAction:      tools.ResolveRestaurantToolName,
		MissingSlots:       []string{slots.SlotRestaurantID},
		ClarificationCount: count,
		CreatedAt:          now,
	}); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}
}
