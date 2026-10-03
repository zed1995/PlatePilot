package retrieval

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/zed1995/platepilot/shared/domain/retrieval"
	"github.com/zed1995/platepilot/shared/domain/review"
	"github.com/zed1995/platepilot/shared/domain/search"
	"github.com/zed1995/platepilot/shared/store"
)

// softRequest is a query that also states a condition only reviews can answer.
func softRequest(query string, conditions ...retrieval.SoftCondition) retrieval.Request {
	req := reqWithQuery(query)
	req.SoftConditions = conditions
	return req
}

// ambience is the condition this file uses throughout: the user says "安静", the
// corpus is tagged "ambience", and the mapping between them is the whole reason
// the topic travels beside the words.
func ambience(text string) retrieval.SoftCondition {
	return retrieval.SoftCondition{Text: text, Topic: review.TopicAmbience}
}

// softOnlyService wires a vector channel with one restaurant the query can
// recall.
func softOnlyService(t *testing.T, embedding *stubEmbedding) (*Service, *stubKnowledge) {
	t.Helper()
	repo := &stubRestaurants{
		details: map[int64]search.RestaurantDetail{
			1: {Name: "Quiet Corner", Borough: "manhattan", RatingCount: 400},
		},
	}
	knowledge := &stubKnowledge{docs: []store.ScoredDocument{
		scoredDoc(1, 1, "Quiet Corner", 0.20),
	}}
	return vectorService(t, repo, knowledge, embedding, true), knowledge
}

// The soft condition has to reach the embedding, and the topic has to travel
// with it: the user writes Chinese and the reviews are tagged in English, so the
// words alone are not enough for the recall to find anything.
func TestVectorChannelEmbedsTheSoftConditionBesideTheQuestion(t *testing.T) {
	embedding := &stubEmbedding{vector: []float32{1, 0, 0}}
	service, _ := softOnlyService(t, embedding)

	if _, err := service.Search(context.Background(), softRequest(
		"italian place in the east village", ambience("安静"))); err != nil {
		t.Fatalf("search: %v", err)
	}

	embedded := embedding.query
	for _, want := range []string{"italian place in the east village", "安静", review.TopicAmbience} {
		if !strings.Contains(embedded, want) {
			t.Fatalf("embedded text %q is missing %q", embedded, want)
		}
	}
}

// Without a soft condition the embedded text is the query and nothing else. This
// is what keeps the M3 recall baseline a baseline: every case that does not
// mention a soft condition must embed exactly what it embedded before.
func TestVectorChannelEmbedsTheBareQueryWhenNothingIsSoft(t *testing.T) {
	embedding := &stubEmbedding{vector: []float32{1, 0, 0}}
	service, _ := softOnlyService(t, embedding)

	const query = "ramen in brooklyn"
	if _, err := service.Search(context.Background(), reqWithQuery(query)); err != nil {
		t.Fatalf("search: %v", err)
	}
	if embedding.query != query {
		t.Fatalf("embedded %q, want the query verbatim (%q)", embedding.query, query)
	}
}

// A soft condition may move a restaurant up the list. It must never be able to
// move one that fails a hard condition back in, and the only way that can be
// checked is that it never reaches the filter at all.
func TestSoftConditionsNeverReachTheFilter(t *testing.T) {
	embedding := &stubEmbedding{vector: []float32{1, 0, 0}}
	service, knowledge := softOnlyService(t, embedding)

	got, err := service.Search(context.Background(), softRequest(
		"italian place in the east village", ambience("安静"), ambience("适合约会")))
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	// The recall statement is the other place a soft condition could leak into a
	// hard one: it is built from the filter, so a borough that is really a soft
	// word would show up here.
	if knowledge.got.Borough == "安静" || knowledge.got.Borough == review.TopicAmbience {
		t.Fatalf("a soft condition became a borough filter: %q", knowledge.got.Borough)
	}
	assertNoSoftInFilter(t, got.Trace.Filters, "安静", review.TopicAmbience)
	if len(got.Candidates) == 0 {
		t.Fatal("the soft-only signal must still recall something")
	}
}

