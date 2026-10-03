package agent_test

import (
	"context"
	"strings"
	"testing"

	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/errs"
	domainmemory "github.com/zed1995/platepilot/shared/domain/memory"
	"github.com/zed1995/platepilot/shared/store"
	"github.com/zed1995/platepilot/shared/testkit"

	"github.com/zed1995/platepilot/chat-service/internal/agent"
	"github.com/zed1995/platepilot/chat-service/internal/agent/slots"
	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
	"github.com/zed1995/platepilot/chat-service/internal/agent/tools"
	"github.com/zed1995/platepilot/chat-service/internal/memorywrite"
)

// memoryRunner builds a runner whose registry offers save_memory over the given
// store, so a turn can be observed end to end: the tool runs, the row lands, and
// the event reports it.
func memoryRunner(
	t *testing.T, provider *scriptedProvider, memories store.MemoryRepository,
) *agent.Runner {
	t.Helper()
	svc, err := memorywrite.NewService(memories)
	if err != nil {
		t.Fatalf("memorywrite.NewService: %v", err)
	}
	registry := toolreg.New(0)
	register(t, registry, tools.SaveMemoryEntry(svc))
	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 4}, agent.Deps{
		Chat:        provider,
		ToolCalling: provider,
		Registry:    registry,
		Extractor:   slots.New(slots.Deps{}),
		Memories:    memories,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner
}

// saveMemoryCall scripts the model asking to remember something.
func saveMemoryCall(id, content, memType, quote string) domainchat.ToolCallResponse {
	args := `{"content":"` + content + `","memory_type":"` + memType +
		`","quoted_user_text":"` + quote + `"}`
	return toolCallResponse(id, tools.SaveMemoryToolName, args)
}

// TestAUserRequestedMemoryIsWrittenAndAnnounced is the slice's acceptance test.
// It asserts the three halves of one thing: the row exists, the stream says so,
// and both name the same memory.
func TestAUserRequestedMemoryIsWrittenAndAnnounced(t *testing.T) {
	ctx := context.Background()
	memories := testkit.NewMemoryRepository()
	provider := &scriptedProvider{
		supportTools: true,
		toolResps: []domainchat.ToolCallResponse{
			saveMemoryCall("c1", "不吃辣", "constraint", "我不吃辣"),
			assistantText("好的，以后给你推荐不辣的。"),
		},
	}
	runner := memoryRunner(t, provider, memories)

	_, stream := runner.Run(ctx, agent.TurnInput{
		ThreadID: "th-1", UserID: "u1", UserInput: "记住我不吃辣",
	})
	events := drain(stream)

	saved := memorySavedEvents(events)
	if len(saved) != 1 {
		t.Fatalf("memory.saved events = %d, want exactly 1", len(saved))
	}
	if saved[0].MemoryContent != "不吃辣" {
		t.Fatalf("event content = %q", saved[0].MemoryContent)
	}
	if saved[0].MemoryType != string(domainmemory.MemoryTypeConstraint) {
		t.Fatalf("event type = %q", saved[0].MemoryType)
	}
	if saved[0].MemoryRefreshed {
		t.Fatal("a first save was announced as a refresh")
	}

	rows, err := memories.List(ctx, "u1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("memories = %d, want 1", len(rows))
	}
	// The row and the frame describe one memory: a client that stored the event
	// must be able to ask the API to delete exactly what it was told about.
	if rows[0].ID != saved[0].MemoryID {
		t.Fatalf("event id %q does not name the stored row %q", saved[0].MemoryID, rows[0].ID)
	}
	if rows[0].Source != memorywrite.SourceUserRequest {
		t.Fatalf("source = %q, want %q", rows[0].Source, memorywrite.SourceUserRequest)
	}
}

