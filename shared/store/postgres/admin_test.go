package postgres_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zed1995/platepilot/shared/domain/admin"
	"github.com/zed1995/platepilot/shared/store/postgres"
)

// defaultAdminDSN points at the development database rather than the
// scratch database the contract suite destroys. The admin suite only reads,
// so it is safe to run against the imported corpus; override with
// PLATEPILOT_INSPECT_POSTGRES_DSN to check another database.
const defaultAdminDSN = "postgres://platepilot:platepilot@localhost:55432/platepilot?sslmode=disable"

// adminDSN returns the database the admin suite reads.
func adminDSN(t *testing.T) string {
	t.Helper()
	if value := os.Getenv("PLATEPILOT_INSPECT_POSTGRES_DSN"); value != "" {
		return value
	}
	return defaultAdminDSN
}

// newAdminClient connects to the live store without changing its data: the
// admin suite verifies the data the pipeline already wrote.
func newAdminClient(t *testing.T) *postgres.Client {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := postgres.Connect(ctx, postgres.Config{
		DSN:            adminDSN(t),
		Database:       "platepilot",
		ConnectTimeout: 5 * time.Second,
		Timeout:        30 * time.Second,
	})
	if err != nil {
		requireDatabase(t, "admin store", err)
		t.SkipNow()
	}
	t.Cleanup(func() { _ = client.Close(context.WithoutCancel(context.Background())) })
	return client
}

// newAdminStore returns a read-only admin store on the live database.
func newAdminStore(t *testing.T) *postgres.AdminStore {
	t.Helper()
	return postgres.NewAdminStore(newAdminClient(t))
}

// TestAdminOverview loads the dashboard and checks the stored invariants.
func TestAdminOverview(t *testing.T) {
	store := newAdminStore(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	overview, err := store.Overview(ctx)
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}

	if overview.Tables.RestaurantsTotal <= 0 {
		t.Errorf("restaurants total must be positive, got %d", overview.Tables.RestaurantsTotal)
	}
	if overview.Tables.RestaurantsActive > overview.Tables.RestaurantsTotal {
		t.Errorf("active restaurants %d exceed total %d",
			overview.Tables.RestaurantsActive, overview.Tables.RestaurantsTotal)
	}
	if !overview.Tables.ReviewsEstimated {
		t.Error("reviews count must be marked as estimated")
	}
	if overview.Tables.DocumentsActive <= 0 {
		t.Errorf("active documents must be positive, got %d", overview.Tables.DocumentsActive)
	}
	// The vector-health invariant: every active document carries a vector.
	if overview.ActiveWithoutVector != 0 {
		t.Errorf("found %d active documents without a vector; violates the embedded-row rule",
			overview.ActiveWithoutVector)
	}
	if len(overview.DocumentBreakdown) == 0 {
		t.Error("document breakdown must not be empty")
	}
	for _, row := range overview.DocumentBreakdown {
		if row.RetrievalScope == "" || row.DocType == "" || row.Count <= 0 {
			t.Errorf("malformed breakdown row: %+v", row)
		}
	}
	if len(overview.Migrations) == 0 {
		t.Error("migration list must not be empty")
	}
}

