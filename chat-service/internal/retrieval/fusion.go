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

	"github.com/zed1995/platepilot/shared/domain/retrieval"
	"github.com/zed1995/platepilot/shared/domain/search"
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
	// Review scales the offline review-digest channel. It sits below Vector
	// because a digest matches how a place was experienced, one inference away
	// from the question, while a profile matches what the place says it is.
	Review float64
	// Quality scales the restaurant's own prior. It is not a recall channel,
	// and its weight is the smallest by design: this ranks places against a
	// question, not against each other's reputation.
	Quality float64
}

// DefaultWeights is the configuration used when nothing is set.
//
// Keyword sits below Structured because a name match is weaker evidence than a
// satisfied condition. Vector sits level with Structured because a semantic
// match is the only channel that can answer a soft condition at all. Review
// sits below Vector because the digest is an offline reading of reviews rather
// than the restaurant's own description; the value is an initial one for the
// evaluation harness to move.
var DefaultWeights = Weights{
	Structured: 1.0,
	Keyword:    0.5,
	Vector:     1.0,
	Review:     0.8,
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
	// Rescore carries the pool-wide exact semantic scores. Zero value means
	// the rescore did not run and every channel fuses its own recall scores.
	Rescore Rescore
	// FusionMethod selects the fusion algorithm: FusionWeighted (the default,
	// min-max plus weights) or FusionRRF. An empty value means weighted so a
	// caller that does not know about the knob keeps today's behaviour.
	FusionMethod string
}

