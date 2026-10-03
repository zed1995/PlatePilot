package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	domainmemory "github.com/zed/platepilot/shared/domain/memory"
)

// maxInjectedMemories bounds how many memories one turn can inject.
const maxInjectedMemories = 10

// loadMemoryContext loads the user's live memories and builds the system
// segment injected before the planning prompt. Constraints and preferences
// must reach the model; facts provide background. When the store is
// unwired or the turn is anonymous, injection is simply skipped.
func (r *Runner) loadMemoryContext(ctx context.Context, st *TurnState) {
	if r.deps.Memories == nil || st.UserID == "" {
		return
	}
	memories, err := r.deps.Memories.List(ctx, st.UserID)
	if err != nil {
		st.Warnings = appendUnique(st.Warnings, "长期记忆加载失败，本轮不注入记忆上下文")
		return
	}
	if len(memories) == 0 {
		return
	}
	kept, dropped := trimByConfidence(memories, maxInjectedMemories)
	if segment := buildMemorySegment(kept); segment != "" {
		st.MemoryContext = segment
	}
	if dropped > 0 {
		st.DroppedMemoryCount = dropped
		st.Warnings = appendUnique(st.Warnings,
			fmt.Sprintf("长期记忆超过注入窗口 %d 条，已按置信度截断 %d 条", maxInjectedMemories, dropped))
	}
}

// trimByConfidence keeps at most cap memories, dropping the lowest-confidence
// ones on overflow. List order (updated_at desc) is preserved for ties so a
// newly refreshed memory wins without a higher confidence score.
func trimByConfidence(memories []domainmemory.Memory, cap int) ([]domainmemory.Memory, int) {
	if len(memories) <= cap {
		return memories, 0
	}
	ranked := make([]domainmemory.Memory, len(memories))
	copy(ranked, memories)
	sort.SliceStable(ranked, func(i, j int) bool {
		return ranked[i].Confidence > ranked[j].Confidence
	})
	kept := ranked[:cap]
	// Restore List order for presentation: groups then read naturally by
	// recency.
	index := make(map[string]int, len(memories))
	for i, mem := range memories {
		index[mem.ID] = i
	}
	sort.SliceStable(kept, func(i, j int) bool {
		return index[kept[i].ID] < index[kept[j].ID]
	})
	return kept, len(memories) - cap
}

// buildMemorySegment renders memories grouped by type. The wording tells the
// model that constraints are hard requirements and preferences are defaults.
func buildMemorySegment(memories []domainmemory.Memory) string {
	var constraints, preferences, facts []string
	for _, mem := range memories {
		content := strings.TrimSpace(mem.Content)
		if content == "" {
			continue
		}
		switch mem.Type {
		case domainmemory.MemoryTypeConstraint:
			constraints = append(constraints, content)
		case domainmemory.MemoryTypePreference:
			preferences = append(preferences, content)
		case domainmemory.MemoryTypeFact:
			facts = append(facts, content)
		}
	}
	var b strings.Builder
	b.WriteString("以下是该用户的长期记忆。约束属于必须遵守的硬性要求；偏好作为默认倾向，在用户本轮明确表达相反意图时以本轮为准；事实仅作背景。不要向用户提及你正在读取记忆。")
	writeMemoryGroup := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		b.WriteString("\n【" + title + "】")
		for _, item := range items {
			b.WriteString("\n- ")
			b.WriteString(item)
		}
	}
	writeMemoryGroup("约束", constraints)
	writeMemoryGroup("偏好", preferences)
	writeMemoryGroup("事实", facts)
	if len(constraints)+len(preferences)+len(facts) == 0 {
		return ""
	}
	return b.String()
}
