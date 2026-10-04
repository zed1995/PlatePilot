package retrieval

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/domain/retrieval"
	"github.com/zed1995/platepilot/shared/domain/search"
	"github.com/zed1995/platepilot/shared/embedding"
	"github.com/zed1995/platepilot/shared/observability/logging"
	"github.com/zed1995/platepilot/shared/rerank"
	"github.com/zed1995/platepilot/shared/store"
)

// ServiceConfig is the retrieval layer's tunable behaviour.
type ServiceConfig struct {
	Weights Weights
	// Oversample is how much deeper than TopK each channel reads. Without it a
	// channel that fills its own page can hide a restaurant another channel
	// ranked first, and fusion can only rerank what it was given.
	Oversample int
	TopK       int
	// EnableStructured, EnableKeyword and EnableVector switch individual
	// channels off. A disabled channel is reported in the trace as not run, so
	// a weakened ranking is visible rather than silent.
	EnableStructured bool
	EnableKeyword    bool
	EnableVector     bool
	// EmbeddingTimeout bounds the on-demand query embedding. It is separate from
	// the pipeline's request timeout because that one is sized for embedding a
	// batch of long documents on a CPU model; an online recall embeds one short
	// string and must not inherit a three-minute wait.
	EmbeddingTimeout time.Duration
}

// structuredOverread multiplies the structured channel's depth.
//
// The channel's rows are unordered with respect to the question — every one of
// them satisfies the filter — so the only thing its page size decides is how many
// correct answers fusion ever learns about. Overreading costs a wider page scan
// and buys the guarantee that a correct restaurant is not excluded by where the
// list happened to stop.
const structuredOverread = 10

// maxStructuredDepth bounds the overread so a filter matching the whole corpus
// cannot turn one request into a table scan. The bound is a page, not a
// judgement about how many restaurants exist: past it, a candidate recalled by
// another channel still competes on that channel's own score.
const maxStructuredDepth = 500

// vectorOverread multiplies the vector channel's depth, for the same reason the
// structured channel overreads: the page size decides how many correct answers
// fusion ever learns about.
//
// It is not a tuning knob that happened to help, and the number is not chosen
// to move a score. The channel used to read one page of ten documents out of
// thirty thousand. Ten is about the size of the answer itself, so the semantic
// channel was not recalling a pool for fusion to rank — it was naming the
// answer, and everything outside its own top ten was invisible to the ranking
// no matter how well the other channels scored it.
//
// Measured on the M6-02 fixture set, the same code and corpus with the page at
// ten versus a hundred: the share of a returned page that actually answers the
// question asked falls from 0.96 to 0.62, and one query in eighty-six comes
// back empty. Recall over the same fixtures barely moves, 0.857 against 0.843,
// because recall only asks whether a handful of named restaurants appeared —
// which is why the shallow page survived review for so long. The page looked
// fine to the metric that was being watched and wrong to the user reading it.
const vectorOverread = 10

// maxVectorDepth bounds that overread. An HNSW recall is not a table scan, but
// every row it returns is also a row the service reads back one indexed point
// lookup at a time to make it displayable, so the page has a real cost.
const maxVectorDepth = 200

// DefaultServiceConfig is the configuration used when nothing is set.
var DefaultServiceConfig = ServiceConfig{
	Weights:          DefaultWeights,
	Oversample:       2,
	TopK:             defaultTopK,
	EnableStructured: true,
	EnableKeyword:    true,
	// The vector channel is part of the package default rather than opt-in: a
	// search that silently drops the only channel able to read a soft condition
	// looks like it ranked well and did not. Deployments that have not built a
	// vector index turn it off at the configuration layer instead, which is
	// where the decision to spend the embedding latency belongs.
	EnableVector:     true,
	EmbeddingTimeout: 5 * time.Second,
}

// Service answers restaurant searches.
//
// It owns no state beyond its dependencies, so it is safe to share across
// requests and cheap to construct in a test.
type Service struct {
	restaurants store.RestaurantRepository
	knowledge   store.KnowledgeRepository
	embedding   embedding.EmbeddingProvider
	rerank      rerank.RerankProvider
	cfg         ServiceConfig
	logger      *slog.Logger
}

