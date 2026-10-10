package knowledge

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/domain/restaurant"
	"github.com/zed1995/platepilot/shared/domain/review"

	"github.com/zed1995/platepilot/data-pipeline/internal/pipeline/curate"
)

// The two shipped digest generators. The version string is the prompt version
// and the generated_by value at once: it is what the cache key, the document
// metadata, and the config all name, so a switch between them is always an
// explicit regeneration rather than a silent drift.
const (
	// DigestRulesVersion is the zero-model baseline: a fixed template over the
	// topic rollups. It exists so the eval can separate the gain of the 1:1
	// architecture from the gain of model comprehension.
	DigestRulesVersion = "digest:rules:v1"
	// DigestLLMVersion is the model-backed generator's first version.
	DigestLLMVersion = "digest:llm:v1"
)

// DigestBundle is one restaurant's bounded, hashable input: the restaurant
// header, every topic rollup, and the stratified representative reviews.
//
// It is deliberately bounded (the reviews are already truncated and
// PII-scrubbed by SelectRepresentative) and deliberately canonical: the
// serialization below sorts everything it iterates, so the hash cannot depend
// on the order the caller happened to assemble the slices in.
type DigestBundle struct {
	Restaurant restaurant.Restaurant
	// Average is the mean rating over all text reviews, the same baseline the
	// topic sentiment is measured against.
	Average *float64
	// Topics holds every per-topic rollup, already sorted by topic name.
	Topics []TopicStats
	// Reviews holds the selected representative reviews.
	Reviews []review.Review
	// TextReviewCount is how many text reviews the rollups were computed from.
	TextReviewCount int
}

// AssembleDigestBundle builds the digest input from a restaurant and its text
// reviews. It returns false when the restaurant has no usable review material:
// without review text there is nothing to comprehend, and the stage skips the
// restaurant instead of manufacturing a digest from master data alone.
func AssembleDigestBundle(r restaurant.Restaurant, textReviews []review.Review) (DigestBundle, bool) {
	selected := SelectRepresentative(textReviews, int64(len(textReviews)))
	if len(selected) == 0 {
		return DigestBundle{}, false
	}
	var average *float64
	if len(textReviews) > 0 {
		var sum float64
		for _, item := range textReviews {
			sum += float64(item.Rating)
		}
		mean := sum / float64(len(textReviews))
		average = &mean
	}
	return DigestBundle{
		Restaurant:      r,
		Average:         average,
		Topics:          SummarizeTopics(textReviews, average),
		Reviews:         selected,
		TextReviewCount: len(textReviews),
	}, true
}

// SerializeDigestBundle renders the bundle as a fixed-template byte stream.
//
// The same rendering feeds the bundle hash and the LLM prompt, so what the
// model saw is exactly what the hash committed to. Every iteration is over a
// sorted copy and every number has a fixed format: the same data always yields
// byte-identical output regardless of map or slice order upstream.
func SerializeDigestBundle(bundle DigestBundle) string {
	r := bundle.Restaurant
	var w strings.Builder
	fmt.Fprintf(&w, "Restaurant: %s\n", r.Name)
	if cuisine := primaryCuisine(r); cuisine != "" {
		fmt.Fprintf(&w, "Primary cuisine: %s\n", cuisine)
	}
	if borough := strings.TrimSpace(r.BoroughGuess); borough != "" {
		fmt.Fprintf(&w, "Borough: %s\n", borough)
	}
	if level := priceLevelValue(r); level != "" {
		fmt.Fprintf(&w, "Price level: %s\n", level)
	}
	if bundle.Average != nil {
		fmt.Fprintf(&w, "Average sample rating: %.2f (sample size: %d text reviews)\n",
			*bundle.Average, bundle.TextReviewCount)
	}

	topics := append([]TopicStats(nil), bundle.Topics...)
	sort.Slice(topics, func(i, j int) bool { return topics[i].Topic < topics[j].Topic })
	for _, s := range topics {
		fmt.Fprintf(&w, "Topic: %s | reviews %d | avg %.2f | positive ratio %.2f | relative sentiment %+.2f | keywords %s\n",
			s.Topic, s.Count, s.AverageRating, s.PositiveRatio, s.Sentiment,
			strings.Join(s.Keywords, ", "))
	}

	reviews := append([]review.Review(nil), bundle.Reviews...)
	sort.Slice(reviews, func(i, j int) bool { return reviews[i].ID < reviews[j].ID })
	for _, item := range reviews {
		fmt.Fprintf(&w, "Review #%d | %d stars | %s | %s\n",
			item.ID, item.Rating, item.ReviewedAt.Format("2006-01-02"),
			truncateRunes(curate.NormalizeText(item.Text), representativeSampleText))
	}
	return strings.TrimRight(w.String(), "\n")
}

