// Package contract holds the behaviour suite that every implementation of the
// write-side ports must satisfy. Running the same suite against the in-memory
// store and the Postgres adapter is what keeps the mock from drifting.
package contract

import (
	"context"
	"testing"
	"time"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/search"
	"github.com/zed1995/platepilot/shared/store"
)

// ReadStores bundles the read-side ports under test.
type ReadStores struct {
	Restaurants store.RestaurantRepository
	Knowledge   store.KnowledgeRepository
}

// ReadFactory returns a fresh, empty ReadStores for one subtest.
type ReadFactory func(t *testing.T) ReadStores

// RunRead executes the read-side contract against the given factory.
//
// The read path is where a silent filter is most expensive: a hard condition
// that is quietly ignored returns a plausible list, and the wrongness only
// surfaces when a user sees a restaurant they explicitly excluded. So this suite
// asserts on *which rows are absent* at least as much as on which are present.
func RunRead(t *testing.T, newStores ReadFactory) {
	t.Helper()
	t.Run("GetByID", func(t *testing.T) { runReadGetByID(t, newStores(t).Restaurants) })
	t.Run("Search", func(t *testing.T) { runReadSearch(t, newStores(t).Restaurants) })
	t.Run("MatchByText", func(t *testing.T) { runReadMatchByText(t, newStores(t).Restaurants) })
}

// ReadSeedRows exposes the fixture to adapter tests that need to seed a
// repository before running the contract.
func ReadSeedRows() []search.RestaurantDetail { return seedRows() }

// seedRows are the rows every read test filters against. They are chosen so
// that each one violates exactly one filter, which is what makes an
// over-permissive implementation detectable: a filter that is silently dropped
// returns the row that was supposed to be excluded.
func seedRows() []search.RestaurantDetail {
	cheapest := 1
	mid := 2
	manhattan := 4.5
	brooklyn := 3.9
	at := time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC)

	return []search.RestaurantDetail{
		{
			RestaurantID: 1, Name: "Casa Verdi", Address: "7 Carmine St, Manhattan",
			Borough: "manhattan", Cuisines: []string{"italian", "pizza"}, PriceLevel: &cheapest,
			Rating: &manhattan, RatingCount: 300, Source: "google_local_2021",
			SnapshotStatus: "open", KnowledgeScore: 0.9, IsActiveForDemo: true,
			SnapshotAt: at, Attributes: map[string]string{"open_now": "true"},
		},
		{
			RestaurantID: 2, Name: "Noodle Bar", Address: "22 Bowery, Manhattan",
			Borough: "manhattan", Cuisines: []string{"chinese", "noodle"}, PriceLevel: &mid,
			Rating: &brooklyn, RatingCount: 50, Source: "google_local_2021",
			SnapshotStatus: "open", KnowledgeScore: 0.5, IsActiveForDemo: true,
			SnapshotAt: at, Attributes: map[string]string{"open_now": "false"},
		},
		{
			RestaurantID: 3, Name: "Vino Rosso", Address: "5 Court St, Brooklyn",
			Borough: "brooklyn", Cuisines: []string{"italian"}, PriceLevel: &mid,
			Rating: &manhattan, RatingCount: 120, Source: "google_local_2021",
			SnapshotStatus: "open", KnowledgeScore: 0.7, IsActiveForDemo: true,
			SnapshotAt: at, Attributes: map[string]string{"open_now": "unknown"},
		},
		{
			RestaurantID: 4, Name: "Taqueria Del Norte", Address: "9 Grand St, Manhattan",
			Borough: "manhattan", Cuisines: []string{"mexican"}, PriceLevel: &cheapest,
			Rating: &brooklyn, RatingCount: 0, Source: "google_local_2021",
			SnapshotStatus: "closed", KnowledgeScore: 0.3, IsActiveForDemo: true,
			SnapshotAt: at,
		},
		{
			// No price level and no rating at all: the rows a filter must exclude
			// rather than treat as a match.
			RestaurantID: 5, Name: "Unknown Eats", Address: "1 Nowhere",
			Borough: "manhattan", Cuisines: []string{"italian"}, Source: "google_local_2021",
			SnapshotStatus: "unknown", KnowledgeScore: 0.1, IsActiveForDemo: true,
			SnapshotAt: at,
		},
	}
}

