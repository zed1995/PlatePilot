// Package pipeline owns the data production stages: raw ingestion, curation,
// scoring, knowledge document building, and embedding.
package pipeline

import (
	"fmt"

	"github.com/zed/platepilot/data-pipeline/internal/config"
)

// ConfigSummary summarises the pipeline configuration without touching any
// data. It is safe to run before the database is reachable.
func ConfigSummary(cfg config.Config) string {
	return fmt.Sprintf(
		"data dir: %s (batch=%d, workers=%d)\npostgres: enabled=%t database=%s\nembedding: provider=%q model=%q dimensions=%d batch=%d",
		cfg.Pipeline.DataDir,
		cfg.Pipeline.BatchSize,
		cfg.Pipeline.Workers,
		cfg.Postgres.Enabled(),
		cfg.Postgres.Database,
		cfg.Embedding.Provider,
		cfg.Embedding.Model,
		cfg.Embedding.Dimensions,
		cfg.Embedding.BatchSize(),
	)
}
