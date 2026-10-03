package app

import (
	"context"
	"testing"

	"github.com/zed1995/platepilot/chat-service/internal/agent"
	"github.com/zed1995/platepilot/chat-service/internal/agent/tools"
	"github.com/zed1995/platepilot/chat-service/internal/httpapi"
	"github.com/zed1995/platepilot/chat-service/internal/memorywrite"
	"github.com/zed1995/platepilot/shared/domain/errs"
	domainmemory "github.com/zed1995/platepilot/shared/domain/memory"
	"github.com/zed1995/platepilot/shared/testkit"
)

// memoryService builds the adapter over a real memory store, so an edit and its
// effect on the list are observed through the same store the endpoints use.
func memoryService(t *testing.T) (*chatService, *testkit.MemoryRepository) {
	t.Helper()
	repo := testkit.NewMemoryRepository()
	writer, err := memorywrite.NewService(repo)
	if err != nil {
		t.Fatalf("memorywrite.NewService: %v", err)
	}
	return newChatService(chatServiceDeps{Memories: repo, MemoryWrite: writer}), repo
}

func seedMemory(t *testing.T, repo *testkit.MemoryRepository, content string) domainmemory.Memory {
	t.Helper()
	mem := domainmemory.Memory{
		ID:         "mem-1",
		UserID:     "user-1",
		Type:       domainmemory.MemoryTypePreference,
		Content:    content,
		Source:     memorywrite.SourceUserRequest,
		Confidence: 0.9,
	}
	if err := repo.Upsert(context.Background(), mem); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	stored, err := repo.List(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("seeded %d memories, want 1", len(stored))
	}
	return stored[0]
}

// An edit has to be visible on the very next read. A memory the user can change
// but not see changed is not manageable — it is a row they can only delete.
func TestAnEditIsVisibleOnTheNextList(t *testing.T) {
	ctx := context.Background()
	svc, repo := memoryService(t)
	seedMemory(t, repo, "不吃辣")

	reworded := "不吃辣，也不吃香菜"
	updated, err := svc.UpdateMemory(ctx, httpapi.UpdateMemoryInput{
		UserID: "user-1", MemoryID: "mem-1", Content: &reworded,
	})
	if err != nil {
		t.Fatalf("UpdateMemory: %v", err)
	}
	if updated.Content != reworded {
		t.Fatalf("the edit answered with %q", updated.Content)
	}

	page, err := svc.ListMemories(ctx, "user-1")
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	if len(page.Memories) != 1 {
		t.Fatalf("memories = %d, want 1 (an edit is not a delete plus an insert)", len(page.Memories))
	}
	if page.Memories[0].Content != reworded {
		t.Fatalf("the list still shows %q", page.Memories[0].Content)
	}
	if page.Memories[0].ID != "mem-1" {
		t.Fatalf("the id changed to %q", page.Memories[0].ID)
	}
}

// The type carries its own weight, so switching a preference to a constraint
// changes what the model is told about it.
func TestChangingTheTypeMovesTheConfidenceWithIt(t *testing.T) {
	ctx := context.Background()
	svc, repo := memoryService(t)
	seedMemory(t, repo, "不吃辣")

	constraint := domainmemory.MemoryTypeConstraint
	updated, err := svc.UpdateMemory(ctx, httpapi.UpdateMemoryInput{
		UserID: "user-1", MemoryID: "mem-1", Type: &constraint,
	})
	if err != nil {
		t.Fatalf("UpdateMemory: %v", err)
	}
	if updated.Type != string(domainmemory.MemoryTypeConstraint) {
		t.Fatalf("type = %q", updated.Type)
	}
	if updated.Confidence != memorywrite.ConfidenceByType[domainmemory.MemoryTypeConstraint] {
		t.Fatalf("confidence = %g, want the constraint's weight", updated.Confidence)
	}
	if updated.Content != "不吃辣" {
		t.Fatalf("a type-only edit changed the content to %q", updated.Content)
	}
}

