package retrieval

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/domain/retrieval"
	"github.com/zed1995/platepilot/shared/domain/search"
	"github.com/zed1995/platepilot/shared/store"
)

// reviewOverread multiplies the review channel's depth, for the same reason
// the profile channel overreads: the page size decides how many correct
// answers fusion ever learns about, and a digest page that fills itself hides
// every restaurant whose口碑 another channel would have surfaced.
const reviewOverread = 10

// maxReviewDepth bounds that overread, on the same terms as the profile
// channel: every row returned is also a row read back one point lookup at a
// time to make it displayable.
const maxReviewDepth = 200

// reviewChannel runs the semantic recall over the offline review digests.
//
// It is the profile channel's sibling, not its variant: both read
// restaurant-scoped documents with the same embedding, one document per
// restaurant, so a hit is exactly one candidate with no aggregation and no
// per-document dedup. The difference is the corpus — what a place says about
// itself versus what its reviews say about it — and one gate the profile
// channel does not have: a digest similarity below the configured floor is a
// non-match, because a review corpus that "loosely" matches anything makes the
// channel a second prior instead of evidence.
//
// It never fails the search, for the same reasons the profile channel does
// not: the corpus may not have digests at all (the offline stage has not run),
// and a ranking missing its review half must look degraded, not broken.
func (s *Service) reviewChannel(
	ctx context.Context,
	query string,
	filter search.RestaurantFilter,
	soft []retrieval.SoftCondition,
	depth int,
	sem *semanticQuery,
) (ChannelInput, map[int64]search.RestaurantCandidate, map[int64]float64, error) {
	// Same skip grammar as the profile channel: warn marks a degradation a
	// reader must see, and a filter-only query having nothing to embed is
	// routine.
	skipped := func(note string, warn bool) (ChannelInput, map[int64]search.RestaurantCandidate, map[int64]float64, error) {
		return ChannelInput{Channel: retrieval.ChannelReview, Note: note, Warn: warn}, nil, nil, nil
	}

	if !s.cfg.EnableReview {
		return skipped("评论通道已关闭", true)
	}
	if s.knowledge == nil {
		return skipped("评论通道不可用：未配置知识库", true)
	}
	if s.embedding == nil {
		return skipped("评论通道不可用：未配置 embedding provider", true)
	}
	if strings.TrimSpace(sem.text) == "" {
		return skipped("无自然语言查询，评论通道跳过", false)
	}
	if sem.embedErr != nil {
		// The shared embedding failed transiently; the profile channel reports
		// the same fault under its own name.
		return skipped("评论通道不可用："+vectorFailureReason(sem.embedErr), true)
	}

	reviewDepth := depth * s.cfg.ReviewOverread
	if reviewDepth > maxReviewDepth {
		reviewDepth = maxReviewDepth
	}
	// The doc type is pinned for the same reason the profile channel must pin
	// its own: both kinds live in the restaurant scope, and an unpinned recall
	// would let the two channels read each other's corpus.
	docs, err := s.knowledge.VectorSearch(ctx, store.VectorSearchRequest{
		Scope:    evidence.ScopeRestaurant,
		Query:    sem.vector,
		TopK:     reviewDepth,
		Borough:  search.CanonicalBorough(filter.Borough),
		DocTypes: []evidence.DocType{evidence.DocTypeRestaurantReviewDigest},
	})
	if err != nil {
		return skipped("评论通道不可用："+vectorFailureReason(err), true)
	}

	input := ChannelInput{Channel: retrieval.ChannelReview, Ran: true}
	if len(soft) > 0 {
		input.Note = softConditionNote
		input.Warn = true
	}
	candidates := make(map[int64]search.RestaurantCandidate, len(docs))
	priors := make(map[int64]float64, len(docs))
	// belowThreshold counts the hits the gate refused; it is reported on the
	// channel row rather than raised, because "matched below the floor" is the
	// gate doing its job and the reader only needs the count to trust it.
	belowThreshold := 0
	for _, doc := range docs {
		similarity := cosineSimilarity(doc.Distance)
		if similarity < s.cfg.ReviewMinSim {
			belowThreshold++
			continue
		}
		reviewCount := digestReviewCount(doc.Metadata)
		reason := digestReason(sem.display, sem.softReason, similarity, reviewCount)
		detail := map[string]any{
			"query":      query,
			"similarity": similarity,
			"distance":   doc.Distance,
		}
		if reviewCount > 0 {
			detail["input_review_count"] = reviewCount
		}
		input.Hits = append(input.Hits, retrieval.ChannelHit{
			RestaurantID: doc.RestaurantID,
			Score:        similarity,
			Reason:       reason,
			Detail:       detail,
		})
		candidate := search.RestaurantCandidate{
			RestaurantID: doc.RestaurantID,
			Name:         doc.Title,
			SnapshotAt:   doc.SnapshotAt,
		}
		// A digest carries the name but not the address, price, or rating. A
		// failed read-back is not absorbed: the hit stays, the candidate does
		// not join the pool, and fusion reports the id as an unknown restaurant
		// on this channel's row. Silently ranking a candidate the UI cannot
		// display is the bug this replaces.
		if full, err := s.restaurants.GetByID(ctx, doc.RestaurantID); err == nil {
			candidate = detailAsCandidate(full, candidate)
			priors[doc.RestaurantID] = priorFrom(full)
			candidates[doc.RestaurantID] = candidate
		} else {
			s.logger.Debug("review channel backfill failed",
				slog.Int64("restaurant_id", doc.RestaurantID),
				slog.String("error", err.Error()))
		}
	}
	if belowThreshold > 0 {
		input.Note = joinNote(input.Note, fmt.Sprintf(
			"%d 篇命中低于相似度阈值（%.2f），未入池", belowThreshold, s.cfg.ReviewMinSim))
	}
	return input, candidates, priors, nil
}

// digestReason renders the review channel's user-facing reason.
//
// It quotes the channel's own facts — the question, the similarity, and how
// many reviews the offline reading was built from — and never the digest text
// itself: the digest is embedding fuel, not citable content, and a reason that
// quoted it would promise a citation the evidence layer cannot close. The
// concrete topic numbers a user might check come from the evidence stage's
// summarizable documents after a restaurant is picked.
func digestReason(display, softReason string, similarity float64, reviewCount int) string {
	reason := fmt.Sprintf("评论综合语义匹配“%s”（相似度 %.3f", display, similarity)
	if reviewCount > 0 {
		reason += fmt.Sprintf("，基于 %d 条评论的离线理解", reviewCount)
	}
	reason += "）"
	if softReason != "" {
		reason = softReason + "；" + reason
	}
	return reason
}

// digestReviewCount reads how many reviews the offline digest was built from.
//
// The count lives in the document's metadata, written once by the pipeline;
// the online side only renders it. A missing or malformed value renders as
// zero, and the reason omits the clause rather than claiming a count the
// document does not state.
func digestReviewCount(metadata map[string]any) int {
	switch count := metadata["input_review_count"].(type) {
	case int:
		return count
	case int64:
		return int(count)
	case float64:
		return int(count)
	default:
		return 0
	}
}
