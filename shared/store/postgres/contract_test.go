package postgres_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	sharedcfg "github.com/zed1995/platepilot/shared/config"
	"github.com/zed1995/platepilot/shared/store/contract"
	"github.com/zed1995/platepilot/shared/store/postgres"
)

// defaultTestDSN is the scratch database the suite is allowed to destroy.
//
// It is deliberately a different database from the one the pipeline uses.
// newStores calls client.Drop, which drops every table: pointing the default
// at the development database would mean a routine `go test ./...` silently
// deleted the imported corpus. Making the throwaway name the default means the
// safe path is also the path taken when nothing is configured.
const defaultTestDSN = "postgres://platepilot:platepilot@localhost:55432/platepilot_contract_test?sslmode=disable"

// dsn returns the connection string for the throwaway test database.
//
// The suite needs a real PostgreSQL with pgvector and PostGIS: the adapter's
// whole value is that those extensions do the work, so an in-memory double would
// test nothing that matters. Point PLATEPILOT_TEST_POSTGRES_DSN at a scratch
// database to override; do not point it at a database holding real data.
func dsn(t *testing.T) string {
	t.Helper()
	value := os.Getenv("PLATEPILOT_TEST_POSTGRES_DSN")
	if value == "" {
		value = defaultTestDSN
	}
	return value
}

// newStores returns a migrated, empty database for one subtest.
func newStores(t *testing.T) contract.Stores {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := postgres.Connect(ctx, postgres.Config{
		DSN:            dsn(t),
		Database:       "platepilot",
		ConnectTimeout: 10 * time.Second,
		Timeout:        30 * time.Second,
	})
	if err != nil {
		t.Skipf("PostgreSQL is not reachable (%v); start it with docker compose -f deploy/docker-compose.yml up -d", err)
	}
	t.Cleanup(func() { _ = client.Close(context.WithoutCancel(ctx)) })

	// A fresh schema per subtest keeps the contract assertions independent.
	if err := client.Drop(ctx); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if _, err := client.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return contract.Stores{
		Restaurants:   postgres.NewRestaurantStore(client),
		Reviews:       postgres.NewReviewStore(client),
		Pipeline:      postgres.NewPipelineStore(client),
		Knowledge:     postgres.NewKnowledgeStore(client),
		Runs:          postgres.NewRunRepository(client),
		Conversations: postgres.NewConversationRepository(client),
		Memories:      postgres.NewMemoryRepository(client),
		Reservations:  postgres.NewReservationRepository(client),
	}
}

func TestRestaurantStoreContract(t *testing.T) {
	contract.Run(t, newStores)
}

// The extensions the schema depends on must be present; a database without them
// would fail at migration time with an error that looks like a code bug.
func TestRequiredExtensionsPresent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client, err := postgres.Connect(ctx, postgres.Config{DSN: dsn(t), ConnectTimeout: 10 * time.Second})
	if err != nil {
		t.Skipf("PostgreSQL is not reachable: %v", err)
	}
	defer client.Close(ctx)

	if _, err := client.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	want := map[string]bool{"vector": false, "postgis": false, "pg_trgm": false}
	rows, err := client.Pool().Query(ctx, "SELECT extname FROM pg_extension")
	if err != nil {
		t.Fatalf("query extensions: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if _, ok := want[name]; ok {
			want[name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("extension %q is not installed", name)
		}
	}
}

// Migrate must be idempotent: running it against an already-migrated database
// reports every version as applied and changes nothing.
func TestMigrateIsIdempotent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client, err := postgres.Connect(ctx, postgres.Config{DSN: dsn(t), ConnectTimeout: 10 * time.Second})
	if err != nil {
		t.Skipf("PostgreSQL is not reachable: %v", err)
	}
	defer client.Close(ctx)

	if err := client.Drop(ctx); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	first, err := client.Migrate(ctx)
	if err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("no migrations were applied")
	}
	for _, status := range first {
		if !status.Applied {
			t.Errorf("migration %s reported not applied", status.Version)
		}
	}
	second, err := client.Migrate(ctx)
	if err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if len(second) != len(first) {
		t.Errorf("second run reported %d migrations, first reported %d", len(second), len(first))
	}
	for _, status := range second {
		if status.AppliedAt == nil {
			t.Errorf("migration %s has no applied_at; it was re-applied", status.Version)
		}
	}
}

