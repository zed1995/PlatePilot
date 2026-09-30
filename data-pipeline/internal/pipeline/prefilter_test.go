package pipeline_test

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zed/platepilot/data-pipeline/internal/pipeline"
	"github.com/zed/platepilot/data-pipeline/internal/pipeline/raw"
	"github.com/zed/platepilot/shared/domain/restaurant"
	"github.com/zed/platepilot/shared/testkit"
)

// prefilterFixture writes a review corpus covering every branch: a long usable
// text, a short text, a missing text, an unknown place, and an invalid rating.
func prefilterFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	reviews := []string{
		`{"user_id": "u1", "name": "Jane", "time": 1614600000000, "rating": 5, "text": "The pizza here is genuinely excellent and worth the trip.", "gmap_id": "gmap-1"}`,
		`{"user_id": "u2", "name": "Bob", "time": 1614600000000, "rating": 4, "text": "ok", "gmap_id": "gmap-1"}`,
		`{"user_id": "u3", "name": "Cid", "time": 1614600000000, "rating": 3, "gmap_id": "gmap-1"}`,
		`{"user_id": "u4", "name": "Dee", "time": 1614600000000, "rating": 5, "text": "Lovely spot for a long lazy brunch on a Sunday.", "gmap_id": "gmap-2"}`,
		`{"user_id": "u5", "name": "Eve", "time": 1614600000000, "rating": 1, "text": "This place is not in the restaurant set at all.", "gmap_id": "gmap-9"}`,
		`{"user_id": "u6", "name": "Fay", "time": 1614600000000, "rating": 9, "text": "Rating is out of range and must be rejected.", "gmap_id": "gmap-2"}`,
	}
	writeGzJSONL(t, filepath.Join(dir, pipeline.ReviewFileName), reviews)
	return dir
}

