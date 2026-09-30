package knowledge

import (
	"strings"
	"testing"
	"time"

	"github.com/zed/platepilot/data-pipeline/internal/pipeline/curate"
	"github.com/zed/platepilot/shared/domain/evidence"
	"github.com/zed/platepilot/shared/domain/restaurant"
	"github.com/zed/platepilot/shared/domain/review"
)

func attrsRestaurant() restaurant.Restaurant {
	r := fullRestaurant()
	r.ID = 77
	r.Attributes = restaurant.Attributes{
		AtmosphereTags:    []string{"Casual", "Cozy"},
		PopularForTags:    []string{"Date night"},
		ServiceOptionTags: []string{"Takeout", "Delivery"},
		AccessibilityTags: []string{"Wheelchair accessible"},
	}
	return r
}

// --- attributes ---------------------------------------------------------

func TestBuildAttributesRendersEveryGroup(t *testing.T) {
	doc, ok, err := BuildAttributes(attrsRestaurant(), defaultOptions())
	if err != nil || !ok {
		t.Fatalf("BuildAttributes: ok=%v err=%v", ok, err)
	}
	for _, want := range []string{"氛围：Casual、Cozy", "适合场合：Date night", "服务：Takeout、Delivery", "无障碍：Wheelchair accessible"} {
		if !strings.Contains(doc.Content, want) {
			t.Errorf("content missing %q:\n%s", want, doc.Content)
		}
	}
	if doc.Scope != evidence.ScopeEvidence || doc.DocType != evidence.DocTypeRestaurantAttributes {
		t.Errorf("scope/doc_type = %q/%q", doc.Scope, doc.DocType)
	}
}

// The raw MISC object is full of spelling variants and "False:No" values;
// rendering it would put that noise into the vector.
func TestBuildAttributesIgnoresRawMisc(t *testing.T) {
	r := attrsRestaurant()
	r.AttributesRaw = map[string][]string{
		"False":   {"No", "no", "NO"},
		"Dine-in": {"True"},
	}
	doc, ok, err := BuildAttributes(r, defaultOptions())
	if err != nil || !ok {
		t.Fatalf("BuildAttributes: ok=%v err=%v", ok, err)
	}
	for _, banned := range []string{"False", "NO", "True", "Dine-in"} {
		if strings.Contains(doc.Content, banned) {
			t.Errorf("raw MISC leaked into the document: %q in\n%s", banned, doc.Content)
		}
	}
}

func TestBuildAttributesSkipsRestaurantsWithoutAny(t *testing.T) {
	r := attrsRestaurant()
	r.Attributes = restaurant.Attributes{}
	if _, ok, _ := BuildAttributes(r, defaultOptions()); ok {
		t.Error("an empty attribute set must not produce a document")
	}
}

func TestBuildAttributesOmitsEmptyGroups(t *testing.T) {
	r := attrsRestaurant()
	r.Attributes.AccessibilityTags = nil
	doc, _, _ := BuildAttributes(r, defaultOptions())
	if strings.Contains(doc.Content, "无障碍") {
		t.Errorf("empty group rendered:\n%s", doc.Content)
	}
	if !strings.Contains(doc.Content, "氛围") {
		t.Errorf("non-empty group missing:\n%s", doc.Content)
	}
}

// --- hours --------------------------------------------------------------

func TestBuildHoursUsesRawText(t *testing.T) {
	r := fullRestaurant()
	doc, ok, err := BuildHours(r, defaultOptions())
	if err != nil || !ok {
		t.Fatalf("BuildHours: ok=%v err=%v", ok, err)
	}
	// The source string is what a reader recognises and what the database holds.
	if !strings.Contains(doc.Content, "11AM–10PM") {
		t.Errorf("raw source text not used:\n%s", doc.Content)
	}
}

func TestBuildHoursSkipsRestaurantsWithoutHours(t *testing.T) {
	r := fullRestaurant()
	r.Hours = nil
	if _, ok, _ := BuildHours(r, defaultOptions()); ok {
		t.Error("a restaurant with no hours must not produce an hours document")
	}
}

// --- summaries ----------------------------------------------------------

func summaryFixture() []review.Review {
	return []review.Review{
		{ID: 1, Rating: 5, ReviewedAt: fixtureTime, Text: "the pizza was delicious and the staff were friendly", TopicTags: []string{curate.TopicFood, curate.TopicService}},
		{ID: 2, Rating: 4, ReviewedAt: fixtureTime, Text: "great food, friendly owner, cozy room", TopicTags: []string{curate.TopicFood, curate.TopicService}},
		{ID: 3, Rating: 2, ReviewedAt: fixtureTime, Text: "slow service and a long wait for cold food", TopicTags: []string{curate.TopicService, curate.TopicWait}},
	}
}

