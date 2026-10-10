package answer

import (
	"context"
	"strings"
	"testing"
	"time"

	chatport "github.com/zed1995/platepilot/shared/chat"
	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/evidence"
	domainretrieval "github.com/zed1995/platepilot/shared/domain/retrieval"
	"github.com/zed1995/platepilot/shared/domain/review"
	"github.com/zed1995/platepilot/shared/domain/search"
)

// scriptedChat answers Complete from a queued script; it records every request
// so tests can assert on corrective messages and call counts.
type scriptedChat struct {
	contents []string
	err      error
	requests []domainchat.ChatRequest
}

func (s *scriptedChat) Complete(_ context.Context, req domainchat.ChatRequest) (domainchat.ChatResponse, error) {
	s.requests = append(s.requests, req)
	if s.err != nil {
		return domainchat.ChatResponse{}, s.err
	}
	content := s.contents[0]
	s.contents = s.contents[1:]
	return domainchat.ChatResponse{
		Message:      domainchat.ChatMessage{Role: domainchat.RoleAssistant, Content: content},
		FinishReason: domainchat.FinishReasonStop,
	}, nil
}

func (s *scriptedChat) Stream(context.Context, domainchat.ChatRequest) (chatport.ChatStream, error) {
	return nil, nil
}

func ev(id int64) evidence.Evidence {
	return evidence.Evidence{EvidenceID: id, RestaurantID: id + 1000, Content: "资料正文", DocType: evidence.DocTypeRestaurantProfile}
}

// adequateEvidence is two documents of different kinds — the minimum an answer
// needs to be written without the adequacy caveat. Tests that are about
// something else use it so the caveat does not appear in the text they compare
// against; the caveat itself is pinned in its own test.
func adequateEvidence() []evidence.Evidence {
	attributes := ev(20)
	attributes.DocType = evidence.DocTypeRestaurantAttributes
	return []evidence.Evidence{ev(10), attributes}
}

func TestComposeRequiresQuestion(t *testing.T) {
	c := NewComposer(Deps{Chat: &scriptedChat{}})
	_, err := c.Compose(context.Background(), Input{Evidence: []evidence.Evidence{ev(1)}})
	if errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
}

func TestComposeWithoutEvidenceReturnsFixedRefusalWithoutModel(t *testing.T) {
	chat := &scriptedChat{}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	ans, err := c.Compose(context.Background(), Input{Question: "哪家好？"})
	if err != nil {
		t.Fatal(err)
	}
	if ans.Text != RefusalAnswer {
		t.Fatalf("text = %q, want fixed refusal", ans.Text)
	}
	if len(chat.requests) != 0 {
		t.Fatalf("Complete called %d times, want 0", len(chat.requests))
	}
}

