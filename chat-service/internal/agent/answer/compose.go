// Package answer composes the agent's final answer from the turn's evidence.
//
// The one hard rule enforced here is citation closure: every [^id] marker in
// an answer must reference an EvidenceID that was actually in this turn's
// evidence set. A first violation earns one corrective regeneration; a second
// fails the turn with agent_citation_violation so unverifiable text is never
// shipped.
package answer

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	chatport "github.com/zed/platepilot/shared/chat"
	domainchat "github.com/zed/platepilot/shared/domain/chat"
	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/evidence"
)

const (
	// maxAttempts bounds corrective retries for citation violations. The first
	// generation plus one correction is the whole budget.
	maxAttempts = 2
	// maxFollowUps caps suggested next questions.
	maxFollowUps = 3
)

// Deps builds a Composer.
type Deps struct {
	Chat  chatport.ChatProvider
	Model string
}

// NewComposer returns a grounded-answer composer.
func NewComposer(deps Deps) *Composer {
	return &Composer{chat: deps.Chat, model: deps.Model}
}

// Composer generates citation-validated answers.
type Composer struct {
	chat  chatport.ChatProvider
	model string
}

// Input is one answer composition request.
type Input struct {
	Question string
	Evidence []evidence.Evidence
}

// citationPattern matches [^123] markers.
var citationPattern = regexp.MustCompile(`\[\^(\d+)\]`)

// followUpsPrefix marks the machine-readable suggestion line.
const followUpsPrefix = "FOLLOWUPS:"

// RefusalAnswer is the fixed answer for a fact-seeking restaurant question that
// gathered no citable evidence. It is deliberately a constant rather than a
// model regeneration: with no evidence in context, another generation could
// only hallucinate.
const RefusalAnswer = "我暂时没有找到能支撑这个问题的餐厅资料，无法给出可靠回答。你可以补充餐厅名称、区域或菜系后再问我，我再帮你查一次。"

// Compose produces the grounded final answer.
func (c *Composer) Compose(ctx context.Context, in Input) (domainchat.Answer, error) {
	if strings.TrimSpace(in.Question) == "" {
		return domainchat.Answer{}, errs.New(errs.CodeInvalidArgument, "answer composer requires a question")
	}
	if len(in.Evidence) == 0 {
		return domainchat.Answer{Text: RefusalAnswer}, nil
	}

	allowed := allowedIDs(in.Evidence)
	messages := buildMessages(in)

	var lastText string
	var invalid []int64
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			messages = append(messages, domainchat.ChatMessage{
				Role: domainchat.RoleUser,
				Content: fmt.Sprintf(
					"你上一条回答引用了不属于本次资料的证据 ID：%s。"+
						"只能使用以下证据 ID：%s。请严格根据 <evidence> 资料重新回答，并重新输出 %s 行。",
					joinInts(invalid), joinInts(mapKeysSorted(allowed)), followUpsPrefix),
			})
		}
		resp, err := c.chat.Complete(ctx, domainchat.ChatRequest{
			Model:    c.model,
			Messages: messages,
		})
		if err != nil {
			return domainchat.Answer{}, err
		}
		lastText = resp.Message.Content

		text, followUps := splitFollowUps(lastText)
		cited := extractCitations(lastText)
		invalid = invalidCitations(cited, allowed)
		if len(invalid) == 0 {
			return domainchat.Answer{
				Text:      strings.TrimSpace(text),
				Citations: sortedUnique(cited),
				FollowUps: clampFollowUps(followUps),
			}, nil
		}
	}

	return domainchat.Answer{}, errs.Newf(errs.CodeAgentCitationViolation,
		"answer cited evidence IDs %s after one corrective retry; allowed IDs are %s",
		joinInts(invalid), joinInts(mapKeysSorted(allowed)))
}

func mapKeysSorted(m map[int64]struct{}) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// buildMessages assembles the grounding system instruction, the evidence
// blocks, and the user question.
func buildMessages(in Input) []domainchat.ChatMessage {
	return []domainchat.ChatMessage{
		{
			Role:    domainchat.RoleSystem,
			Content: systemInstruction,
		},
		{
			Role:    domainchat.RoleUser,
			Content: evidenceContext(in.Evidence) + "\n\n问题：" + in.Question,
		},
	}
}

const systemInstruction = `你是 PlatePilot 的纽约餐厅顾问。回答规则：
1. 只能使用用户消息中 <evidence> 标签内的资料，禁止使用标签外的任何事实或推测。
2. 每个事实性结论后用 [^证据id] 标注来源，id 取 evidence 标签上的 id。多个来源可连续标注。
3. 如果资料不足以回答，直接说明缺少什么资料，并给出用户可以继续追问的方向，不要编造。
4. 用简洁中文回答，先给结论，再给必要细节。
5. 在回答最后另起一行，严格按此格式给出最多 3 个可追问的问题（没有则给空数组）：
FOLLOWUPS: ["问题1","问题2"]`

// evidenceContext renders the evidence set as tagged blocks.
func evidenceContext(items []evidence.Evidence) string {
	var b strings.Builder
	for _, item := range items {
		fmt.Fprintf(&b, "<evidence id=%q restaurant=%q type=%q",
			fmt.Sprintf("%d", item.EvidenceID), item.RestaurantName, string(item.DocType))
		if item.Source != "" {
			fmt.Fprintf(&b, " source=%q", item.Source)
		}
		if item.Title != "" {
			fmt.Fprintf(&b, " title=%q", item.Title)
		}
		if item.Topic != "" {
			fmt.Fprintf(&b, " topic=%q", item.Topic)
		}
		b.WriteString(">\n")
		b.WriteString(strings.TrimSpace(item.Content))
		b.WriteString("\n</evidence>\n\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// extractCitations returns every [^id] marker's ID, in encounter order.
func extractCitations(text string) []int64 {
	matches := citationPattern.FindAllStringSubmatch(text, -1)
	ids := make([]int64, 0, len(matches))
	for _, match := range matches {
		var id int64
		for _, ch := range match[1] {
			id = id*10 + int64(ch-'0')
		}
		ids = append(ids, id)
	}
	return ids
}

func allowedIDs(items []evidence.Evidence) map[int64]struct{} {
	allowed := make(map[int64]struct{}, len(items))
	for _, item := range items {
		allowed[item.EvidenceID] = struct{}{}
	}
	return allowed
}

func invalidCitations(cited []int64, allowed map[int64]struct{}) []int64 {
	seen := map[int64]struct{}{}
	var invalid []int64
	for _, id := range cited {
		if _, ok := allowed[id]; ok {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		invalid = append(invalid, id)
	}
	return invalid
}

// sortedUnique sorts IDs and drops duplicates.
func sortedUnique(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// splitFollowUps separates the answer text from the trailing FOLLOWUPS line.
func splitFollowUps(raw string) (string, []string) {
	idx := strings.LastIndex(raw, followUpsPrefix)
	if idx < 0 {
		return raw, nil
	}
	answerText := strings.TrimSpace(raw[:idx])
	payload := strings.TrimSpace(raw[idx+len(followUpsPrefix):])
	start := strings.Index(payload, "[")
	end := strings.LastIndex(payload, "]")
	if start < 0 || end < start {
		return answerText, nil
	}
	var followUps []string
	if err := json.Unmarshal([]byte(payload[start:end+1]), &followUps); err != nil {
		return answerText, nil
	}
	return answerText, followUps
}

func clampFollowUps(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, min(len(items), maxFollowUps))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		out = append(out, item)
		if len(out) == maxFollowUps {
			break
		}
	}
	return out
}

func joinInts(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("%d", id)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
