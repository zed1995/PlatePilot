package curate

import (
	"errors"
	"strings"
	"time"

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/restaurant"
	"github.com/zed/platepilot/shared/domain/review"

	"github.com/zed/platepilot/data-pipeline/internal/pipeline/raw"
)

// CurationVersion identifies the normalisation rules. It is recorded on every
// batch report so data can be traced back to the rules that produced it.
const CurationVersion = "1"

// SourceReviewCountCap is the value at which Google Local's num_of_reviews is
// treated as potentially capped.
const SourceReviewCountCap = 9998

// ErrFiltered marks a record that is valid but out of scope (for example a
// place that is not a restaurant). It is counted separately from a rejection.
var ErrFiltered = errors.New("record filtered out of the knowledge base")

// MetaOptions controls Meta normalisation.
type MetaOptions struct {
	// ObservedAt is the data snapshot time stamped onto the curated place.
	ObservedAt time.Time
	// ServiceArea limits ingestion to a bounding box. The zero value disables
	// the geographic filter.
	ServiceArea ServiceArea
	// Boroughs labels each place with its administrative borough. When nil the
	// label falls back to bounding boxes, which are known to be approximate.
	Boroughs *Boundaries
}

// ReviewOptions controls review normalisation.
type ReviewOptions struct {
	// MinTextChars is the shortest scrubbed text considered usable evidence.
	// Shorter text is dropped (blanked) but the review is still kept so the
	// rating sample stays complete.
	MinTextChars int
	// ObservedAt is the data snapshot time stamped onto the curated review.
	ObservedAt time.Time
}

// NormalizeMeta converts a raw Meta record into a curated restaurant.
//
// It returns ErrFiltered for places outside the food scope and an *errs.Error
// for records that are structurally invalid.
//
// The returned restaurant has a zero ID: restaurants.id is an identity column
// assigned by the database on insert, so the pipeline must not pre-generate one.
// The natural key is SourceRecordID (the source gmap_id).
func NormalizeMeta(m raw.Meta, opts MetaOptions) (restaurant.Restaurant, error) {
	observedAt := opts.ObservedAt
	// Hoisted out of the row path: the resolver is read-only, so building it
	// once here instead of per record keeps a 272k-row import from allocating.
	boroughs := newBoroughResolver(opts.Boroughs)
	if strings.TrimSpace(m.GmapID) == "" {
		return restaurant.Restaurant{}, errs.New(errs.CodeInvalidArgument, "meta: gmap_id is required")
	}
	if !IsFoodPlace(m.Category) {
		return restaurant.Restaurant{}, ErrFiltered
	}
	if !ValidCoordinates(m.Latitude, m.Longitude) {
		return restaurant.Restaurant{}, errs.Newf(errs.CodeInvalidArgument, "meta %s: invalid coordinates", m.GmapID)
	}
	if area := opts.ServiceArea; !area.IsZero() && !area.Contains(*m.Latitude, *m.Longitude) {
		return restaurant.Restaurant{}, ErrFiltered
	}
	name := strings.TrimSpace(m.Name)
	if name == "" {
		return restaurant.Restaurant{}, errs.Newf(errs.CodeInvalidArgument, "meta %s: name is required", m.GmapID)
	}

	r := restaurant.Restaurant{
		Source:         restaurant.SourceGoogleLocal2021,
		SourceRecordID: m.GmapID,
		Name:           name,
		Address:        derefString(m.Address),
		Categories:     m.Category,
		CuisineTags:    CuisineTags(m.Category),
		Description:    derefString(m.Description),
		Price:          restaurant.Price{Raw: derefString(m.Price), Level: PriceLevel(m.Price)},
		Rating:         restaurant.Rating{SourceAvg: m.AvgRating},
		Attributes:     NormalizeAttributes(m.MISC),
		SnapshotStatus: SnapshotStatus(m.State),
		ObservedAt:     observedAt,
		SourceURL:      m.URL,
		ReviewStats: restaurant.ReviewStats{
			StatsUpdatedAt: observedAt,
		},
	}
	if m.Latitude != nil && m.Longitude != nil {
		r.Location = &restaurant.GeoPoint{Longitude: *m.Longitude, Latitude: *m.Latitude}
		r.BoroughGuess = boroughs.borough(*m.Latitude, *m.Longitude)
	}
	if m.NumOfReviews != nil {
		r.ReviewStats.SourceReviewCount = *m.NumOfReviews
		r.ReviewStats.SourceReviewCountCapped = *m.NumOfReviews >= SourceReviewCountCap
	}

	// The auxiliary payloads live on the same row: store what is read together,
	// and none of these is ever fetched separately from its restaurant.
	r.AttributesRaw = map[string][]string(m.MISC)
	r.Hours = ParseHours(m.Hours)
	r.RelativeResults = m.RelativeResults
	return r, nil
}