// Another user's memory is not found, and the refusal says nothing about why.
func TestEditingAnotherUsersMemoryIsNotFound(t *testing.T) {
	ctx := context.Background()
	svc, repo := memoryService(t)
	seedMemory(t, repo, "不吃辣")

	reworded := "不吃香菜"
	_, err := svc.UpdateMemory(ctx, httpapi.UpdateMemoryInput{
		UserID: "user-2", MemoryID: "mem-1", Content: &reworded,
	})
	if err == nil {
		t.Fatal("another user's memory was editable")
	}
	if code := errs.CodeOf(err); code != errs.CodeNotFound {
		t.Fatalf("code = %q, want %q", code, errs.CodeNotFound)
	}
}

// With no memory-write service the management route reports the capability as
// unavailable rather than as a missing memory: the two are different answers.
func TestMemoryManagementReportsADisabledCapability(t *testing.T) {
	svc := newChatService(chatServiceDeps{})
	reworded := "不吃香菜"

	_, err := svc.UpdateMemory(context.Background(), httpapi.UpdateMemoryInput{
		UserID: "user-1", MemoryID: "mem-1", Content: &reworded,
	})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if code := errs.CodeOf(err); code != errs.CodeInvalidArgument {
		t.Fatalf("code = %q, want %q", code, errs.CodeInvalidArgument)
	}
}

// ---- the switch -----------------------------------------------------------

// Switching the write path off has to remove the tool from the model's view, not
// leave it registered and failing: a model that can see save_memory will promise
// the user it remembered something.
func TestSaveMemoryIsRegisteredOnlyWhenTheSwitchIsOn(t *testing.T) {
	on := assemblyConfig()
	on.Agent.MemoryWriteEnabled = true
	application := newAssembly(t, on, reservationDeps(&testkit.MockChatProvider{Tools: true}))
	if !offered(application, tools.SaveMemoryToolName) {
		t.Fatal("save_memory is missing with AGENT_MEMORY_WRITE_ENABLED=true")
	}

	off := assemblyConfig()
	off.Agent.MemoryWriteEnabled = false
	application = newAssembly(t, off, reservationDeps(&testkit.MockChatProvider{Tools: true}))
	if offered(application, tools.SaveMemoryToolName) {
		t.Fatal("save_memory was offered with AGENT_MEMORY_WRITE_ENABLED=false")
	}
}

// Without a memory store the tool could only fail, so it is not offered — and
// the deployment still starts, because a read-only deployment is a supported way
// to run.
func TestSaveMemoryIsNotOfferedWithoutAMemoryStore(t *testing.T) {
	cfg := assemblyConfig()
	cfg.Agent.MemoryWriteEnabled = true
	deps := reservationDeps(&testkit.MockChatProvider{Tools: true})
	deps.Memories = nil

	application := newAssembly(t, cfg, deps)
	if offered(application, tools.SaveMemoryToolName) {
		t.Fatal("save_memory was offered with no memory store to write to")
	}
}

// The event has to survive the agent-to-transport hop: a client that could not
// see what was saved would have to re-read the memory list after every turn.
func TestToStreamEventCarriesTheSavedMemory(t *testing.T) {
	out := toStreamEvent(agent.Event{
		Type:            agent.EventMemorySaved,
		RunID:           "r1",
		ThreadID:        "t1",
		MemoryID:        "mem-1",
		MemoryType:      string(domainmemory.MemoryTypeConstraint),
		MemoryContent:   "不吃辣",
		MemoryRefreshed: true,
	})

	if out.Type != httpapi.StreamMemorySaved {
		t.Fatalf("type = %q, want %q", out.Type, httpapi.StreamMemorySaved)
	}
	if out.MemoryID != "mem-1" || out.MemoryContent != "不吃辣" {
		t.Fatalf("the event lost its payload: %+v", out)
	}
	if out.MemoryType != string(domainmemory.MemoryTypeConstraint) {
		t.Fatalf("type = %q", out.MemoryType)
	}
	if !out.MemoryRefreshed {
		t.Fatal("the refreshed flag did not survive the hop")
	}
}

// A plain turn must not grow a memory payload on the way out, for the same
// reason it must not grow a pending state.
func TestToStreamEventLeavesTheSavedMemoryEmptyOnAnAnswer(t *testing.T) {
	out := toStreamEvent(agent.Event{Type: agent.EventDelta, ThreadID: "t1", Delta: "你好"})
	if out.MemoryID != "" || out.MemoryContent != "" || out.MemoryRefreshed {
		t.Fatalf("a text delta grew a memory payload: %+v", out)
	}
}
