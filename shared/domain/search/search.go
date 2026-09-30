// Package search defines vendor-neutral restaurant search DTOs.
package search

import "time"

// RestaurantFilter holds the hard, deterministic filters applied to a search.
// Optional booleans and numbers are pointers so that "unset" is distinguishable
// from an explicit false or zero value.
type RestaurantFilter struct {
	Cuisines     []string `json:"cuisines,omitempty"`
	PriceLevels  []int    `json:"price_levels,omitempty"`
	MinRating    *float64 `json:"min_rating,omitempty"`
	Neighborhood string   `json:"neighborhood,omitempty"`
	OpenNow      *bool    `json:"open_now,omitempty"`
}

// RestaurantCandidate is a ranked search result.
type RestaurantCandidate struct {
	RestaurantID int64    `json:"restaurant_id"`
	Name         string   `json:"name"`
	Address      string   `json:"address,omitempty"`
	Score        float64  `json:"score"`
	Reasons      []string `json:"reasons,omitempty"`
}

// SearchQuery is a single restaurant search request.
type SearchQuery struct {
	Text   string           `json:"text,omitempty"`
	Filter RestaurantFilter `json:"filter,omitempty"`
	TopK   int              `json:"top_k,omitempty"`
}

// RestaurantDetail is the minimal restaurant projection needed by M0. Fields
// are expected to grow in M1 once the tables are defined.
type RestaurantDetail struct {
	RestaurantID int64             `json:"restaurant_id"`
	Name         string            `json:"name"`
	Address      string            `json:"address,omitempty"`
	Cuisines     []string          `json:"cuisines,omitempty"`
	PriceLevel   *int              `json:"price_level,omitempty"`
	Rating       *float64          `json:"rating,omitempty"`
	Source       string            `json:"source,omitempty"`
	SnapshotAt   time.Time         `json:"snapshot_at,omitempty"`
	Attributes   map[string]string `json:"attributes,omitempty"`
}