func runReadGetByID(t *testing.T, repo store.RestaurantRepository) {
	ctx := context.Background()

	got, err := repo.GetByID(ctx, 1)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.RestaurantID != 1 || got.Name != "Casa Verdi" {
		t.Fatalf("unexpected detail: %+v", got)
	}
	if got.PriceLevel == nil || *got.PriceLevel != 1 {
		t.Fatalf("price level must survive the projection: %+v", got.PriceLevel)
	}
	if got.Rating == nil || *got.Rating != 4.5 || got.RatingCount != 300 {
		t.Fatalf("rating and its sample size must both survive: %v / %d", got.Rating, got.RatingCount)
	}
	if got.SnapshotAt.IsZero() {
		t.Fatal("a candidate cannot show when its data was observed without this")
	}

	if _, err := repo.GetByID(ctx, 999); errs.CodeOf(err) != errs.CodeNotFound {
		t.Fatalf("want not_found for an unknown id, got %v", err)
	}
	if _, err := repo.GetByID(ctx, 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("want invalid_argument for a zero id, got %v", err)
	}
}

func runReadSearch(t *testing.T, repo store.RestaurantRepository) {
	ctx := context.Background()

	t.Run("every returned row satisfies the filter", func(t *testing.T) {
		cheap := 1
		minRating := 4.0
		openNow := true
		for _, tc := range []struct {
			name   string
			filter search.RestaurantFilter
		}{
			{"cuisine", search.RestaurantFilter{Cuisines: []string{"italian"}}},
			{"price", search.RestaurantFilter{PriceLevels: []int{cheap}}},
			{"rating", search.RestaurantFilter{MinRating: &minRating}},
			{"borough", search.RestaurantFilter{Borough: "manhattan"}},
			{"open now", search.RestaurantFilter{OpenNow: &openNow}},
			{
				"combined",
				search.RestaurantFilter{
					Cuisines: []string{"italian"}, PriceLevels: []int{1, 2},
					Borough: "manhattan", MinRating: &minRating,
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got, err := repo.Search(ctx, search.SearchQuery{Filter: tc.filter})
				if err != nil {
					t.Fatalf("search: %v", err)
				}
				for _, candidate := range got {
					assertSatisfies(t, candidate.RestaurantID, tc.filter)
				}
			})
		}
	})

	// "Unknown" is not "cheap" and not "closed". A row missing the field has to
	// be excluded from both, or the filter is inventing a fact.
	t.Run("a missing price is not a matching price", func(t *testing.T) {
		got, err := repo.Search(ctx, search.SearchQuery{
			Filter: search.RestaurantFilter{PriceLevels: []int{1, 2}},
		})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		for _, candidate := range got {
			if candidate.RestaurantID == 5 {
				t.Fatal("a restaurant with no price level must not match a price filter")
			}
		}
	})

	t.Run("a missing rating is not a matching rating", func(t *testing.T) {
		minRating := 1.0
		got, err := repo.Search(ctx, search.SearchQuery{
			Filter: search.RestaurantFilter{MinRating: &minRating},
		})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		for _, candidate := range got {
			if candidate.RestaurantID == 5 {
				t.Fatal("a restaurant with no rating must not match a rating filter")
			}
		}
	})

	// open_now is three-state. "unknown" satisfies neither value, so the row
	// drops out of both the true and the false result.
	t.Run("an unknown open_now satisfies neither value", func(t *testing.T) {
		for _, want := range []bool{true, false} {
			value := want
			got, err := repo.Search(ctx, search.SearchQuery{
				Filter: search.RestaurantFilter{OpenNow: &value},
			})
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			for _, candidate := range got {
				if candidate.RestaurantID == 3 {
					t.Fatalf("open_now=%t must not match an unknown attribute", want)
				}
				if candidate.RestaurantID == 5 {
					t.Fatalf("open_now=%t must not match a row with no attribute", want)
				}
			}
		}
	})

	// An empty result is an answer. Turning it into an error would make a
	// client retry a question that has no answer.
	t.Run("an empty result is not an error", func(t *testing.T) {
		got, err := repo.Search(ctx, search.SearchQuery{
			Filter: search.RestaurantFilter{Cuisines: []string{"nonexistent-cuisine"}},
		})
		if err != nil {
			t.Fatalf("an empty result must not be an error: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("want no candidates, got %d", len(got))
		}
	})

	t.Run("results are ordered reproducibly", func(t *testing.T) {
		filter := search.RestaurantFilter{Borough: "manhattan"}
		first, err := repo.Search(ctx, search.SearchQuery{Filter: filter})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		second, err := repo.Search(ctx, search.SearchQuery{Filter: filter})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(first) != len(second) {
			t.Fatalf("result counts differ: %d vs %d", len(first), len(second))
		}
		for i := range first {
			if first[i].RestaurantID != second[i].RestaurantID {
				t.Fatalf("order differs at %d: %d vs %d", i,
					first[i].RestaurantID, second[i].RestaurantID)
			}
		}
	})

	t.Run("top_k is honoured and bounded", func(t *testing.T) {
		filter := search.RestaurantFilter{Borough: "manhattan"}
		for _, topK := range []int{1, 2, 3} {
			got, err := repo.Search(ctx, search.SearchQuery{Filter: filter, TopK: topK})
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if len(got) > topK {
				t.Fatalf("top_k=%d returned %d rows", topK, len(got))
			}
		}
		// An absurd limit is clamped rather than honoured: one request must not
		// be able to pull the whole corpus.
		huge, err := repo.Search(ctx, search.SearchQuery{Filter: filter, TopK: 100000})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(huge) > maxContractTopK {
			t.Fatalf("top_k=100000 returned %d rows, want at most %d", len(huge), maxContractTopK)
		}
	})

	// A typo and a valid-but-empty query must not look the same, or a user is
	// told there is nothing in a district that simply is not one of the five.
	t.Run("an unknown borough is rejected rather than answered empty", func(t *testing.T) {
		_, err := repo.Search(ctx, search.SearchQuery{
			Filter: search.RestaurantFilter{Borough: "New Jersey"},
		})
		if errs.CodeOf(err) != errs.CodeRetrievalInvalidFilter {
			t.Fatalf("want retrieval_invalid_filter, got %v", err)
		}
	})

	t.Run("an out-of-range price level is rejected", func(t *testing.T) {
		_, err := repo.Search(ctx, search.SearchQuery{
			Filter: search.RestaurantFilter{PriceLevels: []int{9}},
		})
		if errs.CodeOf(err) != errs.CodeRetrievalInvalidFilter {
			t.Fatalf("want retrieval_invalid_filter, got %v", err)
		}
	})
}

// maxContractTopK is the largest page any implementation may return.
const maxContractTopK = 100

// assertSatisfies checks one candidate against the filter that produced it.
func assertSatisfies(t *testing.T, restaurantID int64, filter search.RestaurantFilter) {
	t.Helper()
	switch restaurantID {
	case 1, 2, 3, 4, 5:
	default:
		t.Fatalf("unexpected restaurant %d in the result", restaurantID)
	}
	rows := seedRows()
	var row search.RestaurantDetail
	for _, candidate := range rows {
		if candidate.RestaurantID == restaurantID {
			row = candidate
		}
	}
	if row.RestaurantID == 0 {
		t.Fatalf("restaurant %d is not a seeded row", restaurantID)
	}
	if len(filter.Cuisines) > 0 {
		found := false
		for _, cuisine := range row.Cuisines {
			for _, want := range filter.Cuisines {
				if cuisine == want {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("restaurant %d has cuisines %v, filter wants %v",
				restaurantID, row.Cuisines, filter.Cuisines)
		}
	}
	if len(filter.PriceLevels) > 0 {
		if row.PriceLevel == nil {
			t.Fatalf("restaurant %d has no price level but matched %v", restaurantID, filter.PriceLevels)
		}
		matched := false
		for _, want := range filter.PriceLevels {
			if *row.PriceLevel == want {
				matched = true
			}
		}
		if !matched {
			t.Fatalf("restaurant %d price %d does not match %v", restaurantID, *row.PriceLevel, filter.PriceLevels)
		}
	}
	if filter.MinRating != nil {
		if row.Rating == nil {
			t.Fatalf("restaurant %d has no rating but matched min %v", restaurantID, *filter.MinRating)
		}
		if *row.Rating < *filter.MinRating {
			t.Fatalf("restaurant %d rating %v is below %v", restaurantID, *row.Rating, *filter.MinRating)
		}
	}
	if filter.Borough != "" && row.Borough != filter.Borough {
		t.Fatalf("restaurant %d is in %s, filter wants %s", restaurantID, row.Borough, filter.Borough)
	}
	if filter.OpenNow != nil && row.Attributes["open_now"] != boolText(*filter.OpenNow) {
		t.Fatalf("restaurant %d open_now=%q does not match %v",
			restaurantID, row.Attributes["open_now"], *filter.OpenNow)
	}
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func runReadMatchByText(t *testing.T, repo store.RestaurantRepository) {
	ctx := context.Background()

	t.Run("matches a name case-insensitively", func(t *testing.T) {
		got, err := repo.MatchByText(ctx, "casa verdi", 10)
		if err != nil {
			t.Fatalf("match: %v", err)
		}
		if !containsID(got, 1) {
			t.Fatalf("want restaurant 1, got %v", candidateIDsOf(got))
		}
	})

	t.Run("matches an address fragment", func(t *testing.T) {
		got, err := repo.MatchByText(ctx, "carmine", 10)
		if err != nil {
			t.Fatalf("match: %v", err)
		}
		if !containsID(got, 1) {
			t.Fatalf("want restaurant 1 by address, got %v", candidateIDsOf(got))
		}
	})

	// One restaurant matching on both name and address must appear once, not
	// once per matching field.
	t.Run("returns a restaurant once even when both fields match", func(t *testing.T) {
		got, err := repo.MatchByText(ctx, "casa carmine", 10)
		if err != nil {
			t.Fatalf("match: %v", err)
		}
		seen := map[int64]int{}
		for _, candidate := range got {
			seen[candidate.RestaurantID]++
		}
		for id, count := range seen {
			if count > 1 {
				t.Fatalf("restaurant %d returned %d times", id, count)
			}
		}
	})

	// A literal percent is not a wildcard. Without escaping, "50%" matches every
	// row in the table.
	t.Run("a wildcard character is matched literally", func(t *testing.T) {
		got, err := repo.MatchByText(ctx, "50%", 100)
		if err != nil {
			t.Fatalf("match: %v", err)
		}
		if len(got) >= 5 {
			t.Fatalf("a literal %% matched %d rows; the pattern was not escaped", len(got))
		}
	})

	// A trigram index cannot match two characters, so answering would mean
	// scanning the corpus. Refusing is both faster and more honest.
	t.Run("text too short to index is refused", func(t *testing.T) {
		_, err := repo.MatchByText(ctx, "ab", 10)
		if errs.CodeOf(err) != errs.CodeRetrievalQueryTooShort {
			t.Fatalf("want retrieval_query_too_short, got %v", err)
		}
	})

	t.Run("empty text is refused", func(t *testing.T) {
		_, err := repo.MatchByText(ctx, "   ", 10)
		if errs.CodeOf(err) != errs.CodeRetrievalEmptyQuery {
			t.Fatalf("want retrieval_empty_query, got %v", err)
		}
	})

	// A restaurant with no address must still be ranked by its name. Coalescing
	// the address similarity is what keeps it from sorting last every time.
	t.Run("a missing address does not hide the row", func(t *testing.T) {
		got, err := repo.MatchByText(ctx, "unknown eats", 10)
		if err != nil {
			t.Fatalf("match: %v", err)
		}
		if !containsID(got, 5) {
			t.Fatalf("want restaurant 5, got %v", candidateIDsOf(got))
		}
		if got[0].RestaurantID != 5 {
			t.Fatalf("an exact name match must rank first, got %v", candidateIDsOf(got))
		}
	})

	t.Run("honours the limit", func(t *testing.T) {
		got, err := repo.MatchByText(ctx, "restaurant", 1)
		if err != nil {
			t.Fatalf("match: %v", err)
		}
		if len(got) > 1 {
			t.Fatalf("limit=1 returned %d rows", len(got))
		}
	})
}

func containsID(candidates []search.RestaurantCandidate, id int64) bool {
	for _, candidate := range candidates {
		if candidate.RestaurantID == id {
			return true
		}
	}
	return false
}

func candidateIDsOf(candidates []search.RestaurantCandidate) []int64 {
	out := make([]int64, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, candidate.RestaurantID)
	}
	return out
}
