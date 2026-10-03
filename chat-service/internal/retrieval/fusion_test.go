package retrieval

import (
	"testing"

	"github.com/zed1995/platepilot/shared/domain/retrieval"
	"github.com/zed1995/platepilot/shared/domain/search"
)

func candidate(id int64, name string) search.RestaurantCandidate {
	return search.RestaurantCandidate{RestaurantID: id, Name: name}
}

func poolOf(priors map[int64]float64, candidates ...search.RestaurantCandidate) Pool {
	pool := Pool{Candidates: make(map[int64]search.RestaurantCandidate, len(candidates)), Priors: priors}
	for _, c := range candidates {
		pool.Candidates[c.RestaurantID] = c
	}
	return pool
}

func ids(candidates []search.RestaurantCandidate) []int64 {
	out := make([]int64, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, c.RestaurantID)
	}
	return out
}

// A candidate one channel found and another did not must survive fusion. If it
// did not, a keyword miss would silently delete a restaurant the hard filters
// had already proven correct.
func TestFuseKeepsCandidatesMissingFromOneChannel(t *testing.T) {
	pool := poolOf(map[int64]float64{1: 1, 2: 1}, candidate(1, "A"), candidate(2, "B"))
	inputs := []ChannelInput{
		{Channel: retrieval.ChannelStructured, Ran: true, Hits: []retrieval.ChannelHit{
			{RestaurantID: 1, Score: 1, Reason: "满足硬条件"},
			{RestaurantID: 2, Score: 1, Reason: "满足硬条件"},
		}},
		{Channel: retrieval.ChannelKeyword, Ran: true, Hits: []retrieval.ChannelHit{
			{RestaurantID: 1, Score: 0.9, Reason: "名称匹配"},
		}},
	}
	got := Fuse(inputs, pool, Options{Weights: Weights{Structured: 1, Keyword: 1}, TopK: 10})
	if len(got.Candidates) != 2 {
		t.Fatalf("want both candidates, got %v", ids(got.Candidates))
	}
	if got.Candidates[0].RestaurantID != 1 {
		t.Fatalf("the keyword hit should rank first, got %v", ids(got.Candidates))
	}
}

// The whole point of fusion is that it is reproducible. An unstable order would
// make every ranking test flaky for reasons unrelated to ranking, and would make
// a replayed request return a different list than the original.
func TestFuseIsReproducible(t *testing.T) {
	pool := poolOf(map[int64]float64{1: 0.5, 2: 0.5, 3: 0.5},
		candidate(1, "A"), candidate(2, "B"), candidate(3, "C"))
	inputs := []ChannelInput{
		{Channel: retrieval.ChannelStructured, Ran: true, Hits: []retrieval.ChannelHit{
			{RestaurantID: 3, Score: 1, Reason: "满足硬条件"},
			{RestaurantID: 1, Score: 1, Reason: "满足硬条件"},
			{RestaurantID: 2, Score: 1, Reason: "满足硬条件"},
		}},
	}
	opts := Options{Weights: Weights{Structured: 1}, TopK: 10}

	first := Fuse(inputs, pool, opts)
	second := Fuse(inputs, pool, opts)
	if len(first.Candidates) != 3 || len(second.Candidates) != 3 {
		t.Fatalf("want 3 candidates each, got %d and %d", len(first.Candidates), len(second.Candidates))
	}
	for i := range first.Candidates {
		if first.Candidates[i].RestaurantID != second.Candidates[i].RestaurantID {
			t.Fatalf("order differs at %d: %v vs %v", i,
				ids(first.Candidates), ids(second.Candidates))
		}
		if first.Candidates[i].Score != second.Candidates[i].Score {
			t.Fatalf("score differs for %d: %v vs %v", i,
				first.Candidates[i].Score, second.Candidates[i].Score)
		}
	}
	// Equal scores must fall back to the id tie-breaker rather than to input order.
	want := []int64{1, 2, 3}
	for i, id := range want {
		if first.Candidates[i].RestaurantID != id {
			t.Fatalf("tie must break on ascending id: want %v, got %v", want, ids(first.Candidates))
		}
	}
}

