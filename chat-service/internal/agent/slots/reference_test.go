package slots

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zed1995/platepilot/shared/domain/conversation"
	"github.com/zed1995/platepilot/shared/testkit"
)

// snapshot builds a candidate list in position order, the way the store returns
// one.
func snapshot(entries ...[2]any) []conversation.Candidate {
	out := make([]conversation.Candidate, 0, len(entries))
	for i, entry := range entries {
		candidate := conversation.Candidate{
			Position:     i + 1,
			RestaurantID: entry[0].(int64),
			Name:         entry[1].(string),
		}
		out = append(out, candidate)
	}
	return out
}

// withReasons attaches the recorded match reasons a modifier is matched against.
func withReasons(candidate conversation.Candidate, reasons ...string) conversation.Candidate {
	candidate.Reasons = reasons
	return candidate
}

func twoCandidates() []conversation.Candidate {
	return snapshot(
		[2]any{int64(7), "Joe's Pizza"},
		[2]any{int64(8), "Joe's Shanghai"},
	)
}

// The ordinal is the whole point of persisting candidates: "第二家" has to mean
// one particular row, and it has to mean the same row on every run.
func TestReferenceResolvesAnOrdinalAgainstTheSnapshot(t *testing.T) {
	for _, phrase := range []string{
		"第二家安静吗",
		"第2家安静吗",
		"第 2 家安静吗",
		"第二间安静吗",
		"第二家怎么样？",
	} {
		reference, ok := ResolveReference(phrase, twoCandidates(), 0)
		if !ok {
			t.Fatalf("%q: no reference recognised", phrase)
		}
		if reference.Kind != ReferenceOrdinal {
			t.Fatalf("%q: kind = %q, want ordinal", phrase, reference.Kind)
		}
		if reference.RestaurantID != 8 {
			t.Fatalf("%q: restaurant = %d, want 8 (position 2)", phrase, reference.RestaurantID)
		}
		if !strings.Contains(reference.Note, "Joe's Shanghai") {
			t.Fatalf("%q: the note must name what it understood: %q", phrase, reference.Note)
		}
	}
}

func TestReferenceReadsChineseNumeralsUpToNinetyNine(t *testing.T) {
	candidates := make([]conversation.Candidate, 0, 12)
	for i := 1; i <= 12; i++ {
		candidates = append(candidates, conversation.Candidate{
			Position: i, RestaurantID: int64(100 + i), Name: "R",
		})
	}
	cases := map[string]int64{
		"第十家":  110,
		"第十一家": 111,
		"第十二家": 112,
	}
	for phrase, want := range cases {
		reference, ok := ResolveReference(phrase, candidates, 0)
		if !ok {
			t.Fatalf("%q: no reference recognised", phrase)
		}
		if reference.RestaurantID != want {
			t.Fatalf("%q: restaurant = %d, want %d", phrase, reference.RestaurantID, want)
		}
	}
}

// "最后一家" is an ordinal with no number: it is the end of the list the user is
// looking at, whatever that list's length is.
func TestReferenceResolvesTheLastCandidate(t *testing.T) {
	candidates := twoCandidates()
	reference, ok := ResolveReference("最后一家呢", candidates, 0)
	if !ok {
		t.Fatal("最后一家 was not recognised")
	}
	if reference.RestaurantID != 8 {
		t.Fatalf("restaurant = %d, want the last (8)", reference.RestaurantID)
	}

	three := append(candidates, conversation.Candidate{Position: 3, RestaurantID: 9, Name: "Third"})
	reference, _ = ResolveReference("最后一家呢", three, 0)
	if reference.RestaurantID != 9 {
		t.Fatalf("with three candidates the last is 9, got %d", reference.RestaurantID)
	}
}

// A position the snapshot does not have is still a reference, and the honest
// answer is the count — not the nearest row.
func TestReferenceReportsAnOrdinalBeyondTheSnapshot(t *testing.T) {
	reference, ok := ResolveReference("第七家怎么样", twoCandidates(), 0)
	if !ok {
		t.Fatal("the ordinal must still be recognised")
	}
	if reference.RestaurantID != 0 {
		t.Fatalf("restaurant = %d, want none: there is no seventh candidate",
			reference.RestaurantID)
	}
	if reference.Position != 7 {
		t.Fatalf("position = %d, want the 7 the user asked about", reference.Position)
	}
	if !strings.Contains(reference.Note, "2 家") {
		t.Fatalf("the note must say how short the list is: %q", reference.Note)
	}
}

