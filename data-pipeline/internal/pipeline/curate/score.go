package curate

import (
	"math"
	"sort"
	"strings"

	"github.com/zed1995/platepilot/shared/domain/restaurant"
)

// Demo selection bounds from the PRD: the knowledge base should carry
// 2,000-5,000 high-coverage restaurants into the embedding phase.
const (
	MinDemoRestaurants     = 2000
	MaxDemoRestaurants     = 5000
	DefaultDemoRestaurants = 3000
)

// ScoreReason explains one component of a restaurant's knowledge score.
type ScoreReason struct {
	Factor string
	Points float64
}

// ScoreResult bundles a score with its human-readable breakdown so selection is
// explainable, not a black box.
type ScoreResult struct {
	Score   float64
	Reasons []ScoreReason
}

// KnowledgeScore scores how much knowledge the restaurant can support. Field
// coverage dominates; review volume only helps once there is real text.
func KnowledgeScore(r restaurant.Restaurant) ScoreResult {
	var reasons []ScoreReason
	add := func(factor string, points float64) {
		if points <= 0 {
			return
		}
		reasons = append(reasons, ScoreReason{Factor: factor, Points: points})
	}

	if strings.TrimSpace(r.Description) != "" {
		add("has_description", 1.5)
	}
	if strings.TrimSpace(r.Address) != "" {
		add("has_address", 0.5)
	}
	if r.Location != nil {
		add("has_location", 0.5)
	}
	if len(r.CuisineTags) > 0 {
		add("has_cuisine", 1.0)
	}
	if hasTrueAttribute(r.Attributes) {
		add("has_attributes", 0.5)
	}
	if len(r.Attributes.AtmosphereTags)+len(r.Attributes.PopularForTags) > 0 {
		add("has_atmosphere", 0.5)
	}
	if len(r.Attributes.ServiceOptionTags) > 0 {
		add("has_service_options", 0.5)
	}
	if r.Rating.SourceAvg != nil {
		add("has_rating", 0.5)
	}
	if r.ReviewStats.TextReviewCount > 0 {
		add("text_reviews", math.Log10(1+float64(r.ReviewStats.TextReviewCount))*1.5)
	}
	if r.SnapshotStatus == restaurant.StatusPermanentlyClosed {
		return ScoreResult{Score: -100, Reasons: []ScoreReason{{Factor: "permanently_closed", Points: -100}}}
	}

	total := 0.0
	for _, reason := range reasons {
		total += reason.Points
	}
	return ScoreResult{Score: total, Reasons: reasons}
}

// EligibleForDemo reports whether a restaurant may be selected for the demo.
func EligibleForDemo(r restaurant.Restaurant) bool {
	if r.SnapshotStatus == restaurant.StatusPermanentlyClosed {
		return false
	}
	return r.Location != nil
}

// SelectActiveForDemo returns the ids that should carry is_active_for_demo=true.
// Selection is deterministic: score descending, then source_record_id ascending.
func SelectActiveForDemo(all []restaurant.Restaurant, target int) []int64 {
	eligible := make([]restaurant.Restaurant, 0, len(all))
	for _, r := range all {
		if EligibleForDemo(r) {
			eligible = append(eligible, r)
		}
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		si := KnowledgeScore(eligible[i]).Score
		sj := KnowledgeScore(eligible[j]).Score
		if si != sj {
			return si > sj
		}
		return eligible[i].SourceRecordID < eligible[j].SourceRecordID
	})

	target = clampTarget(target)
	if target > len(eligible) {
		target = len(eligible)
	}
	out := make([]int64, 0, target)
	for _, r := range eligible[:target] {
		out = append(out, r.ID)
	}
	return out
}

func clampTarget(target int) int {
	if target <= 0 {
		target = DefaultDemoRestaurants
	}
	if target < MinDemoRestaurants {
		target = MinDemoRestaurants
	}
	if target > MaxDemoRestaurants {
		target = MaxDemoRestaurants
	}
	return target
}

func hasTrueAttribute(attrs restaurant.Attributes) bool {
	for _, value := range attrs.TriStates {
		if value == restaurant.TriStateTrue {
			return true
		}
	}
	return false
}
