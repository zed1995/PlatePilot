package curate

import (
	"sort"
	"strings"
	"testing"
)

func hasTopic(got []string, want string) bool {
	for _, topic := range got {
		if topic == want {
			return true
		}
	}
	return false
}

// The fixtures below are real review text sampled from the corpus, not invented
// sentences: the classifier has to work on how people actually write, which is
// messier than any vocabulary designed in advance suggests.
func TestClassifyTopicsOnRealCorpusText(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "price and food quality",
			text: "Best Halal in all New York. And no one can beat his price and the quality of the food. The owner was super nice and he already knew us every time we went there. Definitely a go to.",
			want: []string{TopicFood, TopicService, TopicValue},
		},
		{
			name: "cheap meal",
			text: " My favorite... Good meal and drink for just $5 on 14th street.",
			want: []string{TopicFood, TopicValue},
		},
		{
			name: "service and freshness",
			text: "The place was really neat and the customer service was great. There were so many flavors of ice cream and everything was still fresh. It was a pretty cold day today but it was very worth it.",
			want: []string{TopicFood, TopicAmbience, TopicService, TopicValue},
		},
		{
			name: "quick wait and friendly staff",
			text: "But other than that best costumer service ever and the wait is super quick. They make you feel like friends not just customers",
			want: []string{TopicService, TopicWait},
		},
		{
			name: "friendly employees only",
			text: " They're consistent and most of the employees are really friendly. No complaint.",
			want: []string{TopicService},
		},
		{
			name: "nothing recognisable",
			text: " Good going ruma and shanta",
			want: nil,
		},
		{
			name: "potatoes",
			text: " Best with the potatoes!",
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyTopics(tc.text)
			sort.Strings(tc.want)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("ClassifyTopics = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestClassifyTopicsIsCaseAndWhitespaceInsensitive(t *testing.T) {
	a := ClassifyTopics("The SERVICE was great")
	b := ClassifyTopics("  the   service\twas\nGREAT ")
	if strings.Join(a, ",") != strings.Join(b, ",") {
		t.Errorf("%v != %v", a, b)
	}
	if !hasTopic(a, TopicService) {
		t.Errorf("expected service, got %v", a)
	}
}

func TestClassifyTopicsReturnsNothingForEmptyText(t *testing.T) {
	for _, text := range []string{"", "   ", "\n\t "} {
		if got := ClassifyTopics(text); len(got) > 0 {
			t.Errorf("ClassifyTopics(%q) = %v, want none", text, got)
		}
	}
}

// A review with no recognised topic gets no bucket. Forcing one would invent a
// summary for a review that says nothing about any known topic.
func TestClassifyTopicsDoesNotInventTopics(t *testing.T) {
	got := ClassifyTopics("qwerty zxcvbn")
	if len(got) != 0 {
		t.Errorf("ClassifyTopics = %v, want none", got)
	}
}

func TestClassifyTopicsIsDeterministic(t *testing.T) {
	text := "Great food, slow service, good value, cozy atmosphere, great for kids"
	first := strings.Join(ClassifyTopics(text), ",")
	for i := 0; i < 20; i++ {
		if got := strings.Join(ClassifyTopics(text), ","); got != first {
			t.Fatalf("run %d = %q, want %q", i, got, first)
		}
	}
}

func TestCanonicalTopicsCoversEveryVocabulary(t *testing.T) {
	canonical := CanonicalTopics()
	if len(canonical) != len(TopicKeywords) {
		t.Errorf("CanonicalTopics has %d entries, vocabulary has %d", len(canonical), len(TopicKeywords))
	}
	if !sort.StringsAreSorted(canonical) {
		t.Errorf("CanonicalTopics is not sorted: %v", canonical)
	}
	// Every topic must have a display name, or a summary would render the raw
	// key to the user.
	for _, topic := range canonical {
		if TopicLabel(topic) == topic {
			t.Errorf("topic %q has no distinct display label", topic)
		}
	}
}

func TestTopicLabelFallsBackToKey(t *testing.T) {
	if got := TopicLabel("unheard_of"); got != "unheard_of" {
		t.Errorf("TopicLabel = %q, want the key back", got)
	}
}

// The false positives that forced word boundaries in the first place. Each of
// these is a real corpus phrasing that a plain substring match got wrong.
func TestClassifyTopicsRespectsWordBoundaries(t *testing.T) {
	cases := []struct {
		name      string
		text      string
		wantTopic string
		wantMatch bool
	}{
		{"price is not food", "great value for the price", TopicFood, false},
		{"customers is not cost", "they treat their customers well", TopicValue, false},
		{"rice is not price", "reasonable price", TopicFood, false},
		{"waiter is not wait", "the waiter was lovely", TopicWait, false},
		{"bare dollar sign is not a price", "the sign said $ but no amount", TopicValue, false},
		{"dollars word is a price", "ten dollars well spent", TopicValue, true},
		{"priced out", "priced out of my budget", TopicValue, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasTopic(ClassifyTopics(tc.text), tc.wantTopic); got != tc.wantMatch {
				t.Errorf("topic %q present = %v, want %v (text %q)",
					tc.wantTopic, got, tc.wantMatch, tc.text)
			}
		})
	}
}
