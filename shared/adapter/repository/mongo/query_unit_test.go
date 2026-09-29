package mongo

import (
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/zed/platepilot/shared/domain/restaurant"
	"github.com/zed/platepilot/shared/domain/review"
)

// These tests exercise the adapter's own query/logic construction without a
// server: they assert the exact filter and update documents the driver would
// send, which is the part the port-level in-memory mock cannot cover.

func TestRestaurantUpsertModelKeepsIdentityAndOwnedFields(t *testing.T) {
	model, err := restaurantUpsertModel(restaurant.Restaurant{
		ID:              "id-1",
		Source:          restaurant.SourceGoogleLocal2021,
		SourceRecordID:  "gmap-1",
		Name:            "Joe's Pizza",
		Location:        &restaurant.GeoPoint{Longitude: -74.002, Latitude: 40.730},
		ReviewStats:     restaurant.ReviewStats{StoredReviewCount: 5},
		KnowledgeScore:  9.5,
		IsActiveForDemo: true,
	})
	if err != nil {
		t.Fatalf("restaurantUpsertModel: %v", err)
	}

	up, ok := model.(*mongo.UpdateOneModel)
	if !ok {
		t.Fatalf("model = %T want *mongo.UpdateOneModel", model)
	}
	if up.Upsert == nil || !*up.Upsert {
		t.Error("upsert must be true so re-imports update instead of duplicating")
	}
	filter, ok := up.Filter.(bson.M)
	if !ok || filter["source_record_id"] != "gmap-1" {
		t.Fatalf("filter = %#v want source_record_id=gmap-1", up.Filter)
	}

	update, ok := up.Update.(bson.M)
	if !ok {
		t.Fatalf("update = %T want bson.M", up.Update)
	}
	set, ok := update["$set"].(bson.M)
	if !ok {
		t.Fatalf("$set = %#v", update["$set"])
	}
	if set["name"] != "Joe's Pizza" {
		t.Errorf("$set missing name: %#v", set)
	}
	// The meta import owns only rating.source_avg. Writing the whole rating
	// subdocument would clobber computed_avg, which the stats job owns.
	if _, present := set["rating"]; present {
		t.Error("$set must not replace the whole rating subdocument")
	}
	if _, present := set["rating.source_avg"]; !present {
		t.Error("$set must write rating.source_avg")
	}
	for _, forbidden := range []string{"rating", "rating.computed_avg", "rating.rating_count_for_computed_avg"} {
		if _, present := set[forbidden]; present {
			t.Errorf("$set must not write %q", forbidden)
		}
	}
	// Identity and the fields owned by other jobs must never be overwritten by
	// a meta re-import.
	for _, forbidden := range []string{"_id", "created_at", "review_stats", "knowledge_score", "is_active_for_demo"} {
		if _, present := set[forbidden]; present {
			t.Errorf("$set must not overwrite %q", forbidden)
		}
	}
	// The source review count is owned by the meta import and must be written.
	if _, present := set["review_stats.source_review_count"]; !present {
		t.Error("$set must write review_stats.source_review_count from Meta")
	}

	onInsert, ok := update["$setOnInsert"].(bson.M)
	if !ok {
		t.Fatalf("$setOnInsert = %#v", update["$setOnInsert"])
	}
	for _, required := range []string{"_id", "created_at", "knowledge_score", "is_active_for_demo"} {
		if _, present := onInsert[required]; !present {
			t.Errorf("$setOnInsert must initialise %q", required)
		}
	}
	for _, required := range []string{
		"review_stats.stored_review_count",
		"review_stats.text_review_count",
		"review_stats.representative_review_count",
		"review_stats.embedded_review_count",
	} {
		if _, present := onInsert[required]; !present {
			t.Errorf("$setOnInsert must initialise %q", required)
		}
	}
	if _, present := onInsert["review_stats"]; present {
		t.Error("$setOnInsert must not write a whole review_stats subdocument (it would drop the source count)")
	}
	if onInsert["_id"] != "id-1" {
		t.Errorf("$setOnInsert _id = %v", onInsert["_id"])
	}
	if _, present := update["$unset"]; present {
		t.Error("a located restaurant must not unset location")
	}
}

func TestRestaurantUpsertModelUnsetsMissingLocation(t *testing.T) {
	model, err := restaurantUpsertModel(restaurant.Restaurant{ID: "id-1", SourceRecordID: "gmap-1"})
	if err != nil {
		t.Fatalf("restaurantUpsertModel: %v", err)
	}
	update := model.(*mongo.UpdateOneModel).Update.(bson.M)
	unset, ok := update["$unset"].(bson.M)
	if !ok {
		t.Fatalf("$unset = %#v, want location to be removed", update["$unset"])
	}
	if _, present := unset["location"]; !present {
		t.Errorf("$unset = %#v, want location", unset)
	}
	if _, present := update["$set"].(bson.M)["location"]; present {
		t.Error("a missing location must not be written as null in $set")
	}
}

