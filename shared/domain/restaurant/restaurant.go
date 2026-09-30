// Package restaurant defines the curated restaurant master data produced by the
// data pipeline and read by the retrieval/agent layers.
//
// The package is part of the pure domain layer: it depends only on the standard
// library, so driver or vendor types can never leak into it.
package restaurant

import "time"

// SnapshotStatus is the 2021 data snapshot state of a place. It is a snapshot
// enum and must never be presented as a live, current status.
type SnapshotStatus string

const (
	StatusOpen              SnapshotStatus = "open"
	StatusClosed            SnapshotStatus = "closed"
	StatusPermanentlyClosed SnapshotStatus = "permanently_closed"
	StatusUnknown           SnapshotStatus = "unknown"
)

// SourceGoogleLocal2021 is the only content source in phase 1.
const SourceGoogleLocal2021 = "google_local_2021"

// Restaurant is the curated master document: one document per place.
type Restaurant struct {
	ID             int64       `json:"restaurant_id"`
	Source         string      `json:"source"`
	SourceRecordID string      `json:"source_record_id"`
	Name           string      `json:"name"`
	Address        string      `json:"address,omitempty"`
	BoroughGuess   string      `json:"borough_guess,omitempty"`
	Location       *GeoPoint   `json:"location,omitempty"`
	Categories     []string    `json:"categories,omitempty"`
	CuisineTags    []string    `json:"cuisine_tags,omitempty"`
	Description    string      `json:"description,omitempty"`
	Price          Price       `json:"price"`
	Rating         Rating      `json:"rating"`
	ReviewStats    ReviewStats `json:"review_stats"`
	Attributes     Attributes  `json:"attributes"`
	// AttributesRaw keeps the source MISC object so the curation rules can be
	// re-applied without re-reading the 61MB meta file. It lives on the same
	// document because it is only ever read together with the restaurant.
	AttributesRaw map[string][]string `json:"attributes_raw,omitempty" bson:"attributes_raw,omitempty"`
	// Hours is the normalised opening-hours matrix; each entry keeps its source
	// text so the UI can show "11AM-10PM" verbatim.
	Hours []HoursEntry `json:"hours,omitempty" bson:"hours,omitempty"`
	// RelativeResults lists similar source POIs.
	RelativeResults []string       `json:"relative_results,omitempty" bson:"relative_results,omitempty"`
	SnapshotStatus  SnapshotStatus `json:"snapshot_status"`
	KnowledgeScore  float64        `json:"knowledge_score"`
	IsActiveForDemo bool           `json:"is_active_for_demo"`
	ObservedAt      time.Time      `json:"observed_at"`
	SourceURL       string         `json:"source_url,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

// GeoPoint is a GeoJSON position. Coordinates are stored as [longitude,
// latitude] per the GeoJSON spec, which is the opposite of the raw dataset
// field order.
type GeoPoint struct {
	Longitude float64 `json:"longitude"`
	Latitude  float64 `json:"latitude"`
}

// Price keeps both the raw source string and the derived 1-4 level. Level is
// nil when the raw value is missing or unrecognised, so "unknown" is never
// confused with level 0.
type Price struct {
	Raw   string `json:"raw,omitempty"`
	Level *int   `json:"level,omitempty"`
}

// Rating separates the source average from the average computed over the
// sampled reviews that actually made it into the knowledge base.
type Rating struct {
	SourceAvg                 *float64 `json:"source_avg,omitempty"`
	ComputedAvg               *float64 `json:"computed_avg,omitempty"`
	RatingCountForComputedAvg int      `json:"rating_count_for_computed_avg"`
}

// ReviewStats materialises the review-count and rating rollups. The counts have
// distinct meanings (see the PRD) and must never be overwritten by one another.
type ReviewStats struct {
	SourceReviewCount         int        `json:"source_review_count"`
	SourceReviewCountCapped   bool       `json:"source_review_count_capped"`
	StoredReviewCount         int        `json:"stored_review_count"`
	TextReviewCount           int        `json:"text_review_count"`
	RepresentativeReviewCount int        `json:"representative_review_count"`
	EmbeddedReviewCount       int        `json:"embedded_review_count"`
	LastReviewedAt            *time.Time `json:"last_reviewed_at,omitempty"`
	StatsUpdatedAt            time.Time  `json:"stats_updated_at"`
}

// Attributes holds three-state flags plus derived tag lists. TriStates uses the
// string values "true", "false", and "unknown" so that a missing attribute is
// never coerced into false.
type Attributes struct {
	TriStates         map[string]string `json:"tri_states,omitempty"`
	AtmosphereTags    []string          `json:"atmosphere_tags,omitempty"`
	PopularForTags    []string          `json:"popular_for_tags,omitempty"`
	ServiceOptionTags []string          `json:"service_option_tags,omitempty"`
	AccessibilityTags []string          `json:"accessibility_tags,omitempty"`
}

// True, False, and Unknown are the canonical three-state attribute values.
const (
	TriStateTrue    = "true"
	TriStateFalse   = "false"
	TriStateUnknown = "unknown"
)

// HoursEntry is the normalised form of one opening-hours interval. OpenMinute
// and CloseMinute are minutes after midnight; CloseMinute may exceed 1440 for
// intervals that run past midnight.
type HoursEntry struct {
	Weekday     int    `json:"weekday" bson:"weekday"` // 0=Sunday .. 6=Saturday
	OpenMinute  int    `json:"open_minute" bson:"open_minute"`
	CloseMinute int    `json:"close_minute" bson:"close_minute"`
	IsClosed    bool   `json:"is_closed" bson:"is_closed"`
	Raw         string `json:"raw,omitempty" bson:"raw,omitempty"`
}