// NormalizeReview converts a raw review into a curated review for a restaurant.
//
// The returned review has a zero ID: reviews.id is an identity column assigned
// by the database on insert. Use ReviewDedupKey to collapse duplicates within a
// run before the write.
func NormalizeReview(r raw.Review, restaurantID int64, opts ReviewOptions) (review.Review, error) {
	if strings.TrimSpace(r.GmapID) == "" {
		return review.Review{}, errs.New(errs.CodeInvalidArgument, "review: gmap_id is required")
	}
	if restaurantID <= 0 {
		return review.Review{}, errs.New(errs.CodeInvalidArgument, "review: restaurant_id is required")
	}
	if r.Rating < 1 || r.Rating > 5 {
		return review.Review{}, errs.Newf(errs.CodeInvalidArgument, "review %s: rating %d out of range", r.GmapID, r.Rating)
	}
	if r.Time <= 0 {
		return review.Review{}, errs.Newf(errs.CodeInvalidArgument, "review %s: time is required", r.GmapID)
	}
	at := time.UnixMilli(r.Time).UTC()
	if at.Year() < 2000 || at.Year() > 2022 {
		return review.Review{}, errs.Newf(errs.CodeInvalidArgument, "review %s: reviewed_at %s out of range", r.GmapID, at.Format(time.RFC3339))
	}

	text := ""
	if r.Text != nil {
		text = ScrubPII(strings.TrimSpace(*r.Text))
	}
	minChars := opts.MinTextChars
	if minChars <= 0 {
		minChars = 1
	}
	if len([]rune(text)) < minChars {
		// Drop unusable text but keep the rating sample; text_review_count then
		// reflects usable evidence rather than every stored row.
		text = ""
	}
	textHash := TextHash(text)

	return review.Review{
		RestaurantID:     restaurantID,
		Rating:           r.Rating,
		ReviewedAt:       at,
		Text:             text,
		TextHash:         textHash,
		SourceObservedAt: opts.ObservedAt,
	}, nil
}

// SnapshotStatus maps the 2021 source state to a snapshot enum.
func SnapshotStatus(state *string) restaurant.SnapshotStatus {
	if state == nil {
		return restaurant.StatusUnknown
	}
	switch strings.ToLower(strings.TrimSpace(*state)) {
	case "open":
		return restaurant.StatusOpen
	case "closed":
		return restaurant.StatusClosed
	case "permanently closed":
		return restaurant.StatusPermanentlyClosed
	default:
		return restaurant.StatusUnknown
	}
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

// MissingMetaFields lists the optional fields absent from a raw Meta record so
// a batch report can quantify data gaps without storing the record.
func MissingMetaFields(m raw.Meta) []string {
	var missing []string
	if m.Address == nil || strings.TrimSpace(*m.Address) == "" {
		missing = append(missing, "address")
	}
	if m.Description == nil || strings.TrimSpace(*m.Description) == "" {
		missing = append(missing, "description")
	}
	if m.Price == nil || strings.TrimSpace(*m.Price) == "" {
		missing = append(missing, "price")
	}
	if len(m.Hours) == 0 {
		missing = append(missing, "hours")
	}
	if len(m.MISC) == 0 {
		missing = append(missing, "misc")
	}
	if m.AvgRating == nil {
		missing = append(missing, "avg_rating")
	}
	if m.NumOfReviews == nil {
		missing = append(missing, "num_of_reviews")
	}
	return missing
}