func seedRestaurants(t *testing.T, ctx context.Context, stores *testkit.RestaurantStore, ids ...string) {
	t.Helper()
	for _, id := range ids {
		// Ids are assigned by the store; the source record id is the natural key.
		if err := stores.UpsertRestaurant(ctx, restaurant.Restaurant{
			SourceRecordID: id,
			Name:           "Place " + id,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
}

func readFiltered(t *testing.T, path string) []raw.Review {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("gzip %s: %v", path, err)
	}
	defer gz.Close()
	var out []raw.Review
	dec := json.NewDecoder(gz)
	for dec.More() {
		var r raw.Review
		if err := dec.Decode(&r); err != nil {
			t.Fatalf("decode: %v", err)
		}
		out = append(out, r)
	}
	return out
}

func TestPrefilterDropsUnjoinableAndTextlessReviews(t *testing.T) {
	ctx := context.Background()
	dir := prefilterFixture(t)
	stores, restaurants, _, _ := memoryStores()
	seedRestaurants(t, ctx, restaurants, "gmap-1", "gmap-2")

	outDir := filepath.Join(t.TempDir(), "processed")
	result, err := pipeline.Prefilter(ctx, stores, pipeline.PrefilterOptions{
		DataDir: dir, OutDir: outDir, MinTextChars: 20,
	})
	if err != nil {
		t.Fatalf("Prefilter: %v", err)
	}

	if result.RowsRead != 6 {
		t.Errorf("RowsRead = %d want 6", result.RowsRead)
	}
	if result.Unmatched != 1 {
		t.Errorf("Unmatched = %d want 1 (gmap-9 is not ingested)", result.Unmatched)
	}
	if result.NoText != 1 {
		t.Errorf("NoText = %d want 1 (the textless review)", result.NoText)
	}
	if result.ShortText != 1 {
		t.Errorf("ShortText = %d want 1 (\"ok\")", result.ShortText)
	}
	if result.Rejected != 1 {
		t.Errorf("Rejected = %d want 1 (rating 9)", result.Rejected)
	}
	if result.Kept != 2 {
		t.Errorf("Kept = %d want 2 (only the two long-text reviews)", result.Kept)
	}
	if result.RestaurantsAvailable != 2 {
		t.Errorf("RestaurantsAvailable = %d want 2", result.RestaurantsAvailable)
	}

	kept := readFiltered(t, result.OutputFile)
	if len(kept) != 2 {
		t.Fatalf("filtered file has %d rows want 2", len(kept))
	}
	for _, r := range kept {
		if r.Text == nil || len([]rune(*r.Text)) < 20 {
			t.Errorf("kept a row without usable text: %+v", r)
		}
		// Identity fields must not survive into the intermediate corpus.
		if r.UserID != "" || r.Name != "" || len(r.Pics) != 0 || len(r.Resp) != 0 {
			t.Errorf("identity fields leaked into the filtered corpus: %+v", r)
		}
	}
}

func TestPrefilterKeepShortTextRetainsShortRows(t *testing.T) {
	ctx := context.Background()
	dir := prefilterFixture(t)
	stores, restaurants, _, _ := memoryStores()
	seedRestaurants(t, ctx, restaurants, "gmap-1", "gmap-2")

	outDir := filepath.Join(t.TempDir(), "processed")
	result, err := pipeline.Prefilter(ctx, stores, pipeline.PrefilterOptions{
		DataDir: dir, OutDir: outDir, MinTextChars: 20, KeepShortText: true,
	})
	if err != nil {
		t.Fatalf("Prefilter: %v", err)
	}
	// KeepShortText mirrors the importer, which keeps every review that joins to
	// a restaurant and only blanks the unusable text: two long reviews, the
	// short "ok" one, and the textless one all survive. The unknown place and
	// the out-of-range rating are still dropped.
	if result.ShortText != 1 {
		t.Errorf("ShortText = %d want 1", result.ShortText)
	}
	if result.NoText != 1 {
		t.Errorf("NoText = %d want 1", result.NoText)
	}
	if result.Kept != 4 {
		t.Errorf("Kept = %d want 4 (two long, one short, one textless)", result.Kept)
	}
	if result.Unmatched != 1 {
		t.Errorf("Unmatched = %d want 1", result.Unmatched)
	}
	if result.Rejected != 1 {
		t.Errorf("Rejected = %d want 1", result.Rejected)
	}
}

func TestPrefilterScrubsPII(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeGzJSONL(t, filepath.Join(dir, pipeline.ReviewFileName), []string{
		`{"user_id": "u1", "name": "Jane", "time": 1614600000000, "rating": 5, "text": "Reach me at jane.doe@example.com or 212-555-0199 anytime at all.", "gmap_id": "gmap-1"}`,
	})
	stores, restaurants, _, _ := memoryStores()
	seedRestaurants(t, ctx, restaurants, "gmap-1")

	result, err := pipeline.Prefilter(ctx, stores, pipeline.PrefilterOptions{
		DataDir: dir, OutDir: filepath.Join(t.TempDir(), "out"), MinTextChars: 20,
	})
	if err != nil {
		t.Fatalf("Prefilter: %v", err)
	}
	kept := readFiltered(t, result.OutputFile)
	if len(kept) != 1 {
		t.Fatalf("kept %d rows want 1", len(kept))
	}
	text := *kept[0].Text
	if strings.Contains(text, "jane.doe@example.com") {
		t.Errorf("email survived prefilter: %q", text)
	}
	if strings.Contains(text, "212-555-0199") {
		t.Errorf("phone survived prefilter: %q", text)
	}
	if !strings.Contains(text, "[redacted-") {
		t.Errorf("expected redaction markers, got %q", text)
	}
}

func TestPrefilterIsIdempotentAndOverwrites(t *testing.T) {
	ctx := context.Background()
	dir := prefilterFixture(t)
	stores, restaurants, _, _ := memoryStores()
	seedRestaurants(t, ctx, restaurants, "gmap-1", "gmap-2")
	outDir := filepath.Join(t.TempDir(), "out")
	opts := pipeline.PrefilterOptions{DataDir: dir, OutDir: outDir, MinTextChars: 20}

	first, err := pipeline.Prefilter(ctx, stores, opts)
	if err != nil {
		t.Fatalf("first Prefilter: %v", err)
	}
	second, err := pipeline.Prefilter(ctx, stores, opts)
	if err != nil {
		t.Fatalf("second Prefilter: %v", err)
	}
	if first.Kept != second.Kept {
		t.Errorf("Kept differs between runs: %d then %d", first.Kept, second.Kept)
	}
	if got := len(readFiltered(t, second.OutputFile)); got != 2 {
		t.Errorf("second run produced %d rows want 2 (file must be replaced, not appended)", got)
	}
	// The temp file must not survive a successful run.
	if _, err := os.Stat(second.OutputFile + ".partial"); !os.IsNotExist(err) {
		t.Error("a .partial file was left behind")
	}
}

func TestPrefilterLimitBoundsRowsRead(t *testing.T) {
	ctx := context.Background()
	dir := prefilterFixture(t)
	stores, restaurants, _, _ := memoryStores()
	seedRestaurants(t, ctx, restaurants, "gmap-1", "gmap-2")

	result, err := pipeline.Prefilter(ctx, stores, pipeline.PrefilterOptions{
		DataDir: dir, OutDir: filepath.Join(t.TempDir(), "out"), MinTextChars: 20, Limit: 1,
	})
	if err != nil {
		t.Fatalf("Prefilter: %v", err)
	}
	if result.RowsRead != 1 {
		t.Errorf("RowsRead = %d want 1", result.RowsRead)
	}
}

func TestPrefilterRequiresRestaurantStore(t *testing.T) {
	_, err := pipeline.Prefilter(context.Background(), pipeline.Stores{}, pipeline.PrefilterOptions{
		DataDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("prefilter without a restaurant store should fail")
	}
}

func TestPrefilterKeptFraction(t *testing.T) {
	r := pipeline.PrefilterResult{RowsRead: 200, Kept: 50}
	if got := r.KeptFraction(); got != 0.25 {
		t.Errorf("KeptFraction = %v want 0.25", got)
	}
	if got := (pipeline.PrefilterResult{}).KeptFraction(); got != 0 {
		t.Errorf("KeptFraction on an empty result = %v want 0", got)
	}
}
