package answer

import (
	"context"
	"strings"
	"testing"
	"time"

	domainretrieval "github.com/zed1995/platepilot/shared/domain/retrieval"
	"github.com/zed1995/platepilot/shared/domain/review"
	"github.com/zed1995/platepilot/shared/domain/search"
)

// factCandidates is the search hand-off for the 2026-10-06 incident turn:
// the structured facts the answer needs (rating with its sample size) are
// already on the candidate; no evidence document carries them.
func factCandidates() []search.RestaurantCandidate {
	rating := 4.8
	price := 2
	return []search.RestaurantCandidate{{
		RestaurantID: 2059,
		Name:         "Royal Bay Restaurant",
		Address:      "123 60th St, Brooklyn, NY",
		Borough:      "brooklyn",
		Cuisines:     []string{"european"},
		Rating:       &rating,
		RatingCount:  8,
		PriceLevel:   &price,
		SnapshotAt:   time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC),
		Reasons:      []string{"满足全部硬条件（评分>=4.0、行政区=brooklyn）"},
	}}
}

func factFilter() search.RestaurantFilter {
	rating := 4.0
	return search.RestaurantFilter{Borough: "brooklyn", MinRating: &rating}
}

// The incident: the rating is a structured fact the search already enforced
// (min_rating=4) and the candidate carries it. The composer must answer from
// it without any evidence document, instead of returning the fixed refusal.
func TestComposeAnswersRatingQuestionFromCandidatesWithoutEvidence(t *testing.T) {
	chat := &scriptedChat{contents: []string{
		"结论：推荐 Royal Bay Restaurant，评分 4.8（8 条评论样本），据 Google Local 2021 年快照，满足评分 4.0 以上的要求。\nFOLLOWUPS: []",
	}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})

	ans, err := c.Compose(context.Background(), Input{
		Question:   "帮我推荐一家布鲁克林评分4以上的餐厅",
		Candidates: factCandidates(),
		Filters:    factFilter(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ans.Text, RefusalAnswer) || strings.Contains(ans.Text, "无法确认评分") {
		t.Fatalf("candidate facts must be answerable without evidence:\n%s", ans.Text)
	}
	if !strings.Contains(ans.Text, "4.8") || !strings.Contains(ans.Text, "8 条") {
		t.Fatalf("answer must state the rating and its sample size:\n%s", ans.Text)
	}
	if len(chat.requests) != 1 {
		t.Fatalf("Complete called %d times, want 1 (facts route through the composer)", len(chat.requests))
	}
	if len(ans.Citations) != 0 {
		t.Fatalf("citations = %v, want none for fact-only statements", ans.Citations)
	}
}

// A fact-only turn owes the model a call even with zero evidence: its
// material is the candidate block, and a turn routed around the composer
// would ship unvalidated, unprompted text.
func TestComposeFactOnlyTurnReachesTheModel(t *testing.T) {
	chat := &scriptedChat{contents: []string{"档案事实回答。\nFOLLOWUPS: []"}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})

	ans, err := c.Compose(context.Background(), Input{
		Question:   "这家店评分多少？",
		Candidates: factCandidates(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if ans.Text != "档案事实回答。" {
		t.Fatalf("text = %q, want the model's fact answer", ans.Text)
	}
}

// 4.8/8 reviews and 4.8/300 reviews are different claims. The sample size
// lives on the candidate and the block has to render it next to the rating.
func TestComposeRendersRatingSampleSizeNextToTheRating(t *testing.T) {
	chat := &scriptedChat{contents: []string{"结论\nFOLLOWUPS: []"}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})

	if _, err := c.Compose(context.Background(), Input{
		Question:   "评分多少",
		Candidates: factCandidates(),
	}); err != nil {
		t.Fatal(err)
	}

	content := userContentOf(t, chat)
	for _, want := range []string{"评分4.8", "8 条评论样本"} {
		if !strings.Contains(content, want) {
			t.Fatalf("candidate block is missing %q:\n%s", want, content)
		}
	}
}

// No evidence and no opinion question means nothing is "thin": the
// doc-type-count caveat exists to stop a review generalisation, not to flag
// a structured fact.
func TestComposeFactOnlyTurnHasNoAdequacyLeadOrBlock(t *testing.T) {
	chat := &scriptedChat{contents: []string{"评分 4.8。\nFOLLOWUPS: []"}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})

	ans, err := c.Compose(context.Background(), Input{
		Question:   "这家店评分多少？",
		Candidates: factCandidates(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(ans.Text, "资料不足") || strings.Contains(ans.Text, "资料不足") {
		t.Fatalf("a fact-only turn must not carry the thin-evidence caveat:\n%s", ans.Text)
	}
	content := userContentOf(t, chat)
	if strings.Contains(content, "<evidence_adequacy>") || strings.Contains(content, "<review_gap>") {
		t.Fatalf("a fact-only turn must not carry an adequacy block:\n%s", content)
	}
}

// A mixed turn with candidates but zero review evidence keeps the objective
// half and explicitly loses the subjective half. The model is told in code
// rather than asked to notice it had no reviews.
func TestComposeOpinionGapWithCandidatesKeepsFactsButFlagsReviewsMissing(t *testing.T) {
	chat := &scriptedChat{contents: []string{
		"Royal Bay 评分 4.8（8 条评论样本）。\n无法确认：安静——本次没有评论资料。\nFOLLOWUPS: []",
	}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})

	ans, err := c.Compose(context.Background(), Input{
		Question:   "这家店评分多少，安静吗？",
		Candidates: factCandidates(),
		SoftConditions: []domainretrieval.SoftCondition{
			{Text: "安静", Topic: review.TopicAmbience},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ans.Text, "评论资料不足") {
		t.Fatalf("an opinion turn without reviews must open with the review-gap lead:\n%s", ans.Text)
	}
	if !strings.Contains(ans.Text, "评分 4.8（8 条评论样本）") {
		t.Fatalf("candidate facts survive the review gap:\n%s", ans.Text)
	}
	content := userContentOf(t, chat)
	for _, want := range []string{
		"<review_gap>",
		"安静（ambience）：本次资料中没有对应评论，必须列入「无法确认」",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("prompt is missing %q:\n%s", want, content)
		}
	}
}

// The streamed path has to offer the same fact route: a candidates-only
// turn streams the model's text instead of publishing the fixed refusal.
func TestComposeStreamFactOnlyTurnStreamsInsteadOfRefusal(t *testing.T) {
	chat := &streamChat{chunks: []string{
		"Royal Bay 评分 4.8（8 条评论样本）。",
		"\nFOLLOWUPS: []",
	}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	sink, deltas := collectingSink()

	ans, err := c.ComposeStream(context.Background(), Input{
		Question:   "评分多少",
		Candidates: factCandidates(),
	}, StreamHandlers{Delta: sink})
	if err != nil {
		t.Fatal(err)
	}
	if ans.Text != "Royal Bay 评分 4.8（8 条评论样本）。" {
		t.Fatalf("text = %q", ans.Text)
	}
	got := strings.Join(*deltas, "")
	if !strings.Contains(got, "4.8") || strings.Contains(got, RefusalAnswer) {
		t.Fatalf("streamed deltas = %q", got)
	}
}

// The two-source rule lives in the system instruction. It has to (a)
// authorise candidate facts without a footnote, (b) name the snapshot
// source, and (c) forbid fields the candidate does not carry — opening the
// fact route without these three would invite invented opening hours.
func TestComposeInstructionSeparatesFactSourceFromReviewCitations(t *testing.T) {
	chat := &scriptedChat{contents: []string{"结论\nFOLLOWUPS: []"}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	if _, err := c.Compose(context.Background(), Input{
		Question:   "评分多少，几点关门？",
		Candidates: factCandidates(),
	}); err != nil {
		t.Fatal(err)
	}

	system := systemOf(t, chat)
	for _, want := range []string{
		"system venue profiles",
		"state them directly",
		"do not mark them with [^id]",
		"Google Local",
		"review sample size",
		"not included in the venue profile",
	} {
		if !strings.Contains(system, want) {
			t.Fatalf("system instruction must contain %q:\n%s", want, system)
		}
	}
}

// <filters> states hard conditions the search enforced. Candidates are
// guaranteed by the fusion layer to satisfy them, so the block must authorise
// stating that satisfaction directly rather than demanding a citation.
func TestComposeFiltersBlockAuthorisesStatingSatisfaction(t *testing.T) {
	chat := &scriptedChat{contents: []string{"结论\nFOLLOWUPS: []"}}
	c := NewComposer(Deps{Chat: chat, Model: "m"})
	if _, err := c.Compose(context.Background(), Input{
		Question:   "布鲁克林评分4以上",
		Candidates: factCandidates(),
		Filters:    factFilter(),
	}); err != nil {
		t.Fatal(err)
	}

	content := userContentOf(t, chat)
	if !strings.Contains(content, "候选均已由系统确认满足") {
		t.Fatalf("filters block must authorise direct satisfaction statements:\n%s", content)
	}
}
