package postgres_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zed/platepilot/shared/adapter/repository/contract"
	"github.com/zed/platepilot/shared/adapter/repository/postgres"
	sharedcfg "github.com/zed/platepilot/shared/config"
)

// dsn returns the connection string for the test database, or skips.
//
// The suite needs a real PostgreSQL with pgvector and PostGIS: the adapter's
// whole value is that those extensions do the work, so an in-memory double would
// test nothing that matters. Set PLATEPILOT_TEST_POSTGRES_DSN to point at one
// (the compose service is the intended source).
func dsn(t *testing.T) string {
	t.Helper()
	value := os.Getenv("PLATEPILOT_TEST_POSTGRES_DSN")
	if value == "" {
		value = "postgres://platepilot:platepilot@localhost:55432/platepilot?sslmode=disable"
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
		Restaurants: postgres.NewRestaurantStore(client),
		Reviews:     postgres.NewReviewStore(client),
		Pipeline:    postgres.NewPipelineStore(client),
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
