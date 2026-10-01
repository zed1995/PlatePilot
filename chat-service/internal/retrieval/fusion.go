// Package retrieval is the read path's application layer: it turns a question
// into ranked candidates, and recalled documents into citable evidence.
//
// It sits between the transport and the ports, and depends on neither the HTTP
// framework nor the database driver. Everything here is a pure function over
// slices, which is what makes a ranking reproducible and replayable.
package retrieval

import (
	"fmt"
	"sort"
	"strings"

	"github.com/zed/platepilot/shared/domain/retrieval"
	"github.com/zed/platepilot/shared/domain/search"
)

// Weights scale each channel's contribution to the fused score.
//
// They are configuration rather than constants because they encode a product
// decision — how far a soft match may reorder a hard-filtered list — and that
// decision has to be adjustable once an evaluation set can measure it. The
// defaults keep the structured channel dominant: a restaurant satisfying every
// stated condition must not be displaced by one that merely reads well.
type Weights struct {
	Structured float64
	Keyword    float64
	Vector     float64
	// Quality scales the restaurant's own prior. It is not a recall channel,
	// and its weight is the smallest by design: this ranks places against a
	// question, not against each other's reputation.
	Quality float64
}

// DefaultWeights is the configuration used when nothing is set.
//
// Keyword sits below Structured because a name match is weaker evidence than a
// satisfied condition. Vector sits level with Structured because a semantic
// match is the only channel that can answer a soft condition at all.
var DefaultWeights = Weights{
	Structured: 1.0,
	Keyword:    0.5,
	Vector:     1.0,
	Quality:    0.2,
}

// Pool is everything fusion is allowed to rank: the restaurants the channels
// recalled, and the prior each one carries.
//
// Priors are passed separately rather than read off the candidate because the
// prior is an internal ranking signal, not something a result should advertise.
type Pool struct {
	Candidates map[int64]search.RestaurantCandidate
	// Priors holds each restaurant's knowledge score. A missing entry is
	// treated as zero, so a caller may omit it rather than inventing values.
	Priors map[int64]float64
}

// Options controls one fusion.
type Options struct {
	Weights Weights
	// TopK is how many candidates to return after fusion.
	TopK int
	// Filter is echoed into the trace so a result can be reproduced from the
	// trace alone.
	Filter search.RestaurantFilter
	Query  string
}

// ChannelInput is one channel's output, ready to fuse.
type ChannelInput struct {
	Channel retrieval.Channel
	// Ran distinguishes "found nothing" from "was never asked". A channel that
	// did not run still appears in the trace with its weight, because a missing
	// channel and an empty one are different facts.
	Ran  bool
	Hits []retrieval.ChannelHit
	// Note explains a skipped or degraded channel in one sentence.
	Note string
}

// Fused is the outcome of one fusion.
type Fused struct {
	Candidates []search.RestaurantCandidate
	Trace      *retrieval.Trace
}

