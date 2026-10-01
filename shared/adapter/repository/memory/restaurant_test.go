package memory

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/search"
)

func newRestaurant(id int64, name, neighborhood string, cuisines []string, price int, rating float64, openNow string) search.RestaurantDetail {
	return search.RestaurantDetail{
		RestaurantID: id,
		Name:         name,
		Address:      neighborhood + ", New York, NY",
		Cuisines:     cuisines,
		PriceLevel:   &price,
		Rating:       &rating,
		Source:       "google_local_2021",
		SnapshotAt:   time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC),
		Attributes:   map[string]string{"open_now": openNow},
	}
}

func seedRestaurants(t *testing.T, repo *RestaurantRepository) {
	t.Helper()
	ctx := context.Background()
	fixtures := []search.RestaurantDetail{
		newRestaurant(1, "Quiet Pizza", "Greenwich Village", []string{"pizza", "italian"}, 2, 4.6, "true"),
		newRestaurant(2, "Loud Sushi", "SoHo", []string{"sushi", "japanese"}, 3, 4.8, "false"),
		newRestaurant(3, "Budget Tacos", "SoHo", []string{"mexican"}, 1, 4.1, "true"),
	}
	for _, fixture := range fixtures {
		if err := repo.Upsert(ctx, fixture); err != nil {
			t.Fatalf("seed upsert %d: %v", fixture.RestaurantID, err)
		}
	}
}

func TestRestaurantGetByID(t *testing.T) {
	repo := NewRestaurantRepository()
	seedRestaurants(t, repo)

	got, err := repo.GetByID(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != "Quiet Pizza" {
		t.Fatalf("name = %q", got.Name)
	}
}

func TestRestaurantGetByIDNotFoundUsesSentinel(t *testing.T) {
	repo := NewRestaurantRepository()
	_, err := repo.GetByID(context.Background(), 987654321)
	if !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("want errs.ErrNotFound, got %v", err)
	}
}

func TestRestaurantUpsertIsIdempotent(t *testing.T) {
	repo := NewRestaurantRepository()
	ctx := context.Background()
	restaurant := newRestaurant(1, "Quiet Pizza", "SoHo", []string{"pizza"}, 2, 4.6, "true")

	for i := 0; i < 3; i++ {
		if err := repo.Upsert(ctx, restaurant); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}
	candidates, err := repo.Search(ctx, search.SearchQuery{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("repeated upserts produced %d rows, want 1", len(candidates))
	}
}

func TestRestaurantUpsertRequiresID(t *testing.T) {
	repo := NewRestaurantRepository()
	err := repo.Upsert(context.Background(), search.RestaurantDetail{Name: "no id"})
	if errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("want invalid_argument, got %v", err)
	}
}

func TestRestaurantSearchAppliesHardFilters(t *testing.T) {
	repo := NewRestaurantRepository()
	seedRestaurants(t, repo)
	ctx := context.Background()

	priceThree := []int{3}
	cases := []struct {
		name   string
		filter search.RestaurantFilter
		want   []int64
	}{
		{"cuisine", search.RestaurantFilter{Cuisines: []string{"pizza"}}, []int64{1}},
		{"price", search.RestaurantFilter{PriceLevels: priceThree}, []int64{2}},
		{"neighborhood", search.RestaurantFilter{Neighborhood: "soho"}, []int64{2, 3}},
		{"combined", search.RestaurantFilter{Cuisines: []string{"mexican"}, PriceLevels: []int{1}}, []int64{3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repo.Search(ctx, search.SearchQuery{Filter: tc.filter})
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			gotIDs := sortedCopy(candidateIDs(got))
			wantIDs := sortedCopy(tc.want)
			if len(gotIDs) != len(wantIDs) {
				t.Fatalf("got %v, want %v", gotIDs, wantIDs)
			}
			for i := range gotIDs {
				if gotIDs[i] != wantIDs[i] {
					t.Fatalf("got %v, want %v", gotIDs, wantIDs)
				}
			}
		})
	}
}

func TestRestaurantSearchMinRatingAndOpenNow(t *testing.T) {
	repo := NewRestaurantRepository()
	seedRestaurants(t, repo)
	ctx := context.Background()

	minRating := 4.5
	got, err := repo.Search(ctx, search.SearchQuery{Filter: search.RestaurantFilter{MinRating: &minRating}})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if ids := candidateIDs(got); len(ids) != 2 {
		t.Fatalf("min rating filter returned %v, want r1 and r2", ids)
	}

	openNow := true
	got, err = repo.Search(ctx, search.SearchQuery{Filter: search.RestaurantFilter{OpenNow: &openNow}})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if ids := candidateIDs(got); len(ids) != 2 {
		t.Fatalf("open_now filter returned %v, want r1 and r3", ids)
	}
}

func TestRestaurantSearchMissingAttributeIsUnknownNotFalse(t *testing.T) {
	repo := NewRestaurantRepository()
	ctx := context.Background()
	// No "open_now" attribute at all: an unknown hard condition must not match.
	restaurant := newRestaurant(9, "Mystery Diner", "SoHo", []string{"diner"}, 2, 4.0, "true")
	delete(restaurant.Attributes, "open_now")
	if err := repo.Upsert(ctx, restaurant); err != nil {
		t.Fatal(err)
	}

	openNow := false
	got, err := repo.Search(ctx, search.SearchQuery{Filter: search.RestaurantFilter{OpenNow: &openNow}})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("unknown attribute should not satisfy open_now=false, got %v", candidateIDs(got))
	}
}

func TestRestaurantSearchRanksTextMatchesAndHonoursTopK(t *testing.T) {
	repo := NewRestaurantRepository()
	seedRestaurants(t, repo)

	got, err := repo.Search(context.Background(), search.SearchQuery{Text: "pizza"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) == 0 || got[0].RestaurantID != 1 {
		t.Fatalf("text search should rank the name match first, got %v", candidateIDs(got))
	}

	limited, err := repo.Search(context.Background(), search.SearchQuery{Text: "pizza", TopK: 1})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("top_k=1 returned %d results", len(limited))
	}
}

func TestRestaurantSearchDefaultTopKIsBounded(t *testing.T) {
	repo := NewRestaurantRepository()
	ctx := context.Background()
	for i := 0; i < 25; i++ {
		restaurant := newRestaurant(int64(i+1), "Diner", "SoHo", []string{"diner"}, 2, 4.0, "true")
		if err := repo.Upsert(ctx, restaurant); err != nil {
			t.Fatal(err)
		}
	}
	got, err := repo.Search(ctx, search.SearchQuery{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != defaultTopK {
		t.Fatalf("default top_k returned %d results, want %d", len(got), defaultTopK)
	}
}

// sortedCopy returns a sorted copy so assertions do not depend on tie-break order.
func sortedCopy(in []int64) []int64 {
	out := append([]int64(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func candidateIDs(candidates []search.RestaurantCandidate) []int64 {
	ids := make([]int64, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.RestaurantID)
	}
	return ids
}
