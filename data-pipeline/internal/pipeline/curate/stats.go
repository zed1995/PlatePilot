package curate

import (
	"time"

	"github.com/zed/platepilot/shared/domain/restaurant"
	"github.com/zed/platepilot/shared/domain/review"
)

// BuildReviewStats materialises a restaurant's review rollup. Fields owned by
// other stages are carried over from previous: source_review_count comes from
// Meta, and embedded_review_count is filled in by the embedding stage.
func BuildReviewStats(previous restaurant.ReviewStats, counts review.Counts, now time.Time) restaurant.ReviewStats {
	stats := previous
	stats.StoredReviewCount = int(counts.StoredCount)
	stats.TextReviewCount = int(counts.TextCount)
	stats.RepresentativeReviewCount = int(counts.RepresentativeCount)
	stats.LastReviewedAt = counts.LastReviewedAt
	stats.StatsUpdatedAt = now
	return stats
}

// ComputedRating projects a rollup onto restaurant.Rating so the computed
// average never overwrites the source average.
func ComputedRating(source restaurant.Rating, counts review.Counts) restaurant.Rating {
	out := source
	out.ComputedAvg = counts.ComputedAvg
	out.RatingCountForComputedAvg = int(counts.StoredCount)
	return out
}