func TestSummarizeTopicsAggregatesPerTopic(t *testing.T) {
	average := 3.5
	stats := SummarizeTopics(summaryFixture(), &average)
	if len(stats) != 3 {
		t.Fatalf("topics = %d, want 3 (food, service, wait)", len(stats))
	}
	byTopic := make(map[string]TopicStats, len(stats))
	for _, s := range stats {
		byTopic[s.Topic] = s
	}

	food := byTopic[curate.TopicFood]
	if food.Count != 2 {
		t.Errorf("food count = %d, want 2", food.Count)
	}
	if food.AverageRating != 4.5 {
		t.Errorf("food average = %v, want 4.5", food.AverageRating)
	}
	if food.PositiveRatio != 1.0 {
		t.Errorf("food positive ratio = %v, want 1.0", food.PositiveRatio)
	}
	// Sentiment is an offset from the restaurant's own average, not a score.
	if food.Sentiment != 1.0 {
		t.Errorf("food sentiment = %v, want 1.0", food.Sentiment)
	}

	service := byTopic[curate.TopicService]
	if service.Count != 3 {
		t.Errorf("service count = %d, want 3", service.Count)
	}
	// Service ratings are 5, 4 and 2, so the topic average is 3.67 - above the
	// restaurant's own 3.5, and the offset must say so rather than describing
	// the topic in absolute terms.
	if service.Sentiment <= 0 {
		t.Errorf("service sentiment = %v, want above the restaurant average", service.Sentiment)
	}

	wait := byTopic[curate.TopicWait]
	if wait.Count != 1 {
		t.Errorf("wait count = %d, want 1", wait.Count)
	}
	if wait.Sentiment >= 0 {
		t.Errorf("wait sentiment = %v, want below the restaurant average", wait.Sentiment)
	}
}

func TestSummarizeTopicsIsSortedAndDeterministic(t *testing.T) {
	first := SummarizeTopics(summaryFixture(), nil)
	for i := 0; i < 20; i++ {
		got := SummarizeTopics(summaryFixture(), nil)
		if len(got) != len(first) {
			t.Fatalf("run %d produced %d topics", i, len(got))
		}
		for j := range got {
			if got[j].Topic != first[j].Topic || got[j].Count != first[j].Count {
				t.Fatalf("run %d topic %d = %+v, want %+v", i, j, got[j], first[j])
			}
		}
	}
}

func TestSummarizeTopicsWithoutTopicsReturnsNothing(t *testing.T) {
	if got := SummarizeTopics([]review.Review{{ID: 1, Text: "no tags"}}, nil); got != nil {
		t.Errorf("SummarizeTopics = %v, want nil", got)
	}
}

func TestSummaryDocumentsOnePerTopic(t *testing.T) {
	r := fullRestaurant()
	stats := SummarizeTopics(summaryFixture(), nil)
	summaries := BuildTopicSummaries(r.ID, stats, fixtureTime, fixtureTime)
	docs := BuildSummaryDocuments(r, summaries, defaultOptions())

	if len(docs) != len(summaries) {
		t.Fatalf("documents = %d, want one per topic (%d)", len(docs), len(summaries))
	}
	for _, doc := range docs {
		if doc.DocType != evidence.DocTypeRestaurantReviewSummary {
			t.Errorf("doc_type = %q", doc.DocType)
		}
		topic, _ := doc.Metadata["topic"].(string)
		if topic == "" {
			t.Error("document is missing its topic metadata")
		}
		// A question about one topic must be able to select only that topic.
		if !strings.Contains(doc.Content, curate.TopicLabel(topic)) {
			t.Errorf("document does not name its topic:\n%s", doc.Content)
		}
	}
}

// The document and the stored row are rendered from the same numbers. If they
// could drift, the citation and the audit row would disagree.
func TestSummaryDocumentAgreesWithStoredSummary(t *testing.T) {
	r := fullRestaurant()
	stats := SummarizeTopics(summaryFixture(), nil)
	summaries := BuildTopicSummaries(r.ID, stats, fixtureTime, fixtureTime)
	docs := BuildSummaryDocuments(r, summaries, defaultOptions())

	for i, doc := range docs {
		summary := summaries[i]
		parsed, ok := parseTopicAverage(summary.Summary)
		if !ok {
			t.Fatalf("summary %q does not carry a parseable average", summary.Summary)
		}
		if !strings.Contains(doc.Content, renderAverage(parsed)) {
			t.Errorf("document shows a different average than the stored row:\n%s\nrow: %s", doc.Content, summary.Summary)
		}
		if summary.GeneratedBy != RulesVersion {
			t.Errorf("generated_by = %q, want %q", summary.GeneratedBy, RulesVersion)
		}
	}
}

