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

	chatport "github.com/zed1995/platepilot/shared/chat"
	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/domain/retrieval"
	"github.com/zed1995/platepilot/shared/domain/search"
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
//
// Evidence is what may be cited; Candidates and SoftConditions are what the
// answer has to talk about. They are separate because they answer different
// questions: the evidence set is the closed citation universe, while a
// candidate may well be named in an answer without any evidence supporting it
// yet — the answer just has to say so rather than imply it was checked.
type Input struct {
	Question string
	Evidence []evidence.Evidence
	// Candidates are the restaurants this turn's search returned, in ranking
	// order. They carry the snapshot time and the match reasons, which is what
	// lets the answer date its own recommendations instead of presenting a
	// ranking as eternal.
	Candidates []search.RestaurantCandidate
	// Filters are the hard conditions the search actually enforced. They are
	// passed as the filter rather than as a sentence because the answer has to
	// account for every one of them, and a prose summary could quietly omit the
	// one the user cares about.
	Filters search.RestaurantFilter
	// SoftConditions are the requirements the user stated that only reviews can
	// support, paired with the review topic each one maps onto. They are what
	// the answer has to either attribute to reviews or explicitly decline.
	SoftConditions []retrieval.SoftCondition
	// MissingSlots are the slots the plan needed and the user never supplied.
	// They are a real gap rather than a disclaimer: the answer is told to name
	// them because the ranking really was computed without them.
	MissingSlots []string
	// Warnings are this turn's retrieval degradations — a channel that did not
	// run, a rerank that failed. An answer that does not mention them presents a
	// weakened ranking with the confidence of a complete one.
	Warnings []string
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
				// The adequacy caveat is added after validation: it is a
				// statement about the evidence set, not a claim drawn from it,
				// so it carries no citation and cannot be reordered or dropped
				// by a regeneration.
				Text:      measureAdequacy(in.Evidence).lead() + strings.TrimSpace(text),
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

// buildMessages assembles the grounding system instruction, the context
// blocks, and the user question.
//
// The blocks are ordered by how much the model needs to treat them as fact:
// evidence first (the only citable material), then how much that evidence can
// support, then the hard conditions the search enforced, then the candidates,
// then the soft conditions with their support verdict, and finally the gaps. A
// gap listed before the material it qualifies reads as a general disclaimer;
// after it, it reads as what it is — the part of this specific answer that is
// missing.
func buildMessages(in Input) []domainchat.ChatMessage {
	var b strings.Builder
	b.WriteString(evidenceContext(in.Evidence))
	for _, block := range []string{
		measureAdequacy(in.Evidence).contextBlock(),
		filtersContext(in.Filters),
		candidatesContext(in.Candidates),
		softConditionsContext(in.SoftConditions, in.Evidence),
		gapsContext(in),
	} {
		if block == "" {
			continue
		}
		b.WriteString("\n\n")
		b.WriteString(block)
	}
	b.WriteString("\n\n问题：")
	b.WriteString(in.Question)

	return []domainchat.ChatMessage{
		{
			Role:    domainchat.RoleSystem,
			Content: systemInstruction,
		},
		{
			Role:    domainchat.RoleUser,
			Content: b.String(),
		},
	}
}

const systemInstruction = `你是 PlatePilot 的纽约餐厅顾问。回答规则：
1. 只能使用用户消息中 <evidence> 标签内的资料，禁止使用标签外的任何事实或推测。
2. 每个事实性结论后用 [^证据id] 标注来源，id 取 evidence 标签上的 id。多个来源可连续标注。
3. 如果资料不足以回答，直接说明缺少什么资料，并给出用户可以继续追问的方向，不要编造。
4. 用简洁中文回答，先给结论，再给必要细节。
5. 在回答最后另起一行，严格按此格式给出最多 3 个可追问的问题（没有则给空数组）：
FOLLOWUPS: ["问题1","问题2"]
6. 由评论推断的结论必须写明依据来自评论，并标注对应 [^id]；不得表述为客观事实；没有任何证据支持的条件必须列入「无法确认」。
7. 每条推荐都要写出资料里给出的数据时间与来源；没有数据时间就说明资料未标注时间，不要省略这两项。

推荐类问题的回答顺序（每部分都要有，没有内容就写"无"）：
① 结论：1–3 句，写明找到几家候选；
② 匹配原因：对照 <filters> 里的每一条硬条件说明满足情况，软条件必须标注"评论推断"；
③ 每条推荐的来源与数据时间；
④ 无法确认的条件：合并 <soft_conditions> 中标为无法确认的条目与 <gaps> 里列出的缺口；
⑤ FOLLOWUPS 追问建议。`

// filtersContext states the hard conditions the search enforced.
//
// The model is told what was enforced, not asked to infer it from the
// candidates: a condition that returned nothing would otherwise be invisible,
// and the answer would report a search that never happened.
func filtersContext(filter search.RestaurantFilter) string {
	described := filter.Describe()
	if described == "" {
		return ""
	}
	return "<filters>\n本次检索实际执行的硬条件（每条都要在回答里对应说明）：" + described + "\n</filters>"
}

// gapsContext lists the concrete gaps behind the "无法确认" section.
//
// Every line comes from something that really happened: a slot the user never
// supplied, or a degradation the search recorded. A fixed disclaimer sentence
// would say the same thing on every turn, which trains the reader to skip it —
// including on the turn where it is the only warning that matters.
func gapsContext(in Input) string {
	var lines []string
	for _, slot := range in.MissingSlots {
		slot = strings.TrimSpace(slot)
		if slot == "" {
			continue
		}
		lines = append(lines, "- 用户未提供的条件："+slot+"（不要替用户假设）")
	}
	for _, warning := range in.Warnings {
		warning = strings.TrimSpace(warning)
		if warning == "" {
			continue
		}
		lines = append(lines, "- 检索降级："+warning)
	}
	if len(lines) == 0 {
		return ""
	}
	return "<gaps>\n以下内容本次确实无法确认，必须出现在「无法确认」里：\n" +
		strings.Join(lines, "\n") + "\n</gaps>"
}

// candidatesContext renders the ranked candidates as a tagged block.
//
// The candidates are shown without any claim that they are supported: a
// candidate is what the search returned, and whether the knowledge base can
// back it up is a separate question the evidence block answers. Leaving them
// out entirely was the earlier behaviour, and it forced the model to talk about
// restaurants it had only seen in a tool message it is not allowed to cite.
func candidatesContext(candidates []search.RestaurantCandidate) string {
	if len(candidates) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<candidates>\n")
	for i, candidate := range candidates {
		fmt.Fprintf(&b, "%d. id=%d %s", i+1, candidate.RestaurantID, candidate.Name)
		var facts []string
		if candidate.Borough != "" {
			facts = append(facts, candidate.Borough)
		}
		if len(candidate.Cuisines) > 0 {
			facts = append(facts, strings.Join(candidate.Cuisines, "/"))
		}
		if candidate.Rating != nil {
			facts = append(facts, fmt.Sprintf("评分%.1f", *candidate.Rating))
		}
		if candidate.PriceLevel != nil {
			facts = append(facts, fmt.Sprintf("价格%d", *candidate.PriceLevel))
		}
		if candidate.Address != "" {
			facts = append(facts, candidate.Address)
		}
		if !candidate.SnapshotAt.IsZero() {
			facts = append(facts, "数据时间"+candidate.SnapshotAt.UTC().Format("2006-01-02"))
		}
		if len(facts) > 0 {
			fmt.Fprintf(&b, "（%s）", strings.Join(facts, " / "))
		}
		b.WriteString("\n")
		if len(candidate.Reasons) > 0 {
			fmt.Fprintf(&b, "   命中理由：%s\n", strings.Join(candidate.Reasons, "；"))
		}
	}
	b.WriteString("</candidates>")
	return b.String()
}

// softConditionsContext states, per condition, whether the evidence set
// supports it.
//
// The support verdict is computed here rather than left to the model because
// the model cannot see the mapping: it reads "安静" and a pile of documents
// tagged "ambience", and nothing tells it those are the same thing. Deciding it
// in code means the "无法确认" list cannot be silently dropped by a generation
// that found the sentence flow awkward.
func softConditionsContext(conditions []retrieval.SoftCondition, evidenceItems []evidence.Evidence) string {
	if len(conditions) == 0 {
		return ""
	}
	supported := make(map[string]int, len(conditions))
	for _, item := range evidenceItems {
		if item.Topic == "" {
			continue
		}
		supported[item.Topic]++
	}

	lines := make([]string, 0, len(conditions))
	seen := map[string]struct{}{}
	for _, condition := range conditions {
		text := strings.TrimSpace(condition.Text)
		topic := strings.TrimSpace(condition.Topic)
		if text == "" && topic == "" {
			continue
		}
		label := text
		if label == "" {
			label = topic
		}
		if topic != "" {
			label = fmt.Sprintf("%s（%s）", label, topic)
		}
		key := strings.ToLower(label)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		switch {
		case topic == "":
			lines = append(lines, "- "+label+"：无法与评论主题对应，必须列入「无法确认」")
		case supported[topic] == 0:
			lines = append(lines, "- "+label+"：本次资料中没有对应评论，必须列入「无法确认」")
		default:
			lines = append(lines, fmt.Sprintf(
				"- %s：有 %d 条评论证据，可作答但必须写明依据来自评论并标注 [^id]",
				label, supported[topic]))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "<soft_conditions>\n" + strings.Join(lines, "\n") + "\n</soft_conditions>"
}

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
