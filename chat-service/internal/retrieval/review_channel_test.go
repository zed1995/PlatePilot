package retrieval

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/domain/retrieval"
	"github.com/zed1995/platepilot/shared/domain/search"
	"github.com/zed1995/platepilot/shared/store"
)

// semanticService wires both semantic channels over one repository, with an
// optional mutation for the case-specific knobs. The channel switches stay
// explicit for the same reason vectorService's does: a zero value cannot say
// whether a caller asked for a channel off or did not care.
func semanticService(
	t *testing.T, repo *stubRestaurants, knowledge *stubKnowledge,
	embedding *stubEmbedding, mutate func(*ServiceConfig),
) *Service {
	t.Helper()
	service := newService(t, repo, ServiceConfig{})
	service.knowledge = knowledge
	service.embedding = embedding
	service.cfg.EnableVector = true
	service.cfg.EnableReview = true
	if mutate != nil {
		mutate(&service.cfg)
	}
	if service.cfg.EmbeddingTimeout == 0 {
		service.cfg.EmbeddingTimeout = DefaultServiceConfig.EmbeddingTimeout
	}
	return service
}

// traceChannel returns one channel's trace row.
func traceChannel(t *testing.T, result retrieval.SearchResult, channel retrieval.Channel) retrieval.ChannelSummary {
	t.Helper()
	for _, summary := range result.Trace.Channels {
		if summary.Channel == channel {
			return summary
		}
	}
	t.Fatalf("the %s channel is missing from the trace", channel)
	return retrieval.ChannelSummary{}
}

// traceCandidate returns one candidate's score derivation from the trace.
func traceCandidate(t *testing.T, result retrieval.SearchResult, id int64) retrieval.CandidateScore {
	t.Helper()
	return candidateScoreOf(t, result.Trace.Candidates, id)
}

// candidateScoreOf finds one candidate's derivation in a trace's candidate
// list, whatever produced it.
func candidateScoreOf(t *testing.T, candidates []retrieval.CandidateScore, id int64) retrieval.CandidateScore {
	t.Helper()
	for _, entry := range candidates {
		if entry.RestaurantID == id {
			return entry
		}
	}
	t.Fatalf("candidate %d is missing from the trace", id)
	return retrieval.CandidateScore{}
}

// traceChannelScore returns one channel's term of a candidate's derivation.
func traceChannelScore(t *testing.T, entry retrieval.CandidateScore, channel retrieval.Channel) retrieval.ChannelScore {
	t.Helper()
	for _, score := range entry.Channels {
		if score.Channel == channel {
			return score
		}
	}
	t.Fatalf("candidate %d has no %s term", entry.RestaurantID, channel)
	return retrieval.ChannelScore{}
}

// The review channel recalls the digest corpus, pins its doc type so it cannot
// read the profile corpus, and quotes the digest's own facts — similarity and
// how many reviews the offline reading was built from.
func TestReviewChannelRecallsDigests(t *testing.T) {
	repo := &stubRestaurants{details: map[int64]search.RestaurantDetail{
		2: {Name: "Rooftop Garden", Address: "9 Rooftop Way", Borough: "manhattan", RatingCount: 220},
	}}
	knowledge := &stubKnowledge{digestDocs: []store.ScoredDocument{
		scoredDigestDoc(7, 2, "Rooftop Garden", 0.10, 12),
	}}
	embedding := &stubEmbedding{vector: []float32{1, 0, 0}}

	service := semanticService(t, repo, knowledge, embedding,
		func(c *ServiceConfig) { c.EnableVector = false })
	got, err := service.Search(context.Background(), reqWithQuery("romantic dinner"))
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got.Candidates) != 1 || got.Candidates[0].RestaurantID != 2 {
		t.Fatalf("want the digest hit, got %v", ids(got.Candidates))
	}
	// The recall must be scoped to the digest corpus: an unpinned request would
	// let this channel read the profile documents the vector channel recalls.
	if len(knowledge.got.DocTypes) != 1 ||
		knowledge.got.DocTypes[0] != evidence.DocTypeRestaurantReviewDigest {
		t.Fatalf("doc types = %v, want the digest corpus pinned", knowledge.got.DocTypes)
	}
	if knowledge.got.Scope != evidence.ScopeRestaurant {
		t.Fatalf("scope = %q, want the restaurant scope", knowledge.got.Scope)
	}
	summary := traceChannel(t, got, retrieval.ChannelReview)
	if !summary.Ran {
		t.Fatal("the review channel must be recorded as run")
	}
	// The reason is the digest's own wording, and it names the offline reading
	// for what it is: an understanding built from N reviews, not a quote.
	joined := strings.Join(got.Candidates[0].Reasons, "；")
	want := "评论综合语义匹配“romantic dinner”（相似度 0.900，基于 12 条评论的离线理解）"
	if !strings.Contains(joined, want) {
		t.Fatalf("reasons %v must contain %q", got.Candidates[0].Reasons, want)
	}
}

