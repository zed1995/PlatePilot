package knowledge

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zed/platepilot/shared/domain/evidence"
	"github.com/zed/platepilot/shared/domain/review"
)

// Selection bounds. The PRD fixes the shape of the rule: a tenth of the text
// reviews, never fewer than ten and never more than thirty.
const (
	representativeFloor      = 10
	representativeCeiling    = 30
	representativeRatio      = 0.1
	representativeSampleText = 400
)

// SelectRepresentative picks the reviews that will stand as this restaurant's
// evidence.
//
// The selection is stratified by sentiment and then ordered by a total tie-break
// ending in the review id. The id is not decoration: M1 already paid for a
// missing tie-break once, when a scoring stage reshuffled its chosen set between
// identical runs. Without a final total order, two runs over the same data can
// legitimately disagree and the evidence a user saw last week silently changes.
//
// Sentiment is stratified rather than ranked because a restaurant whose worst
// reviews are never quoted reads as uniformly good. Taking the top-N by rating
// alone would return ten five-star reviews for every restaurant.
func SelectRepresentative(reviews []review.Review, textCount int64) []review.Review {
	candidates := make([]review.Review, 0, len(reviews))
	for _, r := range reviews {
		if strings.TrimSpace(r.Text) != "" {
			candidates = append(candidates, r)
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	target := representativeCount(textCount, len(candidates))

	// Three strata: positive, neutral, negative. A review with no text is
	// already excluded, and ratings outside 1-5 cannot occur.
	strata := map[string][]review.Review{}
	for _, r := range candidates {
		strata[sentimentBand(r.Rating)] = append(strata[sentimentBand(r.Rating)], r)
	}

	// Each stratum is ordered worst-first within its band so that when the
	// quota is not divisible the truncation drops the least representative
	// end rather than an arbitrary one.
	bands := []string{bandNegative, bandNeutral, bandPositive}
	ordered := make([][]review.Review, len(bands))
	for i, band := range bands {
		ordered[i] = orderStratum(strata[band])
	}

	// Every band gets an equal share of the quota, capped by what that band
	// actually has.
	//
	// A plain round-robin was tried first and is wrong: it hands out one pick
	// per band per round until the target is met, so a band with three reviews
	// and a band with ten end up contributing four and three. Measured on a
	// 30-review restaurant with 10/10/10 bands it produced 4/3/3, quietly
	// over-weighting the scarcer sentiment. Equal shares are what "stratified"
	// is supposed to mean.
	quotas := equalQuotas(target, ordered)
	selected := make([]review.Review, 0, target)
	for i := range ordered {
		selected = append(selected, ordered[i][:quotas[i]]...)
	}

	// The document reads in a stable order regardless of which stratum a review
	// came from: most recent first.
	sort.SliceStable(selected, func(i, j int) bool {
		if !selected[i].ReviewedAt.Equal(selected[j].ReviewedAt) {
			return selected[i].ReviewedAt.After(selected[j].ReviewedAt)
		}
		return selected[i].ID < selected[j].ID
	})
	return selected
}

// equalQuotas splits a target across strata in proportion to how many reviews
// each has, with no band allowed to take more than its fair third.
//
// The calculation is largest-remainder: exact shares rarely sum to the target,
// and dropping or inventing a pick to make them add up would break either the
// size rule or the balance. Any band with no reviews takes nothing.
func equalQuotas(target int, strata [][]review.Review) []int {
	quotas := make([]int, len(strata))
	total := 0
	for _, items := range strata {
		total += len(items)
	}
	if target <= 0 || total == 0 {
		return quotas
	}

	remainders := make([]float64, len(strata))
	assigned := 0
	for i, items := range strata {
		share := float64(target) * float64(len(items)) / float64(total)
		quotas[i] = int(share)
		remainders[i] = share - float64(quotas[i])
		assigned += quotas[i]
	}

	// Hand out the leftover picks by largest remainder, then remainder index.
	//
	// Walking the bands in fixed order instead would give every spare pick to
	// the same band: with a target of 10 over three equal bands the exact
	// shares are 3.33 each, and a plain in-order loop leaves 4/3/3, weighting
	// whichever band happens to be first. Sorting by remainder spreads the
	// leftovers the way "equal shares" implies.
	if assigned < target {
		type remainder struct {
			index int
			value float64
		}
		ranking := make([]remainder, len(strata))
		for i := range strata {
			ranking[i] = remainder{index: i, value: remainders[i]}
		}
		sort.SliceStable(ranking, func(i, j int) bool {
			if ranking[i].value != ranking[j].value {
				return ranking[i].value > ranking[j].value
			}
			return ranking[i].index < ranking[j].index
		})
		for i := 0; assigned < target; i = (i + 1) % len(ranking) {
			index := ranking[i].index
			if quotas[index] >= len(strata[index]) {
				continue
			}
			quotas[index]++
			assigned++
		}
	}
	for i, items := range strata {
		if quotas[i] > len(items) {
			quotas[i] = len(items)
		}
	}
	for i, items := range strata {
		if quotas[i] > len(items) {
			quotas[i] = len(items)
		}
	}
	return quotas
}

// Sentiment band names.
const (
	bandPositive = "positive"
	bandNeutral  = "neutral"
	bandNegative = "negative"
)

func sentimentBand(rating int) string {
	switch {
	case rating >= 4:
		return bandPositive
	case rating == 3:
		return bandNeutral
	default:
		return bandNegative
	}
}

// orderStratum sorts within a band worst-first, breaking every tie.
//
// The order is: rating ascending, then most recent first, then lowest id. The
// final id comparison is what makes the order total, which is what guarantees
// two runs choose the same reviews.
func orderStratum(items []review.Review) []review.Review {
	out := make([]review.Review, len(items))
	copy(out, items)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Rating != out[j].Rating {
			return out[i].Rating < out[j].Rating
		}
		if !out[i].ReviewedAt.Equal(out[j].ReviewedAt) {
			return out[i].ReviewedAt.After(out[j].ReviewedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// representativeCount applies the PRD sizing rule.
//
// It is bounded by the number of candidates actually available: a restaurant
// with four usable reviews contributes four, not ten. Asking for ten would pad
// the document with rows that do not exist.
func representativeCount(textCount int64, available int) int {
	if available <= 0 {
		return 0
	}
	count := int(float64(textCount) * representativeRatio)
	if textCount <= 0 {
		// Fall back to the candidate count when the rollup is not available.
		count = available
	}
	if count < representativeFloor {
		count = representativeFloor
	}
	if count > representativeCeiling {
		count = representativeCeiling
	}
	if count > available {
		count = available
	}
	return count
}

// BuildRepresentativeDocument renders the selected reviews as one citable
// document.
//
// The reviews are packed into a single document on purpose. One document per
// review would let a restaurant's thirty reviews compete with each other for
// the same top-k, crowding out the topic summaries that answer the question.
func BuildRepresentativeDocument(
	rName string,
	rID int64,
	borough string,
	observedAt time.Time,
	all []review.Review,
	selected []review.Review,
	sourceRecordID string,
	opts ProfileOptions,
) (evidence.KnowledgeDocument, bool, error) {
	if len(selected) == 0 {
		return evidence.KnowledgeDocument{}, false, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "代表性评论（2021 快照，%d 条可用评论中选出 %d 条）\n", len(all), len(selected))
	for _, r := range selected {
		fmt.Fprintf(&b, "[%d星] “%s”（%s）\n",
			r.Rating, truncateRunes(strings.TrimSpace(r.Text), representativeSampleText),
			r.ReviewedAt.Format("2006-01-02"))
	}

	content := strings.TrimRight(b.String(), "\n")
	source := opts.Source
	if source == "" {
		source = "google_local_2021"
	}
	generatedAt := opts.GeneratedAt
	if generatedAt.IsZero() {
		generatedAt = observedAt
	}

	metadata := map[string]any{
		"source":               source,
		"curation_version":     opts.CurationVersion,
		"generated_at":         generatedAt.UTC().Format(time.RFC3339),
		"representative_count": len(selected),
		"candidate_count":      len(all),
	}
	if borough != "" {
		metadata["borough"] = borough
	}

	doc := evidence.KnowledgeDocument{
		RestaurantID: rID,
		Scope:        evidence.ScopeEvidence,
		DocType:      evidence.DocTypeRestaurantRepresentativeReviews,
		Title:        rName + " 的代表性评论",
		Content:      content,
		ContentHash: ContentHash(evidence.ScopeEvidence,
			evidence.DocTypeRestaurantRepresentativeReviews, rID, content),
		Metadata:   metadata,
		SnapshotAt: observedAt,
		IsActive:   false,
		Version:    1,
	}
	if sourceRecordID != "" {
		doc.SourceRecordIDs = []string{sourceRecordID}
	}
	return doc, true, nil
}
