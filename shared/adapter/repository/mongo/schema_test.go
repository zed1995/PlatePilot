package mongo

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/zed/platepilot/shared/domain/restaurant"
	"github.com/zed/platepilot/shared/domain/review"
)

func TestRestaurantMappingRoundTrip(t *testing.T) {
	level := 2
	sourceAvg := 4.5
	computedAvg := 4.4
	lastReviewed := time.Date(2021, 8, 1, 12, 0, 0, 0, time.UTC)
	observed := time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC)

	in := restaurant.Restaurant{
		ID:             "id-1",
		Source:         restaurant.SourceGoogleLocal2021,
		SourceRecordID: "gmap-1",
		Name:           "Joe's Pizza",
		Address:        "7 Carmine St, New York, NY",
		BoroughGuess:   "manhattan",
		Location:       &restaurant.GeoPoint{Longitude: -74.002, Latitude: 40.730},
		Categories:     []string{"Pizza restaurant", "Restaurant"},
		CuisineTags:    []string{"pizza", "italian"},
		Description:    "Slice shop",
		Price:          restaurant.Price{Raw: "$$", Level: &level},
		Rating: restaurant.Rating{
			SourceAvg:                 &sourceAvg,
			ComputedAvg:               &computedAvg,
			RatingCountForComputedAvg: 180,
		},
		ReviewStats: restaurant.ReviewStats{
			SourceReviewCount:       9998,
			SourceReviewCountCapped: true,
			StoredReviewCount:       5000,
			TextReviewCount:         4200,
			LastReviewedAt:          &lastReviewed,
			StatsUpdatedAt:          observed,
		},
		Attributes: restaurant.Attributes{
			TriStates:      map[string]string{"wheelchair_accessible": restaurant.TriStateTrue, "outdoor_seating": restaurant.TriStateUnknown},
			AtmosphereTags: []string{"casual"},
		},
		SnapshotStatus:  restaurant.StatusOpen,
		KnowledgeScore:  9.5,
		IsActiveForDemo: true,
		ObservedAt:      observed,
		SourceURL:       "https://www.google.com/maps/place/x",
		CreatedAt:       observed,
		UpdatedAt:       observed,
	}

	got := docToRestaurant(restaurantToDoc(in))
	if !reflect.DeepEqual(in, got) {
		t.Fatalf("round trip mismatch:\n in = %+v\nout = %+v", in, got)
	}

	// A restaurant with no location must round-trip without inventing one.
	in.Location = nil
	got = docToRestaurant(restaurantToDoc(in))
	if got.Location != nil {
		t.Errorf("nil location became %+v", got.Location)
	}
}

func TestReviewMappingRoundTrip(t *testing.T) {
	at := time.Date(2021, 3, 1, 12, 0, 0, 0, time.UTC)
	in := review.Review{
		ID:               "sha256:abc",
		RestaurantID:     "id-1",
		Rating:           5,
		ReviewedAt:       at,
		Text:             "Great pizza and fast service.",
		Language:         "en",
		TextHash:         "sha256:def",
		IsRepresentative: true,
		TopicTags:        []string{"food", "service"},
		SourceObservedAt: at,
	}
	got := docToReview(reviewToDoc(in))
	if !reflect.DeepEqual(in, got) {
		t.Fatalf("round trip mismatch:\n in = %+v\nout = %+v", in, got)
	}
}

func TestBatchReportMappingRoundTrip(t *testing.T) {
	started := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	finished := started.Add(90 * time.Second)
	in := review.BatchReport{
		BatchID:         "b-1",
		Stage:           review.StageMeta,
		CurationVersion: "1",
		SourceFile:      "meta-New_York.json.gz",
		StartedAt:       started,
		FinishedAt:      finished,
		DurationMS:      90000,
		RowsRead:        100,
		Written:         90,
		Rejected:        10,
		MissingFields:   []review.FieldMissing{{Field: "hours", Count: 3}},
		Status:          review.StatusSucceeded,
	}
	got := docToBatch(batchToDoc(in))
	if !reflect.DeepEqual(in, got) {
		t.Fatalf("round trip mismatch:\n in = %+v\nout = %+v", in, got)
	}

	// A still-running batch has no finished_at and must not gain one.
	in.FinishedAt = time.Time{}
	got = docToBatch(batchToDoc(in))
	if !got.FinishedAt.IsZero() {
		t.Errorf("running batch gained finished_at %v", got.FinishedAt)
	}
}

func TestSearchIndexDefinitionMatchesCommittedFile(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "scripts", "atlas", "search_index_restaurants.json")
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read committed search index: %v", err)
	}
	if got := SearchIndexDefinition(); string(got) != string(want) {
		t.Errorf("search index definition drifted from %s:\n got: %s\nwant: %s", path, got, want)
	}
	name, err := ParseSearchIndexDefinition()
	if err != nil {
		t.Fatalf("ParseSearchIndexDefinition: %v", err)
	}
	if name != SearchIndexName {
		t.Errorf("index name = %q want %q", name, SearchIndexName)
	}
}

func TestClientConfigValidate(t *testing.T) {
	if problems := (Config{}).Validate(); len(problems) == 0 {
		t.Error("empty config should report a missing MONGO_URI")
	}
	cfg := Config{URI: "mongodb://localhost:27017", Database: "platepilot"}
	if problems := cfg.Validate(); len(problems) != 0 {
		t.Errorf("valid config reported problems: %v", problems)
	}
	withDefaults := (Config{URI: "mongodb://x"}).withDefaults()
	if withDefaults.Database != DefaultDatabase || withDefaults.Timeout != DefaultTimeout {
		t.Errorf("defaults not applied: %+v", withDefaults)
	}
}

func TestIndexSpecsAreNamed(t *testing.T) {
	specs := indexSpecs()
	if len(specs) == 0 {
		t.Fatal("no index specs declared")
	}
	for collection, models := range specs {
		for i, model := range models {
			if model.Options == nil || model.Options.Name == nil || *model.Options.Name == "" {
				t.Errorf("%s index %d has no name", collection, i)
			}
			if model.Keys == nil {
				t.Errorf("%s index %d has no keys", collection, i)
			}
		}
	}
	if _, ok := specs[CollectionRestaurants]; !ok {
		t.Error("restaurants indexes missing")
	}
}