// BundleHash is the cache anchor of one input bundle: the same material always
// hashes the same, and any change to the header, rollups, or quoted reviews
// produces a new hash and therefore a new digest.
//
// The serialization is flattened through the review normalizer before hashing,
// so incidental whitespace cannot churn a version either.
func BundleHash(bundle DigestBundle) string {
	return curate.HashNormalized(curate.NormalizeText(SerializeDigestBundle(bundle)))
}

// BuildRulesDigest renders the digest:rules:v1 baseline: a fixed English
// template over the topic rollups and keywords, with no model involved.
//
// It is a pure function with the same byte-stability contract as BuildProfile:
// the same bundle always yields the same bytes, so an unchanged re-run is a
// cache hit rather than a new document version.
func BuildRulesDigest(bundle DigestBundle) string {
	r := bundle.Restaurant
	var w strings.Builder
	fmt.Fprintf(&w, "%s review comprehension (offline rule statistics over %d text reviews).\n",
		r.Name, bundle.TextReviewCount)

	var header []string
	if cuisine := primaryCuisine(r); cuisine != "" {
		header = append(header, "cuisine: "+cuisine)
	}
	if borough := strings.TrimSpace(r.BoroughGuess); borough != "" {
		header = append(header, "borough: "+borough)
	}
	if level := priceLevelValue(r); level != "" {
		header = append(header, "price: "+level)
	}
	if len(header) > 0 {
		fmt.Fprintf(&w, "%s.\n", strings.Join(header, ", "))
	}

	if bundle.Average != nil {
		positive := 0
		for _, item := range bundle.Reviews {
			if item.Rating >= 4 {
				positive++
			}
		}
		fmt.Fprintf(&w, "Average sample rating %.1f stars; positive (4-5 stars) share among representative reviews is %.0f%%.\n",
			*bundle.Average, float64(positive)/float64(len(bundle.Reviews))*100)
	}

	topics := append([]TopicStats(nil), bundle.Topics...)
	sort.Slice(topics, func(i, j int) bool { return topics[i].Topic < topics[j].Topic })
	for _, s := range topics {
		sentimentText := "in line with the overall restaurant"
		if direction := rulesSentimentText(s.Sentiment); direction != "" {
			sentimentText = "relatively " + direction
		}
		keywords := "no notable keywords"
		if len(s.Keywords) > 0 {
			keywords = strings.Join(s.Keywords, ", ")
		}
		fmt.Fprintf(&w, "%s: %d reviews, average %.1f stars, positive ratio %.0f%%, %s; keywords: %s.\n",
			s.Topic, s.Count, s.AverageRating, s.PositiveRatio*100,
			sentimentText, keywords)
	}
	w.WriteString("This is a rule-based statistics text used only for retrieval ranking, not as citation evidence.")
	return w.String()
}

// rulesSentimentText describes a topic offset in English for the rules digest.
// The shared describeSentiment helper returns Chinese, so the rules baseline
// keeps its own wording rather than pulling Chinese into an English document.
func rulesSentimentText(sentiment float64) string {
	switch {
	case sentiment > 0.15:
		return "better reviewed"
	case sentiment < -0.15:
		return "lower reviewed"
	default:
		return ""
	}
}

