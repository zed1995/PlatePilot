package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/zed1995/platepilot/shared/domain/restaurant"
	"github.com/zed1995/platepilot/shared/domain/search"
	"github.com/zed1995/platepilot/shared/store"
	"github.com/zed1995/platepilot/shared/store/contract"
	"github.com/zed1995/platepilot/shared/store/postgres"
)

// newReadStores returns a migrated, empty database seeded with the read
// contract's fixture.
//
// The seed goes through the write-side store rather than raw SQL so the rows are
// exactly the ones the pipeline would produce. A hand-written INSERT could
// differ in a column the read side depends on — a missing borough, a NULL
// price level — and the contract would then be testing a corpus that does not
// exist.
func newReadStores(t *testing.T) contract.ReadStores {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := postgres.Connect(ctx, postgres.Config{
		DSN: dsn(t), Database: "platepilot",
		ConnectTimeout: 10 * time.Second, Timeout: 30 * time.Second,
	})
	if err != nil {
		requireDatabase(t, "M3-01 read contract", err)
		t.SkipNow()
	}
	t.Cleanup(func() { _ = client.Close(context.WithoutCancel(ctx)) })

	if err := client.Drop(ctx); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if _, err := client.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	store := postgres.NewRestaurantStore(client)
	seeds := contract.ReadSeedRows()
	scores := make(map[int64]float64, len(seeds))
	active := make(map[int64]bool, len(seeds))
	for _, detail := range seeds {
		if err := store.UpsertRestaurant(ctx, seedFromDetail(detail)); err != nil {
			t.Fatalf("seed restaurant %d: %v", detail.RestaurantID, err)
		}
		// The meta upsert deliberately does not write the rating rollup or the
		// scoring columns: those belong to the stats and score stages, so a
		// single upsert leaves them at their defaults. The read side filters and
		// ranks on exactly those columns, so the seed has to run the same stages
		// or it is testing a corpus the pipeline never produces.
		if err := store.UpdateReviewStats(ctx, detail.RestaurantID,
			restaurant.ReviewStats{StoredReviewCount: detail.RatingCount, StatsUpdatedAt: detail.SnapshotAt},
			restaurant.Rating{
				ComputedAvg:               detail.Rating,
				RatingCountForComputedAvg: detail.RatingCount,
			}); err != nil {
			t.Fatalf("seed stats for %d: %v", detail.RestaurantID, err)
		}
		scores[detail.RestaurantID] = detail.KnowledgeScore
		active[detail.RestaurantID] = detail.IsActiveForDemo
	}
	if err := store.UpdateScores(ctx, scores, active); err != nil {
		t.Fatalf("seed scores: %v", err)
	}

	return contract.ReadStores{
		Restaurants: postgres.NewRestaurantSearchRepository(client),
		Knowledge:   memoryKnowledge(),
	}
}

// memoryKnowledge returns an empty knowledge repository. Evidence recall is
// M3-04's scope; the read contract for it is written when that task lands.
func memoryKnowledge() store.KnowledgeRepository { return nil }

// seedFromDetail renders a read-contract fixture as a curated restaurant.
func seedFromDetail(detail search.RestaurantDetail) restaurant.Restaurant {
	observed := detail.SnapshotAt
	if observed.IsZero() {
		observed = time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC)
	}
	status := restaurant.SnapshotStatus(detail.SnapshotStatus)
	if status == "" {
		status = restaurant.StatusUnknown
	}
	attributes := map[string][]string{}
	for key, value := range detail.Attributes {
		attributes[key] = []string{value}
	}

	return restaurant.Restaurant{
		ID:              detail.RestaurantID,
		Source:          restaurant.SourceGoogleLocal2021,
		SourceRecordID:  "gmap-read-" + detail.Name,
		Name:            detail.Name,
		Address:         detail.Address,
		BoroughGuess:    detail.Borough,
		CuisineTags:     detail.Cuisines,
		Price:           restaurant.Price{Level: detail.PriceLevel},
		Rating:          restaurant.Rating{ComputedAvg: detail.Rating, RatingCountForComputedAvg: detail.RatingCount},
		KnowledgeScore:  detail.KnowledgeScore,
		IsActiveForDemo: detail.IsActiveForDemo,
		SnapshotStatus:  status,
		ObservedAt:      observed,
		Attributes:      restaurant.Attributes{TriStates: detail.Attributes},
		AttributesRaw:   attributes,
		CreatedAt:       observed,
		UpdatedAt:       observed,
	}
}

// TestRestaurantReadContract runs the shared read-side behaviour suite against
// the real store. It skips when PostgreSQL is unreachable unless
// PLATEPILOT_REQUIRE_DB is set, because a silently skipped suite reads as a pass.
func TestRestaurantReadContract(t *testing.T) {
	contract.RunRead(t, newReadStores)
}

// The corpus filter is what keeps a Top-20 page from scanning 36k rows. A
// restaurant that is not in the demo set must not appear in a result.
func TestRestaurantSearchExcludesRowsOutsideTheDemoSet(t *testing.T) {
	stores := newReadStores(t)
	repo := stores.Restaurants

	got, err := repo.Search(context.Background(), search.SearchQuery{
		Filter: search.RestaurantFilter{Borough: "queens"},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("no seeded restaurant is in queens, got %d", len(got))
	}
}

// Reading one restaurant must not drag the whole row in. The read projection is
// lighter than the write one on purpose, and this is the check that it stays
// usable for the fields a candidate displays.
func TestRestaurantSearchProjectsTheFieldsACandidateDisplays(t *testing.T) {
	stores := newReadStores(t)
	detail, err := stores.Restaurants.GetByID(context.Background(), 1)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if detail.Name != "Casa Verdi" {
		t.Fatalf("name = %q", detail.Name)
	}
	if detail.Borough != "manhattan" {
		t.Fatalf("borough = %q", detail.Borough)
	}
	if len(detail.Cuisines) == 0 {
		t.Fatal("cuisines must be projected; a candidate displays them")
	}
	if detail.KnowledgeScore <= 0 {
		t.Fatalf("knowledge_score = %v; the structured channel ranks on it", detail.KnowledgeScore)
	}
	if detail.RatingCount != 300 {
		t.Fatalf("rating_count = %d; a rating without its sample size is not a claim", detail.RatingCount)
	}
}

// An empty filter is refused rather than answered with the corpus in prior
// order: a filterless structured search is a listing, not an answer.
func TestRestaurantSearchRefusesAnEmptyFilter(t *testing.T) {
	stores := newReadStores(t)
	_, err := stores.Restaurants.Search(context.Background(), search.SearchQuery{})
	if err == nil {
		t.Fatal("a filterless structured search must be refused")
	}
}
