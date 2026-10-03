package knowledge

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zed1995/platepilot/data-pipeline/internal/pipeline/curate"
	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/domain/restaurant"
	"github.com/zed1995/platepilot/shared/domain/review"
)

// RulesVersion identifies the deterministic summary rules. It is recorded on
// every summary so a later run that changes the wording can be told apart from
// one that merely re-derived the same numbers.
const RulesVersion = "rules:v1"

// TopicStats is the aggregate for one (restaurant, topic) pair.
type TopicStats struct {
	Topic         string
	Count         int
	AverageRating float64
	PositiveRatio float64
	// Sentiment is the topic's rating offset from the restaurant's own average,
	// not an absolute score. "Service is worse than the food here" is a
	// statement about this restaurant; "service scores 3.1" is not comparable
	// across restaurants.
	Sentiment float64
	// Keywords are the most frequent significant words in this topic's reviews.
	Keywords []string
	// Sample holds the reviews the numbers came from, so callers can quote them.
	Sample []review.Review
}

// SummarizeTopics aggregates reviews into per-topic rollups.
//
// restaurantAverage is the mean over all the restaurant's rated reviews; when
// it is nil the sentiment is reported as zero rather than against an invented
// baseline.
func SummarizeTopics(reviews []review.Review, restaurantAverage *float64) []TopicStats {
	grouped := make(map[string][]review.Review)
	for _, r := range reviews {
		for _, topic := range r.TopicTags {
			grouped[topic] = append(grouped[topic], r)
		}
	}
	if len(grouped) == 0 {
		return nil
	}

	out := make([]TopicStats, 0, len(grouped))
	for topic, items := range grouped {
		stats := TopicStats{Topic: topic, Count: len(items), Sample: items}
		var ratingSum float64
		positive := 0
		for _, r := range items {
			ratingSum += float64(r.Rating)
			if r.Rating >= 4 {
				positive++
			}
		}
		stats.AverageRating = ratingSum / float64(len(items))
		stats.PositiveRatio = float64(positive) / float64(len(items))
		if restaurantAverage != nil {
			stats.Sentiment = stats.AverageRating - *restaurantAverage
		}
		stats.Keywords = topKeywords(items, 3)
		out = append(out, stats)
	}
	// A stable order keeps the generated documents byte-identical across runs.
	sort.Slice(out, func(i, j int) bool { return out[i].Topic < out[j].Topic })
	return out
}

// BuildTopicSummaries turns topic stats into review.Summary rows.
//
// The mean rating is written into the summary text rather than kept in a column:
// review_summaries stores sentiment, positive_ratio, and evidence_count, and
// adding a column for a number the text already carries would make the two able
// to disagree. parseTopicAverage reads it back for the document.
func BuildTopicSummaries(restaurantID int64, stats []TopicStats, validFrom time.Time, now time.Time) []review.Summary {
	out := make([]review.Summary, 0, len(stats))
	for _, s := range stats {
		out = append(out, review.Summary{
			RestaurantID:  restaurantID,
			Topic:         s.Topic,
			Sentiment:     s.Sentiment,
			PositiveRatio: s.PositiveRatio,
			Summary:       renderTopicSummary(s),
			EvidenceCount: s.Count,
			ValidFrom:     validFrom,
			GeneratedBy:   RulesVersion,
			GeneratedAt:   now,
		})
	}
	return out
}

// BuildSummaryDocuments renders one restaurant_review_summary document per
// topic.
//
// One document per topic rather than one combined document: a question about
// waiting times should recall the waiting evidence, not the paragraph that also
// mentions the food. Splitting by topic is what makes a topic filter in M3
// mean anything.
func BuildSummaryDocuments(
	r restaurant.Restaurant,
	summaries []review.Summary,
	opts ProfileOptions,
) []evidence.KnowledgeDocument {
	docs := make([]evidence.KnowledgeDocument, 0, len(summaries))
	for _, summary := range summaries {
		content := renderSummaryDocument(r, summary)
		docs = append(docs, newEvidenceDocument(r, evidence.DocTypeRestaurantReviewSummary, content,
			map[string]any{
				"topic":          summary.Topic,
				"topic_label":    curate.TopicLabel(summary.Topic),
				"sentiment":      summary.Sentiment,
				"positive_ratio": summary.PositiveRatio,
				"evidence_count": summary.EvidenceCount,
				"generated_by":   summary.GeneratedBy,
			}, opts))
	}
	return docs
}

// renderTopicSummary is the stored one-line rollup.
func renderTopicSummary(s TopicStats) string {
	return fmt.Sprintf("%s：%d 条评论，平均 %.1f 星，正面占比 %.0f%%。%s",
		curate.TopicLabel(s.Topic), s.Count, s.AverageRating, s.PositiveRatio*100,
		keywordPhrase(s.Keywords))
}

// renderSummaryDocument is the citable document body.
func renderSummaryDocument(r restaurant.Restaurant, summary review.Summary) string {
	label := curate.TopicLabel(summary.Topic)

	// The sentiment wording states the direction and the size, not a score. A
	// number like "-0.4" in a user-facing sentence means nothing on its own.
	direction := describeSentiment(summary.Sentiment)

	var b strings.Builder
	fmt.Fprintf(&b, "%s的%s评论\n", r.Name, label)
	average, ok := parseTopicAverage(summary.Summary)
	ratingText := "评分未知"
	if ok {
		ratingText = renderAverage(average)
	}
	fmt.Fprintf(&b, "在 %d 条提到%s的评论中，平均评分 %s，正面（4–5 星）占比 %.0f%%。",
		summary.EvidenceCount, label, ratingText, summary.PositiveRatio*100)
	if direction != "" {
		fmt.Fprintf(&b, "该主题相对本店整体%s。", direction)
	}
	return b.String()
}

// parseTopicAverage reads the mean back out of the stored summary text.
//
// The row has no column for it, so the document and the row are rendered from
// the same string instead of from two numbers that could drift apart. A text
// that does not parse yields 0 and the document says "评分未知" rather than
// claiming a zero average, which would read as "every review was one star".
func parseTopicAverage(summary string) (float64, bool) {
	const marker = "平均 "
	index := strings.Index(summary, marker)
	if index < 0 {
		return 0, false
	}
	rest := summary[index+len(marker):]
	rest = strings.TrimPrefix(rest, "评分")
	rest = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), "平均"))
	end := strings.IndexAny(rest, " 星")
	if end <= 0 {
		return 0, false
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(rest[:end]), 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

// renderAverage formats the mean for the document.
func renderAverage(average float64) string {
	return fmt.Sprintf("%.1f 星", average)
}

// describeSentiment turns the offset into a phrase.
func describeSentiment(sentiment float64) string {
	switch {
	case sentiment > 0.15:
		return "更受好评"
	case sentiment < -0.15:
		return "评价偏低"
	default:
		return ""
	}
}

// keywordPhrase renders the frequent words, or a fixed fallback.
func keywordPhrase(keywords []string) string {
	if len(keywords) == 0 {
		return "暂无明显关键词。"
	}
	return "高频词：" + strings.Join(keywords, "、") + "。"
}