// Digest output bounds. English prose runs roughly six runes per word, so
// the prompt's 40-100 word range spans about 240-600 runes; the ceiling
// sits above that with headroom so an answer is only rejected when it is
// genuinely out of contract. The floor only catches a truncated or empty
// reply — it must stay below the rules baseline (a statistics text of ~500+
// runes even on a small bundle), which is validated by the same gate.
const (
	minDigestRunes = 150
	maxDigestRunes = 800
)

// The code-enforced half of the output contract (the prompt-level rules, like
// "write only from the bundle", are deliberately not checked here — see the
// design doc, §3.2). Patterns mirror the chat service's injection scanner: a
// digest feeds the embedding index, so contact details, links, and
// instruction-shaped text must never enter it even though the text itself is
// never shown to a user.
var (
	digestURLPattern = regexp.MustCompile(`(?i)(https?://|www\.)\S+`)
	digestEmailPatt  = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	digestPhonePatt  = regexp.MustCompile(
		`(\+?\d{1,3}[-.\s]\d{3}[-.\s]\d{4})|(1[3-9]\d{9})`)
	// The same narrow instruction-override set the answer node scans for: a
	// broad match would flag ordinary review phrasing and train operators to
	// ignore the rejection.
	digestInstructionPattern = regexp.MustCompile(
		`(?i)(忽略[^\n]{0,6}指令|无视[^\n]{0,6}指令|不要(再)?(遵守|听取)[^\n]{0,6}(指令|规则)|` +
			`ignore\s+(all\s+)?(the\s+)?(previous|prior|above|preceding|earlier)\s+(instruction|prompt|rule)s?|` +
			`disregard\s+(all\s+)?(the\s+)?(previous|prior|above|preceding)\s+(instruction|prompt|rule)s?|` +
			`forget\s+(all\s+)?(the\s+)?(previous|prior|above)\s+(instruction|prompt|rule)s?)`)
)