// The classifier is required for a reason: "第二次" is an occasion, and reading
// it as a position would answer about a restaurant the user never mentioned.
func TestReferenceIgnoresCountsThatAreNotRestaurants(t *testing.T) {
	for _, phrase := range []string{
		"第一次来纽约",
		"第二次来纽约",
		"第三次拜访",
		"第一印象不错",
	} {
		if reference, ok := ResolveReference(phrase, twoCandidates(), 0); ok {
			t.Fatalf("%q was read as %q: %+v", phrase, reference.Kind, reference)
		}
	}
}

// The weak modifier form must not swallow a clause. "我第一次去这家店" contains a
// demonstrative preceded by a verb phrase, and the phrase is not a description
// of any candidate — reading it as one would lose the reference that is
// actually there.
func TestAVerbPhraseBeforeTheMarkerIsNotADescription(t *testing.T) {
	reference, ok := ResolveReference("我第一次去这家店，感觉怎么样", twoCandidates(), 7)
	if !ok {
		t.Fatal("the demonstrative must still be recognised")
	}
	if reference.Kind != ReferenceDemonstrative {
		t.Fatalf("kind = %q, want demonstrative: %+v", reference.Kind, reference)
	}
	if reference.RestaurantID != 7 {
		t.Fatalf("restaurant = %d, want the pinned 7", reference.RestaurantID)
	}
}

// "这家 / 那家 / 它" means the restaurant the thread is already on.
func TestReferencePrefersThePinnedRestaurant(t *testing.T) {
	for _, phrase := range []string{"这家怎么样", "那家呢", "它安静吗", "this one?"} {
		reference, ok := ResolveReference(phrase, twoCandidates(), 8)
		if !ok {
			t.Fatalf("%q: no reference recognised", phrase)
		}
		if reference.Kind != ReferenceDemonstrative {
			t.Fatalf("%q: kind = %q, want demonstrative", phrase, reference.Kind)
		}
		if reference.RestaurantID != 8 {
			t.Fatalf("%q: restaurant = %d, want the pinned 8", phrase, reference.RestaurantID)
		}
	}
}

// With nothing pinned the demonstrative is the top candidate: the answer said
// "为你找到这几家", and the first is the recommendation.
func TestReferenceFallsBackToTheTopCandidate(t *testing.T) {
	reference, ok := ResolveReference("这家怎么样", twoCandidates(), 0)
	if !ok {
		t.Fatal("no reference recognised")
	}
	if reference.RestaurantID != 7 {
		t.Fatalf("restaurant = %d, want the top candidate 7", reference.RestaurantID)
	}
	if !strings.Contains(reference.Note, "第一家") {
		t.Fatalf("the note must say which one it read: %q", reference.Note)
	}
}

// A pinned restaurant the snapshot no longer lists is still the right answer:
// the snapshot is a window onto the last search, not the address book.
func TestReferenceKeepsAPinnedRestaurantOutsideTheSnapshot(t *testing.T) {
	reference, ok := ResolveReference("它呢", twoCandidates(), 999)
	if !ok {
		t.Fatal("no reference recognised")
	}
	if reference.RestaurantID != 999 {
		t.Fatalf("restaurant = %d, want the pinned 999", reference.RestaurantID)
	}
}

// A referring description is matched inside the snapshot. It is the reason the
// match reasons are persisted beside the position.
func TestReferenceMatchesADescriptionAgainstTheReasons(t *testing.T) {
	candidates := []conversation.Candidate{
		withReasons(conversation.Candidate{Position: 1, RestaurantID: 7, Name: "Joe's Pizza"},
			"匹配行政区：manhattan"),
		withReasons(conversation.Candidate{Position: 2, RestaurantID: 8, Name: "Joe's Pizza"},
			"匹配行政区：brooklyn"),
	}
	reference, ok := ResolveReference("布鲁克林那家怎么样", candidates, 0)
	if !ok {
		t.Fatal("no reference recognised")
	}
	if reference.Kind != ReferenceModifier {
		t.Fatalf("kind = %q, want modifier", reference.Kind)
	}
	if reference.RestaurantID != 8 {
		t.Fatalf("restaurant = %d, want the brooklyn one (8)", reference.RestaurantID)
	}
}

func TestReferenceMatchesADescriptionAgainstTheName(t *testing.T) {
	reference, ok := ResolveReference("那家 Shanghai 的怎么样", twoCandidates(), 0)
	if !ok {
		t.Fatal("no reference recognised")
	}
	if reference.Kind != ReferenceModifier {
		t.Fatalf("kind = %q, want modifier", reference.Kind)
	}
	if reference.RestaurantID != 8 {
		t.Fatalf("restaurant = %d, want the one whose name matches (8)", reference.RestaurantID)
	}
}

