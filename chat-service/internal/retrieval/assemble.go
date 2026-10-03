package retrieval

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/domain/retrieval"
)

// DefaultEvidenceTokenBudget is how much evidence an answer is assembled from
// when the caller names no budget.
//
// It is sized against the model rather than against the corpus. A typical
// answer quote is a sentence or two, and a few thousand tokens covers half a
// dozen citations with room for the question and the reply — enough to ground an
// answer, few enough that the evidence is not competing with the answer for the
// window. Exceeding it means the evidence stops being support and starts being
// the response.
const DefaultEvidenceTokenBudget = 2000

// minEvidenceTokenBudget is the smallest budget that can hold one citation.
//
// It exists because a budget below it has no honest outcome: the assembler
// either returns nothing (an answer with no evidence, which is the failure this
// stage exists to prevent) or returns a truncated fragment (a quote that stops
// mid-sentence, which is worse than no quote). Refusing is the only response
// that lets the caller see the problem.
const minEvidenceTokenBudget = 24

// maxPerRestaurantDocType bounds how many documents of one kind one restaurant
// may contribute.
//
// The store may legitimately return nine review summaries for a place, and a
// model handed all nine will quote two of them. The rest are spent budget that
// another restaurant's single strongest review needed. Three is enough to show
// variety — a repeated complaint, a repeated praise, and an outlier — without
// letting one well-documented restaurant crowd out the rest of the page.
const maxPerRestaurantDocType = 3

// minSnapshotYear is the earliest plausible observation year.
//
// It is a floor against defaulted timestamps, not a freshness policy: the
// corpus is a 2021 snapshot, so anything before the web had reviews is a
// malformed value rather than an old one.
const minSnapshotYear = 2000

// AssembleOptions controls one assembly.
type AssembleOptions struct {
	// TokenBudget bounds the assembled content. Zero uses the default.
	TokenBudget int
	// MaxPerRestaurantDocType overrides the per-kind cap. Zero uses the default.
	MaxPerRestaurantDocType int
}

// AssemblyReport explains what assembly did.
//
// It exists because assembly is the only stage that discards evidence on
// purpose. A caller that gets three citations instead of nine has no way to tell
// a budget decision from a thin corpus, and those two call for different
// responses: one is normal, the other is a retrieval problem worth investigating.
type AssemblyReport struct {
	// Considered is how many documents entered assembly.
	Considered int
	// Kept is how many survived.
	Kept int
	// Dropped is Considered minus Kept: the net loss, not the number of
	// decisions. It can be smaller than the sum of DroppedByReason, because a
	// document discarded by the budget and later replaced by one of another kind
	// was discarded once and admitted once.
	Dropped int
	// Tokens is the estimated token cost of the kept content.
	Tokens      int
	TokenBudget int
	// DroppedByReason counts each discard by cause — the budget, the diversity
	// cap, or the support floor — so a caller can see which rule did the
	// cutting. It counts events, not net losses.
	DroppedByReason map[string]int
}

// AssembleEvidence turns a recalled set into a citable bundle.
//
// Four passes, in order, because each one removes a different kind of waste:
//
//  1. Completeness. A document with no source, no snapshot time, or no content
//     cannot be cited. It is dropped rather than passed through, because a
//     citation that renders without an attribution looks correct and cannot be
//     checked.
//  2. Redundancy. The same paragraph can arrive twice — once alone, once quoted
//     inside a review chunk. Keeping both spends budget restating one sentence
//     and reads as the corpus agreeing with itself.
//  3. Budget. What is left is admitted in score order until the budget is full.
//  4. Support. If that left a single kind of document standing while another was
//     available, the weakest admitted document trades places with the strongest
//     document of the missing kind. Relevance decided the order; support decides
//     that an answer still has something to check itself against.
//
// Nothing is truncated. A quote that stops mid-sentence is not a quote, so a
// document that does not fit is dropped whole. The alternative — cutting it to
// fit — produces text that looks citable and is not.
func AssembleEvidence(items []evidence.Evidence, opts AssembleOptions) ([]evidence.Evidence, AssemblyReport, error) {
	budget := opts.TokenBudget
	if budget <= 0 {
		budget = DefaultEvidenceTokenBudget
	}
	if budget < minEvidenceTokenBudget {
		return nil, AssemblyReport{}, errs.Newf(errs.CodeRetrievalBudgetExceeded,
			"a token budget of %d cannot hold even one citation; the smallest "+
				"usable budget is %d", budget, minEvidenceTokenBudget)
	}
	perType := opts.MaxPerRestaurantDocType
	if perType <= 0 {
		perType = maxPerRestaurantDocType
	}

	report := AssemblyReport{
		Considered:      len(items),
		TokenBudget:     budget,
		DroppedByReason: map[string]int{},
	}

	candidates := make([]evidence.Evidence, 0, len(items))
	now := time.Now()
	for _, item := range items {
		if reason := incomplete(item, now); reason != "" {
			report.DroppedByReason[reason]++
			continue
		}
		candidates = append(candidates, item)
	}

	if len(candidates) == 0 {
		// Every document failed the completeness check. That is a data problem,
		// not a budget problem, and reporting it as one sends the caller to the
		// wrong knob: they would raise a budget that was never the constraint.
		return nil, report, errs.Newf(errs.CodeValidationFailed,
			"none of the %d recalled documents can be cited: %s",
			report.Considered, describeDrops(report.DroppedByReason))
	}

	candidates = dedupeBySource(candidates)
	candidates = capPerSource(candidates, perType, report.DroppedByReason)

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].EvidenceID < candidates[j].EvidenceID
	})

	kept := make([]evidence.Evidence, 0, len(candidates))
	spent := 0
	for _, item := range candidates {
		cost := EstimateTokens(item.Content)
		if spent+cost > budget {
			report.DroppedByReason["token_budget"]++
			continue
		}
		spent += cost
		kept = append(kept, item)
	}

	if len(kept) == 0 {
		// An answer with no evidence is the exact failure this stage exists to
		// prevent, so it is surfaced rather than returned as an empty bundle.
		// Returning empty would be indistinguishable from "this restaurant has
		// nothing on record", which is a different claim and a different bug.
		return nil, report, errs.Newf(errs.CodeRetrievalBudgetExceeded,
			"a budget of %d tokens cannot hold any of the %d recalled documents; "+
				"the smallest costs %d", budget, len(candidates),
			cheapestTokenCost(candidates))
	}

	kept, spent = floorDocTypes(kept, candidates, budget, spent, &report)

	report.Kept = len(kept)
	report.Dropped = report.Considered - report.Kept
	report.Tokens = spent
	return kept, report, nil
}

