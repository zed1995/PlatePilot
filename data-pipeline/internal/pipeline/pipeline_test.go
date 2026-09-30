package pipeline

import (
	"strconv"
	"strings"
	"testing"

	"github.com/zed/platepilot/data-pipeline/internal/config"
	sharedcfg "github.com/zed/platepilot/shared/config"
)

func TestConfigSummarySummarisesConfiguration(t *testing.T) {
	cfg := config.Config{
		Pipeline: config.PipelineConfig{DataDir: "/data", BatchSize: 10, Workers: 2},
		Postgres: sharedcfg.PostgresConfig{Database: "platepilot"},
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

// The embedding batch size is the knob M2-06 says must be tuned against a real
// model, so check-config has to print the value that will actually be used --
// otherwise confirming the setting took effect means running a real embedding
// batch and timing it.
//
// The configured value is 0 here on purpose. Printing the raw field would show
// "batch=0", which is both wrong and worse than showing nothing: an operator
// reads 0 as a bug in the pipeline rather than an unset knob.
func TestConfigSummaryReportsEffectiveEmbeddingBatch(t *testing.T) {
	cfg := config.Config{
		Postgres:  sharedcfg.PostgresConfig{Database: "platepilot"},
		Embedding: sharedcfg.EmbeddingConfig{Provider: "ollama", Model: "m", Dimensions: 1024},
	}
	if got := ConfigSummary(cfg); !strings.Contains(got, "batch="+strconv.Itoa(sharedcfg.DefaultMaxBatch)) {
		t.Errorf("unset batch should report the default %d, got %q", sharedcfg.DefaultMaxBatch, got)
	}
	cfg.Embedding.MaxBatch = 64
	if got := ConfigSummary(cfg); !strings.Contains(got, "batch=64") {
		t.Errorf("configured batch should be reported, got %q", got)
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