// ValidateDigestOutput enforces the code-level output contract on a generated
// digest before it may be written. A violation is a hard error: the text is
// dropped, the restaurant is reported failed, and nothing reaches the store.
func ValidateDigestOutput(text string) error {
	trimmed := strings.TrimSpace(text)
	var problems []string
	if n := utf8.RuneCountInString(trimmed); n < minDigestRunes {
		problems = append(problems, fmt.Sprintf("too short (%d runes, want >= %d)", n, minDigestRunes))
	} else if n > maxDigestRunes {
		problems = append(problems, fmt.Sprintf("too long (%d runes, want <= %d)", n, maxDigestRunes))
	}
	for _, check := range []struct {
		name    string
		pattern *regexp.Regexp
	}{
		{"url", digestURLPattern},
		{"email", digestEmailPatt},
		{"phone", digestPhonePatt},
		{"instruction", digestInstructionPattern},
	} {
		if match := check.pattern.FindString(trimmed); match != "" {
			// The matched excerpt is quoted because it is model output, not
			// user content or a secret, and an operator needs to see what
			// tripped the gate.
			problems = append(problems, fmt.Sprintf("%s pattern matched %q", check.name, match))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return errs.Newf(errs.CodeValidationFailed, "digest output rejected: %s", strings.Join(problems, "; "))
}

// DigestLLMOutput is the JSON contract the model answers with.
type DigestLLMOutput struct {
	Content         string  `json:"content"`
	SourceReviewIDs []int64 `json:"source_review_ids"`
}

// ParseDigestOutput decodes the model's JSON answer, validates the digest body
// with ValidateDigestOutput, and verifies that every cited review id is one of
// the review ids offered in the bundle. Returned ids are deduplicated and
// sorted ascending so the stored provenance is deterministic.
//
// A cited id outside the offered set is a hallucination, and the whole answer
// is rejected rather than storing a broken link: provenance pointing at a
// review the model never saw is worse than no provenance.
func ParseDigestOutput(raw string, allowedIDs []int64) (string, []int64, error) {
	var out DigestLLMOutput
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		return "", nil, errs.Newf(errs.CodeValidationFailed,
			"digest output rejected: output must be a JSON object: %v", err)
	}
	if decoder.More() {
		return "", nil, errs.New(errs.CodeValidationFailed,
			"digest output rejected: exactly one JSON object expected")
	}
	if err := ValidateDigestOutput(out.Content); err != nil {
		return "", nil, err
	}
	if len(out.SourceReviewIDs) == 0 {
		return "", nil, errs.New(errs.CodeValidationFailed,
			"digest output rejected: source_review_ids must not be empty")
	}

	allowed := make(map[int64]struct{}, len(allowedIDs))
	for _, id := range allowedIDs {
		allowed[id] = struct{}{}
	}
	seen := make(map[int64]struct{}, len(out.SourceReviewIDs))
	cited := make([]int64, 0, len(out.SourceReviewIDs))
	for _, id := range out.SourceReviewIDs {
		if _, ok := allowed[id]; !ok {
			return "", nil, errs.Newf(errs.CodeValidationFailed,
				"digest output rejected: source_review_ids contains %d, which was not offered in the input bundle", id)
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		cited = append(cited, id)
	}
	sort.Slice(cited, func(i, j int) bool { return cited[i] < cited[j] })
	return strings.TrimSpace(out.Content), cited, nil
}

// BundleReviewIDs returns the representative review ids carried in a bundle in
// ascending order. It is the set the rules digest adopts wholesale and the
// allowed set for the LLM output contract.
func BundleReviewIDs(bundle DigestBundle) []int64 {
	ids := make([]int64, 0, len(bundle.Reviews))
	for _, item := range bundle.Reviews {
		ids = append(ids, item.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// BuildDigestDocument packages a validated digest text as the single
// restaurant-scope document the review channel recalls.
//
// citable=false is metadata, not decoration: the digest lives in
// ScopeRestaurant so it is physically outside every evidence read path, and
// the flag records that intent for anyone inspecting the row alone. The
// content hash follows the shared document rule, so the versioning and
// activation semantics are exactly the profile path's.
func BuildDigestDocument(
	r restaurant.Restaurant,
	bundle DigestBundle,
	bundleHash string,
	content string,
	sourceReviewIDs []int64,
	promptVersion string,
	modelID string,
	opts ProfileOptions,
) evidence.KnowledgeDocument {
	source := opts.Source
	if source == "" {
		source = restaurant.SourceGoogleLocal2021
	}
	generatedAt := opts.GeneratedAt
	if generatedAt.IsZero() {
		generatedAt = r.ObservedAt
	}

	metadata := map[string]any{
		"source":             source,
		"curation_version":   opts.CurationVersion,
		"generated_at":       generatedAt.UTC().Format(time.RFC3339),
		"citable":            false,
		"bundle_hash":        bundleHash,
		"generated_by":       promptVersion,
		"prompt_version":     promptVersion,
		"model_id":           modelID,
		"input_review_count": bundle.TextReviewCount,
	}
	if borough := strings.TrimSpace(r.BoroughGuess); borough != "" {
		metadata["borough"] = borough
	}

	doc := evidence.KnowledgeDocument{
		RestaurantID: r.ID,
		Scope:        evidence.ScopeRestaurant,
		DocType:      evidence.DocTypeRestaurantReviewDigest,
		Title:        r.Name + " review digest",
		Content:      content,
		ContentHash: ContentHash(evidence.ScopeRestaurant,
			evidence.DocTypeRestaurantReviewDigest, r.ID, content),
		Metadata:   metadata,
		SnapshotAt: r.ObservedAt,
		IsActive:   false, // activated by the embed stage's activation page, like every document.
		Version:    1,
	}
	if r.SourceRecordID != "" {
		doc.SourceRecordIDs = []string{r.SourceRecordID}
	}
	if len(sourceReviewIDs) > 0 {
		doc.SourceReviewIDs = append([]int64(nil), sourceReviewIDs...)
	}
	return doc
}