// Fuse merges the recall channels into one ranked candidate list.
//
// A candidate missing from a channel scores zero there rather than being
// dropped. A restaurant the structured filter kept but the keyword channel did
// not surface is still a correct answer, and dropping it would let one channel's
// silence erase another channel's hit.
//
// Ordering is fused score, then restaurant id. The tie-breaker is not cosmetic:
// without it, equal scores come back in whatever order the channels produced,
// and two identical requests return two different rankings.
func Fuse(inputs []ChannelInput, pool Pool, opts Options) Fused {
	weights := weightFor(opts.Weights)
	trace := &retrieval.Trace{
		Query:   opts.Query,
		Filters: opts.Filter,
		TopK:    opts.TopK,
	}

	// bestHit keeps one channel's strongest hit per restaurant. Recall can
	// legitimately return the same restaurant twice — two documents, one
	// restaurant — and letting both count would let a document count inflate a
	// restaurant's rank.
	bestHit := make(map[retrieval.Channel]map[int64]retrieval.ChannelHit, len(inputs))
	// The hard filter is applied to the merged pool, not to one channel. The
	// structured channel already produced rows that satisfy it, but the keyword
	// and vector channels recall by text and by meaning, and neither can see a
	// price level or a rating. Letting their rows through would reintroduce a
	// restaurant the caller explicitly excluded, and it would do so inside a
	// result that looks exactly like a correct one — a silent filter, which is
	// the one failure this layer cannot detect downstream.
	poolIDs := make([]int64, 0, len(pool.Candidates))
	if !opts.Filter.IsEmpty() {
		rejected := 0
		for id, candidate := range pool.Candidates {
			if !opts.Filter.Matches(candidate) {
				delete(pool.Candidates, id)
				delete(pool.Priors, id)
				rejected++
			}
		}
		if rejected > 0 {
			trace.Warn(fmt.Sprintf(
				"融合时移除了 %d 条不满足硬条件的候选（软条件通道无法看到价格/评分/菜系）",
				rejected))
		}
	}
	seen := make(map[int64]struct{}, len(pool.Candidates))

	for _, input := range inputs {
		trace.Channels = append(trace.Channels, retrieval.ChannelSummary{
			Channel: input.Channel,
			Ran:     input.Ran,
			Weight:  weights[input.Channel],
			Results: len(input.Hits),
			Note:    input.Note,
		})
		if input.Note != "" {
			trace.Warn(input.Note)
		}
		if !input.Ran {
			continue
		}
		byID := make(map[int64]retrieval.ChannelHit, len(input.Hits))
		for _, hit := range input.Hits {
			if existing, ok := byID[hit.RestaurantID]; !ok || hit.Score > existing.Score {
				byID[hit.RestaurantID] = hit
			}
		}
		bestHit[input.Channel] = byID
		for id := range byID {
			if _, known := pool.Candidates[id]; !known {
				// A channel recalled a restaurant the pool does not describe.
				// Dropping it is correct: without a name, address, and rating
				// there is nothing to return, and inventing one would be worse.
				trace.Warn(fmt.Sprintf("%s 通道返回了未知餐厅 %d，已忽略", input.Channel, id))
				continue
			}
			if _, dup := seen[id]; !dup {
				seen[id] = struct{}{}
				poolIDs = append(poolIDs, id)
			}
		}
	}
	trace.CandidatePool = len(poolIDs)

	// Bounds are computed over the pool so two candidates always land on the
	// same scale. Normalizing per candidate would make every hit 1.0.
	bounds := make(map[retrieval.Channel][2]float64, len(inputs))
	for _, input := range inputs {
		if input.Ran {
			bounds[input.Channel] = scoreBounds(input.Hits)
		}
	}
	priorRange := boundsOf(pool.Priors, poolIDs)

	scored := make([]retrieval.CandidateScore, 0, len(poolIDs))
	for _, id := range poolIDs {
		entry := retrieval.CandidateScore{RestaurantID: id}
		var total float64
		for _, input := range inputs {
			if !input.Ran {
				continue
			}
			weight := weights[input.Channel]
			hit, ok := bestHit[input.Channel][id]
			if !ok {
				entry.Channels = append(entry.Channels, retrieval.ChannelScore{
					Channel: input.Channel, Weight: weight,
				})
				continue
			}
			normalized := normalize(hit.Score, bounds[input.Channel])
			contribution := weight * normalized
			total += contribution
			entry.Channels = append(entry.Channels, retrieval.ChannelScore{
				Channel:    input.Channel,
				Raw:        hit.Score,
				Weight:     weight,
				Normalized: normalized,
				Contrib:    contribution,
				Reason:     hit.Reason,
			})
		}
		// The prior is added after the channels rather than as a fourth channel:
		// it is a property of the restaurant, not something a query recalled,
		// and normalizing it against recall scores would compare two unrelated
		// quantities.
		if opts.Weights.Quality > 0 {
			prior := normalize(pool.Priors[id], priorRange)
			contribution := opts.Weights.Quality * prior
			total += contribution
			entry.Channels = append(entry.Channels, retrieval.ChannelScore{
				Channel:    PriorChannel,
				Raw:        pool.Priors[id],
				Weight:     opts.Weights.Quality,
				Normalized: prior,
				Contrib:    contribution,
				Reason:     PriorReason,
			})
		}
		entry.Total = total
		scored = append(scored, entry)
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Total != scored[j].Total {
			return scored[i].Total > scored[j].Total
		}
		return scored[i].RestaurantID < scored[j].RestaurantID
	})

	if opts.TopK > 0 && len(scored) > opts.TopK {
		scored = scored[:opts.TopK]
	}

	fused := make([]search.RestaurantCandidate, 0, len(scored))
	trace.Candidates = make([]retrieval.CandidateScore, 0, len(scored))
	for _, entry := range scored {
		candidate := pool.Candidates[entry.RestaurantID]
		candidate.Score = entry.Total
		candidate.Reasons = explain(entry.Channels)
		fused = append(fused, candidate)
		trace.Candidates = append(trace.Candidates, entry)
	}
	trace.Returned = len(fused)
	return Fused{Candidates: fused, Trace: trace}
}

