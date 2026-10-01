package search

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestSearchQueryJSONRoundTrip(t *testing.T) {
	minRating := 4.2
	openNow := true
	original := SearchQuery{
		Text: "quiet pizza",
		Filter: RestaurantFilter{
			Cuisines:     []string{"pizza"},
			PriceLevels:  []int{2, 3},
			MinRating:    &minRating,
			Neighborhood: "Greenwich Village",
			OpenNow:      &openNow,
		},
		TopK: 5,
	}
	var decoded SearchQuery
	data, _ := json.Marshal(original)
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(original, decoded) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", decoded, original)
	}
}

func TestUnsetOptionalFiltersStayNil(t *testing.T) {
	original := SearchQuery{Text: "any"}
	data, _ := json.Marshal(original.Filter)
	if string(data) != "{}" {
		t.Fatalf("unset filter should serialize as {}, got %s", data)
	}
}

func TestRestaurantDetailJSONRoundTrip(t *testing.T) {
	price := 2
	rating := 4.5
	original := RestaurantDetail{
		RestaurantID: 1,
		Name:         "Joe's Pizza",
		Address:      "7 Carmine St",
		Cuisines:     []string{"pizza"},
		PriceLevel:   &price,
		Rating:       &rating,
		Source:       "google_local_2021",
		SnapshotAt:   time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC),
		Attributes:   map[string]string{"outdoor_seating": "true"},
	}
	var decoded RestaurantDetail
	data, _ := json.Marshal(original)
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(original, decoded) {
		t.Fatalf("round trip mismatch: got %+v want %+v", decoded, original)
	}
}

// Matches is what stops a soft recall from reintroducing a restaurant the user
// excluded, so its edge cases are the whole contract.
func TestRestaurantFilterMatches(t *testing.T) {
	price := func(v int) *int { return &v }
	cases := []struct {
		name      string
		filter    RestaurantFilter
		candidate RestaurantCandidate
		want      bool
	}{
		{
			name:      "no filter matches everything",
			filter:    RestaurantFilter{},
			candidate: RestaurantCandidate{Name: "anything"},
			want:      true,
		},
		{
			name:      "cuisine satisfied",
			filter:    RestaurantFilter{Cuisines: []string{"italian"}},
			candidate: RestaurantCandidate{Cuisines: []string{"pizza", "italian"}},
			want:      true,
		},
		{
			name:      "cuisine absent",
			filter:    RestaurantFilter{Cuisines: []string{"italian"}},
			candidate: RestaurantCandidate{Cuisines: []string{"pizza"}},
			want:      false,
		},
		{
			name:      "cuisine unknown is not a match",
			filter:    RestaurantFilter{Cuisines: []string{"italian"}},
			candidate: RestaurantCandidate{},
			want:      false,
		},
		{
			name:      "one of several price levels is enough",
			filter:    RestaurantFilter{PriceLevels: []int{2, 3}},
			candidate: RestaurantCandidate{PriceLevel: price(3)},
			want:      true,
		},
		{
			name:      "price unknown is not a match",
			filter:    RestaurantFilter{PriceLevels: []int{1}},
			candidate: RestaurantCandidate{},
			want:      false,
		},
		{
			name:      "rating at the boundary satisfies the minimum",
			filter:    RestaurantFilter{MinRating: floatPtr(4.0)},
			candidate: RestaurantCandidate{Rating: floatPtr(4.0)},
			want:      true,
		},
		{
			name:      "rating below the minimum",
			filter:    RestaurantFilter{MinRating: floatPtr(4.0)},
			candidate: RestaurantCandidate{Rating: floatPtr(3.9)},
			want:      false,
		},
		{
			name:      "rating unknown is not a match",
			filter:    RestaurantFilter{MinRating: floatPtr(4.0)},
			candidate: RestaurantCandidate{},
			want:      false,
		},
		{
			name:      "borough in any casing",
			filter:    RestaurantFilter{Borough: "Manhattan"},
			candidate: RestaurantCandidate{Borough: "manhattan"},
			want:      true,
		},
		{
			name:      "borough mismatch",
			filter:    RestaurantFilter{Borough: "manhattan"},
			candidate: RestaurantCandidate{Borough: "brooklyn"},
			want:      false,
		},
		{
			name:      "neighborhood is a substring of the address",
			filter:    RestaurantFilter{Neighborhood: "Carmine"},
			candidate: RestaurantCandidate{Address: "7 Carmine St"},
			want:      true,
		},
		{
			name:      "neighborhood absent from the address",
			filter:    RestaurantFilter{Neighborhood: "Carmine"},
			candidate: RestaurantCandidate{Address: "22 Bowery"},
			want:      false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.filter.Matches(tc.candidate); got != tc.want {
				t.Fatalf("Matches = %v, want %v", got, tc.want)
			}
		})
	}
}

func floatPtr(v float64) *float64 { return &v }
