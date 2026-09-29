package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/zed/platepilot/data-pipeline/internal/config"
	sharedcfg "github.com/zed/platepilot/shared/config"
)

func TestRemainingStagesReportTheirMilestone(t *testing.T) {
	cfg := config.Config{}
	cases := []struct {
		name      string
		milestone string
		run       func() error
	}{
		{"build-documents", "M2-03", func() error { return BuildDocuments(context.Background(), cfg) }},
		{"embed", "M2-06", func() error { return Embed(context.Background(), cfg) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatal("stage should not silently succeed before it is implemented")
			}
			if !strings.Contains(err.Error(), tc.name) || !strings.Contains(err.Error(), tc.milestone) {
				t.Fatalf("error %q should name the stage and milestone %s", err, tc.milestone)
			}
		})
	}
}

func TestConfigSummarySummarisesConfiguration(t *testing.T) {
	cfg := config.Config{
		Pipeline: config.PipelineConfig{DataDir: "/data", BatchSize: 10, Workers: 2},
		Mongo:    sharedcfg.MongoConfig{Database: "platepilot"},
		Embedding: sharedcfg.EmbeddingConfig{
			Provider:   "ollama",
			Model:      "qwen3-embedding:0.6b",
			Dimensions: 1024,
		},
	}
	summary := ConfigSummary(cfg)
	for _, want := range []string{"/data", "batch=10", "workers=2", "qwen3-embedding:0.6b", "1024", "platepilot"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary %q should contain %q", summary, want)
		}
	}
}

func TestDefaultImportOptions(t *testing.T) {
	cfg := config.Config{
		Pipeline: config.PipelineConfig{DataDir: "/data", BatchSize: 250, Workers: 4},
	}
	opts := DefaultImportOptions(cfg)
	if opts.Stage != "all" || opts.DataDir != "/data" || opts.BatchSize != 250 {
		t.Fatalf("defaults = %+v", opts)
	}
	// A zero batch size must fall back to a usable default rather than looping.
	opts = DefaultImportOptions(config.Config{})
	if opts.BatchSize <= 0 {
		t.Fatalf("batch size = %d", opts.BatchSize)
	}
}
