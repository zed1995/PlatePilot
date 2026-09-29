package mongo

import (
	"context"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/restaurant"
)

// RestaurantStore is the Mongo implementation of port.RestaurantStore.
type RestaurantStore struct {
	client *Client
}

// NewRestaurantStore returns a Mongo-backed restaurant store.
func NewRestaurantStore(client *Client) *RestaurantStore {
	return &RestaurantStore{client: client}
}

func (s *RestaurantStore) coll() *mongo.Collection {
	return s.client.collection(CollectionRestaurants)
}

// UpsertRestaurant inserts or replaces one restaurant keyed by source_record_id.
func (s *RestaurantStore) UpsertRestaurant(ctx context.Context, r restaurant.Restaurant) error {
	model, err := restaurantUpsertModel(r)
	if err != nil {
		return err
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	if _, err := s.coll().UpdateOne(ctx, model.(*mongo.UpdateOneModel).Filter, model.(*mongo.UpdateOneModel).Update.(bson.M), options.Update().SetUpsert(true)); err != nil {
		return operationError("mongo: upsert restaurant", err)
	}
	return nil
}

// UpsertRestaurants upserts a batch and returns the number of documents written.
func (s *RestaurantStore) UpsertRestaurants(ctx context.Context, rs []restaurant.Restaurant) (int, error) {
	if len(rs) == 0 {
		return 0, nil
	}
	models := make([]mongo.WriteModel, 0, len(rs))
	for _, r := range rs {
		model, err := restaurantUpsertModel(r)
		if err != nil {
			return 0, err
		}
		models = append(models, model)
	}
	ctx, cancel := s.client.withWriteTimeout(ctx)
	defer cancel()
	res, err := s.coll().BulkWrite(ctx, models, options.BulkWrite().SetOrdered(false))
	if err != nil {
		return 0, operationError("mongo: bulk upsert restaurants", err)
	}
	// A matched-and-modified document appears in both MatchedCount and
	// ModifiedCount; documents touched is upserted + matched, counted once.
	return int(res.UpsertedCount + res.MatchedCount), nil
}

// GetBySourceRecordID returns the curated restaurant for a gmap_id.
func (s *RestaurantStore) GetBySourceRecordID(ctx context.Context, sourceRecordID string) (restaurant.Restaurant, error) {
	if strings.TrimSpace(sourceRecordID) == "" {
		return restaurant.Restaurant{}, errs.New(errs.CodeInvalidArgument, "source_record_id is required")
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	var doc restaurantDoc
	if err := s.coll().FindOne(ctx, bson.M{"source_record_id": sourceRecordID}).Decode(&doc); err != nil {
		return restaurant.Restaurant{}, operationError("mongo: get restaurant", err)
	}
	return docToRestaurant(doc), nil
}

// GetByID returns the curated restaurant by its internal id.
func (s *RestaurantStore) GetByID(ctx context.Context, restaurantID string) (restaurant.Restaurant, error) {
	if strings.TrimSpace(restaurantID) == "" {
		return restaurant.Restaurant{}, errs.New(errs.CodeInvalidArgument, "restaurant_id is required")
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	var doc restaurantDoc
	if err := s.coll().FindOne(ctx, bson.M{"_id": restaurantID}).Decode(&doc); err != nil {
		return restaurant.Restaurant{}, operationError("mongo: get restaurant by id", err)
	}
	return docToRestaurant(doc), nil
}

// ListRestaurants returns restaurants ordered by source_record_id.
func (s *RestaurantStore) ListRestaurants(ctx context.Context, limit int) ([]restaurant.Restaurant, error) {
	findOpts := options.Find().SetSort(bson.D{{Key: "source_record_id", Value: 1}})
	if limit > 0 {
		findOpts.SetLimit(int64(limit))
	}
	ctx, cancel := s.client.withWriteTimeout(ctx)
	defer cancel()
	cursor, err := s.coll().Find(ctx, bson.M{}, findOpts)
	if err != nil {
		return nil, operationError("mongo: list restaurants", err)
	}
	defer cursor.Close(context.Background())
	out := make([]restaurant.Restaurant, 0)
	for cursor.Next(ctx) {
		var doc restaurantDoc
		if err := cursor.Decode(&doc); err != nil {
			return nil, operationError("mongo: decode restaurant", err)
		}
		out = append(out, docToRestaurant(doc))
	}
	if err := cursor.Err(); err != nil {
		return nil, operationError("mongo: iterate restaurants", err)
	}
	return out, nil
}

// MapSourceRecordIDs resolves gmap_ids to restaurant ids in bulk.
func (s *RestaurantStore) MapSourceRecordIDs(ctx context.Context, sourceRecordIDs []string) (map[string]string, error) {
	out := make(map[string]string, len(sourceRecordIDs))
	if len(sourceRecordIDs) == 0 {
		return out, nil
	}
	ctx, cancel := s.client.withWriteTimeout(ctx)
	defer cancel()
	cursor, err := s.coll().Find(ctx,
		bson.M{"source_record_id": bson.M{"$in": sourceRecordIDs}},
		options.Find().SetProjection(bson.M{"source_record_id": 1}),
	)
	if err != nil {
		return nil, operationError("mongo: map source record ids", err)
	}
	defer cursor.Close(context.Background())
	for cursor.Next(ctx) {
		var doc struct {
			ID             string `bson:"_id"`
			SourceRecordID string `bson:"source_record_id"`
		}
		if err := cursor.Decode(&doc); err != nil {
			return nil, operationError("mongo: decode source record id", err)
		}
		out[doc.SourceRecordID] = doc.ID
	}
	if err := cursor.Err(); err != nil {
		return nil, operationError("mongo: iterate source record ids", err)
	}
	return out, nil
}

// UpdateReviewStats writes the materialised review_stats for one restaurant.
func (s *RestaurantStore) UpdateReviewStats(ctx context.Context, restaurantID string, stats restaurant.ReviewStats, computed restaurant.Rating) error {
	if strings.TrimSpace(restaurantID) == "" {
		return errs.New(errs.CodeInvalidArgument, "restaurant_id is required")
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	res, err := s.coll().UpdateOne(ctx,
		bson.M{"_id": restaurantID},
		bson.M{"$set": bson.M{
			"review_stats":                         reviewStatsDoc(stats),
			"rating.computed_avg":                  computed.ComputedAvg,
			"rating.rating_count_for_computed_avg": computed.RatingCountForComputedAvg,
			"updated_at":                           time.Now().UTC(),
		}},
	)
	if err != nil {
		return operationError("mongo: update review stats", err)
	}
	if res.MatchedCount == 0 {
		return errs.Newf(errs.CodeNotFound, "restaurant %q not found", restaurantID)
	}
	return nil
}

// UpdateScores writes knowledge_score and is_active_for_demo for a batch.
func (s *RestaurantStore) UpdateScores(ctx context.Context, scores map[string]float64, active map[string]bool) error {
	if len(scores) == 0 && len(active) == 0 {
		return nil
	}
	fields := make(map[string]bson.M, len(scores)+len(active))
	for id, score := range scores {
		f := fields[id]
		if f == nil {
			f = bson.M{}
			fields[id] = f
		}
		f["knowledge_score"] = score
	}
	for id, isActive := range active {
		f := fields[id]
		if f == nil {
			f = bson.M{}
			fields[id] = f
		}
		f["is_active_for_demo"] = isActive
	}

	now := time.Now().UTC()
	models := make([]mongo.WriteModel, 0, len(fields))
	for id, f := range fields {
		f["updated_at"] = now
		models = append(models, mongo.NewUpdateOneModel().
			SetFilter(bson.M{"_id": id}).
			SetUpdate(bson.M{"$set": f}))
	}
	ctx, cancel := s.client.withWriteTimeout(ctx)
	defer cancel()
	if _, err := s.coll().BulkWrite(ctx, models, options.BulkWrite().SetOrdered(false)); err != nil {
		return operationError("mongo: update scores", err)
	}
	return nil
}

// SelectForDemo returns active-for-demo restaurants ordered by score.
func (s *RestaurantStore) SelectForDemo(ctx context.Context, limit int) ([]restaurant.Restaurant, error) {
	findOpts := options.Find().SetSort(bson.D{
		{Key: "knowledge_score", Value: -1},
		{Key: "source_record_id", Value: 1},
	})
	if limit > 0 {
		findOpts.SetLimit(int64(limit))
	}
	ctx, cancel := s.client.withWriteTimeout(ctx)
	defer cancel()
	cursor, err := s.coll().Find(ctx, bson.M{"is_active_for_demo": true}, findOpts)
	if err != nil {
		return nil, operationError("mongo: select for demo", err)
	}
	defer cursor.Close(context.Background())

	out := make([]restaurant.Restaurant, 0)
	for cursor.Next(ctx) {
		var doc restaurantDoc
		if err := cursor.Decode(&doc); err != nil {
			return nil, operationError("mongo: decode restaurant", err)
		}
		out = append(out, docToRestaurant(doc))
	}
	if err := cursor.Err(); err != nil {
		return nil, operationError("mongo: iterate restaurants", err)
	}
	return out, nil
}

// CountActiveForDemo reports how many restaurants are active for the demo.
func (s *RestaurantStore) CountActiveForDemo(ctx context.Context) (int64, error) {
	ctx, cancel := s.client.withWriteTimeout(ctx)
	defer cancel()
	n, err := s.coll().CountDocuments(ctx, bson.M{"is_active_for_demo": true})
	if err != nil {
		return 0, operationError("mongo: count active for demo", err)
	}
	return n, nil
}

// restaurantUpsertModel builds the upsert that keeps _id and created_at stable
// across re-imports, and leaves the stats/score fields to their owning jobs.
func restaurantUpsertModel(r restaurant.Restaurant) (mongo.WriteModel, error) {
	if err := validateRestaurantDomain(r); err != nil {
		return nil, err
	}
	doc := restaurantToDoc(r)
	now := time.Now().UTC()
	doc.UpdatedAt = now
	if doc.CreatedAt.IsZero() {
		doc.CreatedAt = now
	}

	set := bson.M{
		"source":           doc.Source,
		"source_record_id": doc.SourceRecordID,
		"name":             doc.Name,
		"address":          doc.Address,
		"borough_guess":    doc.BoroughGuess,
		"categories":       doc.Categories,
		"cuisine_tags":     doc.CuisineTags,
		"description":      doc.Description,
		"price":            doc.Price,
		// Only the source average is owned by the meta import. Writing the
		// whole rating subdocument would clobber computed_avg, which the stats
		// job writes.
		"rating.source_avg": doc.Rating.SourceAvg,
		"attributes":        doc.Attributes,
		"attributes_raw":    doc.AttributesRaw,
		"hours":             doc.Hours,
		"relative_results":  doc.RelativeResults,
		"snapshot_status":   doc.SnapshotStatus,
		"observed_at":       doc.ObservedAt,
		"source_url":        doc.SourceURL,
		"updated_at":        doc.UpdatedAt,
		// The source review count is owned by the meta import, so it is carried
		// on updates too; the sampled counts stay with the stats job.
		"review_stats.source_review_count":        doc.ReviewStats.SourceReviewCount,
		"review_stats.source_review_count_capped": doc.ReviewStats.SourceReviewCountCapped,
	}
	unset := bson.M{}
	if doc.Location != nil {
		set["location"] = doc.Location
	} else {
		unset["location"] = ""
	}

	update := bson.M{
		"$set": set,
		"$setOnInsert": bson.M{
			"_id":        doc.ID,
			"created_at": doc.CreatedAt,
			// Only the sampled counts are initialised here; they belong to the
			// stats job and must never be reset by a meta re-import.
			"review_stats.stored_review_count":         0,
			"review_stats.text_review_count":           0,
			"review_stats.representative_review_count": 0,
			"review_stats.embedded_review_count":       0,
			"review_stats.stats_updated_at":            doc.ReviewStats.StatsUpdatedAt,
			"knowledge_score":                          0.0,
			"is_active_for_demo":                       false,
		},
	}
	if len(unset) > 0 {
		update["$unset"] = unset
	}

	return mongo.NewUpdateOneModel().
		SetFilter(bson.M{"source_record_id": doc.SourceRecordID}).
		SetUpdate(update).
		SetUpsert(true), nil
}

func validateRestaurantDomain(r restaurant.Restaurant) error {
	if strings.TrimSpace(r.ID) == "" {
		return errs.New(errs.CodeInvalidArgument, "restaurant_id is required")
	}
	if strings.TrimSpace(r.SourceRecordID) == "" {
		return errs.New(errs.CodeInvalidArgument, "source_record_id is required")
	}
	return nil
}