// floorDocTypes stops the budget from cutting a bundle down to a single kind of
// document when another kind was available.
//
// The budget pass admits in score order, which is the right order for relevance
// and the wrong one for support: three review summaries from one restaurant are
// three corroborations of the same sentence, and an answer written from them
// cannot tell a documented fact from a well-reviewed impression. When the cut
// leaves one kind standing, the weakest admitted document gives up its place to
// the strongest document of a different kind — but only when that swap actually
// buys a new kind, and never below one document, because an empty bundle is the
// failure this stage exists to prevent.
//
// The swap is a whole-document exchange, never a truncation: the discarded
// document is dropped intact and the admitted one arrives intact. Cutting either
// to fit would produce a quote that stops mid-sentence, which the caller cannot
// tell from a short review.
func floorDocTypes(
	kept, candidates []evidence.Evidence, budget, spent int, report *AssemblyReport,
) ([]evidence.Evidence, int) {
	// One document has nothing to be diverse against, and a bundle that already
	// spans two kinds is what this function exists to produce.
	if len(kept) < 2 || evidence.DocTypesFrom(kept) >= evidence.MinAnswerableDocTypes {
		return kept, spent
	}

	present := make(map[int64]struct{}, len(kept))
	for _, item := range kept {
		present[item.EvidenceID] = struct{}{}
	}
	keptKinds := make(map[evidence.DocType]struct{}, len(kept))
	for _, item := range kept {
		keptKinds[item.DocType] = struct{}{}
	}

	// candidates is in score order, so the first document of an absent kind is
	// the strongest one available. If every kind is already represented, the
	// corpus simply has only one kind and there is nothing to buy.
	var extra *evidence.Evidence
	for i := range candidates {
		if _, ok := keptKinds[candidates[i].DocType]; ok {
			continue
		}
		if _, admitted := present[candidates[i].EvidenceID]; admitted {
			continue
		}
		extra = &candidates[i]
		break
	}
	if extra == nil {
		return kept, spent
	}

	// Evict the weakest admitted document until the newcomer fits. The loop is
	// bounded by the number of admitted documents, so it terminates: either the
	// newcomer fits, or the bundle is down to one document and the swap is
	// abandoned — a single strong citation beats a single weak one.
	work := append([]evidence.Evidence(nil), kept...)
	room := budget - spent
	evicted := 0
	for len(work) > 1 && room < EstimateTokens(extra.Content) {
		victim := weakestEvidence(work)
		room += EstimateTokens(victim.Content)
		work = dropEvidence(work, victim.EvidenceID)
		evicted++
	}
	if room < EstimateTokens(extra.Content) {
		return kept, spent
	}

	report.DroppedByReason["doc_type_floor"] += evicted
	work = append(work, *extra)
	sort.SliceStable(work, func(i, j int) bool {
		if work[i].Score != work[j].Score {
			return work[i].Score > work[j].Score
		}
		return work[i].EvidenceID < work[j].EvidenceID
	})

	total := 0
	for _, item := range work {
		total += EstimateTokens(item.Content)
	}
	return work, total
}

// weakestEvidence is the admitted document that costs the answer least: lowest
// score, then highest id, so two equal scores evict the same one every run.
func weakestEvidence(items []evidence.Evidence) evidence.Evidence {
	weakest := items[0]
	for _, item := range items[1:] {
		if item.Score < weakest.Score ||
			(item.Score == weakest.Score && item.EvidenceID > weakest.EvidenceID) {
			weakest = item
		}
	}
	return weakest
}

