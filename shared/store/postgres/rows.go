package postgres

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/restaurant"
	"github.com/zed1995/platepilot/shared/domain/review"
)

// pgLocation adapts a geography(Point,4326) column to the domain GeoPoint.
//
// PostGIS geography is carried as a WKB/EWKB byte string, so the value is
// round-tripped through text rather than reaching for a spatial driver: the
// column is always a single point written by this package, and going through
// ST_AsText/ST_GeogFromText keeps the SQL visible in one place.
type pgLocation struct {
	Valid bool
	Lat   float64
	Lon   float64
}

func newLocation(p *restaurant.GeoPoint) any {
	if p == nil {
		// NULL, not a zero point: a restaurant with no usable coordinate must
		// stay distinguishable from one at (0,0).
		return nil
	}
	// ST_GeogFromText takes lon,lat order to match GeoJSON.
	return fmt.Sprintf("SRID=4326;POINT(%g %g)", p.Longitude, p.Latitude)
}

func (l pgLocation) point() *restaurant.GeoPoint {
	if !l.Valid {
		return nil
	}
	return &restaurant.GeoPoint{Longitude: l.Lon, Latitude: l.Lat}
}

// --- restaurant row --------------------------------------------------------

// restaurantRow mirrors the restaurants table. Every column is listed so that
// adding a column is a compile error here rather than a silent data loss.
type restaurantRow struct {
	ID             int64
	Source         string
	SourceRecordID string
	Name           string
	Address        *string
	Borough        *string
	LocationText   *string
	Categories     []string
	CuisineTags    []string
	Description    *string

	PriceRaw   *string
	PriceLevel *int16

	RatingSourceAvg   *float64
	RatingComputedAvg *float64
	RatingCount       int32

	SourceReviewCount         int32
	SourceReviewCountCapped   bool
	StoredReviewCount         int32
	TextReviewCount           int32
	RepresentativeReviewCount int32
	EmbeddedReviewCount       int32
	LastReviewedAt            *time.Time
	StatsUpdatedAt            *time.Time

	Attributes      []byte
	AttributesRaw   []byte
	Hours           []byte
	RelativeResults []string

	SnapshotStatus  string
	KnowledgeScore  float64
	IsActiveForDemo bool

	ObservedAt time.Time
	SourceURL  *string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

const restaurantColumns = `
	id, source, source_record_id, name, address, borough,
	ST_AsText(location) AS location_text,
	categories, cuisine_tags, description,
	price_raw, price_level,
	rating_source_avg, rating_computed_avg, rating_count,
	source_review_count, source_review_count_capped, stored_review_count,
	text_review_count, representative_review_count, embedded_review_count,
	last_reviewed_at, stats_updated_at,
	attributes, attributes_raw, hours, relative_results,
	snapshot_status, knowledge_score, is_active_for_demo,
	observed_at, source_url, created_at, updated_at`

// toDomain converts a row into the domain type. JSON columns are decoded here
// so a malformed stored document surfaces as a domain error rather than
// panicking inside a scan.
func (r restaurantRow) toDomain() (restaurant.Restaurant, error) {
	out := restaurant.Restaurant{
		ID:              r.ID,
		Source:          r.Source,
		SourceRecordID:  r.SourceRecordID,
		Name:            r.Name,
		Categories:      r.Categories,
		CuisineTags:     r.CuisineTags,
		SnapshotStatus:  restaurant.SnapshotStatus(r.SnapshotStatus),
		KnowledgeScore:  r.KnowledgeScore,
		IsActiveForDemo: r.IsActiveForDemo,
		ObservedAt:      r.ObservedAt,
		CreatedAt:       r.CreatedAt,
		UpdatedAt:       r.UpdatedAt,
		ReviewStats: restaurant.ReviewStats{
			SourceReviewCount:         int(r.SourceReviewCount),
			SourceReviewCountCapped:   r.SourceReviewCountCapped,
			StoredReviewCount:         int(r.StoredReviewCount),
			TextReviewCount:           int(r.TextReviewCount),
			RepresentativeReviewCount: int(r.RepresentativeReviewCount),
			EmbeddedReviewCount:       int(r.EmbeddedReviewCount),
			LastReviewedAt:            r.LastReviewedAt,
		},
		RelativeResults: r.RelativeResults,
	}
	if out.Categories == nil {
		out.Categories = []string{}
	}
	if out.CuisineTags == nil {
		out.CuisineTags = []string{}
	}
	if r.Address != nil {
		out.Address = *r.Address
	}
	if r.Borough != nil {
		out.BoroughGuess = *r.Borough
	}
	if r.Description != nil {
		out.Description = *r.Description
	}
	if r.PriceRaw != nil {
		out.Price.Raw = *r.PriceRaw
	}
	if r.PriceLevel != nil {
		level := int(*r.PriceLevel)
		out.Price.Level = &level
	}
	if r.RatingSourceAvg != nil {
		out.Rating.SourceAvg = r.RatingSourceAvg
	}
	if r.RatingComputedAvg != nil {
		out.Rating.ComputedAvg = r.RatingComputedAvg
	}
	out.Rating.RatingCountForComputedAvg = int(r.RatingCount)
	if r.StatsUpdatedAt != nil {
		out.ReviewStats.StatsUpdatedAt = *r.StatsUpdatedAt
	}
	if r.SourceURL != nil {
		out.SourceURL = *r.SourceURL
	}
	if r.LocationText != nil {
		lon, lat, err := parsePoint(*r.LocationText)
		if err != nil {
			return restaurant.Restaurant{}, errs.Wrap(errs.CodeInternal,
				"postgres: decode restaurant location", err)
		}
		out.Location = &restaurant.GeoPoint{Longitude: lon, Latitude: lat}
	}
	if err := json.Unmarshal(orEmptyObject(r.Attributes), &out.Attributes); err != nil {
		return restaurant.Restaurant{}, errs.Wrap(errs.CodeInternal, "postgres: decode attributes", err)
	}
	if err := json.Unmarshal(orEmptyObject(r.AttributesRaw), &out.AttributesRaw); err != nil {
		return restaurant.Restaurant{}, errs.Wrap(errs.CodeInternal, "postgres: decode attributes_raw", err)
	}
	if err := json.Unmarshal(orEmptyArray(r.Hours), &out.Hours); err != nil {
		return restaurant.Restaurant{}, errs.Wrap(errs.CodeInternal, "postgres: decode hours", err)
	}
	return out, nil
}

// parsePoint reads the "POINT(lon lat)" text PostGIS emits.
func parsePoint(text string) (lon, lat float64, err error) {
	var x, y float64
	if _, err := fmt.Sscanf(text, "POINT(%g %g)", &x, &y); err != nil {
		return 0, 0, fmt.Errorf("parse %q: %w", text, err)
	}
	return x, y, nil
}

func orEmptyObject(raw []byte) []byte {
	if len(raw) == 0 {
		return []byte(`{}`)
	}
	return raw
}

func orEmptyArray(raw []byte) []byte {
	if len(raw) == 0 {
		return []byte(`[]`)
	}
	return raw
}

// marshalOrNilOrNil behaves like marshalOrNil but keeps a nil map as SQL NULL.
// missing_fields is always written, because "no field was missing" is a real
// answer, whereas an absent reject_reasons means the stage never ran a check.
func marshalOrNilOrNil(v map[string]int64) (any, error) {
	if len(v) == 0 {
		return nil, nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, "postgres: encode json column", err)
	}
	return data, nil
}

