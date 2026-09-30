package curate

import (
	"sort"
	"strings"
	"unicode"
)

// Review topics. These are the canonical buckets the PRD names; a review is
// tagged with the ones its text actually discusses.
const (
	TopicFood          = "food"
	TopicService       = "service"
	TopicAmbience      = "ambience"
	TopicValue         = "value"
	TopicWait          = "wait"
	TopicKidFriendly   = "kid_friendly"
	TopicGroupFriendly = "group_friendly"
)

// TopicKeywords maps a topic to the phrases that indicate it.
//
// Matching is substring-based on a lowercased, whitespace-collapsed text rather
// than word-based: review text is messy ("staff were SO friendly", "took forever
// to get a table"), and a word-boundary tokenizer would miss most of the
// phrasings people actually write. Substring matching over an explicit
// vocabulary is the honest tradeoff here - it over-matches a little, but it is
// reproducible and explainable, which is what the summary documents need.
//
// The vocabulary is deliberately small and reviewable. An automatic topic model
// would produce topics nobody can audit, and a topic a reader cannot predict is
// a topic that will not be trusted in a citation.
var TopicKeywords = map[string][]string{
	TopicFood: {
		"food", "dish", "dishes", "cuisine", "menu", "pizza", "pasta", "sushi",
		"burger", "ramen", "taco", "noodle", "steak", "sandwich", "salad", "soup",
		"chicken", "fish", "rice", "bread", "sauce", "flavor", "flavour", "taste",
		"meal", "meals", "quality", "drink", "drinks",
		"delicious", "bland", "portions", "portion", "fresh", "cooked",
	},
	TopicService: {
		"service", "server", "waiter", "waitress", "staff", "host", "hostess",
		"friendly", "rude", "attentive", "courteous", "helpful", "professional",
		"manager", "cashier", "order", "ordered", "refill", "tip", "tips",
		"owner", "nice", "kind", "pleasant",
	},
	TopicAmbience: {
		"ambiance", "ambience", "atmosphere", "decor", "music", "loud", "quiet",
		"cozy", "cosy", "dirty", "clean", "space", "spacious", "crowded", "neat",
		"lighting", "vibe", "seating", "patio", "noise",
	},
	TopicValue: {
		"price", "prices", "priced", "value", "cheap", "expensive", "affordable",
		"overpriced", "worth", "bill", "check", "dollars", "budget",
		// "$12", "priced at 30" - a price is often written with no value word
		// at all, so the currency symbol counts as one. It only matches when
		// an amount follows.
		"$",
	},
	TopicWait: {
		"wait", "waiting", "waited", "line", "lines", "queue", "queued", "delay",
		"delayed", "slow", "quick", "fast", "hours", "long", "forever",
	},
	TopicKidFriendly: {
		"kids", "kid", "children", "child", "toddler", "baby", "babies",
		"family-friendly", "family friendly", "playground", "highchair",
	},
	TopicGroupFriendly: {
		"group", "groups", "party", "celebration", "birthday", "anniversary",
		"gatherings", "reunion", "date night",
	},
}

// TopicLabels are the human-readable Chinese names used in the generated
// summary documents. The vocabulary keys stay English because they are what the
// database and the retrieval filters use.
var TopicLabels = map[string]string{
	TopicFood:          "菜品",
	TopicService:       "服务",
	TopicAmbience:      "环境氛围",
	TopicValue:         "性价比",
	TopicWait:          "等待时间",
	TopicKidFriendly:   "亲子友好",
	TopicGroupFriendly: "聚餐友好",
}

// CanonicalTopics lists every topic in a stable order.
func CanonicalTopics() []string {
	out := make([]string, 0, len(TopicKeywords))
	for topic := range TopicKeywords {
		out = append(out, topic)
	}
	sort.Strings(out)
	return out
}

// TopicLabel returns the display name for a topic, falling back to the key so
// an unexpected topic still renders as something readable.
func TopicLabel(topic string) string {
	if label, ok := TopicLabels[topic]; ok {
		return label
	}
	return topic
}

// ClassifyTopics returns the topics a review discusses, sorted.
//
// A review with no recognised topic yields an empty slice rather than a
// fallback bucket: "no topic" and "some topic" are different facts, and forcing
// every review into a bucket would invent a summary for reviews that say
// nothing about any of them.
func ClassifyTopics(text string) []string {
	normalized := normalizeForMatch(text)
	if normalized == "" {
		return nil
	}
	var topics []string
	for topic, keywords := range TopicKeywords {
		for _, keyword := range keywords {
			if containsPhrase(normalized, keyword) {
				topics = append(topics, topic)
				break
			}
		}
	}
	sort.Strings(topics)
	return topics
}

// normalizeForMatch lowercases and collapses whitespace so a phrase spanning a
// line break still matches.
func normalizeForMatch(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	space := false
	for _, r := range strings.ToLower(text) {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

// containsPhrase reports whether the normalized text contains the phrase as a
// whole word or a whole multi-word phrase.
//
// Word boundaries are enforced, and they turned out to be necessary rather than
// merely tidy: measured against real corpus text, plain substring matching made
// "rice" match inside "price" and "cost" match inside "customers". Both put a
// review about price or about staff into the food or value bucket, which is
// exactly the kind of quiet mislabelling that makes a topic summary
// untrustworthy.
//
// Multi-word entries such as "family friendly" still match across whitespace
// because the text has already been collapsed to single spaces.
func containsPhrase(haystack, needle string) bool {
	for offset := 0; ; {
		index := strings.Index(haystack[offset:], needle)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(needle)
		// A currency symbol is punctuation, not a word: "$5" is a price, so
		// the trailing-digit rule replaces the word-boundary check for it.
		if needle == "$" {
			if followedByDigit(haystack, end) {
				return true
			}
			offset = start + 1
			continue
		}
		if !isWordBoundary(haystack, start, end) {
			offset = start + 1
			continue
		}
		return true
	}
}

// isWordBoundary reports whether the match at [start,end) is not glued to
// another word character.
func isWordBoundary(haystack string, start, end int) bool {
	if start > 0 && isWordByte(haystack[start-1]) {
		return false
	}
	if end < len(haystack) && isWordByte(haystack[end]) {
		return false
	}
	return true
}

// followedByDigit reports whether a price marker is followed by an amount.
//
// The "$" entry in the value vocabulary would otherwise match a bare currency
// symbol with no number attached. Requiring a digit keeps "$5" a price remark
// while a stray "$" stays unclassified.
func followedByDigit(haystack string, end int) bool {
	return end < len(haystack) && haystack[end] >= '0' && haystack[end] <= '9'
}

// isWordByte reports whether b is part of a word for boundary purposes.
func isWordByte(b byte) bool {
	return b == '_' ||
		(b >= '0' && b <= '9') ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z')
}
