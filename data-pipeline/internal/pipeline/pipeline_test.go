package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/zed/platepilot/data-pipeline/internal/config"
	sharedcfg "github.com/zed/platepilot/shared/config"
)

func TestStagesReportTheirMilestone(t *testing.T) {
	cfg := config.Config{}
	cases := []struct {
		name      string
		milestone string
		run       func() error
	}{
		{"import", "M1-04", func() error { return Import(context.Background(), cfg) }},
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

func TestReportSummarisesConfiguration(t *testing.T) {
	cfg := config.Config{
		Pipeline: config.PipelineConfig{DataDir: "/data", BatchSize: 10, Workers: 2},
		Mongo:    sharedcfg.MongoConfig{Database: "platepilot"},
		Embedding: sharedcfg.EmbeddingConfig{
			Provider:   "ollama",
			Model:      "qwen3-embedding:0.6b",
			Dimensions: 1024,
		},
	}
	report := Report(cfg)
	for _, want := range []string{"/data", "batch=10", "workers=2", "qwen3-embedding:0.6b", "1024", "platepilot"} {
		if !strings.Contains(report, want) {
			t.Errorf("report %q should contain %q", report, want)
		}
	}
}