// A description that matches exactly one candidate resolves even in the weak
// form, which is how "布鲁克林那家" works: the snapshot's reasons are written in
// the corpus's spelling, and the alias tables bridge the two.
func TestReferenceMatchesADescriptionThroughTheAliasTables(t *testing.T) {
	candidates := []conversation.Candidate{
		withReasons(conversation.Candidate{Position: 1, RestaurantID: 7, Name: "Joe's Pizza"},
			"匹配行政区：manhattan"),
		withReasons(conversation.Candidate{Position: 2, RestaurantID: 8, Name: "Joe's Pizza"},
			"匹配行政区：brooklyn"),
	}
	reference, ok := ResolveReference("布鲁克林那家怎么样", candidates, 0)
	if !ok {
		t.Fatal("no reference recognised")
	}
	if reference.RestaurantID != 8 {
		t.Fatalf("restaurant = %d, want the brooklyn one (8)", reference.RestaurantID)
	}
}

// A description that fits more than one candidate is exactly the case where the
// user has to choose. Picking one would answer a different question.
func TestReferenceRefusesToPickBetweenEquallyGoodDescriptions(t *testing.T) {
	candidates := []conversation.Candidate{
		withReasons(conversation.Candidate{Position: 1, RestaurantID: 7, Name: "Joe's Pizza"},
			"评论推断：安静"),
		withReasons(conversation.Candidate{Position: 2, RestaurantID: 8, Name: "Joe's Pizza"},
			"评论推断：安静"),
	}
	reference, ok := ResolveReference("那家安静的怎么样", candidates, 0)
	if !ok {
		t.Fatal("no reference recognised")
	}
	if reference.RestaurantID != 0 {
		t.Fatalf("restaurant = %d, want none while two candidates answer to the description",
			reference.RestaurantID)
	}
	if !strings.Contains(reference.Note, "2 家") {
		t.Fatalf("the note must say how many fitted: %q", reference.Note)
	}
}

func TestReferenceReportsADescriptionThatMatchesNothing(t *testing.T) {
	reference, ok := ResolveReference("那家米其林的怎么样", twoCandidates(), 0)
	if !ok {
		t.Fatal("no reference recognised")
	}
	if reference.RestaurantID != 0 {
		t.Fatalf("restaurant = %d, want none", reference.RestaurantID)
	}
	if !strings.Contains(reference.Note, "米其林") {
		t.Fatalf("the note must quote what did not match: %q", reference.Note)
	}
}

// Without a snapshot there is nothing to refer to, and guessing would be worse
// than leaving the list to the model.
func TestReferenceNeedsASnapshot(t *testing.T) {
	if _, ok := ResolveReference("第二家怎么样", nil, 0); ok {
		t.Fatal("a reference without a snapshot must not resolve")
	}
}

func TestReferenceIgnoresAMessageWithNoReference(t *testing.T) {
	for _, phrase := range []string{
		"曼哈顿有什么安静的意大利餐厅？",
		"帮我订周五四个人",
		"你好",
	} {
		if reference, ok := ResolveReference(phrase, twoCandidates(), 0); ok {
			t.Fatalf("%q was read as a reference: %+v", phrase, reference)
		}
	}
}

// English markers are matched on word boundaries, and a bare "it" is not a
// marker at all: it is too common in ordinary prose for the false positives to
// be worth whatever it would catch.
func TestReferenceDoesNotReadAMarkerOutOfAnEnglishWord(t *testing.T) {
	for _, phrase := range []string{
		"the kitchen is open?",
		"is the onetime favourite still open?",
		"which one is it?",
	} {
		if reference, ok := ResolveReference(phrase, twoCandidates(), 8); ok {
			t.Fatalf("%q was read as a reference: %+v", phrase, reference)
		}
	}
}

// The possessive form reaches a Latin name the user typed inside a Chinese
// sentence, spaces and all.
func TestReferenceMatchesAMultiWordLatinName(t *testing.T) {
	candidates := []conversation.Candidate{
		{Position: 1, RestaurantID: 7, Name: "Joe's Pizza"},
		{Position: 2, RestaurantID: 8, Name: "Joe's Shanghai"},
	}
	reference, ok := ResolveReference("那家 Joe's Shanghai 的怎么样", candidates, 0)
	if !ok {
		t.Fatal("no reference recognised")
	}
	if reference.Kind != ReferenceModifier {
		t.Fatalf("kind = %q, want modifier: %+v", reference.Kind, reference)
	}
	if reference.RestaurantID != 8 {
		t.Fatalf("restaurant = %d, want the one whose name matches (8)", reference.RestaurantID)
	}
}