// A model that decided on its own to remember something has nothing to quote,
// so the write is refused and the turn goes on without one. The refusal is
// visible to the model as a tool error — which is what lets it either stop or
// ask the user — and invisible in the store.
func TestAFabricatedMemoryIsRefusedAtTheToolBoundary(t *testing.T) {
	ctx := context.Background()
	memories := testkit.NewMemoryRepository()
	provider := &scriptedProvider{
		supportTools: true,
		toolResps: []domainchat.ToolCallResponse{
			// The user said nothing about spicy food; the model invented both
			// the memory and the quote.
			saveMemoryCall("c1", "喜欢安静的环境", "preference", "我喜欢安静的环境"),
			assistantText("好的。"),
		},
	}
	runner := memoryRunner(t, provider, memories)

	_, stream := runner.Run(ctx, agent.TurnInput{
		ThreadID: "th-1", UserID: "u1", UserInput: "帮我找一家意大利餐厅",
	})
	events := drain(stream)

	if saved := memorySavedEvents(events); len(saved) != 0 {
		t.Fatalf("a fabricated memory was announced: %+v", saved)
	}
	rows, err := memories.List(ctx, "u1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("a fabricated memory was written: %+v", rows)
	}

	// The tool did run and did fail, so the model is told why. A silent no-op
	// would leave it believing the memory was kept.
	failures := toolFailures(events, tools.SaveMemoryToolName)
	if len(failures) != 1 {
		t.Fatalf("tool.finish failures for save_memory = %d, want 1", len(failures))
	}
	if !strings.Contains(failures[0].Error, string(errs.CodeMemoryWriteNotRequested)) {
		t.Fatalf("the failure does not name the guard: %q", failures[0].Error)
	}
}

// Saying the same thing twice refreshes the row, and the event says so: a client
// that announced a new memory each time would be describing a duplicate that
// does not exist.
func TestARepeatedRequestRefreshesAndSaysSo(t *testing.T) {
	ctx := context.Background()
	memories := testkit.NewMemoryRepository()
	provider := &scriptedProvider{
		supportTools: true,
		toolResps: []domainchat.ToolCallResponse{
			saveMemoryCall("c1", "不吃辣", "constraint", "我不吃辣"),
			assistantText("好的。"),
			saveMemoryCall("c2", "不吃辣", "constraint", "我不吃辣"),
			assistantText("已经记着了。"),
		},
	}
	runner := memoryRunner(t, provider, memories)

	for i := 0; i < 2; i++ {
		_, stream := runner.Run(ctx, agent.TurnInput{
			ThreadID: "th-1", UserID: "u1", UserInput: "记住我不吃辣",
		})
		events := drain(stream)
		saved := memorySavedEvents(events)
		if len(saved) != 1 {
			t.Fatalf("turn %d: memory.saved events = %d, want 1", i+1, len(saved))
		}
		if wantRefreshed := i == 1; saved[0].MemoryRefreshed != wantRefreshed {
			t.Fatalf("turn %d: refreshed = %v, want %v", i+1, saved[0].MemoryRefreshed, wantRefreshed)
		}
	}

	rows, err := memories.List(ctx, "u1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("memories = %d, want 1 after saying it twice", len(rows))
	}
}

// ---- the injection closed loop --------------------------------------------