func TestComposeValidAnswerParsesCitationsAndFollowUps(t *testing.T) {
	chat := &scriptedChat{contents: []string{
		"结论：A 更好[^10][^20]。细节如此。\nFOLLOWUPS: [\"能订位吗?\",\"人均多少?\"]",
	}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	ans, err := c.Compose(context.Background(), Input{
		Question: "哪家好？",
		Evidence: adequateEvidence(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "结论：A 更好[^10][^20]。细节如此。"; ans.Text != want {
		t.Fatalf("text = %q, want %q", ans.Text, want)
	}
	if got := citationIDs(ans); !equalInt64(got, []int64{10, 20}) {
		t.Fatalf("citations = %v, want [10 20]", got)
	}
	if len(ans.FollowUps) != 2 || ans.FollowUps[0] != "能订位吗?" {
		t.Fatalf("followUps = %v", ans.FollowUps)
	}
	if len(chat.requests) != 1 {
		t.Fatalf("Complete called %d times, want 1", len(chat.requests))
	}
	// Evidence must be carried into the grounding context.
	userContent := chat.requests[0].Messages[1].Content
	if !strings.Contains(userContent, "<evidence id=\"10\"") {
		t.Fatalf("evidence block missing from prompt:\n%s", userContent)
	}
}

func TestComposeDeduplicatesAndSortsCitations(t *testing.T) {
	chat := &scriptedChat{contents: []string{"正文[^20][^10][^20]\nFOLLOWUPS: []"}}
	c := NewComposer(Deps{Chat: chat})
	ans, err := c.Compose(context.Background(), Input{
		Question: "q",
		Evidence: []evidence.Evidence{ev(10), ev(20)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := citationIDs(ans); !equalInt64(got, []int64{10, 20}) {
		t.Fatalf("citations = %v, want [10 20]", got)
	}
}

func TestComposeRetriesOnceAfterOutOfRangeCitation(t *testing.T) {
	chat := &scriptedChat{contents: []string{
		"第一次回答[^99]\nFOLLOWUPS: []",
		"修正回答[^10]\nFOLLOWUPS: []",
	}}
	c := NewComposer(Deps{Chat: chat})
	ans, err := c.Compose(context.Background(), Input{
		Question: "q",
		Evidence: adequateEvidence(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if ans.Text != "修正回答[^10]" {
		t.Fatalf("text = %q", ans.Text)
	}
	if len(chat.requests) != 2 {
		t.Fatalf("Complete called %d times, want 2", len(chat.requests))
	}
	last := chat.requests[1].Messages[len(chat.requests[1].Messages)-1]
	if last.Role != domainchat.RoleUser {
		t.Fatalf("correction role = %s", last.Role)
	}
	if !strings.Contains(last.Content, "99") || !strings.Contains(last.Content, "10") {
		t.Fatalf("correction message must name invalid and allowed IDs:\n%s", last.Content)
	}
}

func TestComposeFailsAfterSecondViolation(t *testing.T) {
	chat := &scriptedChat{contents: []string{
		"第一次[^99]\nFOLLOWUPS: []",
		"第二次还是[^99]\nFOLLOWUPS: []",
	}}
	c := NewComposer(Deps{Chat: chat})
	_, err := c.Compose(context.Background(), Input{
		Question: "q",
		Evidence: []evidence.Evidence{ev(10)},
	})
	if errs.CodeOf(err) != errs.CodeAgentCitationViolation {
		t.Fatalf("err = %v, want agent_citation_violation", err)
	}
	if len(chat.requests) != 2 {
		t.Fatalf("Complete called %d times, want 2", len(chat.requests))
	}
}

func TestComposePropagatesModelError(t *testing.T) {
	modelErr := errs.New(errs.CodeProviderUnavailable, "down")
	c := NewComposer(Deps{Chat: &scriptedChat{err: modelErr}})
	if _, err := c.Compose(context.Background(), Input{
		Question: "q",
		Evidence: []evidence.Evidence{ev(1)},
	}); errs.CodeOf(err) != errs.CodeProviderUnavailable {
		t.Fatalf("err = %v, want provider_unavailable", err)
	}
}

func TestFollowUpsClampedToThree(t *testing.T) {
	chat := &scriptedChat{contents: []string{
		"正文[^10]\nFOLLOWUPS: [\"a\",\"b\",\"c\",\"d\",\"e\"]",
	}}
	c := NewComposer(Deps{Chat: chat})
	ans, err := c.Compose(context.Background(), Input{
		Question: "q",
		Evidence: []evidence.Evidence{ev(10)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ans.FollowUps) != 3 {
		t.Fatalf("followUps = %v, want at most 3", ans.FollowUps)
	}
}

func TestFollowUpsMalformedJSONTolerated(t *testing.T) {
	chat := &scriptedChat{contents: []string{"正文[^10]\nFOLLOWUPS: 不是数组"}}
	c := NewComposer(Deps{Chat: chat})
	ans, err := c.Compose(context.Background(), Input{
		Question: "q",
		Evidence: adequateEvidence(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if ans.FollowUps != nil {
		t.Fatalf("followUps = %v, want nil", ans.FollowUps)
	}
	if ans.Text != "正文[^10]" {
		t.Fatalf("text = %q", ans.Text)
	}
}

func TestNoFollowUpsLine(t *testing.T) {
	chat := &scriptedChat{contents: []string{"只有正文[^10]"}}
	c := NewComposer(Deps{Chat: chat})
	ans, err := c.Compose(context.Background(), Input{
		Question: "q",
		Evidence: adequateEvidence(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if ans.Text != "只有正文[^10]" || ans.FollowUps != nil {
		t.Fatalf("ans = %+v", ans)
	}
}

func citationIDs(a domainchat.Answer) []int64 { return a.Citations }

func equalInt64(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// reviewEvidence is evidence that was tagged with a review topic. The topic is
// what makes "安静" and a document about ambience the same request, so a test
// about soft-condition support cannot use the untagged helper.
func reviewEvidence(id int64, topic, content string) evidence.Evidence {
	item := ev(id)
	item.Topic = topic
	item.Content = content
	return item
}

// systemOf returns the system instruction the composer actually sent.
func systemOf(t *testing.T, chat *scriptedChat) string {
	t.Helper()
	if len(chat.requests) == 0 {
		t.Fatal("the composer never called the model")
	}
	for _, message := range chat.requests[0].Messages {
		if message.Role == domainchat.RoleSystem {
			return message.Content
		}
	}
	t.Fatal("no system message was sent")
	return ""
}

// userContentOf returns the user message: the evidence, the candidates, the soft
// conditions, and the question, in that order.
func userContentOf(t *testing.T, chat *scriptedChat) string {
	t.Helper()
	if len(chat.requests) == 0 {
		t.Fatal("the composer never called the model")
	}
	for _, message := range chat.requests[0].Messages {
		if message.Role == domainchat.RoleUser {
			return message.Content
		}
	}
	t.Fatal("no user message was sent")
	return ""
}

// The rule that keeps an inference from being sold as a fact has to be in the
// instruction, not merely hoped for: the model is the only thing that writes the
// sentence the user reads.
func TestComposeInstructionRequiresLabellingReviewInferences(t *testing.T) {
	chat := &scriptedChat{contents: []string{"结论[^10]\nFOLLOWUPS: []"}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	if _, err := c.Compose(context.Background(), Input{
		Question: "安静的意大利餐厅？",
		Evidence: []evidence.Evidence{ev(10)},
	}); err != nil {
		t.Fatal(err)
	}

	system := systemOf(t, chat)
	for _, want := range []string{"inferred from reviews", "unconfirmed", "data timestamps", "note the source"} {
		if !strings.Contains(system, want) {
			t.Fatalf("system instruction must mention %q:\n%s", want, system)
		}
	}
}

// Which soft conditions have support is decided in code, because the model
// cannot see the mapping between the user's words and the corpus's topics. The
// composer's job is to say, per condition, which of the three situations applies.
func TestComposeSortsSoftConditionsByWhetherEvidenceSupportsThem(t *testing.T) {
	chat := &scriptedChat{contents: []string{"结论[^10]\nFOLLOWUPS: []"}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})

	_, err := c.Compose(context.Background(), Input{
		Question: "安静的餐厅，服务要好",
		Evidence: []evidence.Evidence{
			reviewEvidence(10, review.TopicAmbience, "很安静，适合聊天"),
		},
		SoftConditions: []domainretrieval.SoftCondition{
			{Text: "安静", Topic: review.TopicAmbience},
			{Text: "服务好", Topic: review.TopicService},
			{Text: "有个安静的院子"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	content := userContentOf(t, chat)
	for _, want := range []string{
		"安静（ambience）：有 1 条评论证据",
		"服务好（service）：本次资料中没有对应评论，必须列入「无法确认」",
		"有个安静的院子：无法与评论主题对应，必须列入「无法确认」",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("soft-condition block is missing %q:\n%s", want, content)
		}
	}
}

// No soft condition means no block at all. An empty <soft_conditions> element
// would read as "the user asked for nothing and we found nothing", which is a
// different sentence from "the user did not ask".
func TestComposeOmitsTheSoftBlockWhenNothingIsSoft(t *testing.T) {
	chat := &scriptedChat{contents: []string{"结论[^10]\nFOLLOWUPS: []"}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	if _, err := c.Compose(context.Background(), Input{
		Question: "q",
		Evidence: []evidence.Evidence{ev(10)},
	}); err != nil {
		t.Fatal(err)
	}
	if content := userContentOf(t, chat); strings.Contains(content, "<soft_conditions>") {
		t.Fatalf("an unconditional question must not carry a soft block:\n%s", content)
	}
}

// A recommendation has to be datable. The snapshot time lives on the candidate
// and on nothing citable, so if it is not put in front of the model the answer
// cannot state it and the recommendation reads as timeless.
func TestComposeRendersCandidatesWithTheirDataTime(t *testing.T) {
	snapshot := time.Date(2021, 9, 1, 12, 0, 0, 0, time.UTC)
	chat := &scriptedChat{contents: []string{"结论[^10]\nFOLLOWUPS: []"}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})

	_, err := c.Compose(context.Background(), Input{
		Question: "安静又好吃的意大利餐厅？",
		Evidence: []evidence.Evidence{ev(10)},
		Candidates: []search.RestaurantCandidate{{
			RestaurantID: 42,
			Name:         "Trattoria Bella",
			Borough:      "manhattan",
			Cuisines:     []string{"italian"},
			SnapshotAt:   snapshot,
			Reasons:      []string{"评论推断：安静（ambience）"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	content := userContentOf(t, chat)
	for _, want := range []string{
		"<candidates>",
		"Trattoria Bella",
		"数据时间2021-09-01",
		"命中理由：评论推断：安静（ambience）",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("candidate block is missing %q:\n%s", want, content)
		}
	}
}

// A turn with no candidates is not a turn with an empty candidate list, and the
// prompt must not suggest otherwise.
func TestComposeOmitsTheCandidateBlockWhenTheSearchFoundNothing(t *testing.T) {
	chat := &scriptedChat{contents: []string{"结论[^10]\nFOLLOWUPS: []"}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	if _, err := c.Compose(context.Background(), Input{
		Question: "q",
		Evidence: []evidence.Evidence{ev(10)},
	}); err != nil {
		t.Fatal(err)
	}
	if content := userContentOf(t, chat); strings.Contains(content, "<candidates>") {
		t.Fatalf("a turn with no candidates must not carry the block:\n%s", content)
	}
}

// The explanation order is what makes a recommendation readable: a conclusion
// first, then why, then provenance, then what could not be established. Leaving
// it to the model produces a different shape every turn, which is a different
// answer for the same question.
func TestComposeInstructionFixesTheExplanationOrder(t *testing.T) {
	chat := &scriptedChat{contents: []string{"结论[^10]\nFOLLOWUPS: []"}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	if _, err := c.Compose(context.Background(), Input{
		Question: "安静又好吃的意大利餐厅？",
		Evidence: []evidence.Evidence{ev(10)},
	}); err != nil {
		t.Fatal(err)
	}

	system := systemOf(t, chat)
	for _, want := range []string{
		"(1) Conclusion", "(2) Why they match", "(3) Source and data time",
		"(4) Unconfirmed conditions", "(5) FOLLOWUPS",
	} {
		if !strings.Contains(system, want) {
			t.Fatalf("system instruction must state step %q:\n%s", want, system)
		}
	}
}

// The hard conditions are shown as the filter the search enforced, rendered by
// the same code the trace uses. If the prompt paraphrased them, a condition that
// returned nothing could quietly vanish from the answer.
func TestComposeTellsTheModelWhichHardConditionsWereEnforced(t *testing.T) {
	chat := &scriptedChat{contents: []string{"结论[^10]\nFOLLOWUPS: []"}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})

	minRating := 4.0
	_, err := c.Compose(context.Background(), Input{
		Question: "曼哈顿 4 星以上的意大利菜",
		Evidence: []evidence.Evidence{ev(10)},
		Filters: search.RestaurantFilter{
			Borough:     "manhattan",
			Cuisines:    []string{"italian"},
			MinRating:   &minRating,
			PriceLevels: []int{2, 3},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	content := userContentOf(t, chat)
	for _, want := range []string{
		"<filters>",
		"菜系=italian",
		"行政区=manhattan",
		"评分>=4.0",
		"价格=$2/$3",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("filters block is missing %q:\n%s", want, content)
		}
	}
}

// "Cannot confirm" has to come from something that really happened. A fixed
// disclaimer reads the same on every turn, which trains the reader to skip it —
// including on the turn where it is the only warning that matters.
func TestComposeDerivesTheCannotConfirmSectionFromRealGaps(t *testing.T) {
	chat := &scriptedChat{contents: []string{"结论[^10]\nFOLLOWUPS: []"}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})

	_, err := c.Compose(context.Background(), Input{
		Question: "安静的有包间的意大利餐厅？",
		Evidence: []evidence.Evidence{ev(10)},
		// A slot the user never supplied and a channel that did not run: both
		// are facts about this turn, not boilerplate.
		MissingSlots: []string{"party_size"},
		Warnings:     []string{"向量通道已关闭"},
	})
	if err != nil {
		t.Fatal(err)
	}

	content := userContentOf(t, chat)
	for _, want := range []string{
		"<gaps>",
		"用户未提供的条件：party_size",
		"检索降级：向量通道已关闭",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("gaps block is missing %q:\n%s", want, content)
		}
	}
}

// A turn with nothing missing carries no gaps block at all: an empty one would
// read as "something is missing and we are not saying what".
func TestComposeOmitsTheGapsBlockWhenNothingIsMissing(t *testing.T) {
	chat := &scriptedChat{contents: []string{"结论[^10]\nFOLLOWUPS: []"}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	if _, err := c.Compose(context.Background(), Input{
		Question: "q",
		Evidence: []evidence.Evidence{ev(10)},
	}); err != nil {
		t.Fatal(err)
	}
	if content := userContentOf(t, chat); strings.Contains(content, "<gaps>") {
		t.Fatalf("a complete turn must not carry a gaps block:\n%s", content)
	}
}

// The blocks sit in a fixed order: citable material first, then how much that
// material can support, then what the search enforced, then what it found, then
// what is missing. A gap listed before the material it qualifies reads as a
// general disclaimer rather than a statement about this answer.
func TestComposeOrdersTheContextBlocks(t *testing.T) {
	chat := &scriptedChat{contents: []string{"结论[^10]\nFOLLOWUPS: []"}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})

	_, err := c.Compose(context.Background(), Input{
		Question: "安静的有包间的意大利餐厅？",
		Evidence: []evidence.Evidence{reviewEvidence(10, review.TopicAmbience, "很安静")},
		Filters:  search.RestaurantFilter{Borough: "manhattan"},
		Candidates: []search.RestaurantCandidate{
			{RestaurantID: 42, Name: "Trattoria Bella"},
		},
		SoftConditions: []domainretrieval.SoftCondition{{Text: "安静", Topic: review.TopicAmbience}},
		MissingSlots:   []string{"party_size"},
	})
	if err != nil {
		t.Fatal(err)
	}

	content := userContentOf(t, chat)
	order := []string{"<evidence ", "<evidence_adequacy>", "<filters>", "<candidates>", "<soft_conditions>", "<gaps>", "问题："}
	previous := -1
	for _, tag := range order {
		index := strings.Index(content, tag)
		if index < 0 {
			t.Fatalf("block %q is missing:\n%s", tag, content)
		}
		if index <= previous {
			t.Fatalf("block %q is out of order:\n%s", tag, content)
		}
		previous = index
	}
}

// One document is one source's word on one aspect, so a conclusion drawn from
// it cannot be checked against anything. The caveat is prepended in code rather
// than requested from the model because it is the one line that must not be
// dropped, and a scripted model here returns a bare sentence with no caveat.
func TestComposeStatesInsufficientEvidenceInTheAnswerItself(t *testing.T) {
	chat := &scriptedChat{contents: []string{"结论：这家很好[^10]\nFOLLOWUPS: []"}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	ans, err := c.Compose(context.Background(), Input{
		Question: "这家怎么样？",
		Evidence: []evidence.Evidence{ev(10)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ans.Text, "资料不足") {
		t.Fatalf("a one-document answer must open with the caveat:\n%s", ans.Text)
	}
	if !strings.Contains(ans.Text, "1 类文档") || !strings.Contains(ans.Text, "1 条") {
		t.Fatalf("the caveat must state what was found, not just that it was little:\n%s", ans.Text)
	}
	if !strings.Contains(ans.Text, "这家很好[^10]") {
		t.Fatalf("the model's own text was lost:\n%s", ans.Text)
	}
	// The caveat is a statement about the evidence set, not a claim drawn from
	// it, so it must not add a citation of its own.
	if len(citationIDs(ans)) != 1 || citationIDs(ans)[0] != 10 {
		t.Fatalf("citations = %v, want [10]", citationIDs(ans))
	}
}

// Two kinds is the threshold, and counting documents is not the same as counting
// kinds: three review summaries are three restatements of the review channel.
func TestComposeCountsKindsNotDocuments(t *testing.T) {
	items := []evidence.Evidence{
		reviewEvidence(10, review.TopicAmbience, "很安静"),
		reviewEvidence(11, review.TopicService, "服务不错"),
		reviewEvidence(12, review.TopicFood, "菜不错"),
	}
	chat := &scriptedChat{contents: []string{"结论[^10][^11][^12]\nFOLLOWUPS: []"}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	ans, err := c.Compose(context.Background(), Input{Question: "q", Evidence: items})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ans.Text, "资料不足") || !strings.Contains(ans.Text, "3 条可用资料、1 类文档") {
		t.Fatalf("three documents of one kind are still one kind:\n%s", ans.Text)
	}
}

func TestComposeDropsTheCaveatWhenTwoDocTypesSupportIt(t *testing.T) {
	chat := &scriptedChat{contents: []string{"结论[^10]\nFOLLOWUPS: []"}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	ans, err := c.Compose(context.Background(), Input{Question: "q", Evidence: adequateEvidence()})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ans.Text, "资料不足") {
		t.Fatalf("two kinds of document are enough to answer:\n%s", ans.Text)
	}
}

// Lowering the assertion strength is a separate instruction from reporting the
// shortage: a model told only that the evidence is thin will mention it and then
// generalise anyway.
func TestComposeTellsTheModelTheEvidenceIsTooThinToGeneralise(t *testing.T) {
	chat := &scriptedChat{contents: []string{"结论[^10]\nFOLLOWUPS: []"}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	if _, err := c.Compose(context.Background(), Input{
		Question: "服务好不好？",
		Evidence: []evidence.Evidence{ev(10)},
	}); err != nil {
		t.Fatal(err)
	}

	content := userContentOf(t, chat)
	if !strings.Contains(content, "<evidence_adequacy>") {
		t.Fatalf("the prompt must flag a thin bundle:\n%s", content)
	}
	for _, want := range []string{"1 类文档", "不得用单条资料描述"} {
		if !strings.Contains(content, want) {
			t.Fatalf("adequacy block must contain %q:\n%s", want, content)
		}
	}
}

func TestComposeOmitsTheAdequacyBlockWhenTheEvidenceIsEnough(t *testing.T) {
	chat := &scriptedChat{contents: []string{"结论[^10]\nFOLLOWUPS: []"}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	if _, err := c.Compose(context.Background(), Input{
		Question: "q",
		Evidence: adequateEvidence(),
	}); err != nil {
		t.Fatal(err)
	}
	if content := userContentOf(t, chat); strings.Contains(content, "<evidence_adequacy>") {
		t.Fatalf("a sufficient bundle must not carry an adequacy block:\n%s", content)
	}
}