// ChannelInput is one channel's output, ready to fuse.
type ChannelInput struct {
	Channel retrieval.Channel
	// Ran distinguishes "found nothing" from "was never asked". A channel that
	// did not run still appears in the trace with its weight, because a missing
	// channel and an empty one are different facts.
	Ran  bool
	Hits []retrieval.ChannelHit
	// Note explains a skipped or degraded channel in one sentence. It is always
	// recorded on the channel's trace row.
	Note string
	// Warn elevates Note into the trace's warning list. Set it for genuine
	// degradations (the channel is disabled or unavailable) and for one-time
	// product disclosures a reader must not miss; leave it false for routine
	// skips that are normal operation ("no keyword", "no filter", "no natural
	// language query"), which would otherwise put a yellow warning on almost
	// every search.
	Warn bool
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
	method := fusionMethodOf(opts.FusionMethod)
	trace := &retrieval.Trace{
		Query:        opts.Query,
		Filters:      opts.Filter,
		TopK:         opts.TopK,
		FusionMethod: method,
	}
	// A rescore that failed is a degradation, not a silence: the ranking fell
	// back to the recall pages' approximate scores, and the trace has to say
	// so or the two runs are indistinguishable.
	for _, warning := range opts.Rescore.Warnings {
		trace.Warn(warning)
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
	// channelSummaryIdx remembers where each channel's trace row landed, so a
	// later stage (the rescore threshold gate) can annotate the row it
	// belongs to without re-walking the slice.
	channelSummaryIdx := make(map[retrieval.Channel]int, len(inputs))

	for _, input := range inputs {
		summary := retrieval.ChannelSummary{
			Channel: input.Channel,
			Ran:     input.Ran,
			Weight:  weights[input.Channel],
			Results: len(input.Hits),
			Note:    input.Note,
		}
		trace.Channels = append(trace.Channels, summary)
		summaryIdx := len(trace.Channels) - 1
		channelSummaryIdx[input.Channel] = summaryIdx
		// Only notes the channel marked as a warning reach the warning list.
		// A routine skip stays on the channel row and never nags the caller.
		if input.Note != "" && input.Warn {
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
		// Unknown ids are collected, sorted, and reported once. A stale vector
		// index can return dozens of them in a single turn; one warning per id
		// would flood the caller, and map order would make the list
		// non-reproducible. The ids themselves are kept on the channel row.
		var unknownIDs []int64
		for id := range byID {
			if _, known := pool.Candidates[id]; !known {
				// A channel recalled a restaurant the pool does not describe.
				// Dropping it is correct: without a name, address, and rating
				// there is nothing to return, and inventing one would be worse.
				unknownIDs = append(unknownIDs, id)
				continue
			}
			if _, dup := seen[id]; !dup {
				seen[id] = struct{}{}
				poolIDs = append(poolIDs, id)
			}
		}
		if len(unknownIDs) > 0 {
			sort.Slice(unknownIDs, func(i, j int) bool { return unknownIDs[i] < unknownIDs[j] })
			trace.Channels[summaryIdx].DroppedRestaurantIDs = unknownIDs
			trace.Warn(fmt.Sprintf(
				"%s 通道返回了 %d 家候选池外的未知餐厅，已忽略",
				input.Channel, len(unknownIDs)))
		}
	}
	trace.CandidatePool = len(poolIDs)

	// The rescore's exact scores are gated before anything else: a score below
	// a channel's threshold is not a weak hit but a non-match, and letting it
	// into the bounds would stretch the family's scale around numbers the
	// channel has already refused. What survives participates in both the
	// bounds and the fused scores.
	belowThreshold, participating := thresholdRescore(opts.Rescore)
	for channel, count := range belowThreshold {
		idx, ok := channelSummaryIdx[channel]
		if !ok {
			continue
		}
		trace.Channels[idx].Note = joinNote(trace.Channels[idx].Note,
			fmt.Sprintf("补分后 %d 家低于相似度阈值（%.2f），按通道缺席处理",
				count, opts.Rescore.Thresholds[channel]))
	}

	// RRF needs each channel's ranks once, over the fused pool. Ranking by raw
	// score with the id as tie-break keeps the ranks identical across runs no
	// matter what order the channels or the maps produced their hits in.
	rrfRanks := rrfRanksOf(method, inputs, poolIDs, bestHit, participating)

	// Bounds are computed over the pool so two candidates always land on the
	// same scale. Normalizing per candidate would make every hit 1.0.
	//
	// The semantic channels share one set of bounds: their raw scores are both
	// cosines on the same scale, and normalizing each channel on its own would
	// stretch a weak digest match to 1.0 exactly when the profile channel's
	// strongest hit is also 1.0, making the two incomparable inside one
	// ranking. When the pool-wide exact rescore ran, its scores are the
	// authoritative bounds as well as the authoritative fused scores; without
	// it the family falls back to the union of its own recall hits, which for
	// a single running semantic channel is exactly the old per-channel scale.
	bounds := make(map[retrieval.Channel][2]float64, len(inputs))
	familyRescored := false
	familyScores := make([]float64, 0, len(poolIDs))
	for _, channel := range semanticFamily {
		if scores, ok := participating[channel]; ok {
			familyRescored = true
			for _, score := range scores {
				familyScores = append(familyScores, score)
			}
		}
	}
	if familyRescored {
		familyBounds := boundsOfScores(familyScores)
		for _, channel := range semanticFamily {
			bounds[channel] = familyBounds
		}
	} else {
		familyHits := make([]float64, 0, len(poolIDs))
		for _, input := range inputs {
			if input.Ran && inSemanticFamily(input.Channel) {
				for _, hit := range input.Hits {
					familyHits = append(familyHits, hit.Score)
				}
			}
		}
		familyBounds := boundsOfScores(familyHits)
		for _, channel := range semanticFamily {
			bounds[channel] = familyBounds
		}
	}
	for _, input := range inputs {
		if input.Ran && !inSemanticFamily(input.Channel) {
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
			hit, recalled := bestHit[input.Channel][id]

			// Resolve what this channel says about the candidate. When the
			// exact rescore ran for the channel, its score is the only one
			// fused -- an ANN distance approximates the cosine, and mixing the
			// two instruments in one ranking would let a candidate's position
			// depend on which page it happened to be recalled by. An absent or
			// below-threshold exact score leaves the channel silent for the
			// candidate: the absence-equals-zero rule, unchanged.
			var (
				raw       float64
				reason    string
				source    string
				scoresFor bool
			)
			if rescored, ok := participating[input.Channel]; ok {
				exact, hitByRescore := rescored[id]
				if !hitByRescore {
					entry.Channels = append(entry.Channels, retrieval.ChannelScore{
						Channel: input.Channel, Weight: weight,
					})
					continue
				}
				raw = exact
				reason = opts.Rescore.Reasons[input.Channel][id]
				source = retrieval.SourceRescored
				if recalled {
					source = retrieval.SourceRecalled
				}
				scoresFor = true
			} else if recalled {
				raw = hit.Score
				reason = hit.Reason
				if inSemanticFamily(input.Channel) {
					source = retrieval.SourceRecalled
				}
				scoresFor = true
			}
			if !scoresFor {
				entry.Channels = append(entry.Channels, retrieval.ChannelScore{
					Channel: input.Channel, Weight: weight,
				})
				continue
			}

			var normalized, contribution float64
			if method == FusionRRF {
				rank, ranked := rrfRanks[input.Channel][id]
				if !ranked {
					entry.Channels = append(entry.Channels, retrieval.ChannelScore{
						Channel: input.Channel, Weight: weight,
					})
					continue
				}
				// The reciprocal rank replaces the min-max term: the channel
				// says where the candidate stood, not how far its score was
				// from its neighbours, and raw stays on the trace for reference.
				normalized = 1 / (rrfK + float64(rank))
				contribution = weight * normalized
			} else {
				normalized = normalize(raw, bounds[input.Channel])
				contribution = weight * normalized
			}
			total += contribution
			entry.Channels = append(entry.Channels, retrieval.ChannelScore{
				Channel:    input.Channel,
				Raw:        raw,
				Weight:     weight,
				Normalized: normalized,
				Contrib:    contribution,
				Reason:     reason,
				Source:     source,
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
// A channel that did not recall the candidate is left out: it has nothing to
// say, and naming it would produce a paragraph explaining an absence.
//
// A channel that did recall it is named even when its contribution is zero.
// Min-max normalization maps the weakest hit in a channel to exactly 0, so the
// last of two keyword hits contributes nothing -- but the candidate is in the
// results because that channel recalled it, and dropping the term leaves a
// returned restaurant with no stated reason at all. The recall is the fact; the
// zero is an artifact of the scale the recall was measured on.
func explain(channels []retrieval.ChannelScore) []string {
	reasons := make([]string, 0, len(channels))
	for _, channel := range channels {
		if channel.Reason == "" {
			continue
		}
		if channel.Contrib > 0 {
			reasons = append(reasons, fmt.Sprintf("%s（权重 %.2f，贡献 %.3f）",
				channel.Reason, channel.Weight, channel.Contrib))
			continue
		}
		reasons = append(reasons, fmt.Sprintf("%s（权重 %.2f，归一化后贡献为 0："+
			"该通道内最弱命中，仍被该通道召回）", channel.Reason, channel.Weight))
	}
	return reasons
}

// scoreBounds returns the min and max raw score in a channel's hits.
func scoreBounds(hits []retrieval.ChannelHit) [2]float64 {
	if len(hits) == 0 {
		return [2]float64{}
	}
	scores := make([]float64, 0, len(hits))
	for _, hit := range hits {
		scores = append(scores, hit.Score)
	}
	return boundsOfScores(scores)
}

// boundsOfScores returns the min and max of a plain score slice. An empty
// slice yields the zero bounds, which normalize maps to the neutral 0.5 — the
// same "no ranking information" reading an empty channel gets.
func boundsOfScores(scores []float64) [2]float64 {
	if len(scores) == 0 {
		return [2]float64{}
	}
	bounds := [2]float64{scores[0], scores[0]}
	for _, score := range scores[1:] {
		if score < bounds[0] {
			bounds[0] = score
		}
		if score > bounds[1] {
			bounds[1] = score
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
		retrieval.ChannelReview:     configured.Review,
		PriorChannel:                configured.Quality,
	}
	for _, channel := range retrieval.AllChannels {
		if _, ok := weights[channel]; !ok {
			weights[channel] = 0
		}
	}
	return weights
}

// semanticFamily lists the channels whose raw scores share one cosine scale:
// both recall restaurant-level documents embedded by the same model against the
// same query. They are normalized against one shared set of bounds so their
// contributions stay comparable inside a single ranking.
var semanticFamily = []retrieval.Channel{retrieval.ChannelVector, retrieval.ChannelReview}

func inSemanticFamily(channel retrieval.Channel) bool {
	for _, member := range semanticFamily {
		if member == channel {
			return true
		}
	}
	return false
}

// Fusion algorithm names, as they appear in the trace's fusion_method.
const (
	// FusionWeighted is the default: family-shared min-max normalization
	// scaled by channel weights.
	FusionWeighted = "weighted"
	// FusionRRF ranks candidates by weighted reciprocal rank instead of by
	// normalized score, which is robust when two channels' raw scales are not
	// trusted to be comparable at all.
	FusionRRF = "rrf"
)

// rrfK is the standard RRF dampener. It flattens the head of the rank
// distribution so the top of one channel's page does not dominate by an order
// of magnitude the way a raw 1/rank would.
const rrfK = 60

// fusionMethodOf resolves the configured method. An empty or unknown value
// falls back to weighted: the configuration layer rejects unknown names, so
// the only caller that can hand one in is a test or a future rename, and the
// safe landing is the behaviour that existed first.
func fusionMethodOf(method string) string {
	if method == FusionRRF {
		return FusionRRF
	}
	return FusionWeighted
}

// rrfRanksOf computes each channel's 1-based ranks over the fused pool, for
// the RRF method. A channel's universe is its exact rescore scores when the
// rescore ran for it, otherwise its recalled hits; candidates outside the
// pool after the hard filter are not ranked at all.
//
// Ranks are ordered by raw score descending with the restaurant id as the
// tie-break, so the same inputs always produce the same ranks no matter what
// order the channels or the underlying maps produced their hits in. A nil
// result means the method is not RRF and the ranks are never consulted.
func rrfRanksOf(
	method string,
	inputs []ChannelInput,
	poolIDs []int64,
	bestHit map[retrieval.Channel]map[int64]retrieval.ChannelHit,
	participating map[retrieval.Channel]map[int64]float64,
) map[retrieval.Channel]map[int64]int {
	if method != FusionRRF {
		return nil
	}
	inPool := make(map[int64]struct{}, len(poolIDs))
	for _, id := range poolIDs {
		inPool[id] = struct{}{}
	}
	ranks := make(map[retrieval.Channel]map[int64]int, len(inputs))
	for _, input := range inputs {
		if !input.Ran {
			continue
		}
		scores := make(map[int64]float64)
		if rescored, ok := participating[input.Channel]; ok {
			for id, score := range rescored {
				if _, keep := inPool[id]; keep {
					scores[id] = score
				}
			}
		} else {
			for id, hit := range bestHit[input.Channel] {
				if _, keep := inPool[id]; keep {
					scores[id] = hit.Score
				}
			}
		}
		ids := make([]int64, 0, len(scores))
		for id := range scores {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			if scores[ids[i]] != scores[ids[j]] {
				return scores[ids[i]] > scores[ids[j]]
			}
			return ids[i] < ids[j]
		})
		rank := make(map[int64]int, len(ids))
		for i, id := range ids {
			rank[id] = i + 1
		}
		ranks[input.Channel] = rank
	}
	return ranks
}

// thresholdRescore splits the rescore overlay into the scores that participate
// in fusion and per-channel counts of the ones a channel's threshold refused.
// A channel whose rescore ran gets an entry even when every score was refused,
// because "the rescore ran and the channel still says nothing" is exactly the
// state fusion must distinguish from "the rescore never ran".
func thresholdRescore(rescore Rescore) (map[retrieval.Channel]int, map[retrieval.Channel]map[int64]float64) {
	below := make(map[retrieval.Channel]int)
	participating := make(map[retrieval.Channel]map[int64]float64, len(rescore.Scores))
	if !rescore.Enabled {
		return below, participating
	}
	for channel, scores := range rescore.Scores {
		gated := make(map[int64]float64, len(scores))
		threshold, gatedChannel := rescore.Thresholds[channel]
		for id, score := range scores {
			if gatedChannel && score < threshold {
				below[channel]++
				continue
			}
			gated[id] = score
		}
		participating[channel] = gated
	}
	return below, participating
}

// joinNote appends a second fact to a channel's note. Notes are one sentence
// per fact joined by the same separator the reasons use, so a trace row reads
// as a short list rather than a paragraph.
func joinNote(note, extra string) string {
	if note == "" {
		return extra
	}
	return note + "；" + extra
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
//
// The rendering itself lives in the domain, because the answer prompt quotes
// the same sentence: a second renderer here would be free to name a different
// subset of the conditions than the one the search actually enforced, and the
// user reading the answer would have no way to notice.
func describeFilters(filter search.RestaurantFilter) string {
	return filter.Describe()
}
