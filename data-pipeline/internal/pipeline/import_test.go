package pipeline_test

import (
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/zed1995/platepilot/data-pipeline/internal/pipeline"
	"github.com/zed1995/platepilot/shared/domain/restaurant"
	"github.com/zed1995/platepilot/shared/domain/review"
	"github.com/zed1995/platepilot/shared/testkit"
)

func writeGzJSONL(t *testing.T, path string, lines []string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer file.Close()
	gz := gzip.NewWriter(file)
	for _, line := range lines {
		if _, err := gz.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

// seedDataDir writes small gzip JSONL fixtures that exercise the accept,
// filter, reject, and unmatched paths.
func seedDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	meta := []string{
		`{"gmap_id":"gmap-1","name":"Joe's Pizza","address":"7 Carmine St, New York, NY","latitude":40.730,"longitude":-74.002,"category":["Pizza restaurant","Restaurant"],"avg_rating":4.5,"num_of_reviews":9998,"price":"$$","hours":[["Monday","11AM\u201310PM"]],"MISC":{"Service options":["Outdoor seating"]},"state":"Open","url":"https://maps.example/1"}`,
		`{"gmap_id":"gmap-2","name":"Corner Cafe","address":"1 Broadway, New York, NY","latitude":40.720,"longitude":-74.000,"category":["Cafe"],"avg_rating":4.1,"num_of_reviews":120,"state":"Open","url":"https://maps.example/2"}`,
		`{"gmap_id":"gmap-3","name":"City Museum","latitude":40.7,"longitude":-74.0,"category":["Museum"],"state":"Open"}`,
		`{"gmap_id":"gmap-4","name":"No Coords Diner","category":["Diner"],"state":"Open"}`,
		`{"gmap_id":`,
	}
	reviews := []string{
		`{"gmap_id":"gmap-1","user_id":"u1","name":"Jane","time":1614600000000,"rating":5,"text":"Great pizza and very fast service, would return."}`,
		`{"gmap_id":"gmap-2","user_id":"u2","name":"Bob","time":1614600000000,"rating":4,"text":"ok"}`,
		`{"gmap_id":"gmap-9","user_id":"u3","time":1614600000000,"rating":5,"text":"This place does not exist in the meta file."}`,
		`{"gmap_id":"gmap-1","user_id":"u4","time":1614600000000,"rating":9,"text":"rating out of range"}`,
	}
	writeGzJSONL(t, filepath.Join(dir, pipeline.MetaFileName), meta)
	writeGzJSONL(t, filepath.Join(dir, pipeline.ReviewFileName), reviews)
	return dir
}

func memoryStores() (pipeline.Stores, *testkit.RestaurantStore, *testkit.ReviewStore, *testkit.PipelineStore) {
	restaurants := testkit.NewRestaurantStore()
	reviews := testkit.NewReviewStore()
	batches := testkit.NewPipelineStore()
	return pipeline.Stores{Restaurants: restaurants, Reviews: reviews, Pipeline: batches}, restaurants, reviews, batches
}

func TestRunImportEndToEnd(t *testing.T) {
	ctx := context.Background()
	dir := seedDataDir(t)
	stores, restaurants, reviews, batches := memoryStores()

	opts := pipeline.ImportOptions{Stage: review.StageAll, DataDir: dir, BatchSize: 2, MinTextChars: 20}
	reports, err := pipeline.RunImport(ctx, stores, opts)
	if err != nil {
		t.Fatalf("RunImport: %v", err)
	}
	if len(reports) != 4 {
		t.Fatalf("reports = %d want 4 (meta, review, stats, score)", len(reports))
	}

	metaReport := reports[0]
	if metaReport.RowsRead != 5 || metaReport.Accepted != 2 || metaReport.Filtered != 1 || metaReport.Rejected != 2 {
		t.Fatalf("meta report = %+v", metaReport)
	}
	if metaReport.Status != review.StatusSucceeded {
		t.Errorf("meta status = %q", metaReport.Status)
	}

	reviewReport := reports[1]
	if reviewReport.RowsRead != 4 || reviewReport.Accepted != 2 || reviewReport.Written != 2 || reviewReport.Unmatched != 1 || reviewReport.Rejected != 1 {
		t.Fatalf("review report = %+v", reviewReport)
	}

	// Both food places were curated; the museum and the invalid records were not.
	all, err := restaurants.ListRestaurants(ctx, 0)
	if err != nil {
		t.Fatalf("ListRestaurants: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("restaurants = %d want 2", len(all))
	}
	if _, err := restaurants.GetBySourceRecordID(ctx, "gmap-3"); err == nil {
		t.Error("non-food place should not be stored")
	}

	// Reviews keep the rating sample but blank unusable text.
	joe, err := restaurants.GetBySourceRecordID(ctx, "gmap-1")
	if err != nil {
		t.Fatalf("GetBySourceRecordID: %v", err)
	}
	cafe, err := restaurants.GetBySourceRecordID(ctx, "gmap-2")
	if err != nil {
		t.Fatalf("GetBySourceRecordID cafe: %v", err)
	}
	joeCounts, _ := reviews.CountByRestaurant(ctx, joe.ID)
	if joeCounts.StoredCount != 1 || joeCounts.TextCount != 1 {
		t.Errorf("joe counts = %+v", joeCounts)
	}
	cafeCounts, _ := reviews.CountByRestaurant(ctx, cafe.ID)
	if cafeCounts.StoredCount != 1 || cafeCounts.TextCount != 0 {
		t.Errorf("cafe counts = %+v (short text should be blanked)", cafeCounts)
	}

	// The stats stage materialised review_stats onto the restaurants.
	joe, _ = restaurants.GetBySourceRecordID(ctx, "gmap-1")
	if joe.ReviewStats.StoredReviewCount != 1 || joe.ReviewStats.TextReviewCount != 1 || joe.ReviewStats.SourceReviewCount != 9998 {
		t.Errorf("joe review stats = %+v", joe.ReviewStats)
	}

	// The score stage selected every eligible restaurant for the (small) demo.
	active, err := restaurants.CountActiveForDemo(ctx)
	if err != nil {
		t.Fatalf("CountActiveForDemo: %v", err)
	}
	if active != 2 {
		t.Errorf("active = %d want 2", active)
	}

	// Every stage produced a queryable audit record.
	stored, err := batches.ListBatches(ctx, 10)
	if err != nil {
		t.Fatalf("ListBatches: %v", err)
	}
	if len(stored) != 4 {
		t.Errorf("batches = %d want 4", len(stored))
	}
	_, rejections, err := batches.BatchDetail(ctx, metaReport.BatchID)
	if err != nil {
		t.Fatalf("BatchDetail: %v", err)
	}
	if len(rejections) != 2 {
		t.Errorf("meta rejections = %d want 2", len(rejections))
	}
}

func TestRunImportIsIdempotent(t *testing.T) {
	ctx := context.Background()
	dir := seedDataDir(t)
	stores, restaurants, reviews, _ := memoryStores()
	opts := pipeline.ImportOptions{Stage: review.StageAll, DataDir: dir, BatchSize: 2, MinTextChars: 20}

	if _, err := pipeline.RunImport(ctx, stores, opts); err != nil {
		t.Fatalf("first RunImport: %v", err)
	}
	first, _ := restaurants.ListRestaurants(ctx, 0)
	ids, _ := reviews.RestaurantIDsWithReviews(ctx)
	beforeCounts := make(map[int64]review.Counts, len(ids))
	for _, id := range ids {
		counts, _ := reviews.CountByRestaurant(ctx, id)
		beforeCounts[id] = counts
	}

	if _, err := pipeline.RunImport(ctx, stores, opts); err != nil {
		t.Fatalf("second RunImport: %v", err)
	}
	second, _ := restaurants.ListRestaurants(ctx, 0)
	if len(second) != len(first) {
		t.Errorf("restaurant count grew from %d to %d on re-import", len(first), len(second))
	}
	ids, _ = reviews.RestaurantIDsWithReviews(ctx)
	for _, id := range ids {
		counts, _ := reviews.CountByRestaurant(ctx, id)
		if counts.StoredCount != beforeCounts[id].StoredCount {
			t.Errorf("review count for %d grew from %d to %d", id, beforeCounts[id].StoredCount, counts.StoredCount)
		}
	}
}

func TestRunImportDryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	dir := seedDataDir(t)
	stores, restaurants, reviews, batches := memoryStores()
	opts := pipeline.ImportOptions{Stage: review.StageMeta, DataDir: dir, BatchSize: 2, DryRun: true}

	reports, err := pipeline.RunImport(ctx, stores, opts)
	if err != nil {
		t.Fatalf("RunImport dry-run: %v", err)
	}
	if reports[0].Accepted != 2 {
		t.Errorf("dry-run accepted = %d want 2", reports[0].Accepted)
	}
	if reports[0].Written != 0 {
		t.Errorf("dry-run wrote %d documents", reports[0].Written)
	}
	all, _ := restaurants.ListRestaurants(ctx, 0)
	if len(all) != 0 {
		t.Errorf("dry-run stored %d restaurants", len(all))
	}
	if ids, _ := reviews.RestaurantIDsWithReviews(ctx); len(ids) != 0 {
		t.Errorf("dry-run stored reviews for %v", ids)
	}
	if stored, _ := batches.ListBatches(ctx, 10); len(stored) != 0 {
		t.Errorf("dry-run persisted %d batch reports", len(stored))
	}
}

func TestRunImportRejectsUnknownStage(t *testing.T) {
	_, err := pipeline.RunImport(context.Background(), pipeline.Stores{}, pipeline.ImportOptions{Stage: "nope"})
	if err == nil {
		t.Fatal("unknown stage should fail")
	}
}

func TestRunImportCountsDuplicateRecords(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	row := `{"gmap_id":"gmap-1","name":"Joe's Pizza","address":"7 Carmine St","latitude":40.73,"longitude":-74.002,"category":["Pizza restaurant"],"avg_rating":4.5,"num_of_reviews":100,"state":"Open"}`
	reviewLine := `{"gmap_id":"gmap-1","user_id":"u1","time":1614600000000,"rating":5,"text":"Great pizza and very fast service, would return."}`
	writeGzJSONL(t, filepath.Join(dir, pipeline.MetaFileName), []string{row, row})
	writeGzJSONL(t, filepath.Join(dir, pipeline.ReviewFileName), []string{reviewLine, reviewLine})

	stores, restaurants, reviews, _ := memoryStores()
	opts := pipeline.ImportOptions{Stage: review.StageAll, DataDir: dir, BatchSize: 10, MinTextChars: 20}
	reports, err := pipeline.RunImport(ctx, stores, opts)
	if err != nil {
		t.Fatalf("RunImport: %v", err)
	}
	if reports[0].Deduped != 1 || reports[0].Accepted != 2 {
		t.Errorf("meta dedup: deduped=%d accepted=%d want 1/2", reports[0].Deduped, reports[0].Accepted)
	}
	if reports[1].Deduped != 1 || reports[1].Accepted != 2 {
		t.Errorf("review dedup: deduped=%d accepted=%d want 1/2", reports[1].Deduped, reports[1].Accepted)
	}

	// Duplicates collapse to a single document each.
	all, _ := restaurants.ListRestaurants(ctx, 0)
	if len(all) != 1 {
		t.Errorf("restaurants = %d want 1", len(all))
	}
	counts, _ := reviews.CountByRestaurant(ctx, all[0].ID)
	if counts.StoredCount != 1 {
		t.Errorf("reviews = %d want 1", counts.StoredCount)
	}
}

func TestImportReviewFileOverride(t *testing.T) {
	// The prefilter writes a differently named corpus, so the importer must be
	// able to read it without the operator renaming the file by hand.
	ctx := context.Background()
	dir := t.TempDir()
	store := filepath.Join(dir, pipeline.DefaultFilteredReviewFile)

	reviews := []string{
		`{"gmap_id": "gmap-1", "user_id": "u1", "name": "Jane", "time": 1614600000000, "rating": 5, "text": "Great pizza and very fast service, would return."}`,
	}
	writeGzJSONL(t, store, reviews)
	// The raw default name must be absent, so only the override can succeed.
	if _, err := os.Stat(filepath.Join(dir, pipeline.ReviewFileName)); !os.IsNotExist(err) {
		t.Fatal("fixture should not contain the default review file name")
	}

	stores, restaurants, reviewStore, _ := memoryStores()
	seed := []restaurant.Restaurant{{
		SourceRecordID: "gmap-1", Name: "Joe's Pizza",
		Location: &restaurant.GeoPoint{Longitude: -74.002, Latitude: 40.730},
	}}
	if _, err := restaurants.UpsertRestaurants(ctx, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Restaurant ids are now assigned by the store on insert, so resolve the
	// seeded id through the same source-record map the importer uses.
	ids, err := restaurants.MapSourceRecordIDs(ctx, []string{"gmap-1"})
	if err != nil {
		t.Fatalf("map source record ids: %v", err)
	}
	seededID, ok := ids["gmap-1"]
	if !ok {
		t.Fatal("seeded restaurant id not resolvable from gmap-1")
	}

	reports, err := pipeline.RunImport(ctx, stores, pipeline.ImportOptions{
		Stage: "review", DataDir: dir, ReviewFile: pipeline.DefaultFilteredReviewFile,
		BatchSize: 10, MinTextChars: 20,
	})
	if err != nil {
		t.Fatalf("RunImport with override: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("reports = %d want 1", len(reports))
	}
	report := reports[0]
	if report.Accepted != 1 || report.Written != 1 {
		t.Fatalf("report = %+v, want 1 accepted and written", report)
	}
	// The batch report must record the file actually read, not the default name.
	if report.SourceFile != pipeline.DefaultFilteredReviewFile {
		t.Errorf("SourceFile = %q want %q", report.SourceFile, pipeline.DefaultFilteredReviewFile)
	}
	if got, _ := reviewStore.CountByRestaurant(ctx, seededID); got.StoredCount != 1 {
		t.Errorf("stored reviews = %d want 1", got.StoredCount)
	}
}
