package postgres_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/store/postgres"
)

// connectTestClient opens the throwaway test database, skipping when there is
// none. It shares the DSN helper with the contract suite deliberately: both
// suites want a scratch database they are allowed to destroy, and a second
// environment variable would only be a second thing to forget to set.
func connectTestClient(t *testing.T) *postgres.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := postgres.Connect(ctx, postgres.Config{
		DSN: dsn(t),
		// A label that cannot collide with a real database name, so the
		// assertion about which of the two the guard prints is meaningful even
		// when the test DSN happens to point at a database called platepilot.
		Database:       "configured-label",
		ConnectTimeout: 10 * time.Second,
		Timeout:        30 * time.Second,
	})
	if err != nil {
		t.Skipf("PostgreSQL is not reachable (%v); start it with docker compose -f deploy/docker-compose.yml up -d", err)
	}
	t.Cleanup(func() { _ = client.Close(context.WithoutCancel(ctx)) })
	return client
}

// dsnDatabase returns the database name inside the test DSN.
func dsnDatabase(t *testing.T) string {
	t.Helper()
	parsed, err := url.Parse(dsn(t))
	if err != nil {
		t.Fatalf("parse the test DSN: %v", err)
	}
	name := strings.TrimPrefix(parsed.Path, "/")
	if name == "" {
		t.Fatal("the test DSN carries no database name")
	}
	return name
}

// TestPendingMigrationsTracksTheDatabase walks the three states the startup
// guard has to tell apart, because two of them are one comparison away from each
// other and both are silent at runtime.
//
// The third state is the one that produced this test. A database whose code was
// updated while the migration was not — the bookkeeping row is missing and
// every schema change in that migration is missing with it. Reproducing it by
// deleting the row is exact for what this function reads: PendingMigrations
// answers from schema_migrations, so an unrecorded migration is indistinguishable
// from one that never ran. That is the property being pinned, not the ability to
// detect a hand-edited schema.
func TestPendingMigrationsTracksTheDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	client := connectTestClient(t)

	all, err := postgres.Migrations()
	if err != nil {
		t.Fatalf("Migrations: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("the binary embeds no migrations; the guard would never fire")
	}
	last := all[len(all)-1].Version

	// Leave the shared test database recorded as fully migrated, so a run that
	// fails half way through does not hand the next suite a stale bookkeeping
	// table. The contract suite drops everything it touches, but the row is
	// this test's edit and it puts it back.
	t.Cleanup(func() {
		restore, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = client.Pool().Exec(restore,
			"INSERT INTO schema_migrations (version) VALUES ($1) ON CONFLICT DO NOTHING", last)
	})

	// State 1: no schema at all. The answer is every migration, not an error
	// about the missing bookkeeping table.
	if err := client.Drop(ctx); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	pending, err := client.PendingMigrations(ctx)
	if err != nil {
		t.Fatalf("PendingMigrations on an empty database: %v", err)
	}
	if len(pending) != len(all) {
		t.Errorf("an empty database reports %d pending migrations, want all %d; "+
			"a service pointed at an un-migrated database has to be told so", len(pending), len(all))
	}
	if err := client.VerifySchema(ctx); err == nil {
		t.Error("VerifySchema accepted a database with no schema")
	} else if got := errs.CodeOf(err); got != errs.CodeInvalidArgument {
		t.Errorf("VerifySchema on an empty database reports %q, want %q", got, errs.CodeInvalidArgument)
	}

	// State 2: migrated. Nothing pending, and the guard passes.
	if _, err := client.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pending, err = client.PendingMigrations(ctx)
	if err != nil {
		t.Fatalf("PendingMigrations after Migrate: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("after Migrate %d migrations are still pending: %v", len(pending), pending)
	}
	if err := client.VerifySchema(ctx); err != nil {
		t.Errorf("VerifySchema on a migrated database: %v", err)
	}

	// State 3: behind by exactly one. The guard has to name it — "the schema is
	// stale" without saying which migration is the difference between a
	// diagnosable startup failure and another afternoon of reading logs.
	if _, err := client.Pool().Exec(ctx,
		"DELETE FROM schema_migrations WHERE version = $1", last); err != nil {
		t.Fatalf("delete the bookkeeping row for %s: %v", last, err)
	}
	pending, err = client.PendingMigrations(ctx)
	if err != nil {
		t.Fatalf("PendingMigrations with one row removed: %v", err)
	}
	if len(pending) != 1 || pending[0].Version != last {
		t.Fatalf("one removed bookkeeping row reports pending %v, want exactly [%s]", pending, last)
	}

	err = client.VerifySchema(ctx)
	if err == nil {
		t.Fatal("VerifySchema accepted a database one migration behind; the guard is a no-op")
	}
	if got := errs.CodeOf(err); got != errs.CodeInvalidArgument {
		t.Errorf("VerifySchema reports %q, want %q", got, errs.CodeInvalidArgument)
	}
	message := err.Error()
	for _, want := range []string{last, "make migrate"} {
		if !strings.Contains(message, want) {
			t.Errorf("VerifySchema message %q does not mention %q; the operator is told the "+
				"schema is stale but not which migration is missing or how to apply it", message, want)
		}
	}
	// The message must name the database it reached. Config.Database is a label
	// the deployment sets on its own, so a guard reading that would be able to
	// point the operator at a database it never opened.
	if reached := dsnDatabase(t); !strings.Contains(message, reached) {
		t.Errorf("VerifySchema message %q does not name the database it reached (%s); "+
			"it is reporting the configured label instead", message, reached)
	}
}
