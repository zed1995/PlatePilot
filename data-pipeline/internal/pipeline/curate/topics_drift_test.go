package curate

import (
	"sort"
	"testing"

	"github.com/zed1995/platepilot/shared/domain/review"
)

// TestTopicVocabularyMatchesSharedDomain pins the keyword table's key set to
// the shared domain's canonical topics.
//
// The two sides of a soft-condition recall are the pipeline that tags reviews
// and the chat service that maps "安静" onto a topic. If the chat service names
// a topic the keyword table does not produce, the recall filters on something
// that matches nothing, and the visible result — an empty list — is exactly
// what a topic with no reviews looks like. A set-equality assertion here turns
// that invisible failure into a build error.
func TestTopicVocabularyMatchesSharedDomain(t *testing.T) {
	keywordKeys := CanonicalTopics()
	sharedKeys := review.CanonicalTopics()

	if len(keywordKeys) != len(sharedKeys) {
		t.Fatalf("keyword table has %d topics %v, shared domain has %d %v",
			len(keywordKeys), keywordKeys, len(sharedKeys), sharedKeys)
	}
	for i := range keywordKeys {
		if keywordKeys[i] != sharedKeys[i] {
			t.Fatalf("topic vocabularies differ at %d: pipeline %q, shared %q\n"+
				"    pipeline: %v\n    shared:   %v",
				i, keywordKeys[i], sharedKeys[i], keywordKeys, sharedKeys)
		}
	}

	// Every topic the summariser can label must be one the shared domain knows,
	// so a summary document never carries a topic the chat service cannot name.
	for _, topic := range sortedKeys(TopicLabels) {
		if !review.KnownTopic(topic) {
			t.Errorf("label table names topic %q which is not a shared canonical topic", topic)
		}
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
