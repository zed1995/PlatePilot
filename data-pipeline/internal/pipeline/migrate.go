package pipeline

import (
	"context"

	"github.com/zed/platepilot/shared/adapter/repository/mongo"
)

// MigrateOptions controls the migrate command.
type MigrateOptions struct {
	// Drop removes every pipeline collection first. Destructive; local resets only.
	Drop bool
	// IndexesOnly skips collection creation and only ensures indexes.
	IndexesOnly bool
}

// MigrateResult summarises a migrate run.
type MigrateResult struct {
	Database    string
	Dropped     bool
	Collections []mongo.CollectionStatus
	Indexes     []mongo.IndexStatus
}

// Migrate ensures the Atlas schema and indexes. It is idempotent: running it
// twice leaves the cluster unchanged and reports the same collections.
func Migrate(ctx context.Context, client *mongo.Client, opts MigrateOptions) (MigrateResult, error) {
	result := MigrateResult{Database: client.DatabaseName()}
	if opts.Drop {
		if err := client.DropCollections(ctx); err != nil {
			return result, err
		}
		result.Dropped = true
	}
	if !opts.IndexesOnly {
		collections, err := client.EnsureSchema(ctx)
		if err != nil {
			return result, err
		}
		result.Collections = collections
	}
	indexes, err := client.EnsureIndexes(ctx)
	if err != nil {
		return result, err
	}
	result.Indexes = indexes
	return result, nil
}