// PriorChannel labels the prior term in a derivation. It is not a recall
// channel, so it is deliberately absent from retrieval.AllChannels: nothing
// recalls by it.
const PriorChannel retrieval.Channel = "prior"

// PriorReason explains what the prior is, in a trace a reviewer will read.
const PriorReason = "评分先验（按评论样本量收缩）"

// explain turns the contributing terms into the candidate's user-facing reasons.
//
// Only terms that actually contributed are named. Listing the channels that
// found nothing would produce a paragraph saying nothing, which is worth less
// than no reasons at all.
func explain(channels []retrieval.ChannelScore) []string {
	reasons := make([]string, 0, len(channels))
	for _, channel := range channels {
		if channel.Reason == "" || channel.Contrib <= 0 {
			continue
		}
		reasons = append(reasons, fmt.Sprintf("%s（权重 %.2f，贡献 %.3f）",
			channel.Reason, channel.Weight, channel.Contrib))
	}
	return reasons
}

// scoreBounds returns the min and max raw score in a channel's hits.
func scoreBounds(hits []retrieval.ChannelHit) [2]float64 {
	if len(hits) == 0 {
		return [2]float64{}
	}
	bounds := [2]float64{hits[0].Score, hits[0].Score}
	for _, hit := range hits[1:] {
		if hit.Score < bounds[0] {
			bounds[0] = hit.Score
		}
		if hit.Score > bounds[1] {
			bounds[1] = hit.Score
		}
	}
	return bounds
}

// boundsOf returns the min and max of a keyed value over the fused pool.
func boundsOf(values map[int64]float64, ids []int64) [2]float64 {
	if len(ids) == 0 {
		return [2]float64{}
	}
	bounds := [2]float64{values[ids[0]], values[ids[0]]}
	for _, id := range ids[1:] {
		value := values[id]
		if value < bounds[0] {
			bounds[0] = value
		}
		if value > bounds[1] {
			bounds[1] = value
		}
	}
	return bounds
}

// normalize scales a score into [0,1] against its channel's bounds.
//
// A channel where every candidate scored the same carries no ranking
// information, so it maps to a neutral 0.5 rather than to zero. Zero would
// silently delete the channel's contribution and make a tie look like a loss;
// one would claim the channel confidently preferred everything.
func normalize(score float64, bounds [2]float64) float64 {
	span := bounds[1] - bounds[0]
	if span <= 0 {
		return 0.5
	}
	return (score - bounds[0]) / span
}

// weightFor maps each channel to its configured weight. Every channel gets an
// entry, including ones with no input, so a trace can always report a weight.
func weightFor(configured Weights) map[retrieval.Channel]float64 {
	weights := map[retrieval.Channel]float64{
		retrieval.ChannelStructured: configured.Structured,
		retrieval.ChannelKeyword:    configured.Keyword,
		retrieval.ChannelVector:     configured.Vector,
		PriorChannel:                configured.Quality,
	}
	for _, channel := range retrieval.AllChannels {
		if _, ok := weights[channel]; !ok {
			weights[channel] = 0
		}
	}
	return weights
}

// Page sizes are bounded for the same reason the store bounds them: an
// unbounded top-k turns one request into a table scan.
const (
	// defaultTopK is the page size when the caller does not set one.
	defaultTopK = 5
	// maxTopK is the largest page a caller may request.
	maxTopK = 100
)

// describeFilters renders the active filters for a trace note.
func describeFilters(filter search.RestaurantFilter) string {
	var parts []string
	if len(filter.Cuisines) > 0 {
		parts = append(parts, "菜系="+strings.Join(filter.Cuisines, "/"))
	}
	if len(filter.PriceLevels) > 0 {
		levels := make([]string, 0, len(filter.PriceLevels))
		for _, level := range filter.PriceLevels {
			levels = append(levels, fmt.Sprintf("$%d", level))
		}
		parts = append(parts, "价格="+strings.Join(levels, "/"))
	}
	if filter.MinRating != nil {
		parts = append(parts, fmt.Sprintf("评分>=%.1f", *filter.MinRating))
	}
	if filter.Neighborhood != "" {
		parts = append(parts, "地区="+filter.Neighborhood)
	}
	if filter.Borough != "" {
		parts = append(parts, "行政区="+filter.Borough)
	}
	if filter.OpenNow != nil {
		parts = append(parts, fmt.Sprintf("营业=%t", *filter.OpenNow))
	}
	if filter.HasDistance() {
		parts = append(parts, fmt.Sprintf("距离<%dm", *filter.MaxDistanceMeters))
	}
	return strings.Join(parts, "、")
}
