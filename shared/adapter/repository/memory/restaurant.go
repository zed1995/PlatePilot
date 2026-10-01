package memory

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/search"
	"github.com/zed/platepilot/shared/port"
)

// defaultTopK bounds search results when the caller does not set one.
const defaultTopK = 10

// maxTopK caps a caller-supplied limit. Without a ceiling, one request can read
// the whole corpus into memory and return it as a page.
const maxTopK = 100

// minMatchText is the shortest text a fuzzy name or address match accepts.
//
// Three characters is not arbitrary: a trigram index cannot match anything
// shorter, so a two-character query against one is a table scan that returns
// the entire corpus before the limit cuts it. Refusing it is both faster and
// more honest, because a two-character restaurant name is almost always a typo.
const minMatchText = 3

// triStateUnknown mirrors the three-state attribute vocabulary. A row whose
// attribute is absent or explicitly unknown cannot satisfy an open-now filter
// either way, so both are treated the same.
const triStateUnknown = "unknown"

// RestaurantRepository is an in-memory port.RestaurantRepository.
type RestaurantRepository struct {
	mu   sync.RWMutex
	byID map[int64]search.RestaurantDetail
}

// NewRestaurantRepository returns an empty in-memory restaurant repository.
func NewRestaurantRepository() *RestaurantRepository {
	return &RestaurantRepository{byID: make(map[int64]search.RestaurantDetail)}
}

