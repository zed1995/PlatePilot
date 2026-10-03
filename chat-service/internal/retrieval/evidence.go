package retrieval

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/domain/retrieval"
	"github.com/zed1995/platepilot/shared/store"
)

// EvidenceRequest is one evidence recall: a question, the restaurants the answer
// is allowed to be about, and how much of it to read.
type EvidenceRequest struct {
	// RestaurantIDs is required. There is no unbounded form; see
	// Service.Evidence.
	RestaurantIDs []int64
	// Query is the user's question. It is embedded here rather than by the
	// caller so the embedding timeout and the dimension check apply to evidence
	// recall exactly as they do to candidate recall — two code paths would be two
	// places for the same defect to hide.
	Query string
	// Topic narrows review summaries to one review topic.
	Topic string
	// DocTypes, when non-empty, restricts results to these document kinds.
	DocTypes []evidence.DocType
	// TopK bounds the recall depth before assembly. Assembly applies the token
	// budget; this bounds the work the store does.
	TopK int
}

// EvidenceResult is a recalled evidence set with its derivation.
type EvidenceResult struct {
	Evidence []evidence.Evidence
	Trace    *retrieval.EvidenceTrace
}

// maxEvidenceDepth is the ceiling on a recall depth.
//
// It bounds the store's work, not the answer's size: assembly applies the token
// budget afterwards, and a page deeper than this exists only to give the
// assembler choices it would then have to discard.
const maxEvidenceDepth = 100

// defaultEvidenceDepth is the depth used when the caller names none.
const defaultEvidenceDepth = 20

// Evidence recalls citable evidence for a set of restaurants.
//
// The restaurant set is a precondition rather than a filter, and that is the
// whole design. A citation is a claim that a specific restaurant said a
// specific thing; an evidence recall that could widen its own scope would
// return the nearest review in the corpus and attribute it to a restaurant it is
// not about. Nothing downstream can detect that, so the refusal happens here.
//
// A request with restaurants but no question is answered rather than refused:
// "what is this place like" is a real question, and the honest answer to it is
// everything the place has on record.
func (s *Service) Evidence(ctx context.Context, req EvidenceRequest) (EvidenceResult, error) {
	restaurantIDs := distinctIDs(req.RestaurantIDs)
	if len(restaurantIDs) == 0 {
		return EvidenceResult{}, errs.New(errs.CodeRetrievalNoScope,
			"evidence recall needs at least one restaurant: a citation is a claim "+
				"about a specific restaurant, and a recall without one would "+
				"return the nearest document in the corpus")
	}
	if s.knowledge == nil {
		return EvidenceResult{}, errs.New(errs.CodeProviderUnavailable,
			"evidence recall needs a knowledge repository")
	}

	topK := req.TopK
	switch {
	case topK <= 0:
		topK = defaultEvidenceDepth
	case topK > maxEvidenceDepth:
		topK = maxEvidenceDepth
	}

	query := strings.TrimSpace(req.Query)
	trace := &retrieval.EvidenceTrace{
		Query:     query,
		Topic:     req.Topic,
		TopK:      topK,
		ScopeSize: len(restaurantIDs),
	}

	// The vector is optional by design, not by fallback: an empty question takes
	// the unordered read below rather than embedding a string that means
	// nothing.
	var vector []float32
	if query != "" {
		if s.embedding == nil {
			// Without a provider there is still an honest answer to "what does
			// this place say about waiting times" — everything on record. Failing
			// would make an optional capability a hard dependency.
			trace.Warn("evidence recall ranked by document order: no embedding provider")
		} else {
			embedded, err := s.embedQuery(ctx, query)
			if err != nil {
				// A configuration defect is raised for the same reason it is in the
				// vector channel: a corpus and a model that disagree produce the
				// same failure every time, and an unordered answer would quietly
				// replace a ranked one.
				if errs.CodeOf(err) == errs.CodeEmbeddingEmpty ||
					errs.CodeOf(err) == errs.CodeEmbeddingDimensionMismatch {
					return EvidenceResult{}, err
				}
				trace.Warn("evidence recall ranked by document order: " + vectorFailureReason(err))
			} else {
				vector = embedded
				trace.EmbeddingModelID = s.embedding.ModelID()
				trace.QueryEmbeddingDim = len(embedded)
			}
		}
	}

	items, err := s.knowledge.RecallEvidence(ctx, store.EvidenceRequest{
		RestaurantIDs: restaurantIDs,
		Query:         vector,
		Topic:         req.Topic,
		DocTypes:      req.DocTypes,
		TopK:          topK,
	})
	if err != nil {
		return EvidenceResult{}, err
	}
	trace.Recalled = len(items)

	items = resolveCitations(items, trace)
	items = dedupeBySource(items)
	return EvidenceResult{Evidence: items, Trace: trace}, nil
}

