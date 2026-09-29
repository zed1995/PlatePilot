package mongo

import (
	"context"
	"errors"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/mongo"

	"github.com/zed/platepilot/shared/domain/restaurant"
	"github.com/zed/platepilot/shared/domain/review"
)

// Collection names. These are the phase-1 content collections (M1-02) plus the
// ingestion audit collections (M1-09).
const (
	CollectionRestaurants         = "restaurants"
	CollectionRestaurantDocuments = "restaurant_documents"
	CollectionReviews             = "reviews"
	CollectionReviewSummaries     = "review_summaries"
	CollectionIngestionBatches    = "ingestion_batches"
	CollectionIngestionRejections = "ingestion_rejections"
)

// CollectionStatus reports the outcome of ensuring one collection.
type CollectionStatus struct {
	Name    string
	Created bool
}

// collectionNames returns the collections the pipeline owns, in creation order.
func collectionNames() []string {
	return []string{
		CollectionRestaurants,
		CollectionRestaurantDocuments,
		CollectionReviews,
		CollectionReviewSummaries,
		CollectionIngestionBatches,
		CollectionIngestionRejections,
	}
}

// EnsureSchema explicitly creates every collection the pipeline writes. It is
// idempotent: a collection that already exists is reported as existing.
func (c *Client) EnsureSchema(ctx context.Context) ([]CollectionStatus, error) {
	names := collectionNames()
	out := make([]CollectionStatus, 0, len(names))
	for _, name := range names {
		err := c.db().CreateCollection(ctx, name)
		switch {
		case err == nil:
			out = append(out, CollectionStatus{Name: name, Created: true})
		case isNamespaceExists(err):
			out = append(out, CollectionStatus{Name: name, Created: false})
		default:
			return out, operationError("mongo: create collection "+name, err)
		}
	}
	return out, nil
}

// --- BSON shapes (Mongo types stay in this package) -----------------------

type geoDoc struct {
	Type        string    `bson:"type"`
	Coordinates []float64 `bson:"coordinates"`
}

type priceDoc struct {
	Raw   string `bson:"raw,omitempty"`
	Level *int   `bson:"level,omitempty"`
}

type ratingDoc struct {
	SourceAvg                 *float64 `bson:"source_avg,omitempty"`
	ComputedAvg               *float64 `bson:"computed_avg,omitempty"`
	RatingCountForComputedAvg int      `bson:"rating_count_for_computed_avg"`
}

type reviewStatsDoc struct {
	SourceReviewCount         int        `bson:"source_review_count"`
	SourceReviewCountCapped   bool       `bson:"source_review_count_capped"`
	StoredReviewCount         int        `bson:"stored_review_count"`
	TextReviewCount           int        `bson:"text_review_count"`
	RepresentativeReviewCount int        `bson:"representative_review_count"`
	EmbeddedReviewCount       int        `bson:"embedded_review_count"`
	LastReviewedAt            *time.Time `bson:"last_reviewed_at,omitempty"`
	StatsUpdatedAt            time.Time  `bson:"stats_updated_at"`
}

type attributesDoc struct {
	TriStates         map[string]string `bson:"tri_states,omitempty"`
	AtmosphereTags    []string          `bson:"atmosphere_tags,omitempty"`
	PopularForTags    []string          `bson:"popular_for_tags,omitempty"`
	ServiceOptionTags []string          `bson:"service_option_tags,omitempty"`
	AccessibilityTags []string          `bson:"accessibility_tags,omitempty"`
}

type restaurantDoc struct {
	ID              string         `bson:"_id"`
	Source          string         `bson:"source"`
	SourceRecordID  string         `bson:"source_record_id"`
	Name            string         `bson:"name"`
	Address         string         `bson:"address,omitempty"`
	BoroughGuess    string         `bson:"borough_guess,omitempty"`
	Location        *geoDoc        `bson:"location,omitempty"`
	Categories      []string       `bson:"categories,omitempty"`
	CuisineTags     []string       `bson:"cuisine_tags,omitempty"`
	Description     string         `bson:"description,omitempty"`
	Price           priceDoc       `bson:"price"`
	Rating          ratingDoc      `bson:"rating"`
	ReviewStats     reviewStatsDoc `bson:"review_stats"`
	Attributes      attributesDoc  `bson:"attributes"`
	SnapshotStatus  string         `bson:"snapshot_status"`
	KnowledgeScore  float64        `bson:"knowledge_score"`
	IsActiveForDemo bool           `bson:"is_active_for_demo"`
	ObservedAt      time.Time      `bson:"observed_at"`
	SourceURL       string         `bson:"source_url,omitempty"`
	CreatedAt       time.Time      `bson:"created_at"`
	UpdatedAt       time.Time      `bson:"updated_at"`
}

type restaurantDocumentDoc struct {
	RestaurantID   string    `bson:"restaurant_id"`
	DocumentType   string    `bson:"document_type"`
	Raw            any       `bson:"raw,omitempty"`
	Normalized     any       `bson:"normalized,omitempty"`
	ObservedAt     time.Time `bson:"observed_at"`
	SourceRecordID string    `bson:"source_record_id,omitempty"`
}

