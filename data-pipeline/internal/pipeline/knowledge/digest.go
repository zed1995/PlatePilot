package knowledge

import (
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
	fmt.Fprintf(&w, "店名：%s\n", r.Name)
	if cuisine := primaryCuisine(r); cuisine != "" {
		fmt.Fprintf(&w, "主菜系：%s\n", cuisine)
	}
	if borough := strings.TrimSpace(r.BoroughGuess); borough != "" {
		fmt.Fprintf(&w, "行政区：%s\n", borough)
	}
	if level := priceLevelValue(r); level != "" {
		fmt.Fprintf(&w, "价格等级：%s\n", level)
	}
	if bundle.Average != nil {
		fmt.Fprintf(&w, "样本均分：%.2f（样本量 %d 条文字评论）\n", *bundle.Average, bundle.TextReviewCount)
	}

	topics := append([]TopicStats(nil), bundle.Topics...)
	sort.Slice(topics, func(i, j int) bool { return topics[i].Topic < topics[j].Topic })
	for _, s := range topics {
		fmt.Fprintf(&w, "主题：%s | 评论数 %d | 均分 %.2f | 正面占比 %.2f | 相对情感 %+.2f | 高频词 %s\n",
			s.Topic, s.Count, s.AverageRating, s.PositiveRatio, s.Sentiment,
			strings.Join(s.Keywords, "、"))
	}

	reviews := append([]review.Review(nil), bundle.Reviews...)
	sort.Slice(reviews, func(i, j int) bool { return reviews[i].ID < reviews[j].ID })
	for _, item := range reviews {
		fmt.Fprintf(&w, "评论 #%d | %d 星 | %s | %s\n",
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

// BuildRulesDigest renders the digest:rules:v1 baseline: a fixed Chinese
// template over the topic rollups and keywords, with no model involved.
//
// It is a pure function with the same byte-stability contract as BuildProfile:
// the same bundle always yields the same bytes, so an unchanged re-run is a
// cache hit rather than a new document version.
func BuildRulesDigest(bundle DigestBundle) string {
	r := bundle.Restaurant
	var w strings.Builder
	fmt.Fprintf(&w, "%s的评论综合理解（基于 %d 条文字评论的离线规则统计）。\n", r.Name, bundle.TextReviewCount)

	var header []string
	if cuisine := primaryCuisine(r); cuisine != "" {
		header = append(header, "菜系："+cuisine)
	}
	if borough := strings.TrimSpace(r.BoroughGuess); borough != "" {
		header = append(header, "行政区："+borough)
	}
	if level := priceLevelValue(r); level != "" {
		header = append(header, "价格："+level)
	}
	if len(header) > 0 {
		fmt.Fprintf(&w, "%s。\n", strings.Join(header, "，"))
	}

	if bundle.Average != nil {
		positive := 0
		for _, item := range bundle.Reviews {
			if item.Rating >= 4 {
				positive++
			}
		}
		fmt.Fprintf(&w, "样本均分 %.1f 星，代表性评论中正面（4–5 星）占比 %.0f%%。\n",
			*bundle.Average, float64(positive)/float64(len(bundle.Reviews))*100)
	}

	topics := append([]TopicStats(nil), bundle.Topics...)
	sort.Slice(topics, func(i, j int) bool { return topics[i].Topic < topics[j].Topic })
	for _, s := range topics {
		direction := describeSentiment(s.Sentiment)
		sentimentText := "与本店整体持平"
		if direction != "" {
			sentimentText = "相对本店整体" + direction
		}
		keywords := "暂无明显关键词"
		if len(s.Keywords) > 0 {
			keywords = strings.Join(s.Keywords, "、")
		}
		fmt.Fprintf(&w, "%s方面：%d 条评论，平均 %.1f 星，正面占比 %.0f%%，%s；高频词：%s。\n",
			curate.TopicLabel(s.Topic), s.Count, s.AverageRating, s.PositiveRatio*100,
			sentimentText, keywords)
	}
	w.WriteString("本文为规则统计文本，仅用于检索排序，不作为引用依据。")
	return w.String()
}

// Digest output bounds. The LLM prompt asks for 300–600 字, and the ceiling is
// deliberately several times that so an answer is only rejected when it is
// genuinely out of contract; the floor catches a truncated or empty reply.
const (
	minDigestRunes = 50
	maxDigestRunes = 2000
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
		Title:        r.Name + " 的评论理解摘要",
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
	return doc
}
