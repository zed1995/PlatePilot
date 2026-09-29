package mongo

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// IndexStatus reports the outcome of ensuring one index.
type IndexStatus struct {
	Collection string
	Name       string
	Created    bool
}

// indexSpecs declares every M1 index in one place so it can be audited and
// diffed. Query predicates drive the compound key order (equality first, then
// range/sort).
func indexSpecs() map[string][]mongo.IndexModel {
	idx := func(name string, unique bool, keys bson.D) mongo.IndexModel {
		opts := options.Index().SetName(name)
		if unique {
			opts = opts.SetUnique(true)
		}
		return mongo.IndexModel{Keys: keys, Options: opts}
	}
	return map[string][]mongo.IndexModel{
		CollectionRestaurants: {
			idx("uniq_source_record_id", true, bson.D{{Key: "source_record_id", Value: 1}}),
			idx("geo_location", false, bson.D{{Key: "location", Value: "2dsphere"}}),
			idx("ix_search_filters", false, bson.D{
				{Key: "is_active_for_demo", Value: 1},
				{Key: "cuisine_tags", Value: 1},
				{Key: "price.level", Value: 1},
				{Key: "rating.source_avg", Value: -1},
			}),
			idx("ix_active_score", false, bson.D{
				{Key: "is_active_for_demo", Value: 1},
				{Key: "knowledge_score", Value: -1},
			}),
			idx("ix_borough", false, bson.D{{Key: "borough_guess", Value: 1}}),
		},
		CollectionRestaurantDocuments: {
			idx("uniq_restaurant_doctype", true, bson.D{
				{Key: "restaurant_id", Value: 1},
				{Key: "document_type", Value: 1},
			}),
		},
		CollectionReviews: {
			idx("ix_restaurant_time", false, bson.D{
				{Key: "restaurant_id", Value: 1},
				{Key: "reviewed_at", Value: -1},
			}),
			idx("ix_representative", false, bson.D{
				{Key: "restaurant_id", Value: 1},
				{Key: "is_representative", Value: 1},
				{Key: "rating", Value: 1},
			}),
			idx("ix_text_hash", false, bson.D{{Key: "text_hash", Value: 1}}),
		},
		CollectionReviewSummaries: {
			idx("uniq_restaurant_topic", true, bson.D{
				{Key: "restaurant_id", Value: 1},
				{Key: "topic", Value: 1},
			}),
		},
		CollectionIngestionBatches: {
			idx("ix_started_at", false, bson.D{{Key: "started_at", Value: -1}}),
			idx("ix_stage_time", false, bson.D{
				{Key: "stage", Value: 1},
				{Key: "started_at", Value: -1},
			}),
		},
		CollectionIngestionRejections: {
			idx("ix_batch", false, bson.D{{Key: "batch_id", Value: 1}}),
		},
	}
}

// EnsureIndexes creates every declared index. Re-running against an identical
// index set is a no-op; a name/spec conflict is reported as a conflict.
func (c *Client) EnsureIndexes(ctx context.Context) ([]IndexStatus, error) {
	var out []IndexStatus
	for collection, models := range indexSpecs() {
		coll := c.db().Collection(collection)

		preexisting := map[string]bool{}
		if specs, err := coll.Indexes().ListSpecifications(ctx); err == nil {
			for _, spec := range specs {
				preexisting[spec.Name] = true
			}
		}

		if _, err := coll.Indexes().CreateMany(ctx, models); err != nil {
			if isIndexConflict(err) {
				return out, operationError("mongo: create index on "+collection, err)
			}
			return out, operationError("mongo: create indexes on "+collection, err)
		}
		for _, model := range models {
			name := ""
			if model.Options != nil && model.Options.Name != nil {
				name = *model.Options.Name
			}
			out = append(out, IndexStatus{Collection: collection, Name: name, Created: !preexisting[name]})
		}
	}
	return out, nil
}
