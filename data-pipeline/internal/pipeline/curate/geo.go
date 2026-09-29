package curate

import (
	"fmt"
	"strconv"
	"strings"
)

// Approximate borough boxes for NYC. The dataset has no administrative boundary,
// so borough_guess is a coarse containment test and must be documented as such.
type boroughBox struct {
	name           string
	minLat, maxLat float64
	minLon, maxLon float64
}

var nycBoroughs = []boroughBox{
	{"staten_island", 40.49, 40.65, -74.26, -74.05},
	{"bronx", 40.78, 40.92, -73.94, -73.74},
	{"manhattan", 40.68, 40.88, -74.02, -73.90},
	{"brooklyn", 40.55, 40.74, -74.05, -73.83},
	{"queens", 40.54, 40.81, -73.96, -73.70},
}

// ValidCoordinates reports whether the pair is numerically valid. A nil pointer
// means the source had no coordinate and is invalid for our purposes.
func ValidCoordinates(lat, lon *float64) bool {
	if lat == nil || lon == nil {
		return false
	}
	if *lat < -90 || *lat > 90 || *lon < -180 || *lon > 180 {
		return false
	}
	// Reject the (0,0) null island placeholder.
	return !(*lat == 0 && *lon == 0)
}

// BoroughGuess returns the first matching NYC borough box, or "" if none match.
func BoroughGuess(lat, lon float64) string {
	for _, box := range nycBoroughs {
		if lat >= box.minLat && lat <= box.maxLat && lon >= box.minLon && lon <= box.maxLon {
			return box.name
		}
	}
	return ""
}

// ServiceArea is the geographic scope for ingesting places. The Google Local
// file named "New_York" is actually US-wide, so without a scope filter the
// knowledge base silently fills with places from every state.
type ServiceArea struct {
	MinLat float64
	MinLon float64
	MaxLat float64
	MaxLon float64
}

// NYCServiceArea approximates the five boroughs. It is a bounding box, not an
// administrative boundary, so it includes a thin margin of New Jersey and
// Long Island.
var NYCServiceArea = ServiceArea{MinLat: 40.49, MinLon: -74.26, MaxLat: 40.93, MaxLon: -73.68}

// Contains reports whether a coordinate falls inside the box.
func (a ServiceArea) Contains(lat, lon float64) bool {
	return lat >= a.MinLat && lat <= a.MaxLat && lon >= a.MinLon && lon <= a.MaxLon
}

// IsZero reports whether the area is unset (meaning "no geographic filter").
func (a ServiceArea) IsZero() bool {
	return a == ServiceArea{}
}

// Validate reports why an area is unusable, or nil when it is well formed.
func (a ServiceArea) Validate() error {
	if a.IsZero() {
		return nil
	}
	if a.MinLat < -90 || a.MaxLat > 90 || a.MinLon < -180 || a.MaxLon > 180 {
		return fmt.Errorf("service area out of range: %+v", a)
	}
	if a.MinLat >= a.MaxLat || a.MinLon >= a.MaxLon {
		return fmt.Errorf("service area must have min<max for lat and lon: %+v", a)
	}
	return nil
}

// ParseServiceArea parses "south,west,north,east" (matching map conventions).
// An empty string yields NYCServiceArea.
func ParseServiceArea(value string) (ServiceArea, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return NYCServiceArea, nil
	}
	parts := strings.Split(trimmed, ",")
	if len(parts) != 4 {
		return ServiceArea{}, fmt.Errorf("service area must be south,west,north,east (got %q)", value)
	}
	numbers := make([]float64, 4)
	for i, part := range parts {
		number, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			return ServiceArea{}, fmt.Errorf("service area value %q: %w", part, err)
		}
		numbers[i] = number
	}
	area := ServiceArea{MinLat: numbers[0], MinLon: numbers[1], MaxLat: numbers[2], MaxLon: numbers[3]}
	if err := area.Validate(); err != nil {
		return ServiceArea{}, err
	}
	return area, nil
}