func TestParseTopicAverageRejectsGarbage(t *testing.T) {
	for _, text := range []string{"", "no numbers here", "平均 ", "平均  星"} {
		if _, ok := parseTopicAverage(text); ok {
			t.Errorf("parseTopicAverage(%q) reported success", text)
		}
	}
}

func TestSummaryDocumentDoesNotClaimAnAverageItLacks(t *testing.T) {
	r := fullRestaurant()
	broken := review.Summary{
		RestaurantID: r.ID, Topic: curate.TopicFood, EvidenceCount: 3,
		Summary: "a summary with no average in it", GeneratedBy: RulesVersion,
	}
	doc := BuildSummaryDocuments(r, []review.Summary{broken}, defaultOptions())
	if len(doc) != 1 {
		t.Fatalf("documents = %d, want 1", len(doc))
	}
	if !strings.Contains(doc[0].Content, "评分未知") {
		t.Errorf("a missing average must be stated, not invented:\n%s", doc[0].Content)
	}
	if strings.Contains(doc[0].Content, "0.0 星") {
		t.Errorf("a missing average must not be rendered as zero:\n%s", doc[0].Content)
	}
}

// --- representative reviews --------------------------------------------

func reviewFixture(n int, rating int) []review.Review {
	out := make([]review.Review, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, review.Review{
			ID:           int64(i),
			RestaurantID: 77,
			Rating:       rating,
			ReviewedAt:   fixtureTime.Add(time.Duration(i) * time.Hour),
			Text:         "review text number " + string(rune('a'+i%26)),
		})
	}
	return out
}

func TestRepresentativeCountFollowsThePRDRule(t *testing.T) {
	cases := []struct {
		textCount int64
		available int
		want      int
	}{
		{5, 5, 5},      // fewer reviews than the floor: take what exists
		{50, 50, 10},   // 5 -> raised to the floor
		{200, 200, 20}, // 10% of 200
		{1000, 1000, 30},
		{5000, 5000, 30}, // capped
		{0, 12, 12},      // no rollup available: fall back to what exists
	}
	for _, tc := range cases {
		if got := representativeCount(tc.textCount, tc.available); got != tc.want {
			t.Errorf("representativeCount(%d, %d) = %d, want %d", tc.textCount, tc.available, got, tc.want)
		}
	}
}

func TestSelectRepresentativeIsDeterministic(t *testing.T) {
	reviews := reviewFixture(40, 3)
	first := SelectRepresentative(reviews, 40)
	for i := 0; i < 30; i++ {
		got := SelectRepresentative(reviews, 40)
		if len(got) != len(first) {
			t.Fatalf("run %d selected %d, want %d", i, len(got), len(first))
		}
		for j := range got {
			if got[j].ID != first[j].ID {
				t.Fatalf("run %d differs at %d: got review %d, want %d", i, j, got[j].ID, first[j].ID)
			}
		}
	}
}

// Input order must not change the outcome: the sort is total, not incidental.
func TestSelectRepresentativeIgnoresInputOrder(t *testing.T) {
	reviews := reviewFixture(40, 4)
	forward := SelectRepresentative(reviews, 40)

	reversed := make([]review.Review, len(reviews))
	for i, r := range reviews {
		reversed[len(reviews)-1-i] = r
	}
	backward := SelectRepresentative(reversed, 40)

	if len(forward) != len(backward) {
		t.Fatalf("selection sizes differ: %d vs %d", len(forward), len(backward))
	}
	for i := range forward {
		if forward[i].ID != backward[i].ID {
			t.Fatalf("position %d differs: %d vs %d", i, forward[i].ID, backward[i].ID)
		}
	}
}

// Two reviews identical in every comparable field must still resolve to one
// order, which is what the id tie-break guarantees.
func TestSelectRepresentativeBreaksExactTies(t *testing.T) {
	same := []review.Review{
		{ID: 9, Rating: 4, ReviewedAt: fixtureTime, Text: "identical text"},
		{ID: 3, Rating: 4, ReviewedAt: fixtureTime, Text: "identical text"},
		{ID: 7, Rating: 4, ReviewedAt: fixtureTime, Text: "identical text"},
	}
	got := SelectRepresentative(same, 3)
	if len(got) != 3 {
		t.Fatalf("selected %d, want 3", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].ID >= got[i].ID {
			t.Errorf("exact ties not resolved by ascending id: %v", idsOf(got))
		}
	}
}

func idsOf(reviews []review.Review) []int64 {
	out := make([]int64, len(reviews))
	for i, r := range reviews {
		out[i] = r.ID
	}
	return out
}