// TestASavedConstraintReachesTheNextTurnsPrompt is the other half of the slice:
// a memory that is written but never injected is a row the user can see and the
// model cannot, which would make "remember this" a promise the system does not
// keep.
//
// The loop is closed in three steps — save, observe, delete — because a test
// that stopped after "the segment appeared" would pass on an implementation that
// injected everything ever written, including what the user deleted.
func TestASavedConstraintReachesTheNextTurnsPrompt(t *testing.T) {
	ctx := context.Background()
	memories := testkit.NewMemoryRepository()
	provider := &scriptedProvider{
		supportTools: true,
		toolResps: []domainchat.ToolCallResponse{
			// Turn one: the user asks to be remembered.
			saveMemoryCall("c1", "不吃辣", "constraint", "我不吃辣"),
			assistantText("好的，以后给你推荐不辣的。"),
			// Turn two and three: plain questions, so the segment is the only
			// thing under test.
			assistantText("好的。"),
			assistantText("好的。"),
		},
	}
	runner := memoryRunner(t, provider, memories)

	if _, stream := runner.Run(ctx, agent.TurnInput{
		ThreadID: "th-1", UserID: "u1", UserInput: "记住我不吃辣",
	}); stream != nil {
		drain(stream)
	}
	afterSave := len(provider.toolReqs)

	// Turn two: the constraint has to be in the system segment of the planning
	// prompt, and it has to be labelled as a constraint rather than as a fact —
	// the label is what tells the model it is a hard requirement.
	if _, stream := runner.Run(ctx, agent.TurnInput{
		ThreadID: "th-1", UserID: "u1", UserInput: "那推荐几家吧",
	}); stream != nil {
		drain(stream)
	}
	segment := memorySegmentIn(t, provider.toolReqs[afterSave].req.Messages)
	if segment == "" {
		t.Fatal("the saved memory was not injected on the next turn")
	}
	if !strings.Contains(segment, "不吃辣") {
		t.Fatalf("the segment does not carry the memory: %q", segment)
	}
	if !strings.Contains(segment, "约束") {
		t.Fatalf("the segment does not classify the memory as a constraint: %q", segment)
	}

	// The user changes their mind. A deleted memory must stop reaching the model
	// on the very next turn, without waiting for anything to expire.
	rows, err := memories.List(ctx, "u1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("memories = %d, want 1", len(rows))
	}
	if err := memories.Delete(ctx, "u1", rows[0].ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	afterDelete := len(provider.toolReqs)

	if _, stream := runner.Run(ctx, agent.TurnInput{
		ThreadID: "th-1", UserID: "u1", UserInput: "那推荐几家吧",
	}); stream != nil {
		drain(stream)
	}
	if segment := memorySegmentIn(t, provider.toolReqs[afterDelete].req.Messages); segment != "" {
		t.Fatalf("a deleted memory still reached the prompt: %q", segment)
	}
}

// An anonymous turn has nobody to remember anything for, so injection is skipped
// rather than shared between users.
func TestMemoriesAreNotInjectedWithoutAUser(t *testing.T) {
	ctx := context.Background()
	memories := testkit.NewMemoryRepository()
	if err := memories.Upsert(ctx, domainmemory.Memory{
		UserID: "u1", Type: domainmemory.MemoryTypeConstraint, Content: "不吃辣", Confidence: 1.0,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	provider := &scriptedProvider{
		supportTools: true,
		toolResps:    []domainchat.ToolCallResponse{assistantText("好的。")},
		completeResps: []domainchat.ChatResponse{{Message: domainchat.ChatMessage{
			Role: domainchat.RoleAssistant, Content: "好的。",
		}}},
	}
	runner := memoryRunner(t, provider, memories)

	if _, stream := runner.Run(ctx, agent.TurnInput{
		ThreadID: "th-1", UserInput: "推荐几家",
	}); stream != nil {
		drain(stream)
	}
	for _, req := range provider.toolReqs {
		if segment := memorySegmentIn(t, req.req.Messages); segment != "" {
			t.Fatalf("an anonymous turn received a user's memories: %q", segment)
		}
	}
}

// ---- helpers --------------------------------------------------------------

func memorySavedEvents(events []agent.Event) []agent.Event {
	var out []agent.Event
	for _, ev := range events {
		if ev.Type == agent.EventMemorySaved {
			out = append(out, ev)
		}
	}
	return out
}

func toolFailures(events []agent.Event, tool string) []agent.Event {
	var out []agent.Event
	for _, ev := range events {
		if ev.Type == agent.EventToolFinish && ev.Tool == tool && !ev.OK {
			out = append(out, ev)
		}
	}
	return out
}

// memorySegmentIn returns the injected memory segment of a prompt, or "" when
// there is none.
//
// It looks for the segment's own opening sentence rather than for a keyword,
// because the point of the assertion is that the *segment* is present or absent
// — a bare content match would also fire on the user's own message.
func memorySegmentIn(t *testing.T, messages []domainchat.ChatMessage) string {
	t.Helper()
	const marker = "以下是该用户的长期记忆"
	for _, msg := range messages {
		if msg.Role == domainchat.RoleSystem && strings.Contains(msg.Content, marker) {
			return msg.Content
		}
	}
	return ""
}
