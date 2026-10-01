package postgres

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"time"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migration is one SQL file applied in filename order.
type Migration struct {
	Version string
	SQL     string
}

// Migrations returns the embedded migrations in the order they must run.
//
// Files are applied in lexical order and the version is the filename, so
// 0002_ runs after 0001_ and a new migration is added by dropping in a file
// with the next number. Each file is idempotent (IF NOT EXISTS throughout), so
// re-running the whole set on an existing database is harmless.
func Migrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	out := make([]Migration, 0, len(names))
	for _, name := range names {
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", name, err)
		}
		out = append(out, Migration{Version: name, SQL: string(body)})
	}
	return out, nil
}

// MigrationStatus reports the outcome of one migration.
type MigrationStatus struct {
	Version   string
	Applied   bool
	AppliedAt *time.Time
}

// Migrate applies every migration that has not been recorded yet.
//
// Each migration runs in its own transaction together with the bookkeeping row,
// so a failure leaves the database on the last good version rather than half
// way through a schema change.
func (c *Client) Migrate(ctx context.Context) ([]MigrationStatus, error) {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	if _, err := c.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return nil, operationError("postgres: create schema_migrations", err)
	}

	migrations, err := Migrations()
	if err != nil {
		return nil, err
	}
	statuses := make([]MigrationStatus, 0, len(migrations))
	for _, m := range migrations {
		applied, appliedAt, err := c.migrationApplied(ctx, m.Version)
		if err != nil {
			return statuses, err
		}
		if applied {
			statuses = append(statuses, MigrationStatus{Version: m.Version, Applied: true, AppliedAt: appliedAt})
			continue
		}
		if err := c.applyMigration(ctx, m); err != nil {
			return statuses, err
		}
		statuses = append(statuses, MigrationStatus{Version: m.Version, Applied: true})
	}
	return statuses, nil
}

// migrationApplied reports whether a version is already recorded.
func (c *Client) migrationApplied(ctx context.Context, version string) (bool, *time.Time, error) {
	var appliedAt time.Time
	err := c.pool.QueryRow(ctx,
		"SELECT applied_at FROM schema_migrations WHERE version = $1", version).Scan(&appliedAt)
	switch {
	case pgErrNoRows(err):
		return false, nil, nil
	case err != nil:
		return false, nil, operationError("postgres: read schema_migrations", err)
	}
	return true, &appliedAt, nil
}

// applyMigration runs one migration and records it atomically.
func (c *Client) applyMigration(ctx context.Context, m Migration) error {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return operationError("postgres: begin migration "+m.Version, err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx, m.SQL); err != nil {
		return operationError("postgres: apply migration "+m.Version, err)
	}
	if _, err := tx.Exec(ctx,
		"INSERT INTO schema_migrations (version) VALUES ($1) ON CONFLICT DO NOTHING", m.Version); err != nil {
		return operationError("postgres: record migration "+m.Version, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return operationError("postgres: commit migration "+m.Version, err)
	}
	return nil
}

// Drop removes every PlatePilot table. It exists for the contract suite, which
// needs a clean database per subtest, and for local rebuilds. It is not part of
// the migrate command.
func (c *Client) Drop(ctx context.Context) error {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	// CASCADE is required because of the foreign keys between the content and
	// audit tables; RESTRICT would need an ordering that changes every schema
	// edit.
	_, err := c.pool.Exec(ctx, `
		DROP TABLE IF EXISTS
			ingestion_rejections, ingestion_batches,
			knowledge_documents, review_summaries, reviews,
			restaurants, boundaries, schema_migrations
		CASCADE`)
	if err != nil {
		return operationError("postgres: drop tables", err)
	}
	return nil
}

// MigrationStatuses reports which of the given migrations are recorded, without
// applying any of them.
func (c *Client) MigrationStatuses(ctx context.Context, migrations []Migration) ([]MigrationStatus, error) {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	out := make([]MigrationStatus, 0, len(migrations))
	for _, m := range migrations {
		applied, appliedAt, err := c.migrationApplied(ctx, m.Version)
		if err != nil {
			return out, err
		}
		out = append(out, MigrationStatus{Version: m.Version, Applied: applied, AppliedAt: appliedAt})
	}
	return out, nil
}