// The trace says the vector channel was steered by reviews, the ranking's
// reasons say which condition and which topic, and the note reaches the warnings
// so the stream shows what the ordering was made of.
func TestSoftConditionsAreLabelledAsAnInferenceFromReviews(t *testing.T) {
	embedding := &stubEmbedding{vector: []float32{1, 0, 0}}
	service, _ := softOnlyService(t, embedding)

	got, err := service.Search(context.Background(), softRequest(
		"italian place in the east village", ambience("安静")))
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	var note string
	for _, summary := range got.Trace.Channels {
		if summary.Channel == retrieval.ChannelVector {
			if !summary.Ran {
				t.Fatal("the vector channel must be recorded as run")
			}
			note = summary.Note
		}
	}
	if note != softConditionNote {
		t.Fatalf("vector note = %q, want %q", note, softConditionNote)
	}
	if !containsString(got.Trace.Warnings, softConditionNote) {
		t.Fatalf("the trace warnings must carry the note, got %v", got.Trace.Warnings)
	}

	reasons := got.Candidates[0].Reasons
	joined := strings.Join(reasons, "；")
	want := fmt.Sprintf("评论推断：安静（%s）", review.TopicAmbience)
	if !strings.Contains(joined, want) {
		t.Fatalf("reasons %v must contain %q", reasons, want)
	}
}

// A condition that mapped onto no topic is still named, without a parenthetical.
// Hiding it would make the reason claim the ranking understood something it did
// not.
func TestAnUnmappedSoftConditionIsStillNamedInTheReason(t *testing.T) {
	embedding := &stubEmbedding{vector: []float32{1, 0, 0}}
	service, _ := softOnlyService(t, embedding)

	got, err := service.Search(context.Background(), softRequest(
		"somewhere nice", retrieval.SoftCondition{Text: "有个好院子"}))
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	joined := strings.Join(got.Candidates[0].Reasons, "；")
	if !strings.Contains(joined, "评论推断：有个好院子") {
		t.Fatalf("reasons %v must name the unmapped condition", got.Candidates[0].Reasons)
	}
	if strings.Contains(joined, "有个好院子（") {
		t.Fatalf("an unmapped condition must not claim a topic: %v", got.Candidates[0].Reasons)
	}
}

// A soft-only search is still a search. Refusing it would leave the one channel
// able to answer it with nothing to do.
func TestASoftOnlySearchIsNotAnEmptySearch(t *testing.T) {
	embedding := &stubEmbedding{vector: []float32{1, 0, 0}}
	service, _ := softOnlyService(t, embedding)

	got, err := service.Search(context.Background(), retrieval.Request{
		SoftConditions: []retrieval.SoftCondition{ambience("安静")},
	})
	if err != nil {
		t.Fatalf("a soft-only search must be served, got %v", err)
	}
	if embedding.calls != 1 {
		t.Fatalf("embedding called %d times, want 1", embedding.calls)
	}
	if len(got.Candidates) == 0 {
		t.Fatal("want candidates from the soft-only recall")
	}
}

// A request with neither text, filter, nor soft condition is still refused: the
// corpus in prior order is not an answer to anything.
func TestAnEmptyRequestIsStillRefused(t *testing.T) {
	embedding := &stubEmbedding{vector: []float32{1, 0, 0}}
	service, _ := softOnlyService(t, embedding)

	if _, err := service.Search(context.Background(), retrieval.Request{}); err == nil {
		t.Fatal("an empty request must be refused")
	}
}

func assertNoSoftInFilter(t *testing.T, filter search.RestaurantFilter, values ...string) {
	t.Helper()
	haystack := append([]string{filter.Borough, filter.Neighborhood}, filter.Cuisines...)
	for _, value := range values {
		for _, field := range haystack {
			if strings.Contains(field, value) {
				t.Fatalf("soft condition %q leaked into the hard filter %+v", value, filter)
			}
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
