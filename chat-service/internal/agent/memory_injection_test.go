package agent_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	domainmemory "github.com/zed1995/platepilot/shared/domain/memory"
	"github.com/zed1995/platepilot/shared/store"
	"github.com/zed1995/platepilot/shared/testkit"

	"github.com/zed1995/platepilot/chat-service/internal/agent"
)

// TestRunnerInjectsEveryConstraintAndRetrievesTheRest pins the injection
// policy: constraints are hard requirements and go in whole, while preferences
// and facts are chosen by relevance to what the user just asked.
//
// The fixture is built so that both of the older rules fail it. The two
// memories the query is about are the oldest and the least confident of the
// set, and the twelve that are neither are the newest and the most confident:
// a recency window and a confidence window each keep the wrong twelve, so
// only retrieval can produce the segment asserted below.
func TestRunnerInjectsEveryConstraintAndRetrievesTheRest(t *testing.T) {
	memRepo := testkit.NewMemoryRepository()

	constraints := []string{
		// This one matches a term of the query. Search is not type-aware, so
		// it comes back from the store as well and has to be recognised as
		// already-injected rather than appended a second time.
		"不吃布鲁克林的生蚝",
		"不接受等位超过 30 分钟",
		"同行的朋友不吃辣",
	}
	for _, content := range constraints {
		upsertMemory(t, memRepo, domainmemory.Memory{
			UserID: "u-search", Type: domainmemory.MemoryTypeConstraint,
			Content: content, Confidence: 0.4,
		})
	}

	relevant := []domainmemory.Memory{
		{UserID: "u-search", Type: domainmemory.MemoryTypePreference, Content: "只在布鲁克林吃意大利菜", Confidence: 0.1},
		{UserID: "u-search", Type: domainmemory.MemoryTypePreference, Content: "偏爱安静点的餐厅", Confidence: 0.1},
	}
	for _, mem := range relevant {
		upsertMemory(t, memRepo, mem)
	}

	for i := 0; i < 12; i++ {
		upsertMemory(t, memRepo, domainmemory.Memory{
			UserID: "u-search", Type: domainmemory.MemoryTypeFact,
			Content: fmt.Sprintf("第 %d 次去过的店在皇后区", i), Confidence: 0.9,
		})
	}

	provider := directAnswerProvider("收到")
	runner := newStateRunner(t, provider, agent.Deps{Memories: memRepo})

	// Every term here clears store.MemoryQueryMinRunes. A shorter one would be
	// dropped before it reached the store, and the assertion below would be
	// testing a query that was never asked.
	result := runDirect(t, runner, "th-mem-search", "u-search", "布鲁克林 安静点 意大利菜")

	segment := memorySegmentOf(t, provider)
	for _, want := range constraints {
		if !strings.Contains(segment, want) {
			t.Fatalf("constraint %q missing from the segment:\n%s", want, segment)
		}
	}
	for _, mem := range relevant {
		if !strings.Contains(segment, mem.Content) {
			t.Fatalf("retrieved memory %q missing from the segment:\n%s", mem.Content, segment)
		}
	}
	if strings.Contains(segment, "皇后区") {
		t.Fatalf("irrelevant memories were injected:\n%s", segment)
	}
	if got := strings.Count(segment, constraints[0]); got != 1 {
		t.Fatalf("constraint injected %d times, want 1:\n%s", got, segment)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "按本轮相关性截断 12 条") {
		t.Fatalf("warnings = %v, want one relevance-trim warning for 12 dropped", result.Warnings)
	}
}

// TestRunnerFallsBackWhenMemorySearchFails covers the adapter being unable to
// answer rather than having nothing to say. Grounding a turn does not depend on
// the memory index, so the turn still gets the memories it can — the user's
// stated preferences were the whole point of loading them.
func TestRunnerFallsBackWhenMemorySearchFails(t *testing.T) {
	memRepo := testkit.NewMemoryRepository()
	upsertMemory(t, memRepo, domainmemory.Memory{
		UserID: "u-broken", Type: domainmemory.MemoryTypeConstraint, Content: "不吃香菜", Confidence: 0.9,
	})
	upsertMemory(t, memRepo, domainmemory.Memory{
		UserID: "u-broken", Type: domainmemory.MemoryTypePreference, Content: "偏好日料", Confidence: 0.8,
	})

	provider := directAnswerProvider("收到")
	runner := newStateRunner(t, provider, agent.Deps{
		Memories: failingSearchMemories{MemoryRepository: memRepo},
	})

	result := runDirect(t, runner, "th-mem-broken", "u-broken", "推荐个餐厅")

	segment := memorySegmentOf(t, provider)
	for _, want := range []string{"不吃香菜", "偏好日料"} {
		if !strings.Contains(segment, want) {
			t.Fatalf("memory %q withheld after a failed search:\n%s", want, segment)
		}
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "检索失败") {
		t.Fatalf("warnings = %v, want one search-failure warning", result.Warnings)
	}
}

// memorySegmentOf returns the injected long-term-memory segment from the
// turn's planning request.
//
// It identifies the segment by the memory preamble rather than by position,
// because the plan prompt, the reference block and the segment are all system
// messages and their order is not what this file is asserting.
func memorySegmentOf(t *testing.T, p *scriptedProvider) string {
	t.Helper()
	if len(p.completeReqs) == 0 {
		t.Fatal("no planning request was recorded")
	}
	for _, msg := range p.completeReqs[0].Messages {
		if strings.HasPrefix(msg.Content, "以下是该用户的长期记忆") {
			return msg.Content
		}
	}
	t.Fatalf("no memory segment was injected: %+v", p.completeReqs[0].Messages)
	return ""
}

func upsertMemory(t *testing.T, repo store.MemoryRepository, mem domainmemory.Memory) {
	t.Helper()
	if err := repo.Upsert(context.Background(), mem); err != nil {
		t.Fatalf("upsert memory %q: %v", mem.Content, err)
	}
}

// failingSearchMemories is a repository whose List works and whose Search does
// not, standing in for an adapter whose index is unavailable. Embedding the
// interface keeps every other method pointed at the working store.
type failingSearchMemories struct {
	store.MemoryRepository
}

func (failingSearchMemories) Search(
	context.Context, string, string, int,
) ([]domainmemory.Memory, error) {
	return nil, errors.New("memory search index unavailable")
}
