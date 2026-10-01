package pipeline

import (
	"context"

	"github.com/zed/platepilot/shared/store/postgres"
)

// MigrateOptions controls the migrate command.
type MigrateOptions struct {
	// Drop removes every pipeline table first. Destructive; local resets only.
	Drop bool
	// StatusOnly reports the current migration state without applying anything.
	StatusOnly bool
}

// MigrateResult summarises a migrate run.
type MigrateResult struct {
	Database   string
	Dropped    bool
	Migrations []postgres.MigrationStatus
	AppliedNow int
}

// Migrate ensures the PostgreSQL schema. It is idempotent: running it twice
// leaves the database unchanged and reports the same versions.
//
// Schema changes live in embedded SQL migrations rather than in Go DDL, so the
// exact statements that produced a database can be read, reviewed, and diffed.
// Each migration runs in its own transaction together with its bookkeeping row,
// which means a failure leaves the database on the last good version.
func Migrate(ctx context.Context, client *postgres.Client, opts MigrateOptions) (MigrateResult, error) {
	result := MigrateResult{Database: client.DatabaseName()}
	if opts.Drop {
		if err := client.Drop(ctx); err != nil {
			return result, err
		}
		result.Dropped = true
	}
	if opts.StatusOnly {
		// Status is derived from the bookkeeping table, so it is reported by
		// running the migration read path without applying anything.
		migrations, err := postgres.Migrations()
		if err != nil {
			return result, err
		}
		statuses, err := client.MigrationStatuses(ctx, migrations)
		if err != nil {
			return result, err
		}
		result.Migrations = statuses
		return result, nil
	}
	statuses, err := client.Migrate(ctx)
	if err != nil {
		return result, err
	}
	result.Migrations = statuses
	for _, status := range statuses {
		if status.Applied && status.AppliedAt == nil {
			result.AppliedNow++
		}
	}
	return result, nil
}