// marshalOrNil encodes v, returning SQL NULL when there is nothing to store.
func marshalOrNil(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, errs.Wrap(errs.CodeInternal, "postgres: encode json column", err)
	}
	// Empty maps and slices are stored as empty JSON rather than NULL so a
	// missing value and an empty value are not conflated on read.
	return data, nil
}

// --- review row ------------------------------------------------------------

type reviewRow struct {
	ID               int64
	RestaurantID     int64
	Rating           int16
	ReviewedAt       time.Time
	Text             string
	Language         *string
	TextHash         string
	IsRepresentative bool
	TopicTags        []string
	SourceObservedAt time.Time
}

const reviewColumns = `
	id, restaurant_id, rating, reviewed_at, text, language, text_hash,
	is_representative, topic_tags, source_observed_at`

func (r reviewRow) toDomain() review.Review {
	out := review.Review{
		ID:               r.ID,
		RestaurantID:     r.RestaurantID,
		Rating:           int(r.Rating),
		ReviewedAt:       r.ReviewedAt,
		Text:             r.Text,
		TextHash:         r.TextHash,
		IsRepresentative: r.IsRepresentative,
		TopicTags:        r.TopicTags,
		SourceObservedAt: r.SourceObservedAt,
	}
	if r.Language != nil {
		out.Language = *r.Language
	}
	if out.TopicTags == nil {
		out.TopicTags = []string{}
	}
	return out
}

