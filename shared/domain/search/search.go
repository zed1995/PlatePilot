// Package search defines vendor-neutral restaurant search DTOs.
package search

import (
	"fmt"
	"strings"
	"time"

	"github.com/zed1995/platepilot/shared/domain/errs"
)

// Boroughs are the administrative areas the corpus is scoped to. The list is
// closed because it is also the set of index partitions: a restaurant outside
// these five has no borough, and a filter naming a sixth one is a mistake rather
// than an empty result.
var Boroughs = []string{"manhattan", "brooklyn", "queens", "bronx", "staten_island"}

// ValidBorough reports whether name is one of the five boroughs. It is
// case-insensitive so a caller may send "Manhattan"; the canonical lowercase
// form is what gets stored and compared.
func ValidBorough(name string) bool {
	for _, borough := range Boroughs {
		if strings.EqualFold(borough, name) {
			return true
		}
	}
	return false
}

// CanonicalBorough returns the lowercase spelling of a borough name.
func CanonicalBorough(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// GeoPoint is a WGS84 coordinate in the order the GeoJSON spec defines, which
// is longitude first and the opposite of how most map APIs order a pair.
type GeoPoint struct {
	Longitude float64 `json:"longitude"`
	Latitude  float64 `json:"latitude"`
}

// RestaurantFilter holds the hard, deterministic filters applied to a search.
// Optional booleans and numbers are pointers so that "unset" is distinguishable
// from an explicit false or zero value.
type RestaurantFilter struct {
	Cuisines    []string `json:"cuisines,omitempty"`
	PriceLevels []int    `json:"price_levels,omitempty"`
	MinRating   *float64 `json:"min_rating,omitempty"`
	// Neighborhood matches anywhere in the address. It is a text filter rather
	// than a geographic one: "Lower East Side" is not a point, and pretending
	// otherwise would need a geocoder this project does not have.
	Neighborhood string `json:"neighborhood,omitempty"`
	// OpenNow is a three-state attribute, so an absent attribute is "unknown"
	// and can never satisfy it. See restaurant.TriStateUnknown.
	OpenNow *bool `json:"open_now,omitempty"`

	// Borough is a hard filter that also selects the vector index, which is why
	// it is a first-class field rather than an attribute lookup: a query with a
	// borough must be answered by the borough index, and one without must not
	// land on it by accident. Values outside the five boroughs are rejected at
	// the edge rather than matched as a string, because an empty result set and
	// a misspelled borough look identical otherwise.
	Borough string `json:"borough,omitempty"`

	// QueryOrigin and MaxDistanceMeters together express "near me". Both must
	// be set for the filter to apply: a radius without a centre is meaningless
	// and a centre without a radius is a viewport, not a distance. Distance is
	// measured in metres, the unit the product reasons in.
	QueryOrigin       *GeoPoint `json:"query_origin,omitempty"`
	MaxDistanceMeters *int      `json:"max_distance_meters,omitempty"`
}

// HasDistance reports whether the near-me filter is fully specified.
func (f RestaurantFilter) HasDistance() bool {
	return f.QueryOrigin != nil && f.MaxDistanceMeters != nil && *f.MaxDistanceMeters > 0
}

// IsEmpty reports whether the filter constrains nothing, in which case a
// structured search is just a listing and its score carries no information.
func (f RestaurantFilter) IsEmpty() bool {
	return len(f.Cuisines) == 0 && len(f.PriceLevels) == 0 && f.MinRating == nil &&
		f.Neighborhood == "" && f.OpenNow == nil && f.Borough == "" && !f.HasDistance()
}

// Matches reports whether a candidate satisfies every hard condition.
//
// It exists because fusion merges the output of channels that answer different
// questions. The structured channel's rows all satisfy the filter; the keyword
// and vector channels do not, because a name match or a semantic match cannot
// see a price level. Without a check at the point where the channels are
// combined, a soft channel would reintroduce a restaurant the user explicitly
// excluded — and it would do so invisibly, in a result that looks exactly like
// a correct one.
//
// The comparison is against the candidate's own fields, never against a value
// the caller supplied, so it cannot be satisfied by a caller asserting that a
// row matches. A missing value never matches: a candidate with no rating cannot
// satisfy a minimum-rating condition, and treating unknown as a pass is the bug
// this method exists to prevent.
func (f RestaurantFilter) Matches(candidate RestaurantCandidate) bool {
	if len(f.Cuisines) > 0 {
		if !anyString(candidate.Cuisines, f.Cuisines) {
			return false
		}
	}
	if len(f.PriceLevels) > 0 {
		if candidate.PriceLevel == nil || !anyIntOne(f.PriceLevels, *candidate.PriceLevel) {
			return false
		}
	}
	if f.MinRating != nil {
		if candidate.Rating == nil || *candidate.Rating < *f.MinRating {
			return false
		}
	}
	if f.Borough != "" && candidate.Borough != CanonicalBorough(f.Borough) {
		return false
	}
	if f.Neighborhood != "" &&
		!strings.Contains(strings.ToLower(candidate.Address), strings.ToLower(f.Neighborhood)) {
		return false
	}
	// OpenNow and the distance filter are not re-checked here. Both need data a
	// candidate does not carry — the live open state and a computed distance —
	// and the repository that evaluated them is the only place that can. They are
	// enforced by pushing them into the structured channel's statement, which is
	// what makes them hard conditions at all.
	return true
}

// Describe renders the active hard conditions as one Chinese phrase.
//
// It is the single rendering of "what the user asked for" that the trace, the
// tool card, and the answer prompt all quote. Three callers, one string: the
// alternative is a user reading a reason that names three of the four
// conditions the search actually enforced, with nothing to reveal the omission.
//
// An empty filter renders as the empty string rather than as a phrase meaning
// "nothing", because "no conditions" is not a condition and a caller that wants
// to say so can test IsEmpty itself.
func (f RestaurantFilter) Describe() string {
	var parts []string
	if len(f.Cuisines) > 0 {
		parts = append(parts, "菜系="+strings.Join(f.Cuisines, "/"))
	}
	if len(f.PriceLevels) > 0 {
		levels := make([]string, 0, len(f.PriceLevels))
		for _, level := range f.PriceLevels {
			levels = append(levels, fmt.Sprintf("$%d", level))
		}
		parts = append(parts, "价格="+strings.Join(levels, "/"))
	}
	if f.MinRating != nil {
		parts = append(parts, fmt.Sprintf("评分>=%.1f", *f.MinRating))
	}
	if f.Neighborhood != "" {
		parts = append(parts, "地区="+f.Neighborhood)
	}
	if f.Borough != "" {
		parts = append(parts, "行政区="+f.Borough)
	}
	if f.OpenNow != nil {
		parts = append(parts, fmt.Sprintf("营业=%t", *f.OpenNow))
	}
	if f.HasDistance() {
		parts = append(parts, fmt.Sprintf("距离<%dm", *f.MaxDistanceMeters))
	}
	return strings.Join(parts, "、")
}

// anyString reports whether values contains at least one of the wanted entries.
func anyString(values, wanted []string) bool {
	for _, want := range wanted {
		for _, value := range values {
			if value == want {
				return true
			}
		}
	}
	return false
}

// anyInt reports whether values contains any of the wanted entries.
func anyInt(values, wanted []int) bool {
	for _, want := range wanted {
		for _, value := range values {
			if value == want {
				return true
			}
		}
	}
	return false
}

// anyIntOne reports whether values contains want.
func anyIntOne(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// RestaurantCandidate is a ranked search result.
//
// The rating travels with its sample size on purpose. A 4.8 that rests on two
// reviews and a 4.8 that rests on three hundred are different claims, and a
// candidate that reports only the first reads as evidence for the second.
type RestaurantCandidate struct {
	RestaurantID int64    `json:"restaurant_id"`
	Name         string   `json:"name"`
	Address      string   `json:"address,omitempty"`
	Score        float64  `json:"score"`
	Reasons      []string `json:"reasons,omitempty"`

	// SnapshotAt travels with the candidate so a recommendation can always say
	// when the underlying data was observed, without a second lookup.
	SnapshotAt time.Time `json:"snapshot_at,omitempty"`

	// Rating is the average over the reviews that are actually in the knowledge
	// base, not the source site's average, which may be truncated.
	Rating      *float64 `json:"rating,omitempty"`
	RatingCount int      `json:"rating_count,omitempty"`
	PriceLevel  *int     `json:"price_level,omitempty"`
	Cuisines    []string `json:"cuisines,omitempty"`
	Borough     string   `json:"borough,omitempty"`
}

// SearchQuery is a single restaurant search request.
type SearchQuery struct {
	// Text is free text: a restaurant name, an address fragment, or a soft
	// condition. Each channel interprets it differently, and the structured
	// channel ignores it entirely — an empty Text with a populated Filter is a
	// valid query, not a missing field.
	Text   string           `json:"text,omitempty"`
	Filter RestaurantFilter `json:"filter,omitempty"`
	TopK   int              `json:"top_k,omitempty"`
}

// RestaurantDetail is the minimal restaurant projection the retrieval layer
// needs. It is intentionally not the full curated restaurant: a search reads
// three columns a candidate does not display, and loading them for every row
// of a Top-20 page would pull the corpus's jsonb through memory for nothing.
type RestaurantDetail struct {
	RestaurantID    int64             `json:"restaurant_id"`
	Name            string            `json:"name"`
	Address         string            `json:"address,omitempty"`
	Borough         string            `json:"borough,omitempty"`
	Cuisines        []string          `json:"cuisines,omitempty"`
	PriceLevel      *int              `json:"price_level,omitempty"`
	Rating          *float64          `json:"rating,omitempty"`
	RatingCount     int               `json:"rating_count,omitempty"`
	Source          string            `json:"source,omitempty"`
	SnapshotStatus  string            `json:"snapshot_status,omitempty"`
	KnowledgeScore  float64           `json:"knowledge_score"`
	IsActiveForDemo bool              `json:"is_active_for_demo"`
	SnapshotAt      time.Time         `json:"snapshot_at,omitempty"`
	Attributes      map[string]string `json:"attributes,omitempty"`
}

// Validate reports whether the filter is one the system can execute.
//
// It rejects rather than returning no rows, because "no restaurants matched" and
// "that is not a borough" are different answers to a user and the second one is
// silently indistinguishable from the first if it is reported as empty.
func (f RestaurantFilter) Validate() error {
	if f.Borough != "" && !ValidBorough(f.Borough) {
		return errs.Newf(errs.CodeRetrievalInvalidFilter,
			"borough must be one of %s (got %q)", strings.Join(Boroughs, "|"), f.Borough)
	}
	for _, level := range f.PriceLevels {
		// The column carries the same check, but a violation there surfaces as
		// a rejected statement rather than as advice about the request.
		if level < 1 || level > 4 {
			return errs.Newf(errs.CodeRetrievalInvalidFilter,
				"price level must be between 1 and 4 (got %d)", level)
		}
	}
	if f.MinRating != nil && (*f.MinRating < 0 || *f.MinRating > 5) {
		return errs.Newf(errs.CodeRetrievalInvalidFilter,
			"min rating must be between 0 and 5 (got %g)", *f.MinRating)
	}
	if f.MaxDistanceMeters != nil && *f.MaxDistanceMeters < 0 {
		return errs.Newf(errs.CodeRetrievalInvalidFilter,
			"max distance must be positive (got %d)", *f.MaxDistanceMeters)
	}
	if f.QueryOrigin != nil {
		if f.QueryOrigin.Latitude < -90 || f.QueryOrigin.Latitude > 90 {
			return errs.Newf(errs.CodeRetrievalInvalidFilter,
				"latitude must be between -90 and 90 (got %g)", f.QueryOrigin.Latitude)
		}
		if f.QueryOrigin.Longitude < -180 || f.QueryOrigin.Longitude > 180 {
			return errs.Newf(errs.CodeRetrievalInvalidFilter,
				"longitude must be between -180 and 180 (got %g)", f.QueryOrigin.Longitude)
		}
	}
	return nil
}