// A channel where everything scored the same carries no ranking information.
// Mapping that to zero would delete the channel's contribution; mapping it to
// one would claim the channel preferred everything.
func TestFuseNormalizesAFlatChannelToNeutral(t *testing.T) {
	pool := poolOf(map[int64]float64{1: 1, 2: 1}, candidate(1, "A"), candidate(2, "B"))
	inputs := []ChannelInput{
		{Channel: retrieval.ChannelStructured, Ran: true, Hits: []retrieval.ChannelHit{
			{RestaurantID: 1, Score: 1, Reason: "满足硬条件"},
			{RestaurantID: 2, Score: 1, Reason: "满足硬条件"},
		}},
	}
	got := Fuse(inputs, pool, Options{Weights: Weights{Structured: 1}, TopK: 10})
	for _, entry := range got.Trace.Candidates {
		for _, channel := range entry.Channels {
			if channel.Normalized != 0.5 {
				t.Fatalf("flat channel must normalize to 0.5, got %v", channel.Normalized)
			}
		}
	}
}

// With the prior switched off, two restaurants that differ only in prior must
// score identically — otherwise the weight is not actually zero.
func TestFuseQualityWeightZeroRemovesThePrior(t *testing.T) {
	pool := poolOf(map[int64]float64{1: 0.1, 2: 0.9}, candidate(1, "A"), candidate(2, "B"))
	inputs := []ChannelInput{
		{Channel: retrieval.ChannelStructured, Ran: true, Hits: []retrieval.ChannelHit{
			{RestaurantID: 1, Score: 1, Reason: "满足硬条件"},
			{RestaurantID: 2, Score: 1, Reason: "满足硬条件"},
		}},
	}
	withPrior := Fuse(inputs, pool, Options{Weights: Weights{Structured: 1, Quality: 0.5}, TopK: 10})
	without := Fuse(inputs, pool, Options{Weights: Weights{Structured: 1}, TopK: 10})

	if withPrior.Candidates[0].Score <= without.Candidates[0].Score {
		t.Fatalf("a positive prior weight must change the score: %v vs %v",
			withPrior.Candidates[0].Score, without.Candidates[0].Score)
	}
	if without.Candidates[0].Score != without.Candidates[1].Score {
		t.Fatalf("without a prior weight the two must tie, got %v and %v",
			without.Candidates[0].Score, without.Candidates[1].Score)
	}
	for _, entry := range without.Trace.Candidates {
		for _, channel := range entry.Channels {
			if channel.Channel == PriorChannel {
				t.Fatal("the prior must not appear when its weight is zero")
			}
		}
	}
}

// A reason has to name what matched. "Semantic match" is not checkable; the
// query and the score are.
func TestFuseReasonsNameTheChannelAndItsContribution(t *testing.T) {
	pool := poolOf(map[int64]float64{1: 1}, candidate(1, "A"))
	inputs := []ChannelInput{
		{Channel: retrieval.ChannelStructured, Ran: true, Hits: []retrieval.ChannelHit{
			{RestaurantID: 1, Score: 1, Reason: "满足硬条件（行政区=manhattan）"},
		}},
	}
	got := Fuse(inputs, pool, Options{Weights: Weights{Structured: 1}, TopK: 10})
	reasons := got.Candidates[0].Reasons
	if len(reasons) != 1 {
		t.Fatalf("want one reason, got %v", reasons)
	}
	if reasons[0] == "满足硬条件（行政区=manhattan）" {
		t.Fatal("the reason should carry its contribution, not just its text")
	}
}

// A channel that never ran must appear in the trace anyway, or a reader cannot
// tell "found nothing" from "was never asked".
func TestFuseTraceRecordsSkippedChannels(t *testing.T) {
	pool := poolOf(map[int64]float64{1: 1}, candidate(1, "A"))
	inputs := []ChannelInput{
		{Channel: retrieval.ChannelStructured, Ran: true, Hits: []retrieval.ChannelHit{
			{RestaurantID: 1, Score: 1, Reason: "满足硬条件"},
		}},
		{Channel: retrieval.ChannelVector, Ran: false, Note: "向量通道未启用"},
	}
	got := Fuse(inputs, pool, Options{Weights: Weights{Structured: 1, Vector: 1}, TopK: 10})

	var found bool
	for _, summary := range got.Trace.Channels {
		if summary.Channel != retrieval.ChannelVector {
			continue
		}
		found = true
		if summary.Ran {
			t.Fatal("the vector channel must be recorded as not run")
		}
		if summary.Weight != 1 {
			t.Fatalf("a skipped channel still reports its weight, got %v", summary.Weight)
		}
		if summary.Note == "" {
			t.Fatal("a skipped channel must explain itself")
		}
	}
	if !found {
		t.Fatal("the vector channel is missing from the trace entirely")
	}
	if len(got.Trace.Warnings) == 0 {
		t.Fatal("a degraded channel must surface as a trace warning")
	}
}