// --- ingestion audit row ---------------------------------------------------

type batchRow struct {
	ID              int64
	Stage           string
	CurationVersion string
	SourceFile      *string
	SourceSHA256    *string
	BoundaryVersion *string
	StartedAt       time.Time
	FinishedAt      *time.Time
	DurationMS      int64
	RowsRead        int64
	Accepted        int64
	Written         int64
	Deduped         int64
	Filtered        int64
	Rejected        int64
	Unmatched       int64
	MissingFields   []byte
	Status          string
	ErrorCode       *string
	// The M2 columns are nullable. A null counter means the stage did not
	// report one, which is different from a stage that reported zero.
	DocumentsBuilt      *int64
	DocumentsEmbedded   *int64
	DocumentsRejected   *int64
	EmbeddingModel      *string
	EmbeddingDimensions *int32
	RejectReasons       []byte
}

const batchColumns = `
	id, stage, curation_version, source_file, source_sha256, boundary_version,
	started_at, finished_at, duration_ms, rows_read, accepted, written,
	deduped, filtered, rejected, unmatched, missing_fields, status, error_code,
	documents_built, documents_embedded, documents_rejected,
	embedding_model, embedding_dimensions, reject_reasons`

func (r batchRow) toDomain() (review.BatchReport, error) {
	out := review.BatchReport{
		BatchID:         r.ID,
		Stage:           r.Stage,
		CurationVersion: r.CurationVersion,
		StartedAt:       r.StartedAt,
		DurationMS:      r.DurationMS,
		RowsRead:        r.RowsRead,
		Accepted:        r.Accepted,
		Written:         r.Written,
		Deduped:         r.Deduped,
		Filtered:        r.Filtered,
		Rejected:        r.Rejected,
		Unmatched:       r.Unmatched,
		Status:          r.Status,

		DocumentsBuilt:      r.DocumentsBuilt,
		DocumentsEmbedded:   r.DocumentsEmbedded,
		DocumentsRejected:   r.DocumentsRejected,
		EmbeddingDimensions: int32PtrToInt(r.EmbeddingDimensions),
	}
	if r.EmbeddingModel != nil {
		out.EmbeddingModel = *r.EmbeddingModel
	}
	if r.SourceFile != nil {
		out.SourceFile = *r.SourceFile
	}
	if r.SourceSHA256 != nil {
		out.SourceSHA256 = *r.SourceSHA256
	}
	if r.BoundaryVersion != nil {
		out.BoundaryVersion = *r.BoundaryVersion
	}
	if r.FinishedAt != nil {
		out.FinishedAt = *r.FinishedAt
	}
	if r.ErrorCode != nil {
		out.ErrorCode = *r.ErrorCode
	}
	if err := json.Unmarshal(orEmptyArray(r.MissingFields), &out.MissingFields); err != nil {
		return review.BatchReport{}, errs.Wrap(errs.CodeInternal, "postgres: decode missing_fields", err)
	}
	// reject_reasons is NULL for a stage with no quality gate, which must not
	// become an empty map: a reader has to be able to tell "nothing was
	// checked" from "everything passed".
	if len(r.RejectReasons) > 0 {
		if err := json.Unmarshal(r.RejectReasons, &out.RejectReasons); err != nil {
			return review.BatchReport{}, errs.Wrap(errs.CodeInternal, "postgres: decode reject_reasons", err)
		}
	}
	return out, nil
}

// int32PtrToInt widens a nullable smallint into the domain's int.
func int32PtrToInt(v *int32) *int {
	if v == nil {
		return nil
	}
	out := int(*v)
	return &out
}

// pgErrNoRows reports whether err is the driver's empty-result sentinel, so
// callers can map it onto the domain's not-found error.
func pgErrNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

// nullString converts an optional string to a pointer-friendly value.
func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// pgInt16 is a small helper for the nullable smallint columns.
func pgInt16(v int) *int16 {
	if v == 0 {
		return nil
	}
	out := int16(v)
	return &out
}