type reviewDoc struct {
	ID               string    `bson:"_id"`
	RestaurantID     string    `bson:"restaurant_id"`
	Rating           int       `bson:"rating"`
	ReviewedAt       time.Time `bson:"reviewed_at"`
	Text             string    `bson:"text"`
	Language         string    `bson:"language,omitempty"`
	TextHash         string    `bson:"text_hash"`
	IsRepresentative bool      `bson:"is_representative"`
	TopicTags        []string  `bson:"topic_tags,omitempty"`
	SourceObservedAt time.Time `bson:"source_observed_at"`
}

type reviewSummaryDoc struct {
	RestaurantID  string    `bson:"restaurant_id"`
	Topic         string    `bson:"topic"`
	Sentiment     float64   `bson:"sentiment"`
	PositiveRatio float64   `bson:"positive_ratio"`
	Summary       string    `bson:"summary"`
	EvidenceCount int       `bson:"evidence_count"`
	ValidFrom     time.Time `bson:"valid_from"`
	ValidTo       time.Time `bson:"valid_to"`
	GeneratedBy   string    `bson:"generated_by"`
	GeneratedAt   time.Time `bson:"generated_at"`
}

type fieldMissingDoc struct {
	Field string `bson:"field"`
	Count int64  `bson:"count"`
}

type batchDoc struct {
	ID              string            `bson:"_id"`
	Stage           string            `bson:"stage"`
	CurationVersion string            `bson:"curation_version"`
	SourceFile      string            `bson:"source_file,omitempty"`
	SourceSHA256    string            `bson:"source_sha256,omitempty"`
	StartedAt       time.Time         `bson:"started_at"`
	FinishedAt      *time.Time        `bson:"finished_at,omitempty"`
	DurationMS      int64             `bson:"duration_ms"`
	RowsRead        int64             `bson:"rows_read"`
	Accepted        int64             `bson:"accepted"`
	Written         int64             `bson:"written"`
	Deduped         int64             `bson:"deduped"`
	Filtered        int64             `bson:"filtered"`
	Rejected        int64             `bson:"rejected"`
	Unmatched       int64             `bson:"unmatched"`
	MissingFields   []fieldMissingDoc `bson:"missing_fields,omitempty"`
	Status          string            `bson:"status"`
	ErrorCode       string            `bson:"error_code,omitempty"`
}

type rejectionDoc struct {
	ID             string `bson:"_id"`
	BatchID        string `bson:"batch_id"`
	Stage          string `bson:"stage"`
	LineNo         int64  `bson:"line_no"`
	Reason         string `bson:"reason"`
	SourceRecordID string `bson:"source_record_id,omitempty"`
}

// --- domain <-> BSON mapping ---------------------------------------------

func restaurantToDoc(r restaurant.Restaurant) restaurantDoc {
	d := restaurantDoc{
		ID:              r.ID,
		Source:          r.Source,
		SourceRecordID:  r.SourceRecordID,
		Name:            r.Name,
		Address:         r.Address,
		BoroughGuess:    r.BoroughGuess,
		Categories:      r.Categories,
		CuisineTags:     r.CuisineTags,
		Description:     r.Description,
		Price:           priceDoc{Raw: r.Price.Raw, Level: r.Price.Level},
		Rating:          ratingDoc(r.Rating),
		ReviewStats:     reviewStatsDoc(r.ReviewStats),
		SnapshotStatus:  string(r.SnapshotStatus),
		KnowledgeScore:  r.KnowledgeScore,
		IsActiveForDemo: r.IsActiveForDemo,
		ObservedAt:      r.ObservedAt,
		SourceURL:       r.SourceURL,
		CreatedAt:       r.CreatedAt,
		UpdatedAt:       r.UpdatedAt,
	}
	if r.Location != nil {
		d.Location = &geoDoc{Type: "Point", Coordinates: []float64{r.Location.Longitude, r.Location.Latitude}}
	}
	d.Attributes = attributesDoc{
		TriStates:         r.Attributes.TriStates,
		AtmosphereTags:    r.Attributes.AtmosphereTags,
		PopularForTags:    r.Attributes.PopularForTags,
		ServiceOptionTags: r.Attributes.ServiceOptionTags,
		AccessibilityTags: r.Attributes.AccessibilityTags,
	}
	return d
}