// Two documents for one restaurant must count once. Otherwise a restaurant with
// more chunks would outrank one with fewer, which is a property of the corpus
// rather than of the question.
func TestFuseDeduplicatesRepeatedHitsWithinAChannel(t *testing.T) {
	pool := poolOf(map[int64]float64{1: 1, 2: 1}, candidate(1, "A"), candidate(2, "B"))
	inputs := []ChannelInput{
		{Channel: retrieval.ChannelStructured, Ran: true, Hits: []retrieval.ChannelHit{
			{RestaurantID: 1, Score: 1, Reason: "满足硬条件"},
			{RestaurantID: 1, Score: 1, Reason: "满足硬条件"},
			{RestaurantID: 2, Score: 1, Reason: "满足硬条件"},
		}},
	}
	got := Fuse(inputs, pool, Options{Weights: Weights{Structured: 1}, TopK: 10})

	for _, entry := range got.Trace.Candidates {
		structured := 0
		for _, channel := range entry.Channels {
			if channel.Channel == retrieval.ChannelStructured && channel.Contrib > 0 {
				structured++
			}
		}
		if structured > 1 {
			t.Fatalf("restaurant %d was counted %d times by one channel", entry.RestaurantID, structured)
		}
	}
	one, two := got.Candidates[0].Score, got.Candidates[1].Score
	if one != two {
		t.Fatalf("two hits must not outrank one: %v vs %v", one, two)
	}
}

// A channel that names a restaurant the pool cannot describe must be dropped
// with a warning, not passed through as a result with no name.
func TestFuseDropsHitsForUnknownRestaurants(t *testing.T) {
	pool := poolOf(map[int64]float64{1: 1}, candidate(1, "A"))
	inputs := []ChannelInput{
		{Channel: retrieval.ChannelKeyword, Ran: true, Hits: []retrieval.ChannelHit{
			{RestaurantID: 1, Score: 0.8, Reason: "名称匹配"},
			{RestaurantID: 99, Score: 0.7, Reason: "名称匹配"},
		}},
	}
	got := Fuse(inputs, pool, Options{Weights: Weights{Keyword: 1}, TopK: 10})
	for _, c := range got.Candidates {
		if c.RestaurantID == 99 {
			t.Fatal("an undescribable restaurant must not be returned")
		}
	}
	if len(got.Trace.Warnings) == 0 {
		t.Fatal("dropping an unknown restaurant must be reported")
	}
}

func TestFuseReportsPoolAndReturnedCounts(t *testing.T) {
	pool := poolOf(map[int64]float64{1: 1, 2: 1, 3: 1},
		candidate(1, "A"), candidate(2, "B"), candidate(3, "C"))
	inputs := []ChannelInput{
		{Channel: retrieval.ChannelStructured, Ran: true, Hits: []retrieval.ChannelHit{
			{RestaurantID: 1, Score: 1, Reason: "满足硬条件"},
			{RestaurantID: 2, Score: 1, Reason: "满足硬条件"},
			{RestaurantID: 3, Score: 1, Reason: "满足硬条件"},
		}},
	}
	got := Fuse(inputs, pool, Options{Weights: Weights{Structured: 1}, TopK: 2})
	if got.Trace.CandidatePool != 3 {
		t.Fatalf("pool = %d, want 3", got.Trace.CandidatePool)
	}
	if got.Trace.Returned != 2 || len(got.Candidates) != 2 {
		t.Fatalf("returned = %d, want 2", got.Trace.Returned)
	}
	if len(got.Trace.Candidates) != len(got.Candidates) {
		t.Fatalf("the trace must be aligned with the candidates: %d vs %d",
			len(got.Trace.Candidates), len(got.Candidates))
	}
}

