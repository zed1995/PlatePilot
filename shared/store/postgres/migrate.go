package postgres

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/zed1995/platepilot/shared/domain/errs"
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

// managedTables is every table the migrations create.
//
// The list is explicit rather than "everything in the schema", because Drop is
// destructive and a database shared with anything else must not be collateral.
// The cost of that safety is that a migration adding a table has to add it
// here, which is exactly the edit that gets forgotten — so
// TestDropRemovesEveryMigratedTable asserts the two stay in step.
var managedTables = []string{
	"reservations", "reservation_slots",
	"conversation_candidates",
	"conversation_messages", "conversation_checkpoints", "conversations",
	"user_memories",
	"run_nodes", "tool_calls", "agent_runs",
	"ingestion_rejections", "ingestion_batches",
	"knowledge_documents", "review_summaries", "reviews",
	"restaurants", "boundaries", "schema_migrations",
}

// ManagedTables returns the tables Drop removes. It is exported so the schema
// can be checked against the drop list in a test; nothing in the serving path
// needs it.
func ManagedTables() []string {
	out := make([]string, len(managedTables))
	copy(out, managedTables)
	return out
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
	_, err := c.pool.Exec(ctx,
		"DROP TABLE IF EXISTS "+strings.Join(managedTables, ", ")+" CASCADE")
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

// PendingMigrations returns the embedded migrations this database has not
// recorded, in the order they must be applied.
//
// It is read-only by construction. Applying migrations is the data pipeline's
// job, and a serving process that quietly ran DDL would be a second,
// unreviewed writer of the schema.
//
// A database without a schema_migrations table is not an error here, it is the
// extreme case of "nothing has been applied": to_regclass reports the missing
// relation as NULL rather than raising, so the caller hears "every migration is
// pending" instead of an undefined_table error about the bookkeeping table
// itself.
func (c *Client) PendingMigrations(ctx context.Context) ([]Migration, error) {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	migrations, err := Migrations()
	if err != nil {
		return nil, err
	}

	var tracked bool
	if err := c.pool.QueryRow(ctx,
		"SELECT to_regclass('schema_migrations') IS NOT NULL").Scan(&tracked); err != nil {
		return nil, operationError("postgres: look up schema_migrations", err)
	}
	if !tracked {
		return migrations, nil
	}

	applied, err := c.appliedVersions(ctx)
	if err != nil {
		return nil, err
	}
	pending := make([]Migration, 0, len(migrations))
	for _, m := range migrations {
		if _, ok := applied[m.Version]; !ok {
			pending = append(pending, m)
		}
	}
	return pending, nil
}

// appliedVersions reads the set of recorded migration versions.
func (c *Client) appliedVersions(ctx context.Context) (map[string]struct{}, error) {
	rows, err := c.pool.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, operationError("postgres: read schema_migrations", err)
	}
	defer rows.Close()

	applied := make(map[string]struct{})
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return nil, operationError("postgres: scan schema_migrations", err)
		}
		applied[version] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: read schema_migrations", err)
	}
	return applied, nil
}

// VerifySchema fails when this database is behind the migrations embedded in
// the binary.
//
// It exists because both halves of that mismatch are quiet. The audit path
// never fails a turn — a write error there is logged and the turn carries on —
// so a statement against a column that does not exist yet loses rows and tells
// the user nothing. The read path does surface it, but as a generic upstream
// error, reported while someone is trying to diagnose something else. Refusing
// to start collapses both into one message that names the missing migration and
// the command that applies it.
//
// It deliberately does not check the opposite direction. The bookkeeping table
// records what has been applied, not what the schema contains, so a database
// that is ahead of the binary — a rollback without a schema rollback — passes
// here and is reported at the first write instead, by operationError's
// not_null_violation case.
//
// An unreachable database is not this function's concern either: Connect
// already pinged it, and "reachable but stale" is the fault worth naming
// separately from "unreachable".
func (c *Client) VerifySchema(ctx context.Context) error {
	pending, err := c.PendingMigrations(ctx)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	versions := make([]string, 0, len(pending))
	for _, m := range pending {
		versions = append(versions, m.Version)
	}
	return errs.New(errs.CodeInvalidArgument, fmt.Sprintf(
		"postgres: database %q is behind this binary: %d migration(s) not applied (%s); run `make migrate`",
		c.connectedDatabase(), len(pending), strings.Join(versions, ", ")))
}

// connectedDatabase reports the database the pool actually opened.
//
// It is deliberately not DatabaseName, which returns the configured label: a
// deployment may set that label independently of the DSN, and a message whose
// single job is to say which database is behind must not be able to name a
// different one.
func (c *Client) connectedDatabase() string {
	if c.pool != nil {
		if cfg := c.pool.Config(); cfg != nil && cfg.ConnConfig != nil {
			if name := cfg.ConnConfig.Database; name != "" {
				return name
			}
		}
	}
	return c.database
}