// A digest similarity below the floor is a non-match: it neither joins the pool
// nor reaches the ranking, and the channel row says how many hits the gate
// refused so a weakened page is a measured fact rather than a silence.
func TestReviewChannelGatesBelowTheSimilarityFloor(t *testing.T) {
	repo := &stubRestaurants{details: map[int64]search.RestaurantDetail{
		3: {Name: "Calm Corner", Borough: "manhattan"},
	}}
	knowledge := &stubKnowledge{digestDocs: []store.ScoredDocument{
		scoredDigestDoc(7, 3, "Calm Corner", 0.20, 30), // similarity 0.80
		scoredDigestDoc(8, 4, "Noisy Den", 0.90, 2),    // similarity 0.10
	}}
	embedding := &stubEmbedding{vector: []float32{1, 0, 0}}

	service := semanticService(t, repo, knowledge, embedding, func(c *ServiceConfig) {
		c.EnableVector = false
		c.ReviewMinSim = 0.30
	})
	got, err := service.Search(context.Background(), reqWithQuery("quiet dinner"))
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got.Candidates) != 1 || got.Candidates[0].RestaurantID != 3 {
		t.Fatalf("the gated hit must not surface, got %v", ids(got.Candidates))
	}
	note := traceChannel(t, got, retrieval.ChannelReview).Note
	if !strings.Contains(note, "1 篇命中低于相似度阈值（0.30）") {
		t.Fatalf("note = %q, want the gate's count and floor", note)
	}
}

// A digest hit whose restaurant cannot be read back is dropped loudly: the hit
// stays on the channel row, the id is reported as unknown, and the warning
// reaches the trace. Silently ranking a candidate the UI cannot display is the
// bug this replaces.
func TestReviewChannelDropsABackedfillFailureLoudly(t *testing.T) {
	// No details at all: every read-back fails.
	repo := &stubRestaurants{}
	knowledge := &stubKnowledge{digestDocs: []store.ScoredDocument{
		scoredDigestDoc(7, 2, "Ghost Kitchen", 0.10, 6),
	}}
	embedding := &stubEmbedding{vector: []float32{1, 0, 0}}

	service := semanticService(t, repo, knowledge, embedding,
		func(c *ServiceConfig) { c.EnableVector = false })
	got, err := service.Search(context.Background(), reqWithQuery("ghost kitchen"))
	if err != nil {
		t.Fatalf("a backfill failure must not fail the search: %v", err)
	}
	if len(got.Candidates) != 0 {
		t.Fatalf("an undisplayable candidate must not be returned, got %v", ids(got.Candidates))
	}
	summary := traceChannel(t, got, retrieval.ChannelReview)
	if len(summary.DroppedRestaurantIDs) != 1 || summary.DroppedRestaurantIDs[0] != 2 {
		t.Fatalf("dropped = %v, want the unreadable id reported", summary.DroppedRestaurantIDs)
	}
	var warned bool
	for _, warning := range got.Trace.Warnings {
		// The channel renders by its constant name in this sentence, as every
		// channel's unknown-id warning does.
		if strings.Contains(warning, "review 通道返回了 1 家候选池外的未知餐厅") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("the drop must reach the warnings, got %v", got.Trace.Warnings)
	}
}

// The profile and the digest corpora are embedded by the same model against
// the same question, so one embedding must serve both channels: paying the
// read path's slowest step twice for identical vectors is a latency bug.
func TestOneQueryEmbeddingServesBothSemanticChannels(t *testing.T) {
	repo := &stubRestaurants{details: map[int64]search.RestaurantDetail{
		2: {Name: "Rooftop Garden", Borough: "manhattan"},
	}}
	knowledge := &stubKnowledge{
		docs:       []store.ScoredDocument{scoredDoc(1, 2, "Rooftop Garden", 0.10)},
		digestDocs: []store.ScoredDocument{scoredDigestDoc(7, 2, "Rooftop Garden", 0.20, 9)},
	}
	embedding := &stubEmbedding{vector: []float32{1, 0, 0}}

	service := semanticService(t, repo, knowledge, embedding, nil)
	got, err := service.Search(context.Background(), reqWithQuery("romantic dinner"))
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if embedding.calls != 1 {
		t.Fatalf("embedding called %d times, want exactly once for both channels", embedding.calls)
	}
	if knowledge.recallCall != 2 {
		t.Fatalf("semantic recalls = %d, want one per doc type", knowledge.recallCall)
	}
	if got.Trace.QueryEmbeddingDim != 3 {
		t.Fatalf("query embedding dim = %d, want the vector that ran", got.Trace.QueryEmbeddingDim)
	}
	for _, channel := range []retrieval.Channel{retrieval.ChannelVector, retrieval.ChannelReview} {
		if !traceChannel(t, got, channel).Ran {
			t.Fatalf("the %s channel must be recorded as run", channel)
		}
	}
}