// Stratifying by sentiment is the point: quoting only five-star reviews would
// make every restaurant read as uniformly good.
func TestSelectRepresentativeCoversEverySentimentBand(t *testing.T) {
	var reviews []review.Review
	for i := 0; i < 10; i++ {
		reviews = append(reviews,
			review.Review{ID: int64(100 + i), Rating: 5, ReviewedAt: fixtureTime, Text: "positive review text"},
			review.Review{ID: int64(200 + i), Rating: 3, ReviewedAt: fixtureTime, Text: "neutral review text"},
			review.Review{ID: int64(300 + i), Rating: 1, ReviewedAt: fixtureTime, Text: "negative review text"},
		)
	}
	// 30 text reviews means a target of 10 (a tenth of the corpus). Ten does
	// not divide evenly by three bands, so the quota is 4/3/3 with the spare
	// pick going to a band by remainder - what matters is that no band is
	// skipped and that the spread is at most one, not that the split is
	// perfectly even.
	got := SelectRepresentative(reviews, 30)
	if len(got) != 10 {
		t.Fatalf("selected %d reviews, want 10", len(got))
	}
	counts := map[string]int{}
	for _, r := range got {
		counts[sentimentBand(r.Rating)]++
	}
	if counts[bandPositive] == 0 || counts[bandNeutral] == 0 || counts[bandNegative] == 0 {
		t.Fatalf("a sentiment band was skipped entirely: %v", counts)
	}
	low, high := counts[bandNegative], counts[bandPositive]
	if low > high {
		low, high = high, low
	}
	if high-low > 1 {
		t.Errorf("band spread is %v, want at most 1: %v", high-low, counts)
	}

	// A larger target that divides evenly must split exactly.
	got = SelectRepresentative(reviews, 30*12)
	if len(got) != 30 {
		t.Fatalf("selected %d reviews, want 30", len(got))
	}
	counts = map[string]int{}
	for _, r := range got {
		counts[sentimentBand(r.Rating)]++
	}
	if counts[bandPositive] != counts[bandNeutral] || counts[bandNeutral] != counts[bandNegative] {
		t.Errorf("an evenly divisible target must split evenly: %v", counts)
	}
}

func TestSelectRepresentativeExcludesEmptyText(t *testing.T) {
	reviews := []review.Review{
		{ID: 1, Rating: 5, ReviewedAt: fixtureTime, Text: "real text"},
		{ID: 2, Rating: 5, ReviewedAt: fixtureTime, Text: "   "},
	}
	got := SelectRepresentative(reviews, 10)
	if len(got) != 1 || got[0].ID != 1 {
		t.Errorf("selected %v, want only the review with text", idsOf(got))
	}
}

func TestSelectRepresentativeReturnsNothingWithoutText(t *testing.T) {
	if got := SelectRepresentative([]review.Review{{ID: 1, Rating: 5}}, 10); got != nil {
		t.Errorf("SelectRepresentative = %v, want nil", idsOf(got))
	}
}

func TestBuildRepresentativeDocumentRendersCitations(t *testing.T) {
	reviews := reviewFixture(12, 4)
	selected := SelectRepresentative(reviews, 12)
	doc, ok, err := BuildRepresentativeDocument(
		"Luca Pizza", 77, "Manhattan", fixtureTime, reviews, selected, "gmap-abc", defaultOptions())
	if err != nil || !ok {
		t.Fatalf("BuildRepresentativeDocument: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(doc.Content, "代表性评论") {
		t.Errorf("missing heading:\n%s", doc.Content)
	}
	// Rating and date survive into the text so a citation can show them without
	// a second lookup.
	if !strings.Contains(doc.Content, "[4星]") {
		t.Errorf("rating missing:\n%s", doc.Content)
	}
	if !strings.Contains(doc.Content, "2021-") {
		t.Errorf("review date missing:\n%s", doc.Content)
	}
	if doc.Metadata["representative_count"] != len(selected) {
		t.Errorf("representative_count = %v, want %d", doc.Metadata["representative_count"], len(selected))
	}
}

func TestBuildRepresentativeDocumentTruncatesLongReviews(t *testing.T) {
	long := strings.Repeat("很长的评论内容", 200)
	reviews := []review.Review{{ID: 1, Rating: 5, ReviewedAt: fixtureTime, Text: long}}
	doc, _, _ := BuildRepresentativeDocument("X", 77, "", fixtureTime, reviews, reviews, "", defaultOptions())
	if !strings.Contains(doc.Content, "…") {
		t.Error("a very long review should be truncated")
	}
	for _, line := range strings.Split(doc.Content, "\n") {
		if len([]rune(line)) > representativeSampleText+40 {
			t.Errorf("line is %d runes, expected truncation to apply", len([]rune(line)))
		}
	}
}

func TestBuildRepresentativeDocumentWithoutSelection(t *testing.T) {
	if _, ok, _ := BuildRepresentativeDocument("X", 77, "", fixtureTime, nil, nil, "", defaultOptions()); ok {
		t.Error("no selection must not produce a document")
	}
}