// Deps are the ports the service is assembled from. Embedding and rerank are
// optional: a search without them still answers, degraded, and says so.
type Deps struct {
	Restaurants store.RestaurantRepository
	Knowledge   store.KnowledgeRepository
	Embedding   embedding.EmbeddingProvider
	Rerank      rerank.RerankProvider
	Logger      *slog.Logger
}

// NewService builds the retrieval service.
func NewService(cfg ServiceConfig, deps Deps) (*Service, error) {
	if deps.Restaurants == nil {
		return nil, errs.New(errs.CodeInvalidArgument, "retrieval requires a restaurant repository")
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Oversample <= 0 {
		cfg.Oversample = DefaultServiceConfig.Oversample
	}
	if cfg.TopK <= 0 {
		cfg.TopK = DefaultServiceConfig.TopK
	}
	if cfg.TopK > maxTopK {
		cfg.TopK = maxTopK
	}
	if cfg.EmbeddingTimeout <= 0 {
		cfg.EmbeddingTimeout = DefaultServiceConfig.EmbeddingTimeout
	}
	return &Service{
		restaurants: deps.Restaurants,
		knowledge:   deps.Knowledge,
		embedding:   deps.Embedding,
		rerank:      deps.Rerank,
		cfg:         cfg,
		logger:      logger,
	}, nil
}

// Search runs the recall channels and fuses them into one ranked list.
//
// A request with neither text nor filters is refused rather than answered with
// the corpus in prior order: that answer looks like a result, and every
// restaurant in it would be presented as though the question had found it.
func (s *Service) Search(ctx context.Context, req retrieval.Request) (retrieval.SearchResult, error) {
	logger := logging.FromContext(ctx).With(
		slog.String("retrieval.scope", "restaurant"))
	logger.Debug("search started",
		slog.String("query", req.Query),
		slog.String("text", req.Text),
		slog.String("filters", describeFilters(req.Filter)))

	if err := req.Filter.Validate(); err != nil {
		return retrieval.SearchResult{}, err
	}
	text := strings.TrimSpace(req.Text)
	query := strings.TrimSpace(req.Query)
	if text == "" && query == "" && req.Filter.IsEmpty() && !req.HasSoftConditions() {
		return retrieval.SearchResult{}, errs.New(errs.CodeRetrievalEmptyQuery,
			"a search needs text, a filter, or both")
	}

	topK := s.pageSize(req.TopK)
	depth := topK * s.cfg.Oversample

	inputs := make([]ChannelInput, 0, 3)
	candidates := make(map[int64]search.RestaurantCandidate, depth)
	priors := make(map[int64]float64, depth)

	structuredInput, structuredCandidates, structuredPriors, err := s.structuredChannel(ctx, req.Filter, depth)
	if err != nil {
		// The structured channel is the only one whose results are guaranteed
		// to satisfy the stated conditions, so its failure is the search's
		// failure. Returning a fuzzy list here would answer a different
		// question than the one asked.
		logger.Error("structured channel failed", slog.String("error", err.Error()))
		return retrieval.SearchResult{}, err
	}
	inputs = append(inputs, structuredInput)
	mergePool(candidates, priors, structuredCandidates, structuredPriors)

	keywordInput, keywordCandidates, keywordPriors, err := s.keywordChannel(ctx, text, depth)
	if err != nil {
		// A request defect is not a channel failure. Text too short to index is
		// something the caller can fix, and degrading would answer it with an
		// empty list that reads as "no such restaurant" rather than "your query
		// was unusable". Only the genuinely survivable failures continue.
		if errs.CodeOf(err) == errs.CodeRetrievalQueryTooShort ||
			errs.CodeOf(err) == errs.CodeRetrievalEmptyQuery {
			return retrieval.SearchResult{}, err
		}
		// A keyword failure is survivable: the structured channel still holds,
		// and fusion is designed for a channel to come back empty. It is
		// recorded rather than raised so the caller learns the ranking is
		// weaker without also learning the search failed.
		logger.Warn("keyword channel failed", slog.String("error", err.Error()))
		// Rebuild the input rather than mutating the returned one: the channel
		// returns an empty input on failure, and overwriting its channel field
		// would be a channel that ran and found nothing.
		keywordInput = ChannelInput{
			Channel: retrieval.ChannelKeyword,
			Note:    "关键词通道不可用：" + string(errs.CodeOf(err)),
		}
	}
	inputs = append(inputs, keywordInput)
	mergePool(candidates, priors, keywordCandidates, keywordPriors)

	vectorInput, vectorCandidates, vectorPriors, vectorDim, err :=
		s.vectorChannel(ctx, query, req.Filter, req.SoftConditions, depth)
	if err != nil {
		// Unlike the other two channels, the vector one can refuse outright: see
		// vectorChannel for why a misconfigured corpus is not something to
		// degrade around.
		return retrieval.SearchResult{}, err
	}
	inputs = append(inputs, vectorInput)
	mergePool(candidates, priors, vectorCandidates, vectorPriors)

	fused := Fuse(inputs, Pool{Candidates: candidates, Priors: priors}, Options{
		Weights: s.cfg.Weights,
		TopK:    topK,
		Filter:  req.Filter,
		Query:   firstNonEmpty(query, text),
	})

	if s.embedding != nil {
		fused.Trace.EmbeddingModelID = s.embedding.ModelID()
	}
	// The model and width are recorded even when the channel did not run. A
	// replay of this trace has to name the model that produced it, and a trace
	// that only records the model on a successful recall cannot explain a
	// result produced by a run where the recall degraded.
	fused.Trace.QueryEmbeddingDim = vectorDim

	final := ApplyRerank(ctx, firstNonEmpty(query, text), fused.Candidates, s.rerank)
	fused.Candidates = final.Candidates
	fused.Trace.RerankApplied = final.Applied
	fused.Trace.RerankModelID = final.ModelID

	logger.Info("search completed",
		slog.Int("candidate_pool", fused.Trace.CandidatePool),
		slog.Int("returned", fused.Trace.Returned),
		slog.Bool("rerank_applied", fused.Trace.RerankApplied),
		slog.Int("warnings", len(fused.Trace.Warnings)))

	return retrieval.SearchResult{Candidates: fused.Candidates, Trace: fused.Trace}, nil
}

// structuredChannel runs the deterministic hard-filter channel.
func (s *Service) structuredChannel(
	ctx context.Context, filter search.RestaurantFilter, depth int,
) (ChannelInput, map[int64]search.RestaurantCandidate, map[int64]float64, error) {
	if !s.cfg.EnableStructured {
		return ChannelInput{
			Channel: retrieval.ChannelStructured,
			Note:    "结构化通道已关闭",
		}, nil, nil, nil
	}
	// A filterless structured search is refused rather than served, because a
	// list of the corpus in prior order is not an answer to anything. The
	// keyword channel carries a text-only search on its own.
	if filter.IsEmpty() {
		return ChannelInput{
			Channel: retrieval.ChannelStructured,
			Note:    "无过滤条件，结构化通道跳过",
		}, nil, nil, nil
	}

	// The structured channel reads deeper than it could need to return.
	//
	// Its hits are not a ranking — every row satisfies the filter equally — so
	// where the list is cut has no bearing on which restaurants are correct
	// answers. But fusion scores a candidate's absence here as zero, and a
	// candidate the repository never returned is absent for the only reason that
	// it fell past an arbitrary row limit. A borough-only filter in Manhattan
	// matches two thousand rows; taking ten of them means two thousand
	// restaurants the user would have accepted score as though they had failed
	// the filter.
	//
	// Reading a page wide enough to cover the plausible result set turns that
	// from a silent exclusion into a bounded one, and the cost is rows that are
	// scored and discarded rather than never seen.
	structuredDepth := depth * structuredOverread
	if structuredDepth > maxStructuredDepth {
		structuredDepth = maxStructuredDepth
	}

	rows, err := s.restaurants.Search(ctx, search.SearchQuery{Filter: filter, TopK: structuredDepth})
	if err != nil {
		return ChannelInput{}, nil, nil, err
	}

	input := ChannelInput{Channel: retrieval.ChannelStructured, Ran: true}
	candidates := make(map[int64]search.RestaurantCandidate, len(rows))
	priors := make(map[int64]float64, len(rows))
	for _, row := range rows {
		// Every row here satisfied every filter, so they are equally correct and
		// the channel's job is to say so, not to rank them. The prior carries the
		// ordering and is scored separately.
		//
		// The score is deliberately flat. A row that scored by its position in
		// this result would make the channel's own arbitrary cut look like a
		// ranking signal: the repository ordered these rows by prior, then cut at
		// TopK, so row one is not a better answer than row ten — it is simply the
		// first of ten that happened to fit. Scoring by position would let a
		// restaurant outside the cut score zero here and lose to one inside it,
		// which is a ranking decided by where the list was truncated rather than
		// by what the user asked. A candidate's structured score is therefore a
		// constant, and the prior is the only ordering this channel contributes.
		input.Hits = append(input.Hits, retrieval.ChannelHit{
			RestaurantID: row.RestaurantID,
			Score:        1,
			Reason:       "满足全部硬条件（" + describeFilters(filter) + "）",
			Detail:       map[string]any{"filters": describeFilters(filter)},
		})
		candidates[row.RestaurantID] = row
		priors[row.RestaurantID] = s.priorOf(ctx, row.RestaurantID)
	}
	return input, candidates, priors, nil
}

// keywordChannel runs the name and address match channel.
func (s *Service) keywordChannel(
	ctx context.Context, text string, depth int,
) (ChannelInput, map[int64]search.RestaurantCandidate, map[int64]float64, error) {
	if text == "" {
		return ChannelInput{
			Channel: retrieval.ChannelKeyword,
			Note:    "无关键词，跳过",
		}, nil, nil, nil
	}
	if !s.cfg.EnableKeyword {
		return ChannelInput{
			Channel: retrieval.ChannelKeyword,
			Note:    "关键词通道已关闭",
		}, nil, nil, nil
	}

	rows, err := s.restaurants.MatchByText(ctx, text, depth)
	if err != nil {
		return ChannelInput{}, nil, nil, err
	}

	input := ChannelInput{Channel: retrieval.ChannelKeyword, Ran: true}
	candidates := make(map[int64]search.RestaurantCandidate, len(rows))
	priors := make(map[int64]float64, len(rows))
	for _, row := range rows {
		input.Hits = append(input.Hits, retrieval.ChannelHit{
			RestaurantID: row.RestaurantID,
			Score:        row.Score,
			Reason: fmt.Sprintf("名称/地址匹配“%s”（相似度 %.3f）",
				text, row.Score),
			Detail: map[string]any{"text": text, "similarity": row.Score},
		})
		candidates[row.RestaurantID] = row
		priors[row.RestaurantID] = s.priorOf(ctx, row.RestaurantID)
	}
	return input, candidates, priors, nil
}

// priorOf reads a restaurant's ranking prior.
//
// It deliberately ignores knowledge_score. That column is -log10 of the demo
// target, so every restaurant the selection kept carries an identical value —
// measured: all 3,000 active rows sit at exactly 5.5. Ranking on it would add
// a term that looks like a signal, appears in every trace, and reorders
// nothing. The prior is rating instead, discounted when the sample is too small
// to support it: a 4.9 from three reviews is not the same claim as a 4.5 from
// three hundred, and treating them as equal would let a thin sample win on
// confidence it does not have.
//
// It is a per-id point lookup rather than a batched read because the channel
// has already done one round trip for the same rows, and the cost here is a
// point read on the primary key. A retrieval that cannot read the prior still
// ranks correctly — the prior is the smallest weight — so a failure is
// swallowed rather than raised.
func (s *Service) priorOf(ctx context.Context, restaurantID int64) float64 {
	detail, err := s.restaurants.GetByID(ctx, restaurantID)
	if err != nil {
		return 0
	}
	return priorFrom(detail)
}

// priorRatingFloor is the sample size below which a rating is discounted.
//
// Bayesian shrinkage toward the corpus mean, expressed as a divisor: a rating
// with n reviews is divided by 1 + n/priorRatingFloor, so a five-review rating
// counts for a sixth of what its raw value suggests and a hundred-review rating
// counts for most of it.
const priorRatingFloor = 50

// priorRatingMean is the value an under-sampled rating is pulled toward. It is
// the corpus's approximate average, used only as the point of shrinkage.
const priorRatingMean = 4.0

// priorFrom computes the prior from a restaurant's rating and its sample size.
func priorFrom(detail search.RestaurantDetail) float64 {
	if detail.Rating == nil {
		return 0
	}
	rating := *detail.Rating
	if rating <= 0 {
		return 0
	}
	shrinkage := float64(detail.RatingCount) / (float64(detail.RatingCount) + priorRatingFloor)
	return priorRatingMean + (rating-priorRatingMean)*shrinkage
}

// pageSize resolves the requested page size against the configuration.
func (s *Service) pageSize(requested int) int {
	switch {
	case requested <= 0:
		return s.cfg.TopK
	case requested > maxTopK:
		return maxTopK
	default:
		return requested
	}
}

// mergePool folds a channel's candidates into the shared fusion pool.
func mergePool(
	candidates map[int64]search.RestaurantCandidate,
	priors map[int64]float64,
	incoming map[int64]search.RestaurantCandidate,
	incomingPriors map[int64]float64,
) {
	for id, candidate := range incoming {
		existing, ok := candidates[id]
		if !ok {
			candidates[id] = candidate
		} else {
			// A candidate recalled twice keeps whichever projection carries more
			// detail, so a channel that did not read the address cannot erase it.
			if len(candidate.Address) > len(existing.Address) {
				candidate.Rating = firstNonNilFloat(existing.Rating, candidate.Rating)
				candidates[id] = candidate
			}
		}
		if prior, ok := incomingPriors[id]; ok {
			priors[id] = prior
		}
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func firstNonNilFloat(values ...*float64) *float64 {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

// vectorChannel runs the semantic recall over restaurant profiles.
//
// It never fails the search. The other two channels are answers to questions
// the user actually asked; this one answers a question they only implied, so its
// absence degrades the ranking without invalidating it. Every way it can fail —
// no provider, no query, a slow model, a dimension mismatch, an unreachable
// store — is recorded in the trace instead.
//
// The filters go into the recall statement rather than being applied to the
// returned rows. Retrieving first and filtering afterwards would rank against
// restaurants the user excluded, then throw most of them away, and a soft
// condition could promote a restaurant that does not satisfy a hard one.
// softConditionNote is the trace line for a vector recall that was steered by
// soft conditions.
//
// It says "inference from reviews" in the trace, not only in the answer, for
// the same reason the trace exists at all: the ranking a reader has to check is
// the one the channels actually produced, and a ranking half of which came from
// review text must not look like a ranking that came from columns. The phrase
// is deliberately repeated in the prompt and here rather than derived from one
// place — the trace is read by an operator and the sentence by a user, and a
// shared constant would only couple two vocabularies that should be free to
// diverge.
const softConditionNote = "软条件按评论主题与向量召回，属于评论推断"

// softReasonPrefix labels a contribution that came from review text rather than
// from a column. It is the wording the answer layer is told to reuse.
const softReasonPrefix = "评论推断："

func (s *Service) vectorChannel(
	ctx context.Context,
	query string,
	filter search.RestaurantFilter,
	soft []retrieval.SoftCondition,
	depth int,
) (ChannelInput, map[int64]search.RestaurantCandidate, map[int64]float64, int, error) {
	skipped := func(note string) (ChannelInput, map[int64]search.RestaurantCandidate, map[int64]float64, int, error) {
		return ChannelInput{Channel: retrieval.ChannelVector, Note: note}, nil, nil, 0, nil
	}

	if !s.cfg.EnableVector {
		return skipped("向量通道已关闭")
	}
	if s.knowledge == nil {
		return skipped("向量通道不可用：未配置知识库")
	}
	if s.embedding == nil {
		return skipped("向量通道不可用：未配置 embedding provider")
	}

	// The embedded text is the question plus the soft conditions the corpus can
	// only answer through reviews.
	//
	// It is built here rather than passed in as one string because the caller's
	// Query is also what the reranker and the trace name: folding "安静 适合约会"
	// into it would make every downstream reader believe the user asked for
	// those words as a subject, when what they did was constrain the ranking.
	embeddingText := composeEmbeddingText(query, soft)
	if strings.TrimSpace(embeddingText) == "" {
		// Embedding an empty string yields a vector that means nothing. A
		// filter-only search has no soft condition to interpret.
		return skipped("无自然语言查询，向量通道跳过")
	}

	vector, err := s.embedQuery(ctx, embeddingText)
	if err != nil {
		// The vector channel answers a question the user only implied, so its
		// absence must not fail a search the other channels can still answer.
		// But a dimension mismatch is not an availability problem -- it means
		// the corpus and the configured model disagree, every recall will fail
		// the same way, and a ranking quietly missing its semantic half reads as
		// a correct answer. Failures the operator has to resolve are raised;
		// transient ones are recorded.
		switch errs.CodeOf(err) {
		case errs.CodeEmbeddingEmpty, errs.CodeEmbeddingDimensionMismatch:
			s.logger.Error("vector channel is misconfigured",
				slog.String("error", err.Error()),
				slog.String("embedding_model", s.embedding.ModelID()))
			return ChannelInput{}, nil, nil, 0, err
		}
		return skipped("向量通道不可用：" + vectorFailureReason(err))
	}

	vectorDepth := depth * vectorOverread
	if vectorDepth > maxVectorDepth {
		vectorDepth = maxVectorDepth
	}
	docs, err := s.knowledge.VectorSearch(ctx, store.VectorSearchRequest{
		Scope:   evidence.ScopeRestaurant,
		Query:   vector,
		TopK:    vectorDepth,
		Borough: search.CanonicalBorough(filter.Borough),
	})
	if err != nil {
		// A store failure reaches here untyped -- an adapter cannot classify a
		// connection loss as anything but an internal fault. Falling back to
		// vectorFailureReason rather than the bare code keeps the note from
		// rendering as the literal string "internal", which reads as a leaked
		// implementation detail instead of a reason.
		return skipped("向量通道不可用：" + vectorFailureReason(err))
	}

	input := ChannelInput{Channel: retrieval.ChannelVector, Ran: true}
	// The note is attached to a channel that ran, which is the one case the
	// fusion layer would otherwise record nothing about. A vector recall steered
	// by soft conditions is not a degradation, but it is a fact about the
	// ranking that a reader cannot recover from the scores: two candidates with
	// the same similarity were not necessarily recalled for the same reason.
	if len(soft) > 0 {
		input.Note = softConditionNote
	}
	// Every hit from this channel was recalled by the same query, so when that
	// query carried soft conditions, each of them was recalled partly for them.
	// The reason says so once, with the topics named, rather than per candidate
	// pretending to know which candidate matched which condition — the corpus
	// returns documents, not per-condition verdicts.
	softReason := describeSoftConditions(soft)
	// The semantic half of the reason quotes the question when there is one. It
	// does not repeat the soft words: those already appear in their own half,
	// and a reason that says the same thing twice reads as two pieces of
	// evidence for one fact.
	display := truncateForReason(query)
	if strings.TrimSpace(display) == "" {
		display = truncateForReason(embeddingText)
	}
	candidates := make(map[int64]search.RestaurantCandidate, len(docs))
	priors := make(map[int64]float64, len(docs))
	for _, doc := range docs {
		similarity := cosineSimilarity(doc.Distance)
		reason := fmt.Sprintf("语义匹配“%s”（相似度 %.3f）", display, similarity)
		if softReason != "" {
			reason = softReason + "；" + reason
		}
		input.Hits = append(input.Hits, retrieval.ChannelHit{
			RestaurantID: doc.RestaurantID,
			Score:        similarity,
			Reason:       reason,
			Detail: map[string]any{
				"query":      query,
				"similarity": similarity,
				"distance":   doc.Distance,
			},
		})
		// The distance is kept beside the similarity in the trace detail because
		// "the vector channel said 0.82" and "the distance was 0.18" are the same
		// fact in two scales, and a reader checking one against the corpus needs
		// the value the database actually returned.
		candidate := search.RestaurantCandidate{
			RestaurantID: doc.RestaurantID,
			Name:         doc.Title,
			SnapshotAt:   doc.SnapshotAt,
		}
		// The profile document carries the name but not the address, price, or
		// rating, and a candidate missing those is not displayable. Reading them
		// back costs one indexed point lookup per hit, against a page of at most
		// a few hundred rows.
		if detail, err := s.restaurants.GetByID(ctx, doc.RestaurantID); err == nil {
			candidate = detailAsCandidate(detail, candidate)
			priors[doc.RestaurantID] = priorFrom(detail)
		}
		candidates[doc.RestaurantID] = candidate
	}
	// The dimension is returned rather than read back off the provider: the
	// vector that ran is the fact worth recording, and a provider that reports a
	// different width than it returns is exactly the case a trace should show.
	return input, candidates, priors, len(vector), nil
}

// embedQuery embeds a query, validating the result before it reaches a query.
//
// The dimension is checked here rather than left to the database. A mismatched
// vector is rejected by the column, but the error that comes back names a type
// rather than the configuration that caused it — so the check exists to produce
// an error a reader can act on, and to avoid a pointless round trip.
func (s *Service) embedQuery(ctx context.Context, query string) ([]float32, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.EmbeddingTimeout)
	defer cancel()

	vector, err := s.embedding.EmbedQuery(ctx, query)
	if err != nil {
		return nil, err
	}
	if len(vector) == 0 {
		return nil, errs.New(errs.CodeEmbeddingEmpty,
			"the embedding provider returned no vector")
	}
	if want := s.embedding.Dimensions(); len(vector) != want {
		return nil, errs.Newf(errs.CodeEmbeddingDimensionMismatch,
			"the embedding provider returned %d dimensions, the corpus is %d: "+
				"the documents were embedded with a different model",
			len(vector), want)
	}
	return vector, nil
}

// vectorFailureReason renders why a recall was skipped.
//
// The error code is preferred over the message: it is the stable part, and a
// provider message may carry a URL or a fragment of a request that does not
// belong in a response body.
func vectorFailureReason(err error) string {
	if code := errs.CodeOf(err); code != errs.CodeInternal {
		return string(code)
	}
	return "embedding 失败"
}

// cosineSimilarity converts a distance into a similarity in [0,1].
func cosineSimilarity(distance float64) float64 {
	similarity := 1 - distance
	if similarity < 0 {
		return 0
	}
	if similarity > 1 {
		return 1
	}
	return similarity
}

// truncateForReason shortens a query for display in a reason string.
func truncateForReason(query string) string {
	const limit = 40
	runes := []rune(strings.TrimSpace(query))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}

// composeEmbeddingText builds the string the query is embedded as.
//
// The soft conditions are appended to the question rather than replacing it,
// because the two answer different halves of the same request: the question
// says what the user is looking for, and the conditions say what would make one
// result better than another. Embedding only the conditions would recall
// restaurants that are quiet and Italian when the user wanted quiet and
// Japanese; embedding only the question would make the conditions decorative.
func composeEmbeddingText(query string, soft []retrieval.SoftCondition) string {
	parts := make([]string, 0, 2)
	if trimmed := strings.TrimSpace(query); trimmed != "" {
		parts = append(parts, trimmed)
	}
	if softText := retrieval.SoftQueryText(soft); softText != "" {
		parts = append(parts, softText)
	}
	return strings.Join(parts, " ")
}

// describeSoftConditions renders "评论推断：安静（ambience）、适合约会（ambience）".
//
// The topic key is shown beside the user's words rather than the localised
// label, because the key is what the stored reviews are tagged with: a reader
// checking the claim can search for "ambience" and find it. A condition that
// mapped onto no topic is still listed, with no parenthetical — hiding it would
// make the reason claim the ranking understood something it did not.
func describeSoftConditions(soft []retrieval.SoftCondition) string {
	if len(soft) == 0 {
		return ""
	}
	var parts []string
	seen := map[string]struct{}{}
	for _, condition := range soft {
		text := strings.TrimSpace(condition.Text)
		topic := strings.TrimSpace(condition.Topic)
		if text == "" && topic == "" {
			continue
		}
		key := strings.ToLower(text) + "\x00" + topic
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		switch {
		case text == "":
			parts = append(parts, topic)
		case topic == "":
			parts = append(parts, text)
		default:
			parts = append(parts, fmt.Sprintf("%s（%s）", text, topic))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return softReasonPrefix + strings.Join(parts, "、")
}

// detailAsCandidate folds a repository row into a recalled candidate.
//
// The recalled fields win where both have them: the candidate came from the
// profile document, so its name and snapshot time are the ones the embedding was
// computed over, and a repository read that happened moments later cannot have
// seen a different snapshot.
func detailAsCandidate(detail search.RestaurantDetail, recalled search.RestaurantCandidate) search.RestaurantCandidate {
	out := recalled
	if out.Name == "" {
		out.Name = detail.Name
	}
	out.Address = detail.Address
	out.Borough = detail.Borough
	out.Cuisines = detail.Cuisines
	out.PriceLevel = detail.PriceLevel
	out.Rating = detail.Rating
	out.RatingCount = detail.RatingCount
	if out.SnapshotAt.IsZero() {
		out.SnapshotAt = detail.SnapshotAt
	}
	return out
}
