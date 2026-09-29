package curate

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
