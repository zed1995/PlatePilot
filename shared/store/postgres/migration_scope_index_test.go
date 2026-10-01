package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zed/platepilot/shared/store/postgres"
)

// M3-03 fixed a silent correctness bug by partitioning the vector index by
// retrieval scope as well as borough. These assertions exist so the index set
// cannot regress to the shape that produced empty recalls.
//
// They read the migration files rather than the database: the point is that the
// schema declares what the read path needs, and a database that was migrated
// before the fix would otherwise pass a test that is supposed to catch exactly
// that.
func TestMigration0004PartitionsTheVectorIndexByScope(t *testing.T) {
	migration, err := findMigration("0004_scope_partitioned_hnsw.sql")
	if err != nil {
		t.Fatal(err)
	}

	// Both scopes and all five boroughs, because a recall filtered on either
	// one and not the other is the bug this migration fixes.
	for _, scope := range []string{"restaurant", "evidence"} {
		if !strings.Contains(migration.SQL, "'"+scope+"'") {
			t.Errorf("the migration does not partition by retrieval scope %q; a "+
				"recall scoped to it would compete with the other scope for the "+
				"index's beam", scope)
		}
	}
	for _, borough := range []string{
		"manhattan", "brooklyn", "queens", "bronx", "staten_island",
	} {
		if !strings.Contains(migration.SQL, "'"+borough+"'") {
			t.Errorf("the migration does not partition by borough %q", borough)
		}
	}
	if !strings.Contains(migration.SQL, "retrieval_scope = %L") {
		t.Error("the partial index predicate must carry retrieval_scope; a " +
			"borough-only predicate is the index that returned nothing")
	}
	// Idempotence is a property of the statement, not of how it was first run.
	if strings.Count(migration.SQL, "IF NOT EXISTS") != 1 {
		t.Errorf("the migration must be re-runnable, but IF NOT EXISTS appears "+
			"%d times; it is expected once, on the templated CREATE INDEX",
			strings.Count(migration.SQL, "IF NOT EXISTS"))
	}
}

// The borough-only indexes from 0001 must survive, because an unscoped recall
// has nothing better to use.
func TestMigration0004KeepsTheUnscopedIndexes(t *testing.T) {
	migration, err := findMigration("0004_scope_partitioned_hnsw.sql")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToUpper(migration.SQL), "DROP INDEX") {
		t.Error("0004 must not drop the 0001 indexes: dropping them takes a full " +
			"rebuild under an exclusive lock, and an unscoped recall still needs them")
	}
}

// The migration must actually apply, and a second run must be a no-op.
func TestMigration0004AppliesAndIsIdempotent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	client, err := postgres.Connect(ctx, postgres.Config{
		DSN: dsn(t), Database: "platepilot",
		ConnectTimeout: 10 * time.Second, Timeout: 120 * time.Second,
	})
	if err != nil {
		requireDatabase(t, "M3-03 scope-partitioned index", err)
		t.SkipNow()
	}
	t.Cleanup(func() { _ = client.Close(context.WithoutCancel(ctx)) })

	const version = "0004_scope_partitioned_hnsw.sql"

	first, err := client.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if !migrationApplied(first, version) {
		t.Fatalf("%s did not report as applied", version)
	}

	// A second run must not re-apply it. The runner records what it did, so this
	// is the property a deployment depends on when it re-runs migrations on
	// every boot. Applied is true either way -- it means "the database is on
	// this version", not "this run executed it" -- so the evidence that a run
	// was a no-op is the applied_at read back from the ledger.
	second, err := client.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate (second run): %v", err)
	}
	for _, status := range second {
		if status.Version != version {
			continue
		}
		if !status.Applied {
			t.Fatal("a recorded migration reported not applied")
		}
		if status.AppliedAt == nil {
			t.Fatal("0004 has no applied_at read back from the ledger; the second " +
				"run re-applied it, which rebuilds ten HNSW indexes for nothing")
		}
	}
}

func findMigration(name string) (postgres.Migration, error) {
	migrations, err := postgres.Migrations()
	if err != nil {
		return postgres.Migration{}, err
	}
	for _, migration := range migrations {
		if migration.Version == name {
			return migration, nil
		}
	}
	return postgres.Migration{}, errMigrationMissing(name)
}

func migrationApplied(statuses []postgres.MigrationStatus, version string) bool {
	for _, status := range statuses {
		if status.Version == version {
			return status.Applied
		}
	}
	return false
}

// errMigrationMissing is a distinct error so a test can say which file it wanted
// rather than failing on an empty loop.
type errMigrationMissing string

func (e errMigrationMissing) Error() string { return "migration " + string(e) + " is not registered" }