func TestRestaurantUpsertModelValidatesIdentity(t *testing.T) {
	if _, err := restaurantUpsertModel(restaurant.Restaurant{SourceRecordID: "g"}); err == nil {
		t.Error("missing restaurant_id should fail")
	}
	if _, err := restaurantUpsertModel(restaurant.Restaurant{ID: "id"}); err == nil {
		t.Error("missing source_record_id should fail")
	}
}

func TestReviewUpsertModelIsKeyedByDeterministicID(t *testing.T) {
	model, err := reviewUpsertModel(review.Review{
		ID:           "sha256:abc",
		RestaurantID: "id-1",
		Rating:       5,
		Text:         "Great pizza",
		TextHash:     "sha256:def",
	})
	if err != nil {
		t.Fatalf("reviewUpsertModel: %v", err)
	}
	up := model.(*mongo.UpdateOneModel)
	if up.Upsert == nil || !*up.Upsert {
		t.Error("upsert must be true")
	}
	if filter := up.Filter.(bson.M); filter["_id"] != "sha256:abc" {
		t.Errorf("filter = %#v, want _id=sha256:abc", up.Filter)
	}

	doc, ok := up.Update.(bson.M)["$set"].(reviewDoc)
	if !ok {
		t.Fatalf("$set = %T want reviewDoc", up.Update.(bson.M)["$set"])
	}
	if doc.ID != "sha256:abc" || doc.RestaurantID != "id-1" || doc.Rating != 5 {
		t.Errorf("doc = %+v", doc)
	}

	// The curated review document must not carry reviewer identity or PII.
	raw, err := bson.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal review doc: %v", err)
	}
	var fields bson.M
	if err := bson.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal review doc: %v", err)
	}
	for _, forbidden := range []string{"user_id", "name", "pics", "resp", "email"} {
		if _, present := fields[forbidden]; present {
			t.Errorf("curated review must not persist %q", forbidden)
		}
	}
}

func TestReviewUpsertModelValidates(t *testing.T) {
	if _, err := reviewUpsertModel(review.Review{RestaurantID: "id"}); err == nil {
		t.Error("missing review_id should fail")
	}
	if _, err := reviewUpsertModel(review.Review{ID: "sha256:x"}); err == nil {
		t.Error("missing restaurant_id should fail")
	}
}

func TestCountsPipelineGroupsPerRestaurant(t *testing.T) {
	pipeline := countsPipeline(bson.M{"restaurant_id": bson.M{"$in": []string{"a", "b"}}})
	if len(pipeline) != 2 {
		t.Fatalf("pipeline stages = %d want 2 (match, group)", len(pipeline))
	}
	match := pipeline[0].(bson.M)["$match"].(bson.M)
	if _, present := match["restaurant_id"]; !present {
		t.Errorf("$match = %#v", match)
	}
	group := pipeline[1].(bson.M)["$group"].(bson.M)
	if group["_id"] != "$restaurant_id" {
		t.Errorf("$group _id = %v want $restaurant_id", group["_id"])
	}
	if stored := group["stored"].(bson.M)["$sum"]; stored != 1 {
		t.Errorf("stored accumulator = %#v", group["stored"])
	}
	if avg := group["avg"].(bson.M)["$avg"]; avg != "$rating" {
		t.Errorf("avg accumulator = %#v", group["avg"])
	}
	if last := group["last"].(bson.M)["$max"]; last != "$reviewed_at" {
		t.Errorf("last accumulator = %#v", group["last"])
	}
	if ratings := group["ratings"].(bson.M)["$push"]; ratings != "$rating" {
		t.Errorf("ratings accumulator = %#v", group["ratings"])
	}
	// The text count only counts non-empty text, so stored and text can differ.
	cond := group["text"].(bson.M)["$sum"].(bson.M)["$cond"].(bson.A)
	if len(cond) != 3 || cond[1] != 1 || cond[2] != 0 {
		t.Errorf("text $cond = %#v", cond)
	}
	// Representative count compares against true, not mere presence.
	repCond := group["representative"].(bson.M)["$sum"].(bson.M)["$cond"].(bson.A)
	if len(repCond) != 3 {
		t.Fatalf("representative $cond = %#v", repCond)
	}
}

func TestRejectionIDIsStableAndBatchScoped(t *testing.T) {
	a := review.Rejection{BatchID: "b-1", Stage: review.StageMeta, LineNo: 7}
	b := review.Rejection{BatchID: "b-1", Stage: review.StageMeta, LineNo: 7}
	if rejectionID(a) != rejectionID(b) {
		t.Error("rejection id must be deterministic")
	}
	if rejectionID(a) == rejectionID(review.Rejection{BatchID: "b-2", Stage: review.StageMeta, LineNo: 7}) {
		t.Error("rejection id must include batch_id")
	}
	if rejectionID(a) == rejectionID(review.Rejection{BatchID: "b-1", Stage: review.StageReview, LineNo: 7}) {
		t.Error("rejection id must include stage")
	}
	if rejectionID(a) == rejectionID(review.Rejection{BatchID: "b-1", Stage: review.StageMeta, LineNo: 8}) {
		t.Error("rejection id must include line number")
	}
}
