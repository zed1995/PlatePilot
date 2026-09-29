// Package pipeline owns the data production stages: raw ingestion, curation,
// scoring, knowledge document building, and embedding.
//
// The write stages (import, migrate, stats, score) are implemented; document
// building and embedding land in M2.
package pipeline

import (
	"context"
	"fmt"

	"github.com/zed/platepilot/data-pipeline/internal/config"
)

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

// ConfigSummary summarises the pipeline configuration without touching any
// data. It is safe to run before Atlas is reachable.
func ConfigSummary(cfg config.Config) string {
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