// GetByID returns a restaurant or a not_found error.
func (r *RestaurantRepository) GetByID(_ context.Context, restaurantID int64) (search.RestaurantDetail, error) {
	// A non-positive id is a malformed request, not a missing row. The two
	// read "you asked for nothing" and "it is not there", and a client retrying
	// the first deserves a different response than one retrying the second.
	if restaurantID <= 0 {
		return search.RestaurantDetail{}, errs.New(errs.CodeInvalidArgument,
			"restaurant_id must be positive")
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	found, ok := r.byID[restaurantID]
	if !ok {
		return search.RestaurantDetail{}, errs.Newf(errs.CodeNotFound, "restaurant %d not found", restaurantID)
	}
	return found, nil
}

// Upsert inserts or replaces a restaurant by ID.
//
// It is not part of port.RestaurantRepository: the read side never writes. The
// method exists so tests can seed a repository directly.
func (r *RestaurantRepository) Upsert(_ context.Context, restaurant search.RestaurantDetail) error {
	if restaurant.RestaurantID == 0 {
		return errs.New(errs.CodeInvalidArgument, "restaurant_id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[restaurant.RestaurantID] = restaurant
	return nil
}

// Search applies hard filters, scores soft text matches, and returns the top-K
// candidates ordered by descending score then ascending id.
func (r *RestaurantRepository) Search(_ context.Context, query search.SearchQuery) ([]search.RestaurantCandidate, error) {
	if err := query.Filter.Validate(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	candidates := make([]search.RestaurantCandidate, 0, len(r.byID))
	for _, restaurant := range r.byID {
		if !matchesFilter(restaurant, query.Filter) {
			continue
		}
		candidates = append(candidates, candidateFrom(restaurant, query.Text))
	}

	sortCandidates(candidates)

	topK := clampTopK(query.TopK)
	if len(candidates) > topK {
		candidates = candidates[:topK]
	}
	return candidates, nil
}

// MatchByText finds restaurants whose name or address resembles text.
//
// The store scores fuzzy matches with trigram similarity; this scores them with
// token overlap, which is close enough for a test double to keep the contract
// honest about *shape* while not pretending to reproduce a ranking. Tests that
// care about ranking quality run against the real store.
func (r *RestaurantRepository) MatchByText(_ context.Context, text string, limit int) ([]search.RestaurantCandidate, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, errs.New(errs.CodeRetrievalEmptyQuery, "match text must not be empty")
	}
	if len([]rune(trimmed)) < minMatchText {
		return nil, errs.Newf(errs.CodeRetrievalQueryTooShort,
			"match text must be at least %d characters", minMatchText)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	candidates := make([]search.RestaurantCandidate, 0)
	for _, restaurant := range r.byID {
		nameScore := tokenSimilarity(trimmed, restaurant.Name)
		addressScore := tokenSimilarity(trimmed, restaurant.Address)
		score := math.Max(nameScore, addressScore)
		if score <= 0 {
			continue
		}
		candidate := candidateFrom(restaurant, "")
		candidate.Score = score
		if nameScore >= addressScore {
			candidate.Reasons = append(candidate.Reasons, "名称匹配")
		}
		if addressScore > 0 {
			candidate.Reasons = append(candidate.Reasons, "地址匹配")
		}
		candidates = append(candidates, candidate)
	}

	sortCandidates(candidates)

	topK := clampTopK(limit)
	if len(candidates) > topK {
		candidates = candidates[:topK]
	}
	return candidates, nil
}

// candidateFrom builds a candidate with the text-derived score and reasons.
func candidateFrom(restaurant search.RestaurantDetail, text string) search.RestaurantCandidate {
	candidate := search.RestaurantCandidate{
		RestaurantID: restaurant.RestaurantID,
		Name:         restaurant.Name,
		Address:      restaurant.Address,
		Borough:      restaurant.Borough,
		Cuisines:     restaurant.Cuisines,
		PriceLevel:   restaurant.PriceLevel,
		Rating:       restaurant.Rating,
		RatingCount:  restaurant.RatingCount,
		SnapshotAt:   restaurant.SnapshotAt,
		Score:        1,
	}
	if strings.TrimSpace(text) == "" {
		return candidate
	}
	needle := strings.ToLower(text)
	if strings.Contains(strings.ToLower(restaurant.Name), needle) {
		candidate.Score += 2
	}
	if strings.Contains(strings.ToLower(restaurant.Address), needle) {
		candidate.Score += 1
	}
	if containsFoldAny(restaurant.Cuisines, []string{needle}) {
		candidate.Score += 1
	}
	return candidate
}

// sortCandidates orders by descending score, then ascending id.
//
// The id tie-breaker is not cosmetic: an unstable sort would return a different
// order for identical input, which breaks pagination and makes every ranking
// test flaky for reasons unrelated to ranking.
func sortCandidates(candidates []search.RestaurantCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].RestaurantID < candidates[j].RestaurantID
	})
}

// tokenSimilarity scores how much of needle appears in haystack as a fraction
// of needle's tokens, in [0,1].
func tokenSimilarity(needle, haystack string) float64 {
	needleTokens := strings.Fields(strings.ToLower(needle))
	if len(needleTokens) == 0 || haystack == "" {
		return 0
	}
	lowerHaystack := strings.ToLower(haystack)
	matched := 0
	for _, token := range needleTokens {
		if strings.Contains(lowerHaystack, token) {
			matched++
		}
	}
	return float64(matched) / float64(len(needleTokens))
}

func clampTopK(topK int) int {
	if topK <= 0 {
		return defaultTopK
	}
	if topK > maxTopK {
		return maxTopK
	}
	return topK
}

func matchesFilter(restaurant search.RestaurantDetail, filter search.RestaurantFilter) bool {
	if len(filter.Cuisines) > 0 && !containsFoldAny(restaurant.Cuisines, filter.Cuisines) {
		return false
	}
	if len(filter.PriceLevels) > 0 {
		// An absent price level cannot satisfy a price condition: "unknown" is
		// not "cheap", and treating it as one would invent a match.
		if restaurant.PriceLevel == nil || !containsInt(filter.PriceLevels, *restaurant.PriceLevel) {
			return false
		}
	}
	if filter.MinRating != nil {
		if restaurant.Rating == nil || *restaurant.Rating < *filter.MinRating {
			return false
		}
	}
	if filter.Neighborhood != "" &&
		!strings.Contains(strings.ToLower(restaurant.Address), strings.ToLower(filter.Neighborhood)) {
		return false
	}
	if filter.Borough != "" && !strings.EqualFold(restaurant.Borough, filter.Borough) {
		return false
	}
	if filter.HasDistance() {
		// The in-memory double has no coordinates on the restaurant detail, so
		// the distance filter cannot be evaluated here. Skipping it rather than
		// rejecting keeps this adapter usable for tests that do not exercise
		// geography; the real store implements it.
		_ = filter
	}
	if filter.OpenNow != nil {
		// An absent attribute is unknown, and unknown satisfies neither value:
		// "we do not know whether it is open" is not "it is closed". Requiring an
		// exact tri-state match keeps the three states from collapsing into two.
		if restaurant.Attributes["open_now"] != strconv.FormatBool(*filter.OpenNow) {
			return false
		}
	}
	return true
}

func containsFoldAny(haystack, needles []string) bool {
	for _, needle := range needles {
		for _, value := range haystack {
			if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(needle)) {
				return true
			}
		}
	}
	return false
}

func containsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

var _ port.RestaurantRepository = (*RestaurantRepository)(nil)