// A Chinese clause that merely happens to carry a space must not be swallowed
// as a description: there is no candidate called "不错 我喜欢".
func TestReferenceDoesNotSwallowASpacedChineseClause(t *testing.T) {
	reference, ok := ResolveReference("那家不错 我喜欢的餐厅怎么样", twoCandidates(), 0)
	if !ok {
		t.Fatal("the demonstrative must still be recognised")
	}
	if reference.Kind != ReferenceDemonstrative {
		t.Fatalf("kind = %q, want demonstrative: %+v", reference.Kind, reference)
	}
}

func TestParseCountHandlesTheFormsPeopleWrite(t *testing.T) {
	ok := map[string]int{
		"1": 1, "2": 2, "10": 10, "99": 99,
		"一": 1, "两": 2, "九": 9,
		"十": 10, "十一": 11, "十九": 19,
		"二十": 20, "二十一": 21, "九十九": 99,
	}
	for raw, want := range ok {
		got, valid := parseCount(raw)
		if !valid || got != want {
			t.Fatalf("parseCount(%q) = %d/%v, want %d", raw, got, valid, want)
		}
	}
	for _, raw := range []string{"", "零", "〇", "0", "-1", "第", "abc", "一百"} {
		if got, valid := parseCount(raw); valid {
			t.Fatalf("parseCount(%q) = %d, want rejected", raw, got)
		}
	}
}

// The resolver runs before the model and never calls one. A turn that needed a
// generation to understand "第二家" would make the persisted snapshot advisory
// rather than load-bearing.
func TestResolveReferenceCallsNoModel(t *testing.T) {
	// A provider that fails every call: resolving must not touch it.
	extractor := New(Deps{Structured: &testkit.MockChatProvider{
		Err: errors.New("structured provider is down"),
	}})
	plan, err := extractor.Extract(context.Background(), "第二家安静吗", ThreadContext{
		Candidates: twoCandidates(),
	})
	if err != nil {
		t.Fatalf("Extract must not fail: %v", err)
	}
	if plan.SelectedRestaurantID != 8 {
		t.Fatalf("selected = %d, want 8 resolved by rules", plan.SelectedRestaurantID)
	}
	if plan.ReferenceNote == "" {
		t.Fatal("a resolved reference must leave a note the answer can use")
	}
}

// The reference fills the slot the checkpoint was waiting for, which is what
// turns a clarification round into a continuation instead of another question.
func TestReferenceFillsThePendingRestaurantSlot(t *testing.T) {
	plan, err := rulesOnly().Extract(context.Background(), "第二家安静吗", ThreadContext{
		Pending: &conversation.Checkpoint{
			State:         conversation.StateAwaitingClarification,
			PendingAction: "resolve_restaurant",
			MissingSlots:  []string{SlotRestaurantID},
		},
		Candidates: twoCandidates(),
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if plan.NeedClarification {
		t.Fatalf("the slot was answered by 第二家, yet the plan still asks: %+v", plan)
	}
	if len(plan.MissingSlots) != 0 {
		t.Fatalf("missing slots = %v, want none", plan.MissingSlots)
	}
	if plan.SelectedRestaurantID != 8 {
		t.Fatalf("selected = %d, want 8", plan.SelectedRestaurantID)
	}
}

// A recognised reference the snapshot cannot satisfy is a warning, not a
// selection: the plan must not carry an id nobody chose.
func TestUnsatisfiableReferenceWarnsInsteadOfSelecting(t *testing.T) {
	plan, err := rulesOnly().Extract(context.Background(), "第七家怎么样", ThreadContext{
		Candidates: twoCandidates(),
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if plan.SelectedRestaurantID != 0 {
		t.Fatalf("selected = %d, want none", plan.SelectedRestaurantID)
	}
	if len(plan.Warnings) == 0 {
		t.Fatal("an unsatisfiable reference must be reported on the plan")
	}
	if !strings.Contains(strings.Join(plan.Warnings, "；"), "2 家") {
		t.Fatalf("warnings = %v, want the candidate count", plan.Warnings)
	}
}

// A reference with no snapshot to read is simply not resolved; the model still
// sees the message and can ask.
func TestExtractWithoutCandidatesLeavesTheReferenceAlone(t *testing.T) {
	plan, err := rulesOnly().Extract(context.Background(), "第二家安静吗",
		ThreadContext{Pending: &conversation.Checkpoint{MissingSlots: []string{SlotRestaurantID}}})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if plan.SelectedRestaurantID != 0 {
		t.Fatalf("selected = %d, want none without a snapshot", plan.SelectedRestaurantID)
	}
	if !plan.NeedClarification {
		t.Fatal("an unfilled restaurant slot must keep the thread asking")
	}
}
