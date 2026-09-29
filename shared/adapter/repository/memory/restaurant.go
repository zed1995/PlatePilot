package memory

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/search"
)

// defaultTopK bounds search results when the caller does not set one.
const defaultTopK = 10

// RestaurantRepository is an in-memory port.RestaurantRepository.
type RestaurantRepository struct {
	mu   sync.RWMutex
	byID map[string]search.RestaurantDetail
}

// NewRestaurantRepository returns an empty in-memory restaurant repository.
func NewRestaurantRepository() *RestaurantRepository {
	return &RestaurantRepository{byID: make(map[string]search.RestaurantDetail)}
}

// GetByID returns a restaurant or a not_found error.
func (r *RestaurantRepository) GetByID(_ context.Context, restaurantID string) (search.RestaurantDetail, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	restaurant, ok := r.byID[restaurantID]
	if !ok {
		return search.RestaurantDetail{}, errs.Newf(errs.CodeNotFound, "restaurant %q not found", restaurantID)
	}
	return restaurant, nil
}

// Upsert inserts or replaces a restaurant by ID.
func (r *RestaurantRepository) Upsert(_ context.Context, restaurant search.RestaurantDetail) error {
	if strings.TrimSpace(restaurant.RestaurantID) == "" {
		return errs.New(errs.CodeInvalidArgument, "restaurant_id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[restaurant.RestaurantID] = restaurant
	return nil
}

// Search applies hard filters, scores soft text matches, and returns the top-K
// candidates ordered by descending score then ascending name.
func (r *RestaurantRepository) Search(_ context.Context, query search.SearchQuery) ([]search.RestaurantCandidate, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	candidates := make([]search.RestaurantCandidate, 0, len(r.byID))
	for _, restaurant := range r.byID {
		if !matchesFilter(restaurant, query.Filter) {
			continue
		}
		candidates = append(candidates, scoreCandidate(restaurant, query.Text))
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].Name < candidates[j].Name
	})

	topK := query.TopK
	if topK <= 0 {
		topK = defaultTopK
	}
	if len(candidates) > topK {
		candidates = candidates[:topK]
	}
	return candidates, nil
}

func matchesFilter(restaurant search.RestaurantDetail, filter search.RestaurantFilter) bool {
	if len(filter.Cuisines) > 0 && !containsFoldAny(restaurant.Cuisines, filter.Cuisines) {
		return false
	}
	if len(filter.PriceLevels) > 0 {
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
	// A missing three-state attribute means "unknown", which cannot satisfy a
	// hard condition; it is never treated as false.
	if filter.OpenNow != nil {
		value, ok := restaurant.Attributes["open_now"]
		if !ok || value != strconv.FormatBool(*filter.OpenNow) {
			return false
		}
	}
	return true
}

func scoreCandidate(restaurant search.RestaurantDetail, text string) search.RestaurantCandidate {
	candidate := search.RestaurantCandidate{
		RestaurantID: restaurant.RestaurantID,
		Name:         restaurant.Name,
		Address:      restaurant.Address,
		Score:        1,
		Reasons:      []string{"matches hard filters"},
	}
	if strings.TrimSpace(text) == "" {
		return candidate
	}

	needle := strings.ToLower(text)
	if strings.Contains(strings.ToLower(restaurant.Name), needle) {
		candidate.Score += 2
		candidate.Reasons = append(candidate.Reasons, "name matches query")
	}
	if strings.Contains(strings.ToLower(restaurant.Address), needle) {
		candidate.Score += 1
		candidate.Reasons = append(candidate.Reasons, "address matches query")
	}
	if containsFoldAny(restaurant.Cuisines, []string{needle}) {
		candidate.Score += 1
		candidate.Reasons = append(candidate.Reasons, "cuisine matches query")
	}
	return candidate
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