// Drop names its tables explicitly, which is the safe choice for a destructive
// operation and the fragile one for a schema that grows: a migration adding a
// table leaves Drop silently incomplete, and the contract suite then starts its
// next subtest on the previous one's rows. This pins the two lists together.
func TestDropRemovesEveryMigratedTable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client, err := postgres.Connect(ctx, postgres.Config{DSN: dsn(t), ConnectTimeout: 10 * time.Second})
	if err != nil {
		t.Skipf("PostgreSQL is not reachable: %v", err)
	}
	defer client.Close(ctx)

	if err := client.Drop(ctx); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if _, err := client.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Owned-by-an-extension tables are excluded: PostGIS installs
	// spatial_ref_sys, which the schema did not create and must not drop.
	rows, err := client.Pool().Query(ctx, `
		SELECT c.relname
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema()
		  AND c.relkind IN ('r', 'p')
		  AND NOT EXISTS (
		      SELECT 1 FROM pg_depend d
		      WHERE d.classid = 'pg_class'::regclass
		        AND d.objid = c.oid
		        AND d.deptype = 'e')`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()

	inDatabase := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		inDatabase[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate tables: %v", err)
	}

	declared := make(map[string]bool, len(postgres.ManagedTables()))
	for _, name := range postgres.ManagedTables() {
		declared[name] = true
	}
	for name := range inDatabase {
		if !declared[name] {
			t.Errorf("table %q exists after Migrate but Drop does not remove it: "+
				"add it to managedTables in migrate.go", name)
		}
	}
	for name := range declared {
		if !inDatabase[name] {
			t.Errorf("Drop removes table %q but no migration creates it", name)
		}
	}
	// A schema with no tables at all would make both loops above vacuously
	// pass, so the count is asserted directly.
	if len(inDatabase) == 0 {
		t.Fatal("Migrate created no tables; the schema check above asserted nothing")
	}
}

// The partial HNSW indexes are the load-bearing part of the schema, so their
// existence is asserted rather than assumed.
func TestVectorIndexStrategyIsPresent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client, err := postgres.Connect(ctx, postgres.Config{DSN: dsn(t), ConnectTimeout: 10 * time.Second})
	if err != nil {
		t.Skipf("PostgreSQL is not reachable: %v", err)
	}
	defer client.Close(ctx)

	if _, err := client.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	want := []string{
		"knowledge_documents_hnsw",
		"knowledge_documents_hnsw_manhattan",
		"knowledge_documents_hnsw_brooklyn",
		"knowledge_documents_hnsw_queens",
		"knowledge_documents_hnsw_bronx",
		"knowledge_documents_hnsw_staten_island",
		"restaurants_location_gist",
		"restaurants_source_record_id_key",
		"reviews_restaurant_reviewed",
	}
	rows, err := client.Pool().Query(ctx, "SELECT indexname FROM pg_indexes WHERE schemaname = 'public'")
	if err != nil {
		t.Fatalf("query indexes: %v", err)
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		found[name] = true
	}
	for _, name := range want {
		if !found[name] {
			t.Errorf("index %q is missing", name)
		}
	}
}

// TestContractEnvironment documents how to run the suite.
func TestContractEnvironment(t *testing.T) {
	t.Logf("DSN: %s", dsn(t))
	if _, err := os.Stat(filepath.Join("migrations", "0001_init.sql")); err != nil {
		t.Errorf("embedded migration directory is unreadable: %v", err)
	}
	_ = sharedcfg.PostgresConfig{}
}