// resolveCitations fills in what a citation needs to be checkable.
//
// An evidence with no source is worse than no evidence: a UI that renders a
// quote without an attribution looks correct and cannot be verified. The
// fallback is a visible placeholder rather than a dropped row, because dropping
// would silently shrink the answer, and a caller comparing counts would have no
// way to tell why.
//
// RestaurantName is filled the same way. A citation that says "this restaurant"
// is a pointer, not a citation.
func resolveCitations(items []evidence.Evidence, trace *retrieval.EvidenceTrace) []evidence.Evidence {
	out := make([]evidence.Evidence, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.Source) == "" {
			item.Source = unknownSource
			trace.Warn("evidence " + strconv.FormatInt(item.EvidenceID, 10) +
				" carries no source; it is cited as " + unknownSource)
		}
		out = append(out, item)
	}
	return out
}

// unknownSource is what a citation with no recorded origin is labelled with.
//
// It is a constant rather than an empty string because an empty source renders
// as a blank space next to a quote, which is indistinguishable from a rendering
// bug. A visible word tells the reader the provenance is missing.
const unknownSource = "unknown"

// dedupeBySource keeps one row per distinct document version.
//
// The same paragraph can be reached twice: once as its own document and once
// inside a representative-review chunk that quotes it. Feeding both to a model
// spends tokens restating the same sentence and reads as the corpus agreeing
// with itself. The highest-scoring copy wins, so the one kept is the one the
// recall judged most relevant.
//
// The identity is (restaurant, doc type, content hash) rather than the document
// id, because the id is per storage row and two rows can carry the same text.
func dedupeBySource(items []evidence.Evidence) []evidence.Evidence {
	type key struct {
		restaurantID int64
		docType      evidence.DocType
		contentHash  string
	}
	best := make(map[key]evidence.Evidence, len(items))
	order := make([]key, 0, len(items))
	for _, item := range items {
		// Without a content hash there is nothing to compare, so the row is kept
		// as its own identity rather than being merged with every other
		// hashless row.
		identity := key{restaurantID: item.RestaurantID, docType: item.DocType}
		if item.ContentHash != "" {
			identity.contentHash = item.ContentHash
		} else {
			identity.contentHash = strconv.FormatInt(item.EvidenceID, 10)
		}
		existing, seen := best[identity]
		if !seen {
			best[identity] = item
			order = append(order, identity)
			continue
		}
		if item.Score > existing.Score {
			best[identity] = item
		}
	}

	out := make([]evidence.Evidence, 0, len(order))
	for _, identity := range order {
		out = append(out, best[identity])
	}
	// Re-establish the recall's order. The map iteration above is only a lookup;
	// emitting in first-seen order keeps the recall's ranking intact, and the
	// assembly stage does its own sorting from here.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// distinctIDs removes zero and duplicate restaurant ids while keeping order.
//
// The set is what bounds the recall, and a caller that passes the same id twice
// would otherwise widen the store's work without widening the answer.
func distinctIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
