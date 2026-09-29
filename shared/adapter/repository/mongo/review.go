package mongo

import (
	"context"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/review"
)

// ReviewStore is the Mongo implementation of port.ReviewStore.
type ReviewStore struct {
	client *Client
}

// NewReviewStore returns a Mongo-backed review store.
func NewReviewStore(client *Client) *ReviewStore {
	return &ReviewStore{client: client}
}

func (s *ReviewStore) coll() *mongo.Collection {
	return s.client.collection(CollectionReviews)
}

// UpsertReviews upserts a batch keyed by the deterministic review ID.
func (s *ReviewStore) UpsertReviews(ctx context.Context, items []review.Review) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}
	models := make([]mongo.WriteModel, 0, len(items))
	for _, r := range items {
		model, err := reviewUpsertModel(r)
		if err != nil {
			return 0, err
		}
		models = append(models, model)
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	res, err := s.coll().BulkWrite(ctx, models, options.BulkWrite().SetOrdered(false))
	if err != nil {
		return 0, operationError("mongo: bulk upsert reviews", err)
	}
	return int(res.UpsertedCount + res.ModifiedCount + res.MatchedCount), nil
}

// ListByRestaurant returns a restaurant's reviews, newest first.
func (s *ReviewStore) ListByRestaurant(ctx context.Context, restaurantID string, limit int) ([]review.Review, error) {
	if strings.TrimSpace(restaurantID) == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "restaurant_id is required")
	}
	findOpts := options.Find().SetSort(bson.D{{Key: "reviewed_at", Value: -1}, {Key: "_id", Value: 1}})
	if limit > 0 {
		findOpts.SetLimit(int64(limit))
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	cursor, err := s.coll().Find(ctx, bson.M{"restaurant_id": restaurantID}, findOpts)
	if err != nil {
		return nil, operationError("mongo: list reviews", err)
	}
	defer cursor.Close(context.Background())

	out := make([]review.Review, 0)
	for cursor.Next(ctx) {
		var doc reviewDoc
		if err := cursor.Decode(&doc); err != nil {
			return nil, operationError("mongo: decode review", err)
		}
		out = append(out, docToReview(doc))
	}
	if err := cursor.Err(); err != nil {
		return nil, operationError("mongo: iterate reviews", err)
	}
	return out, nil
}

// CountByRestaurant returns the materialisable rollup for one restaurant.
func (s *ReviewStore) CountByRestaurant(ctx context.Context, restaurantID string) (review.Counts, error) {
	if strings.TrimSpace(restaurantID) == "" {
		return review.Counts{}, errs.New(errs.CodeInvalidArgument, "restaurant_id is required")
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	results, err := s.aggregateCounts(ctx, bson.M{"restaurant_id": restaurantID})
	if err != nil {
		return review.Counts{}, err
	}
	return results[restaurantID], nil
}

// AggregateStats returns rollups for the given restaurant IDs.
func (s *ReviewStore) AggregateStats(ctx context.Context, restaurantIDs []string) (map[string]review.Counts, error) {
	out := make(map[string]review.Counts, len(restaurantIDs))
	for _, id := range restaurantIDs {
		out[id] = emptyCounts()
	}
	if len(restaurantIDs) == 0 {
		return out, nil
	}
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	results, err := s.aggregateCounts(ctx, bson.M{"restaurant_id": bson.M{"$in": restaurantIDs}})
	if err != nil {
		return nil, err
	}
	for id, counts := range results {
		out[id] = counts
	}
	return out, nil
}

// RestaurantIDsWithReviews returns the distinct restaurant IDs that have reviews.
func (s *ReviewStore) RestaurantIDsWithReviews(ctx context.Context) ([]string, error) {
	ctx, cancel := s.client.withTimeout(ctx)
	defer cancel()
	values, err := s.coll().Distinct(ctx, "restaurant_id", bson.M{})
	if err != nil {
		return nil, operationError("mongo: distinct restaurant ids", err)
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		if id, ok := v.(string); ok && id != "" {
			out = append(out, id)
		}
	}
	return out, nil
}

// reviewUpsertModel builds the idempotent upsert for one curated review. The
// deterministic review id is the _id, so re-importing the same review updates
// rather than duplicates it.
func reviewUpsertModel(r review.Review) (mongo.WriteModel, error) {
	if strings.TrimSpace(r.ID) == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "review_id is required")
	}
	if strings.TrimSpace(r.RestaurantID) == "" {
		return nil, errs.New(errs.CodeInvalidArgument, "restaurant_id is required")
	}
	return mongo.NewUpdateOneModel().
		SetFilter(bson.M{"_id": r.ID}).
		SetUpdate(bson.M{"$set": reviewToDoc(r)}).
		SetUpsert(true), nil
}

type countsAggResult struct {
	RestaurantID   string     `bson:"_id"`
	Stored         int64      `bson:"stored"`
	Text           int64      `bson:"text"`
	Representative int64      `bson:"representative"`
	Avg            *float64   `bson:"avg"`
	Last           *time.Time `bson:"last"`
	Ratings        []int      `bson:"ratings"`
}

func (s *ReviewStore) aggregateCounts(ctx context.Context, match bson.M) (map[string]review.Counts, error) {
	cursor, err := s.coll().Aggregate(ctx, countsPipeline(match))
	if err != nil {
		return nil, operationError("mongo: aggregate review stats", err)
	}
	defer cursor.Close(context.Background())

	out := make(map[string]review.Counts)
	for cursor.Next(ctx) {
		var row countsAggResult
		if err := cursor.Decode(&row); err != nil {
			return nil, operationError("mongo: decode review stats", err)
		}
		counts := review.Counts{
			StoredCount:         row.Stored,
			TextCount:           row.Text,
			RepresentativeCount: row.Representative,
			ComputedAvg:         row.Avg,
			LastReviewedAt:      row.Last,
			RatingDistribution:  make(map[int]int64),
		}
		for _, rating := range row.Ratings {
			counts.RatingDistribution[rating]++
		}
		out[row.RestaurantID] = counts
	}
	if err := cursor.Err(); err != nil {
		return nil, operationError("mongo: iterate review stats", err)
	}
	return out, nil
}

// countsPipeline builds the review rollup aggregation. It is a pure function so
// the grouping semantics can be asserted without a server.
func countsPipeline(match bson.M) bson.A {
	return bson.A{
		bson.M{"$match": match},
		bson.M{"$group": bson.M{
			"_id":            "$restaurant_id",
			"stored":         bson.M{"$sum": 1},
			"text":           bson.M{"$sum": bson.M{"$cond": bson.A{bson.M{"$ne": bson.A{"$text", ""}}, 1, 0}}},
			"representative": bson.M{"$sum": bson.M{"$cond": bson.A{bson.M{"$eq": bson.A{"$is_representative", true}}, 1, 0}}},
			"avg":            bson.M{"$avg": "$rating"},
			"last":           bson.M{"$max": "$reviewed_at"},
			"ratings":        bson.M{"$push": "$rating"},
		}},
	}
}

func emptyCounts() review.Counts {
	return review.Counts{RatingDistribution: make(map[int]int64)}
}