func docToRestaurant(d restaurantDoc) restaurant.Restaurant {
	r := restaurant.Restaurant{
		ID:              d.ID,
		Source:          d.Source,
		SourceRecordID:  d.SourceRecordID,
		Name:            d.Name,
		Address:         d.Address,
		BoroughGuess:    d.BoroughGuess,
		Categories:      d.Categories,
		CuisineTags:     d.CuisineTags,
		Description:     d.Description,
		Price:           restaurant.Price{Raw: d.Price.Raw, Level: d.Price.Level},
		Rating:          restaurant.Rating(d.Rating),
		ReviewStats:     restaurant.ReviewStats(d.ReviewStats),
		SnapshotStatus:  restaurant.SnapshotStatus(d.SnapshotStatus),
		KnowledgeScore:  d.KnowledgeScore,
		IsActiveForDemo: d.IsActiveForDemo,
		ObservedAt:      d.ObservedAt,
		SourceURL:       d.SourceURL,
		CreatedAt:       d.CreatedAt,
		UpdatedAt:       d.UpdatedAt,
	}
	if d.Location != nil && len(d.Location.Coordinates) == 2 {
		r.Location = &restaurant.GeoPoint{Longitude: d.Location.Coordinates[0], Latitude: d.Location.Coordinates[1]}
	}
	r.Attributes = restaurant.Attributes{
		TriStates:         d.Attributes.TriStates,
		AtmosphereTags:    d.Attributes.AtmosphereTags,
		PopularForTags:    d.Attributes.PopularForTags,
		ServiceOptionTags: d.Attributes.ServiceOptionTags,
		AccessibilityTags: d.Attributes.AccessibilityTags,
	}
	return r
}

func reviewToDoc(r review.Review) reviewDoc {
	return reviewDoc{
		ID:               r.ID,
		RestaurantID:     r.RestaurantID,
		Rating:           r.Rating,
		ReviewedAt:       r.ReviewedAt,
		Text:             r.Text,
		Language:         r.Language,
		TextHash:         r.TextHash,
		IsRepresentative: r.IsRepresentative,
		TopicTags:        r.TopicTags,
		SourceObservedAt: r.SourceObservedAt,
	}
}

func docToReview(d reviewDoc) review.Review {
	return review.Review{
		ID:               d.ID,
		RestaurantID:     d.RestaurantID,
		Rating:           d.Rating,
		ReviewedAt:       d.ReviewedAt,
		Text:             d.Text,
		Language:         d.Language,
		TextHash:         d.TextHash,
		IsRepresentative: d.IsRepresentative,
		TopicTags:        d.TopicTags,
		SourceObservedAt: d.SourceObservedAt,
	}
}

func batchToDoc(b review.BatchReport) batchDoc {
	d := batchDoc{
		ID:              b.BatchID,
		Stage:           b.Stage,
		CurationVersion: b.CurationVersion,
		SourceFile:      b.SourceFile,
		SourceSHA256:    b.SourceSHA256,
		StartedAt:       b.StartedAt,
		DurationMS:      b.DurationMS,
		RowsRead:        b.RowsRead,
		Accepted:        b.Accepted,
		Written:         b.Written,
		Deduped:         b.Deduped,
		Filtered:        b.Filtered,
		Rejected:        b.Rejected,
		Unmatched:       b.Unmatched,
		Status:          b.Status,
		ErrorCode:       b.ErrorCode,
	}
	if !b.FinishedAt.IsZero() {
		t := b.FinishedAt
		d.FinishedAt = &t
	}
	for _, f := range b.MissingFields {
		d.MissingFields = append(d.MissingFields, fieldMissingDoc{Field: f.Field, Count: f.Count})
	}
	return d
}

func docToBatch(d batchDoc) review.BatchReport {
	b := review.BatchReport{
		BatchID:         d.ID,
		Stage:           d.Stage,
		CurationVersion: d.CurationVersion,
		SourceFile:      d.SourceFile,
		SourceSHA256:    d.SourceSHA256,
		StartedAt:       d.StartedAt,
		DurationMS:      d.DurationMS,
		RowsRead:        d.RowsRead,
		Accepted:        d.Accepted,
		Written:         d.Written,
		Deduped:         d.Deduped,
		Filtered:        d.Filtered,
		Rejected:        d.Rejected,
		Unmatched:       d.Unmatched,
		Status:          d.Status,
		ErrorCode:       d.ErrorCode,
	}
	if d.FinishedAt != nil {
		b.FinishedAt = *d.FinishedAt
	}
	for _, f := range d.MissingFields {
		b.MissingFields = append(b.MissingFields, review.FieldMissing{Field: f.Field, Count: f.Count})
	}
	return b
}

func rejectionToDoc(r review.Rejection) rejectionDoc {
	return rejectionDoc{
		ID:             rejectionID(r),
		BatchID:        r.BatchID,
		Stage:          r.Stage,
		LineNo:         r.LineNo,
		Reason:         r.Reason,
		SourceRecordID: r.SourceRecordID,
	}
}

func rejectionID(r review.Rejection) string {
	return r.BatchID + ":" + r.Stage + ":" + strconv.FormatInt(r.LineNo, 10)
}

// DropCollections removes every pipeline collection. It must only be called by
// tests and destructive local resets: it is never wired into `migrate` without
// an explicit --drop flag.
func (c *Client) DropCollections(ctx context.Context) error {
	for _, name := range collectionNames() {
		err := c.db().Collection(name).Drop(ctx)
		if err != nil && !isNamespaceNotFound(err) {
			return operationError("mongo: drop collection "+name, err)
		}
	}
	return nil
}

// isNamespaceNotFound reports whether an operation failed because the
// collection does not exist (server error code 26).
func isNamespaceNotFound(err error) bool {
	var cmdErr mongo.CommandError
	if errors.As(err, &cmdErr) {
		return cmdErr.Code == 26 // NamespaceNotFound
	}
	return false
}
