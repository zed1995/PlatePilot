package review

import "sort"

// Review topics. These are the canonical buckets the PRD names; a review is
// tagged with the ones its text actually discusses.
//
// The constants live in the domain layer rather than in the data pipeline that
// first defined them because two independent consumers have to agree: the
// pipeline tags reviews with these keys, and the chat service maps a user's soft
// condition ("安静") onto one of them. A drift between the two does not fail
// loudly — it filters on a topic that exists on one side only, and the visible
// symptom is an empty result that looks exactly like "the corpus has nothing
// like that". One definition, imported by both, removes the failure mode
// instead of testing for it.
const (
	TopicFood          = "food"
	TopicService       = "service"
	TopicAmbience      = "ambience"
	TopicValue         = "value"
	TopicWait          = "wait"
	TopicKidFriendly   = "kid_friendly"
	TopicGroupFriendly = "group_friendly"
)

// TopicLabels maps a topic to its human-readable Chinese name. The keys stay
// English because they are what the stored documents and the recall filters
// carry; only the presentation is localised.
var TopicLabels = map[string]string{
	TopicFood:          "菜品",
	TopicService:       "服务",
	TopicAmbience:      "环境氛围",
	TopicValue:         "性价比",
	TopicWait:          "等待时间",
	TopicKidFriendly:   "亲子友好",
	TopicGroupFriendly: "聚餐友好",
}

// CanonicalTopics lists every topic in a stable, sorted order.
//
// The order is fixed so that anything iterating topics — a prompt, a report, a
// test asserting set equality — produces the same output every run.
func CanonicalTopics() []string {
	out := make([]string, 0, len(TopicLabels))
	for topic := range TopicLabels {
		out = append(out, topic)
	}
	sort.Strings(out)
	return out
}

// KnownTopic reports whether topic is one of the canonical review topics.
//
// Callers use it to reject a topic before it reaches a query, because a filter
// naming a topic that does not exist matches nothing and is indistinguishable
// from a topic that simply has no reviews.
func KnownTopic(topic string) bool {
	_, ok := TopicLabels[topic]
	return ok
}

// TopicLabel returns the display name for a topic, falling back to the key so
// an unexpected topic still renders as something readable.
func TopicLabel(topic string) string {
	if label, ok := TopicLabels[topic]; ok {
		return label
	}
	return topic
}
