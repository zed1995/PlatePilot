package retrieval

import (
	"context"
	"strconv"
	"time"

	"github.com/zed/platepilot/shared/domain/search"
	"github.com/zed/platepilot/shared/port"
)

// DefaultRerankTimeout bounds an optional reranking pass.
//
// The bound exists outside the provider because a provider that hangs is a
// provider that must not take the search down with it. Reranking improves an
// ordering that is already usable; losing the request is a worse trade than
// keeping the fused one.
const DefaultRerankTimeout = 3 * time.Second

// RerankOutcome reports what happened to the reranking pass.
type RerankOutcome struct {
	Candidates []search.RestaurantCandidate
	// Applied is true only when a provider reordered the list and the result
	// passed validation.
	Applied bool
	// ModelID is empty when no provider is configured and set when one is. The
	// two states mean different things — "rerank was never available" versus
	// "rerank was available and did not happen" — and collapsing them would
	// hide a failing reranker in plain sight.
	ModelID string
	// Note explains a downgrade in one sentence.
	Note string
}

// ApplyRerank reranks when a provider is configured and degrades cleanly when
// one is not.
//
// It never returns an error. "No provider" and "provider failed" are both
// recorded in the trace and answered with the fused order, because a caller that
// asked a good question deserves an answer even when an optional improvement is
// unavailable.
func ApplyRerank(
	ctx context.Context,
	query string,
	candidates []search.RestaurantCandidate,
	provider port.RerankProvider,
) RerankOutcome {
	if provider == nil {
		return RerankOutcome{Candidates: candidates}
	}
	modelID := provider.ModelID()

	ctx, cancel := context.WithTimeout(ctx, DefaultRerankTimeout)
	defer cancel()

	reranked, err := provider.Rerank(ctx, query, candidates)
	if err != nil {
		return RerankOutcome{
			Candidates: candidates,
			ModelID:    modelID,
			Note:       "rerank 失败，已保留融合顺序：" + err.Error(),
		}
	}
	if err := validatePermutation(candidates, reranked); err != nil {
		return RerankOutcome{
			Candidates: candidates,
			ModelID:    modelID,
			Note:       "rerank 结果不是候选的排列，已丢弃：" + err.Error(),
		}
	}
	return RerankOutcome{Candidates: reranked, Applied: true, ModelID: modelID}
}

// validatePermutation checks that reranked is exactly the input, reordered.
//
// The check matters because the failure it catches is invisible. A provider
// that drops a candidate it judged irrelevant turns a page of three into a
// page of one, and nothing else in the system can tell a deliberate rerank from
// a lossy one: the request still succeeds and the response still looks complete.
func validatePermutation(input, reranked []search.RestaurantCandidate) error {
	if len(reranked) != len(input) {
		return errLengthMismatch{input: len(input), got: len(reranked)}
	}
	counts := make(map[int64]int, len(input))
	for _, candidate := range input {
		counts[candidate.RestaurantID]++
	}
	for _, candidate := range reranked {
		remaining, ok := counts[candidate.RestaurantID]
		if !ok {
			return errUnknownCandidate{id: candidate.RestaurantID}
		}
		if remaining == 0 {
			return errDuplicateCandidate{id: candidate.RestaurantID}
		}
		counts[candidate.RestaurantID] = remaining - 1
	}
	return nil
}

// errLengthMismatch reports a reranker that changed the result count.
type errLengthMismatch struct{ input, got int }

func (e errLengthMismatch) Error() string {
	return "期望 " + strconv.Itoa(e.input) + " 条候选，实际返回 " + strconv.Itoa(e.got) + " 条"
}

// errUnknownCandidate reports a candidate the reranker invented.
type errUnknownCandidate struct{ id int64 }

func (e errUnknownCandidate) Error() string {
	return "返回了未提供的候选 " + strconv.FormatInt(e.id, 10)
}

// errDuplicateCandidate reports a candidate returned more than once.
type errDuplicateCandidate struct{ id int64 }

func (e errDuplicateCandidate) Error() string {
	return "候选 " + strconv.FormatInt(e.id, 10) + " 重复返回"
}
