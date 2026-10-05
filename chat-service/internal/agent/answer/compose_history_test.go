package answer

import (
	"context"
	"fmt"
	"strings"
	"testing"

	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
)

// The history block is a separate message on purpose. Folding it into the
// evidence message would put citable and non-citable material in one place and
// leave the model to work out which instruction covered which block.
func TestComposeSendsTheConversationAsItsOwnNonCitableBlock(t *testing.T) {
	chat := &scriptedChat{contents: []string{"结论[^10]\nFOLLOWUPS: []"}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})

	if _, err := c.Compose(context.Background(), Input{
		Question: "便宜一点的",
		Evidence: adequateEvidence(),
		History: []domainchat.ChatMessage{
			{Role: domainchat.RoleUser, Content: "曼哈顿 4 星以上的意大利菜"},
			{Role: domainchat.RoleAssistant, Content: "找到两家，都符合条件[^10]。\nFOLLOWUPS: [\"能订位吗?\"]"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	messages := chat.requests[0].Messages
	if len(messages) != 3 {
		t.Fatalf("sent %d messages, want system + recent_turns + material", len(messages))
	}
	if messages[0].Role != domainchat.RoleSystem {
		t.Fatalf("messages[0].Role = %s, want system", messages[0].Role)
	}
	if messages[1].Role != domainchat.RoleUser || !strings.Contains(messages[1].Content, "<recent_turns>") {
		t.Fatalf("messages[1] must be the history block:\n%+v", messages[1])
	}
	if messages[2].Role != domainchat.RoleUser || !strings.Contains(messages[2].Content, "<evidence ") {
		t.Fatalf("messages[2] must be this turn's material:\n%+v", messages[2])
	}

	block := messages[1].Content
	for _, want := range []string{"用户：曼哈顿 4 星以上的意大利菜", "助手：找到两家，都符合条件。"} {
		if !strings.Contains(block, want) {
			t.Fatalf("history block is missing %q:\n%s", want, block)
		}
	}
	// The declaration is what keeps a previous turn's sources out of this
	// turn's answer, so its absence is a correctness bug rather than a style
	// problem.
	if !strings.Contains(block, "不是可引用资料") ||
		!strings.Contains(block, "不得出现在回答的 [^id] 标注里") {
		t.Fatalf("history block must declare itself non-citable:\n%s", block)
	}
}

// A first turn has nothing to be read against, and an empty <recent_turns>
// element would read as "the conversation was empty" rather than "there was no
// conversation".
func TestComposeOmitsTheHistoryBlockOnAFirstTurn(t *testing.T) {
	chat := &scriptedChat{contents: []string{"结论[^10]\nFOLLOWUPS: []"}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})

	if _, err := c.Compose(context.Background(), Input{
		Question: "曼哈顿的意大利菜",
		Evidence: adequateEvidence(),
	}); err != nil {
		t.Fatal(err)
	}

	if messages := chat.requests[0].Messages; len(messages) != 2 {
		t.Fatalf("sent %d messages, want system + material only", len(messages))
	}
	if content := userContentOf(t, chat); strings.Contains(content, "<recent_turns>") {
		t.Fatalf("a first turn must not carry a history block:\n%s", content)
	}
}

// The marker in a previous answer refers to that turn's evidence set. It means
// nothing here, and leaving it in invites the model to copy it — which costs a
// regeneration, or the whole turn if it happens twice.
func TestComposeStripsPriorCitationMarkersAndTheFollowUpsTail(t *testing.T) {
	chat := &scriptedChat{contents: []string{"结论[^10]\nFOLLOWUPS: []"}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})

	if _, err := c.Compose(context.Background(), Input{
		Question: "那家呢",
		Evidence: adequateEvidence(),
		History: []domainchat.ChatMessage{
			{Role: domainchat.RoleAssistant, Content: "上一轮的结论[^20][^10]。\nFOLLOWUPS: [\"还有别的吗?\"]"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	block := historyBlockOf(t, chat)
	if !strings.Contains(block, "上一轮的结论。") {
		t.Fatalf("the previous answer's text must survive:\n%s", block)
	}
	for _, unwanted := range []string{"[^20]", "[^10]", "FOLLOWUPS"} {
		if strings.Contains(block, unwanted) {
			t.Fatalf("history block must not carry %q:\n%s", unwanted, block)
		}
	}
}

// The instruction has to say the same thing the block does. A model told only
// in the block would still be free to treat a fact it read there as established.
func TestComposeInstructionDeclaresTheHistoryNonCitable(t *testing.T) {
	chat := &scriptedChat{contents: []string{"结论[^10]\nFOLLOWUPS: []"}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	if _, err := c.Compose(context.Background(), Input{
		Question: "那家呢",
		Evidence: adequateEvidence(),
		History:  []domainchat.ChatMessage{{Role: domainchat.RoleUser, Content: "之前的那个问题"}},
	}); err != nil {
		t.Fatal(err)
	}

	system := systemOf(t, chat)
	if !strings.Contains(system, "<recent_turns>") {
		t.Fatalf("the instruction must name the history block:\n%s", system)
	}
	for _, want := range []string{"不得写进 [^id] 标注", "不得直接沿用"} {
		if !strings.Contains(system, want) {
			t.Fatalf("the instruction must state %q:\n%s", want, system)
		}
	}
}

// An ID that only ever appeared in the history is not in the allowed set. This
// is the assertion behind "历史不在 allowed 集": the number can be perfectly
// visible to the model and the citation check still rejects it.
func TestComposeRejectsACitationThatOnlyAppearedInTheHistory(t *testing.T) {
	chat := &scriptedChat{contents: []string{
		"沿用上一轮那家[^77]\nFOLLOWUPS: []",
		"修正回答[^10]\nFOLLOWUPS: []",
	}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})

	ans, err := c.Compose(context.Background(), Input{
		Question: "那家还开着吗",
		Evidence: adequateEvidence(),
		History: []domainchat.ChatMessage{
			// The marker is in a user message, so it reaches the block verbatim:
			// the model really can see the number it is about to cite.
			{Role: domainchat.RoleUser, Content: "上一轮你说的那家是不是[^77]"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ans.Text != "修正回答[^10]" {
		t.Fatalf("text = %q, want the corrected answer", ans.Text)
	}
	if len(chat.requests) != 2 {
		t.Fatalf("Complete called %d times, want a corrective retry", len(chat.requests))
	}
	if !strings.Contains(historyBlockOf(t, chat), "[^77]") {
		t.Fatal("the fixture is vacuous: the history block never showed the ID")
	}
	correction := chat.requests[1].Messages[len(chat.requests[1].Messages)-1]
	for _, want := range []string{"77", "10, 20"} {
		if !strings.Contains(correction.Content, want) {
			t.Fatalf("the correction must name %q:\n%s", want, correction.Content)
		}
	}
}

// Six messages is three exchanges. Beyond that the block stops being context
// for an incremental condition and starts being a transcript.
func TestComposeKeepsOnlyTheNewestSixHistoryMessages(t *testing.T) {
	history := make([]domainchat.ChatMessage, 0, 8)
	for i := 1; i <= 8; i++ {
		history = append(history, domainchat.ChatMessage{
			Role:    domainchat.RoleUser,
			Content: fmt.Sprintf("历史消息%d", i),
		})
	}

	got := trimHistory(history)
	if len(got) != maxHistoryMessages {
		t.Fatalf("kept %d messages, want %d", len(got), maxHistoryMessages)
	}
	if got[0].Content != "历史消息3" {
		t.Fatalf("kept from %q, want the newest six starting at 历史消息3", got[0].Content)
	}
	if got[len(got)-1].Content != "历史消息8" {
		t.Fatalf("kept up to %q, want 历史消息8", got[len(got)-1].Content)
	}
}

// A long transcript must not buy itself more room by being long, and what is
// dropped is the oldest — the newest exchange is the one an incremental
// condition refers to.
func TestComposeTrimsHistoryOverBudgetFromTheOldestEnd(t *testing.T) {
	// 302 tokens each, so six of them are comfortably over the 1000-token half
	// of the evidence budget and exactly three fit.
	history := make([]domainchat.ChatMessage, 0, 6)
	for i := 0; i < 6; i++ {
		history = append(history, domainchat.ChatMessage{
			Role:    domainchat.RoleUser,
			Content: fmt.Sprintf("第%d轮%s", i, strings.Repeat("字", 300)),
		})
	}

	got := trimHistory(history)
	if len(got) != 3 {
		t.Fatalf("kept %d messages, want 3 to fit the budget", len(got))
	}
	if got[0].Content != history[3].Content {
		t.Fatalf("kept from %q, want the newest exchange that fits", got[0].Content)
	}
	if got[2].Content != history[5].Content {
		t.Fatalf("the newest message must always survive: kept up to %q", got[2].Content)
	}
	if total := len(got); total > 0 && strings.Contains(recentTurnsContext(history), "第0轮") {
		t.Fatal("a trimmed message must not appear in the block")
	}
}

// A single message over the budget drops the whole block. Spending the prompt
// on one stale answer would push out the evidence the answer has to cite, and
// half a conversation is worse than none.
func TestComposeDropsTheBlockWhenOneMessageIsOverBudget(t *testing.T) {
	history := []domainchat.ChatMessage{{
		Role:    domainchat.RoleAssistant,
		Content: strings.Repeat("字", historyTokenBudget+200),
	}}

	if got := trimHistory(history); len(got) != 0 {
		t.Fatalf("kept %d messages, want none", len(got))
	}
	if block := recentTurnsContext(history); block != "" {
		t.Fatalf("block = %q, want empty", block)
	}
}

// An empty message is not a turn, and rendering it would put a bare "用户："
// line in front of the model.
func TestComposeSkipsBlankHistoryMessages(t *testing.T) {
	block := recentTurnsContext([]domainchat.ChatMessage{
		{Role: domainchat.RoleUser, Content: "   "},
		{Role: domainchat.RoleAssistant, Content: "\n\nFOLLOWUPS: []"},
		{Role: domainchat.RoleUser, Content: "真正的问题"},
	})

	if !strings.Contains(block, "真正的问题") {
		t.Fatalf("the real message must survive:\n%s", block)
	}
	if strings.Contains(block, "助手：") {
		t.Fatalf("a message whose whole content is the tail is not a turn:\n%s", block)
	}
}

// historyBlockOf returns the content of the block the composer sent for the
// conversation so far.
//
// The role check is load-bearing: the system instruction names <recent_turns>
// too, when it tells the model what the tag is for, so matching on the tag
// alone would hand back the instruction and quietly make every assertion about
// the block vacuous.
func historyBlockOf(t *testing.T, chat *scriptedChat) string {
	t.Helper()
	if len(chat.requests) == 0 {
		t.Fatal("the composer never called the model")
	}
	for _, message := range chat.requests[0].Messages {
		if message.Role == domainchat.RoleUser && strings.Contains(message.Content, "<recent_turns>") {
			return message.Content
		}
	}
	t.Fatal("no history block was sent")
	return ""
}