// dropEvidence removes one document by id, leaving the rest in order.
func dropEvidence(items []evidence.Evidence, id int64) []evidence.Evidence {
	out := make([]evidence.Evidence, 0, len(items))
	for _, item := range items {
		if item.EvidenceID == id {
			continue
		}
		out = append(out, item)
	}
	return out
}

// incomplete names why a document cannot be cited, or "" if it can.
//
// The three fields are checked together because they are the minimum a citation
// needs: what was said, where it came from, and when it was observed. A
// document missing the observation date cannot be checked against a newer
// snapshot, which is the whole point of recording one.
func incomplete(item evidence.Evidence, now time.Time) string {
	switch {
	case strings.TrimSpace(item.Content) == "":
		return "empty_content"
	case strings.TrimSpace(item.Source) == "":
		return "missing_source"
	case item.SnapshotAt.IsZero():
		return "missing_snapshot"
	case item.SnapshotAt.After(now):
		// An observation cannot postdate the moment it was observed. When one
		// does, something upstream recorded a generation timestamp as an
		// observation, which makes every freshness claim built on it false in
		// the direction that looks safer: the data claims to be newer than it
		// is. A citation with that date would outlive its own accuracy check.
		return "snapshot_in_future"
	case item.SnapshotAt.Year() < minSnapshotYear:
		// The lower bound is a data-shape check, not a freshness policy. A
		// timestamp near the zero value usually means a missing time was
		// defaulted rather than left empty, and it survived IsZero only because
		// it was given a small non-zero value somewhere upstream.
		return "implausible_snapshot"
	case item.EvidenceID <= 0:
		return "missing_document_id"
	}
	return ""
}

// capPerSource limits how many documents one restaurant contributes per kind.
//
// The cap is applied before the budget so that diversity is decided on its own
// terms: applying it afterwards would mean the budget decided which restaurant
// got represented, and a place with slightly higher scores would fill the page
// on its own.
func capPerSource(
	items []evidence.Evidence, perType int, droppedByReason map[string]int,
) []evidence.Evidence {
	seen := make(map[string]int, len(items))
	out := make([]evidence.Evidence, 0, len(items))
	for _, item := range items {
		key := strconv.FormatInt(item.RestaurantID, 10) + "|" + string(item.DocType)
		if seen[key] >= perType {
			droppedByReason["per_source_diversity"]++
			continue
		}
		seen[key]++
		out = append(out, item)
	}
	return out
}

// describeDrops renders a drop count for an error message.
//
// The reasons are sorted by count and then by name so two runs over the same
// input produce the same sentence; a message that reorders itself between runs
// is harder to grep for in a log than one that is stable but arbitrary.
func describeDrops(droppedByReason map[string]int) string {
	reasons := make([]string, 0, len(droppedByReason))
	for reason := range droppedByReason {
		reasons = append(reasons, reason)
	}
	sort.Slice(reasons, func(i, j int) bool {
		if droppedByReason[reasons[i]] != droppedByReason[reasons[j]] {
			return droppedByReason[reasons[i]] > droppedByReason[reasons[j]]
		}
		return reasons[i] < reasons[j]
	})
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		parts = append(parts, reason+" x"+strconv.Itoa(droppedByReason[reason]))
	}
	return strings.Join(parts, ", ")
}

// cheapestTokenCost is the smallest cost in the set, for the error message.
func cheapestTokenCost(items []evidence.Evidence) int {
	cheapest := 0
	for i, item := range items {
		cost := EstimateTokens(item.Content)
		if i == 0 || cost < cheapest {
			cheapest = cost
		}
	}
	return cheapest
}

// EstimateTokens estimates the token cost of a piece of text.
//
// It is an estimate, and deliberately a conservative one. The alternative —
// running a real tokenizer — means either a dependency in this package or a
// round trip to a model, and both are worse than a slight over-estimate: the
// cost of budgeting high is an answer with fewer citations, while budgeting low
// is an answer that overflows its window and gets truncated mid-sentence.
//
// CJK is counted per character because it is closer to one token per character
// than to four, and the corpus is mixed.
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	tokens := 0
	runes := 0
	for _, r := range text {
		runes++
		// Anything outside Latin-1 is counted per character; the rest is divided
		// by four, which is the usual English ratio.
		if r > 0xFF {
			tokens++
			continue
		}
	}
	tokens += (runes - tokens) / 4
	if tokens == 0 && runes > 0 {
		tokens = 1
	}
	return tokens
}

// AssembleAndReturn is the composition the transport calls: recall, then
// assemble, with the trace carrying both stages' accounting.
func (s *Service) AssembleAndReturn(
	ctx context.Context, req EvidenceRequest, opts AssembleOptions,
) ([]evidence.Evidence, *retrieval.EvidenceTrace, error) {
	result, err := s.Evidence(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	kept, report, err := AssembleEvidence(result.Evidence, opts)
	if err != nil {
		return nil, result.Trace, err
	}
	result.Trace.Kept = report.Kept
	result.Trace.Dropped = report.Dropped
	result.Trace.Tokens = report.Tokens
	result.Trace.TokenBudget = report.TokenBudget
	result.Trace.DroppedByReason = report.DroppedByReason
	return kept, result.Trace, nil
}