// The pool rescore scores every candidate's profile and digest documents with
// the exact cosine, once per doc type. The exact scores then are the fused
// ones — the ANN pages only decided what entered the pool and how a hit is
// labelled — and a digest score below the floor is treated as the channel
// being absent, not as a weak hit.
func TestPoolRescoreFusesExactScoresAndGatesTheDigestFloor(t *testing.T) {
	repo := &stubRestaurants{
		searchRows: []search.RestaurantCandidate{row(1, "A", 0), row(2, "B", 0), row(3, "C", 0)},
		details:    map[int64]search.RestaurantDetail{1: {}, 2: {}, 3: {}},
	}
	exactDoc := func(id int64, distance float64, metadata map[string]any) store.ScoredDocument {
		return store.ScoredDocument{
			KnowledgeDocument: evidence.KnowledgeDocument{RestaurantID: id, Metadata: metadata},
			Distance:          distance,
		}
	}
	knowledge := &stubKnowledge{poolDocs: map[evidence.DocType][]store.ScoredDocument{
		evidence.DocTypeRestaurantProfile: {
			exactDoc(1, 0.30, nil), // exact similarity 0.70
			exactDoc(2, 0.05, nil), // exact similarity 0.95
			exactDoc(3, 0.60, nil), // exact similarity 0.40
		},
		evidence.DocTypeRestaurantReviewDigest: {
			exactDoc(1, 0.10, map[string]any{"input_review_count": 40}), // 0.90
			exactDoc(2, 0.85, nil), // 0.15, below the floor
			exactDoc(3, 0.20, nil), // 0.80
		},
	}}
	embedding := &stubEmbedding{vector: []float32{1, 0, 0}}

	service := semanticService(t, repo, knowledge, embedding, func(c *ServiceConfig) {
		c.EnablePoolRescore = true
		c.ReviewMinSim = 0.30
	})
	got, err := service.Search(context.Background(), reqWithQuery("somewhere warm"))
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	// One exact query per doc type, over the whole merged pool in a stable id
	// order, so a replay reads back the same request.
	if len(knowledge.poolCalls) != 2 {
		t.Fatalf("pool rescore calls = %d, want one per doc type", len(knowledge.poolCalls))
	}
	seenTypes := map[evidence.DocType]bool{}
	for _, call := range knowledge.poolCalls {
		seenTypes[call.DocType] = true
		if call.Scope != evidence.ScopeRestaurant {
			t.Fatalf("rescore scope = %q, want the restaurant scope", call.Scope)
		}
		if len(call.RestaurantIDs) != 3 || call.RestaurantIDs[0] != 1 || call.RestaurantIDs[2] != 3 {
			t.Fatalf("rescore ids = %v, want the whole pool sorted", call.RestaurantIDs)
		}
	}
	if !seenTypes[evidence.DocTypeRestaurantProfile] || !seenTypes[evidence.DocTypeRestaurantReviewDigest] {
		t.Fatalf("rescore doc types = %v, want profile and digest", seenTypes)
	}

	// The fused raw score is the exact cosine, not an ANN distance: candidate 2
	// was never on any ANN page, so only the rescore can speak for it.
	entry := traceCandidate(t, got, 2)
	vectorTerm := traceChannelScore(t, entry, retrieval.ChannelVector)
	if math.Abs(vectorTerm.Raw-0.95) > 1e-9 {
		t.Fatalf("vector raw = %v, want the exact 0.95", vectorTerm.Raw)
	}
	if vectorTerm.Source != retrieval.SourceRescored {
		t.Fatalf("vector source = %q, want %q", vectorTerm.Source, retrieval.SourceRescored)
	}
	// Candidate 1 was also never recalled by an ANN page; its digest term
	// quotes the same reading the reason does.
	entry = traceCandidate(t, got, 1)
	digestTerm := traceChannelScore(t, entry, retrieval.ChannelReview)
	if math.Abs(digestTerm.Raw-0.90) > 1e-9 {
		t.Fatalf("digest raw = %v, want the exact 0.90", digestTerm.Raw)
	}
	if digestTerm.Source != retrieval.SourceRescored {
		t.Fatalf("digest source = %q, want %q", digestTerm.Source, retrieval.SourceRescored)
	}
	if !strings.Contains(digestTerm.Reason, "基于 40 条评论的离线理解") {
		t.Fatalf("digest reason = %q, want the offline reading named", digestTerm.Reason)
	}
	// Candidate 2's digest score fell below the floor: the channel is absent
	// for it, which is the empty-term shape, not a small contribution.
	digestTerm2 := traceChannelScore(t, traceCandidate(t, got, 2), retrieval.ChannelReview)
	if digestTerm2.Contrib != 0 || digestTerm2.Raw != 0 || digestTerm2.Source != "" {
		t.Fatalf("gated digest term = %+v, want the channel-absent shape", digestTerm2)
	}
	note := traceChannel(t, got, retrieval.ChannelReview).Note
	if !strings.Contains(note, "补分后 1 家低于相似度阈值（0.30），按通道缺席处理") {
		t.Fatalf("review note = %q, want the gated count", note)
	}
	if got.Trace.FusionMethod != FusionWeighted {
		t.Fatalf("fusion method = %q, want the default recorded", got.Trace.FusionMethod)
	}
}