// TestAdminRestaurantsKeyset walks every page and proves the keyset neither
// skips nor duplicates a row: the collected id set must equal the store's own
// total count.
func TestAdminRestaurantsKeyset(t *testing.T) {
	store := newAdminStore(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const pageSize = 500
	seen := make(map[int64]struct{})
	var total int64
	var afterID int64
	pages := 0

	for {
		page, err := store.Restaurants(ctx, admin.RestaurantQuery{
			Limit:   pageSize,
			AfterID: afterID,
		})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		if pages == 0 {
			total = page.TotalEstimate
		}
		for _, item := range page.Items {
			if _, dup := seen[item.RestaurantID]; dup {
				t.Fatalf("restaurant %d returned twice", item.RestaurantID)
			}
			seen[item.RestaurantID] = struct{}{}
		}
		pages++
		if page.HasMore {
			afterID = page.Items[len(page.Items)-1].RestaurantID
			continue
		}
		break
	}

	if int64(len(seen)) != total {
		t.Errorf("keyset covered %d ids but total is %d", len(seen), total)
	}
}

// TestAdminDocumentsScopeFilter checks that a scope filter returns only
// documents in that scope.
func TestAdminDocumentsScopeFilter(t *testing.T) {
	store := newAdminStore(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const pageSize = 500
	var total int
	var afterID int64
	for {
		page, err := store.Documents(ctx, admin.DocumentQuery{
			Limit:   pageSize,
			AfterID: afterID,
			Scope:   "restaurant",
		})
		if err != nil {
			t.Fatalf("page after id %d: %v", afterID, err)
		}
		for _, item := range page.Items {
			if item.Scope != "restaurant" {
				t.Fatalf("document %d has scope %q under a restaurant filter",
					item.DocumentID, item.Scope)
			}
		}
		total += len(page.Items)
		if !page.HasMore {
			break
		}
		afterID = page.Items[len(page.Items)-1].DocumentID
	}

	// The corpus carries one profile per selected restaurant, so the profile
	// count is on the scale of three thousand.
	if total < 1000 {
		t.Errorf("expected thousands of restaurant documents, got %d", total)
	}
}

// TestAdminVectorHealth asserts the embedded-row rule directly.
func TestAdminVectorHealth(t *testing.T) {
	store := newAdminStore(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const pageSize = 500
	var afterID int64
	for {
		page, err := store.Documents(ctx, admin.DocumentQuery{
			Limit:    pageSize,
			AfterID:  afterID,
			IsActive: boolPtr(true),
		})
		if err != nil {
			t.Fatalf("page after id %d: %v", afterID, err)
		}
		for _, item := range page.Items {
			if !item.HasEmbedding {
				t.Fatalf("active document %d has no vector", item.DocumentID)
			}
		}
		if !page.HasMore {
			break
		}
		afterID = page.Items[len(page.Items)-1].DocumentID
	}
}

// TestAdminBatchDetail loads the most recent records and their breakdown.
func TestAdminBatchDetail(t *testing.T) {
	store := newAdminStore(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	page, err := store.Batches(ctx, admin.BatchQuery{Limit: 10})
	if err != nil {
		t.Fatalf("Batches: %v", err)
	}
	if len(page.Items) == 0 {
		t.Fatal("expected at least one audit record")
	}

	for _, item := range page.Items {
		detail, err := store.BatchDetail(ctx, item.BatchID, 200)
		if err != nil {
			t.Fatalf("BatchDetail %d: %v", item.BatchID, err)
		}
		if len(detail.Rejections) > 200 {
			t.Errorf("batch %d returned %d rejection items over the cap",
				item.BatchID, len(detail.Rejections))
		}
		for reason, count := range detail.RejectReasons {
			if reason == "" || count < 0 {
				t.Errorf("batch %d has malformed reject reason %q=%d",
					item.BatchID, reason, count)
			}
		}
	}
}

// TestAdminRestaurantDetailNotFound maps a missing primary key to not_found.
func TestAdminRestaurantDetailNotFound(t *testing.T) {
	store := newAdminStore(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := store.RestaurantDetail(ctx, 999_999_999); err == nil {
		t.Fatal("expected an error for a missing restaurant id")
	}
}

// TestAdminRestaurantDetailCarriesActiveFlag proves the detail projection
// carries is_active_for_demo: without it the console's detail badge reads
// inactive for every restaurant that the list marks active.
func TestAdminRestaurantDetailCarriesActiveFlag(t *testing.T) {
	store := newAdminStore(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	active := true
	page, err := store.Restaurants(ctx, admin.RestaurantQuery{Limit: 1, Active: &active})
	if err != nil {
		t.Fatalf("list active restaurants: %v", err)
	}
	if len(page.Items) == 0 {
		t.Skip("database has no demo-active restaurants to compare")
	}

	restaurantID := page.Items[0].RestaurantID
	detail, err := store.RestaurantDetail(ctx, restaurantID)
	if err != nil {
		t.Fatalf("RestaurantDetail %d: %v", restaurantID, err)
	}
	if !detail.ActiveForDemo {
		t.Errorf("restaurant %d is active in the listing but detail carries is_active_for_demo=false",
			restaurantID)
	}
}

// TestAdminKeysetQueriesUseIndexes verifies the keyset statement shapes are
// served from indexes rather than full scans.
func TestAdminKeysetQueriesUseIndexes(t *testing.T) {
	client := newAdminClient(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cases := []struct {
		name      string
		statement string
		arg       any
	}{
		{
			name: "restaurants",
			statement: `SELECT id FROM restaurants
				WHERE id < $1 ORDER BY id DESC LIMIT 26`,
			arg: int64(999_999_999),
		},
		{
			name: "documents",
			statement: `SELECT document_id FROM knowledge_documents
				WHERE document_id < $1 ORDER BY document_id DESC LIMIT 26`,
			arg: int64(999_999_999),
		},
		{
			name: "reviews",
			statement: `SELECT id FROM reviews
				WHERE restaurant_id = $1
				ORDER BY reviewed_at DESC, id DESC LIMIT 26`,
			arg: int64(1),
		},
		{
			name: "batches",
			statement: `SELECT id FROM ingestion_batches
				ORDER BY started_at DESC, id DESC LIMIT 26`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var args []any
			if tc.arg != nil {
				args = []any{tc.arg}
			}
			rows, err := client.Pool().Query(ctx, "EXPLAIN "+tc.statement, args...)
			if err != nil {
				t.Fatalf("explain: %v", err)
			}
			var plan strings.Builder
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					rows.Close()
					t.Fatalf("scan explain row: %v", err)
				}
				plan.WriteString(line)
				plan.WriteByte('\n')
			}
			rows.Close()

			if strings.Contains(plan.String(), "Seq Scan") {
				t.Errorf("%s keyset query performs a sequential scan:\n%s",
					tc.name, plan.String())
			}
		})
	}
}

// boolPtr returns a pointer to value.
func boolPtr(value bool) *bool { return &value }
