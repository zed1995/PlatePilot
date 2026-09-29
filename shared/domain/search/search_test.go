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
		RestaurantID: "r1",
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
