// Package pipeline owns the data production stages: raw ingestion, curation,
// knowledge document building, and embedding.
//
// M0 only provides the seam. Each stage is implemented in the milestone noted on
// its function so that main can stay a thin command dispatcher.
package pipeline

import (
	"context"
	"fmt"

	"github.com/zed/platepilot/data-pipeline/internal/config"
)

// Import reads the raw Google Local Meta and Review JSONL files and writes
// curated restaurants and reviews to Atlas.
//
// TODO(M1-04, M1-05, M1-06, M1-07): streaming reader, cleaning, dedup, upserts.
func Import(_ context.Context, _ config.Config) error {
	return notImplemented("import", "M1-04/M1-05")
}

// BuildDocuments builds restaurant-level and evidence-level knowledge documents.
//
// TODO(M2-03, M2-04, M2-05): document builders, hashing, versioning.
func BuildDocuments(_ context.Context, _ config.Config) error {
	return notImplemented("build-documents", "M2-03/M2-04")
}

// Embed generates and writes 1024-dimension vectors for active knowledge
// documents using the configured Ollama embedding model.
//
// TODO(M2-06, M2-07, M2-08): worker pool, batch writes, quality checks.
func Embed(_ context.Context, _ config.Config) error {
	return notImplemented("embed", "M2-06")
}

// Report summarises the pipeline configuration without touching any data. It is
// safe to run before the data pipeline is implemented.
func Report(cfg config.Config) string {
	return fmt.Sprintf(
		"data dir: %s (batch=%d, workers=%d)\nmongo: enabled=%t database=%s\nembedding: provider=%q model=%q dimensions=%d",
		cfg.Pipeline.DataDir,
		cfg.Pipeline.BatchSize,
		cfg.Pipeline.Workers,
		cfg.Mongo.Enabled(),
		cfg.Mongo.Database,
		cfg.Embedding.Provider,
		cfg.Embedding.Model,
		cfg.Embedding.Dimensions,
	)
}

func notImplemented(stage, milestone string) error {
	return fmt.Errorf("%s is not implemented yet; planned in %s", stage, milestone)
}
