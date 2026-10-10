package retrieval

import (
	"context"
	"sort"

	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/domain/retrieval"
	"github.com/zed1995/platepilot/shared/domain/search"
	"github.com/zed1995/platepilot/shared/store"
)

// Rescore carries the pool-wide exact semantic scores fusion consumes.
//
// The zero value is a valid input meaning "the rescore did not run": every
// channel then fuses its own recall scores, which is the behaviour this layer
// had before the rescore existed.
type Rescore struct {
	// Enabled reports whether exact scores are available for at least one
	// semantic channel. When it is false the Scores map is ignored entirely.
	Enabled bool
	// Scores maps channel -> restaurant -> exact cosine similarity between the
	// query and that restaurant's one active document of the channel's doc
	// type. These are the only scores fusion uses for a channel once its entry
	// exists: an ANN distance approximates the cosine and the two readings are
	// not interchangeable.
	Scores map[retrieval.Channel]map[int64]float64
	// Reasons maps channel -> restaurant -> the user-facing reason rendered
	// from the exact score, so a recalled hit's prose quotes the same number
	// the fused score carries.
	Reasons map[retrieval.Channel]map[int64]string
	// Thresholds maps channel -> the minimum exact similarity below which the
	// channel treats the candidate as absent. Only channels with a gate have
	// an entry; the absence itself is fusion's to apply and count.
	Thresholds map[retrieval.Channel]float64
	// Warnings carries degradations (a failed rescore query) that must reach
	// the trace. The ranking continues on the recall scores instead.
	Warnings []string
}

// rescorePool scores every pool candidate's profile and digest documents with
// the exact cosine, once per doc type, instead of trusting the approximate ANN
// pages.
//
// It exists because "the channel was absent" currently means two different
// things for a candidate: it matched badly, or it fell past the page an
// approximate index happened to return. Only the first is a signal; the second
// is an artifact of the recall page size. Scoring the whole pool exactly turns
// the artifact back into a signal for every candidate the other channels
// recalled.
//
// Every way it can fail degrades rather than fails: without a query vector, a
// store error, or an empty pool the overlay stays empty and fusion keeps the
// recall scores it already had.
func (s *Service) rescorePool(
	ctx context.Context, sem *semanticQuery, pool map[int64]search.RestaurantCandidate,
) Rescore {
	if !s.cfg.EnablePoolRescore || sem == nil || sem.vector == nil || len(pool) == 0 {
		return Rescore{}
	}
	// Sorted so the store receives a request that reads back identically in a
	// replay, whatever order the candidate map happens to enumerate in.
	ids := make([]int64, 0, len(pool))
	for id := range pool {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	out := Rescore{
		Scores:     make(map[retrieval.Channel]map[int64]float64, 2),
		Reasons:    make(map[retrieval.Channel]map[int64]string, 2),
		Thresholds: make(map[retrieval.Channel]float64, 1),
	}
	// A channel that is switched off is not rescored: its absence from the
	// ranking is a configuration fact, not a page artifact to repair.
	if s.cfg.EnableVector {
		scores, reasons, err := s.scorePool(ctx, sem, ids, evidence.DocTypeRestaurantProfile,
			func(doc store.ScoredDocument, similarity float64) string {
				return profileReason(sem.display, sem.softReason, similarity)
			})
		if err != nil {
			out.Warnings = append(out.Warnings,
				"池内精确补分失败，沿用召回近似分："+vectorFailureReason(err))
		} else {
			out.Scores[retrieval.ChannelVector] = scores
			out.Reasons[retrieval.ChannelVector] = reasons
		}
	}
	if s.cfg.EnableReview {
		scores, reasons, err := s.scorePool(ctx, sem, ids, evidence.DocTypeRestaurantReviewDigest,
			func(doc store.ScoredDocument, similarity float64) string {
				return digestReason(sem.display, sem.softReason, similarity,
					digestReviewCount(doc.Metadata))
			})
		if err != nil {
			out.Warnings = append(out.Warnings,
				"池内精确补分失败，沿用召回近似分："+vectorFailureReason(err))
		} else {
			out.Scores[retrieval.ChannelReview] = scores
			out.Reasons[retrieval.ChannelReview] = reasons
			out.Thresholds[retrieval.ChannelReview] = s.cfg.ReviewMinSim
		}
	}
	out.Enabled = len(out.Scores) > 0
	return out
}

// scorePool scores one doc type across the pool and renders each score's
// reason. It is one round trip per doc type against a bounded id list.
func (s *Service) scorePool(
	ctx context.Context,
	sem *semanticQuery,
	ids []int64,
	docType evidence.DocType,
	reasonFor func(doc store.ScoredDocument, similarity float64) string,
) (map[int64]float64, map[int64]string, error) {
	docs, err := s.knowledge.ScorePoolByEmbedding(ctx, store.ScorePoolRequest{
		Scope:         evidence.ScopeRestaurant,
		Query:         sem.vector,
		RestaurantIDs: ids,
		DocType:       docType,
	})
	if err != nil {
		return nil, nil, err
	}
	scores := make(map[int64]float64, len(docs))
	reasons := make(map[int64]string, len(docs))
	for _, doc := range docs {
		similarity := cosineSimilarity(doc.Distance)
		scores[doc.RestaurantID] = similarity
		reasons[doc.RestaurantID] = reasonFor(doc, similarity)
	}
	return scores, reasons, nil
}
