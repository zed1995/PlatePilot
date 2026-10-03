package pipeline

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/zed1995/platepilot/shared/domain/evidence"
)

// captureLogs redirects the default logger for the duration of one test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

// Every log line a stage emits has to name its batch.
//
// The batch id is the pipeline's only trace identifier: the audit row is the
// durable record of a run, and a log line without an id cannot be tied back to
// one. A stage that logs counts but no id is unreadable once two runs overlap.
func TestStageLogsCarryTheBatchID(t *testing.T) {
	logs := captureLogs(t)
	ctx := context.Background()
	stores, _ := stageStores()
	id := seedRestaurant(t, stores, "gmap-log-documents")

	if _, err := RunBuildDocuments(ctx, stores, DocumentsOptions{
		RestaurantID: id, BatchSize: 10, Scope: evidence.ScopeRestaurant,
	}); err != nil {
		t.Fatalf("RunBuildDocuments: %v", err)
	}

	lines := logLines(t, logs)
	if len(lines) == 0 {
		t.Fatal("the document stage logged nothing")
	}
	for _, line := range lines {
		if !strings.Contains(line, "batch_id=") {
			t.Errorf("log line without batch_id: %s", line)
		}
	}
	if !strings.Contains(logs.String(), "by_doc_type.restaurant_profile=1") {
		t.Errorf("per-document-type counts are not queryable:\n%s", logs.String())
	}
}

func TestEmbedStageLogsCarryTheBatchID(t *testing.T) {
	logs := captureLogs(t)
	ctx := context.Background()
	stores, _ := stageStores()
	id := seedRestaurant(t, stores, "gmap-log-embed")
	seedPendingDocument(t, stores, id, "hash-log", "a quiet diner")

	if _, err := RunEmbed(ctx, stores, fakeEmbeddingConfig(), EmbedOptions{BatchSize: 4, Workers: 2}); err != nil {
		t.Fatalf("RunEmbed: %v", err)
	}

	lines := logLines(t, logs)
	if len(lines) == 0 {
		t.Fatal("the embedding stage logged nothing")
	}
	for _, line := range lines {
		if !strings.Contains(line, "batch_id=") {
			t.Errorf("log line without batch_id: %s", line)
		}
	}
	// The model and width are what make a run identifiable after the fact, and
	// they are the fields a model-change investigation starts from.
	if !strings.Contains(logs.String(), "model=fake-model") {
		t.Errorf("the embedding model was not logged:\n%s", logs.String())
	}
}

// A vector must never reach a log. 1024 floats per document would make the
// output both enormous and unreadable, and a vector is derived from review text.
func TestStageLogsNeverContainVectors(t *testing.T) {
	logs := captureLogs(t)
	ctx := context.Background()
	stores, _ := stageStores()
	id := seedRestaurant(t, stores, "gmap-log-vectors")
	seedPendingDocument(t, stores, id, "hash-log-vectors", "a quiet diner")

	if _, err := RunEmbed(ctx, stores, fakeEmbeddingConfig(), EmbedOptions{BatchSize: 4}); err != nil {
		t.Fatalf("RunEmbed: %v", err)
	}
	output := logs.String()
	for _, marker := range []string{"embedding=[", "embedding: [", "0.1234"} {
		if strings.Contains(output, marker) {
			t.Errorf("log output contains vector data (%q):\n%s", marker, output)
		}
	}
}

// logLines splits captured output into non-empty lines.
func logLines(t *testing.T, buf *bytes.Buffer) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}