// The soft channels recall by text and by meaning, and neither can see a price
// level. Merging their rows without re-checking would let a restaurant the user
// explicitly excluded reappear through a fuzzy name match, inside a result that
// looks exactly like a correct one.
func TestFusionRejectsASoftChannelHitThatFailsTheHardFilter(t *testing.T) {
	expensive := search.RestaurantCandidate{
		RestaurantID: 2, Name: "Fancy", Borough: "manhattan",
		PriceLevel: intPtr(4), SnapshotAt: testSnapshot,
	}
	cheap := search.RestaurantCandidate{
		RestaurantID: 1, Name: "Diner", Borough: "manhattan",
		PriceLevel: intPtr(1), SnapshotAt: testSnapshot,
	}
	fused := Fuse([]ChannelInput{
		{
			Channel: retrieval.ChannelStructured, Ran: true,
			Hits: []retrieval.ChannelHit{{RestaurantID: 1, Score: 1}},
		},
		{
			// The keyword channel found a name, with no idea the request said $1.
			Channel: retrieval.ChannelKeyword, Ran: true,
			Hits: []retrieval.ChannelHit{{RestaurantID: 1, Score: 0.4}, {RestaurantID: 2, Score: 1}},
		},
	}, Pool{
		Candidates: map[int64]search.RestaurantCandidate{1: cheap, 2: expensive},
		Priors:     map[int64]float64{1: 1, 2: 1},
	}, Options{
		Weights: DefaultWeights, TopK: 5,
		Filter: search.RestaurantFilter{Borough: "manhattan", PriceLevels: []int{1}},
	})

	for _, candidate := range fused.Candidates {
		if candidate.RestaurantID == 2 {
			t.Fatalf("a $4 restaurant was returned for a $1 request")
		}
		if candidate.PriceLevel != nil && *candidate.PriceLevel != 1 {
			t.Fatalf("candidate %d has price %d, want 1",
				candidate.RestaurantID, *candidate.PriceLevel)
		}
	}
	if len(fused.Trace.Warnings) == 0 {
		t.Fatal("a filter that removed candidates must say so in the trace")
	}
}

// A missing value is not a pass. A candidate with no rating cannot satisfy a
// minimum-rating condition, and treating unknown as satisfied is the bug this
// check exists to prevent.
func TestFusionDoesNotTreatAMissingValueAsAMatch(t *testing.T) {
	unrated := search.RestaurantCandidate{RestaurantID: 1, Name: "New", Borough: "manhattan"}
	fused := Fuse([]ChannelInput{{
		Channel: retrieval.ChannelKeyword, Ran: true,
		Hits: []retrieval.ChannelHit{{RestaurantID: 1, Score: 1}},
	}}, Pool{
		Candidates: map[int64]search.RestaurantCandidate{1: unrated},
		Priors:     map[int64]float64{1: 1},
	}, Options{
		Weights: DefaultWeights, TopK: 5,
		Filter: search.RestaurantFilter{MinRating: floatPtr(4)},
	})
	if len(fused.Candidates) != 0 {
		t.Fatalf("an unrated restaurant cannot satisfy min_rating: %+v", fused.Candidates)
	}
}

// With no filter stated there is nothing to re-check, and every recalled
// candidate stays.
func TestFusionKeepsEveryCandidateWhenNoFilterIsStated(t *testing.T) {
	a := search.RestaurantCandidate{RestaurantID: 1, Borough: "manhattan"}
	b := search.RestaurantCandidate{RestaurantID: 2, Borough: "brooklyn"}
	fused := Fuse([]ChannelInput{{
		Channel: retrieval.ChannelKeyword, Ran: true,
		Hits: []retrieval.ChannelHit{{RestaurantID: 1, Score: 1}, {RestaurantID: 2, Score: 0.9}},
	}}, Pool{
		Candidates: map[int64]search.RestaurantCandidate{1: a, 2: b},
		Priors:     map[int64]float64{1: 1, 2: 1},
	}, Options{Weights: DefaultWeights, TopK: 5})

	if len(fused.Candidates) != 2 {
		t.Fatalf("want both candidates, got %d", len(fused.Candidates))
	}
	if len(fused.Trace.Warnings) != 0 {
		t.Fatalf("an unfiltered search should report no filter warnings: %v", fused.Trace.Warnings)
	}
}

func intPtr(v int) *int { return &v }