// A rescore that fails is a degradation, not a silence: the ranking falls back
// to the recall pages' approximate scores and the trace says so.
func TestARescoreFailureFallsBackToTheRecallScores(t *testing.T) {
	repo := &stubRestaurants{
		searchRows: []search.RestaurantCandidate{row(1, "A", 0)},
		details:    map[int64]search.RestaurantDetail{1: {}},
	}
	knowledge := &stubKnowledge{
		docs:    []store.ScoredDocument{scoredDoc(1, 1, "A", 0.10)},
		poolErr: errors.New("boom"),
	}
	embedding := &stubEmbedding{vector: []float32{1, 0, 0}}

	service := semanticService(t, repo, knowledge, embedding,
		func(c *ServiceConfig) { c.EnablePoolRescore = true })
	got, err := service.Search(context.Background(), reqWithQuery("quiet"))
	if err != nil {
		t.Fatalf("a rescore failure must not fail the search: %v", err)
	}
	entry := traceCandidate(t, got, 1)
	vectorTerm := traceChannelScore(t, entry, retrieval.ChannelVector)
	if math.Abs(vectorTerm.Raw-0.90) > 1e-9 {
		t.Fatalf("vector raw = %v, want the ANN similarity 0.90", vectorTerm.Raw)
	}
	if vectorTerm.Source != retrieval.SourceRecalled {
		t.Fatalf("vector source = %q, want %q", vectorTerm.Source, retrieval.SourceRecalled)
	}
	var warned bool
	for _, warning := range got.Trace.Warnings {
		if strings.Contains(warning, "池内精确补分失败，沿用召回近似分") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("the fallback must reach the warnings, got %v", got.Trace.Warnings)
	}
}

// The configured fusion algorithm is recorded on every trace, whatever it
// produced: a replay cannot be compared with the original if it cannot tell
// which algorithm ranked the list.
func TestTheFusionMethodIsRecordedOnTheTrace(t *testing.T) {
	repo := &stubRestaurants{
		searchRows: []search.RestaurantCandidate{row(1, "A", 0)},
		details:    map[int64]search.RestaurantDetail{1: {}},
	}
	knowledge := &stubKnowledge{}
	embedding := &stubEmbedding{vector: []float32{1}}

	weighted := semanticService(t, repo, knowledge, embedding, nil)
	got, err := weighted.Search(context.Background(), reqWithQuery("quiet"))
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got.Trace.FusionMethod != FusionWeighted {
		t.Fatalf("default fusion method = %q, want %q", got.Trace.FusionMethod, FusionWeighted)
	}

	rrf := semanticService(t, repo, knowledge, embedding,
		func(c *ServiceConfig) { c.FusionMethod = FusionRRF })
	got, err = rrf.Search(context.Background(), reqWithQuery("quiet"))
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got.Trace.FusionMethod != FusionRRF {
		t.Fatalf("fusion method = %q, want %q", got.Trace.FusionMethod, FusionRRF)
	}
}

// rfCandidate builds a displayable pool candidate for the direct-Fuse tests.
func rfCandidate(id int64) search.RestaurantCandidate {
	return search.RestaurantCandidate{
		RestaurantID: id,
		Name:         "place",
		SnapshotAt:   testSnapshot,
		Borough:      "manhattan",
	}
}

