package knowledge

import (
	"sort"
	"strings"
	"unicode"

	"github.com/zed1995/platepilot/shared/domain/review"
)

// stopWords are dropped before counting. They are the words that dominate any
// English review corpus and carry no information about what a diner liked:
// counting them would make every restaurant's keywords the same five words.
var stopWords = map[string]struct{}{
	"a": {}, "about": {}, "after": {}, "all": {}, "also": {}, "am": {}, "an": {},
	"and": {}, "any": {}, "are": {}, "as": {}, "at": {}, "be": {}, "been": {},
	"but": {}, "by": {}, "can": {}, "come": {}, "could": {}, "did": {}, "do": {},
	"does": {}, "for": {}, "from": {}, "get": {}, "go": {}, "going": {},
	"good": {}, "great": {}, "had": {}, "has": {}, "have": {}, "he": {},
	"her": {}, "here": {}, "him": {}, "his": {}, "how": {}, "i": {}, "if": {},
	"in": {}, "into": {}, "is": {}, "it": {}, "its": {}, "just": {}, "like": {},
	"me": {}, "more": {}, "most": {}, "my": {}, "no": {}, "not": {}, "of": {},
	"on": {}, "one": {}, "only": {}, "or": {}, "other": {}, "our": {}, "out": {},
	"over": {}, "place": {}, "really": {}, "s": {}, "she": {}, "so": {},
	"some": {}, "than": {}, "that": {}, "the": {}, "their": {}, "them": {},
	"then": {}, "there": {}, "they": {}, "this": {}, "to": {}, "too": {},
	"up": {}, "us": {}, "very": {}, "was": {}, "we": {}, "were": {}, "what": {},
	"when": {}, "where": {}, "which": {}, "while": {}, "who": {}, "will": {},
	"with": {}, "would": {}, "you": {}, "your": {},
}

// minKeywordLength keeps one- and two-letter fragments out. Most are noise from
// the corpus's casual spelling rather than words anybody means.
const minKeywordLength = 3

// topKeywords returns the most frequent significant words across reviews.
//
// Ties break alphabetically so the result is stable: two words with the same
// count must not swap places between runs, or every re-run would change the
// document text and churn a version.
func topKeywords(reviews []review.Review, limit int) []string {
	if limit <= 0 {
		return nil
	}
	counts := make(map[string]int)
	for _, r := range reviews {
		for _, word := range significantWords(r.Text) {
			counts[word]++
		}
	}
	if len(counts) == 0 {
		return nil
	}

	type scored struct {
		word  string
		count int
	}
	scoredWords := make([]scored, 0, len(counts))
	for word, count := range counts {
		scoredWords = append(scoredWords, scored{word, count})
	}
	sort.Slice(scoredWords, func(i, j int) bool {
		if scoredWords[i].count != scoredWords[j].count {
			return scoredWords[i].count > scoredWords[j].count
		}
		return scoredWords[i].word < scoredWords[j].word
	})
	if len(scoredWords) > limit {
		scoredWords = scoredWords[:limit]
	}

	out := make([]string, 0, len(scoredWords))
	for _, item := range scoredWords {
		out = append(out, item.word)
	}
	return out
}

// significantWords lowercases text and returns its content words.
func significantWords(text string) []string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if len([]rune(field)) < minKeywordLength {
			continue
		}
		if _, skip := stopWords[field]; skip {
			continue
		}
		out = append(out, field)
	}
	return out
}