// RRF ranks by reciprocal rank, so equal raw scores must land in a stable
// order: same ranks, and the restaurant id breaks the resulting score tie.
func TestRRFFusionIsDeterministicAndBreaksTiesByID(t *testing.T) {
	inputs := []ChannelInput{{
		Channel: retrieval.ChannelStructured,
		Ran:     true,
		Hits: []retrieval.ChannelHit{
			{RestaurantID: 7, Score: 1},
			{RestaurantID: 3, Score: 1},
		},
	}}
	opts := Options{Weights: Weights{Structured: 1}, TopK: 10, FusionMethod: FusionRRF}
	run := func() Fused {
		return Fuse(inputs, Pool{
			Candidates: map[int64]search.RestaurantCandidate{3: rfCandidate(3), 7: rfCandidate(7)},
			Priors:     map[int64]float64{},
		}, opts)
	}
	first, second := run(), run()
	for i := range first.Candidates {
		if first.Candidates[i].RestaurantID != second.Candidates[i].RestaurantID {
			t.Fatalf("order differs between runs: %d vs %d",
				first.Candidates[i].RestaurantID, second.Candidates[i].RestaurantID)
		}
		if first.Candidates[i].Score != second.Candidates[i].Score {
			t.Fatalf("score differs between runs for rank %d", i)
		}
	}
	// The two candidates tie on every term; the id decides.
	if first.Candidates[0].RestaurantID != 3 || first.Candidates[1].RestaurantID != 7 {
		t.Fatalf("tie not broken by id: %d then %d",
			first.Candidates[0].RestaurantID, first.Candidates[1].RestaurantID)
	}
}

// The semantic channels share one set of normalization bounds. Normalizing the
// digest channel on its own would stretch a weak digest match to 1.0 exactly
// when the profile channel's strongest hit is also 1.0, making the two
// incomparable inside one ranking.
func TestSemanticFamilyBoundsAreSharedAcrossItsChannels(t *testing.T) {
	inputs := []ChannelInput{
		{Channel: retrieval.ChannelVector, Ran: true, Hits: []retrieval.ChannelHit{
			{RestaurantID: 1, Score: 0.6},
			{RestaurantID: 2, Score: 0.9},
		}},
		{Channel: retrieval.ChannelReview, Ran: true, Hits: []retrieval.ChannelHit{
			{RestaurantID: 3, Score: 0.3},
		}},
	}
	got := Fuse(inputs, Pool{
		Candidates: map[int64]search.RestaurantCandidate{
			1: rfCandidate(1), 2: rfCandidate(2), 3: rfCandidate(3),
		},
		Priors: map[int64]float64{},
	}, Options{Weights: Weights{Vector: 1, Review: 1}, TopK: 10})

	familyBounds := func(entry retrieval.CandidateScore, channel retrieval.Channel) float64 {
		return traceChannelScore(t, entry, channel).Normalized
	}
	// Shared family bounds are [0.3, 0.9]: the vector 0.6 lands mid-scale and
	// the digest 0.3 lands at 0, where a per-channel scale would have mapped it
	// to the neutral 0.5.
	vec1 := familyBounds(candidateScoreOf(t, got.Trace.Candidates, 1), retrieval.ChannelVector)
	if math.Abs(vec1-0.5) > 1e-9 {
		t.Fatalf("vector 0.6 normalized to %v, want 0.5 on the family scale", vec1)
	}
	digest3 := familyBounds(candidateScoreOf(t, got.Trace.Candidates, 3), retrieval.ChannelReview)
	if digest3 != 0 {
		t.Fatalf("digest 0.3 normalized to %v, want 0 on the family scale", digest3)
	}

	// With the review channel not running, the family collapses to the vector
	// channel's own scale — exactly the per-channel behaviour that existed
	// before the review channel did.
	got = Fuse(inputs[:1], Pool{
		Candidates: map[int64]search.RestaurantCandidate{1: rfCandidate(1), 2: rfCandidate(2)},
		Priors:     map[int64]float64{},
	}, Options{Weights: Weights{Vector: 1}, TopK: 10})
	low := familyBounds(candidateScoreOf(t, got.Trace.Candidates, 1), retrieval.ChannelVector)
	if low != 0 {
		t.Fatalf("vector 0.6 normalized to %v without the review channel, want 0", low)
	}
	high := familyBounds(candidateScoreOf(t, got.Trace.Candidates, 2), retrieval.ChannelVector)
	if high != 1 {
		t.Fatalf("vector 0.9 normalized to %v without the review channel, want 1", high)
	}
}
