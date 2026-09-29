package evidence

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestKnowledgeDocumentJSONRoundTrip(t *testing.T) {
	original := KnowledgeDocument{
		DocumentID:          "doc-1",
		RestaurantID:        "r1",
		Scope:               ScopeEvidence,
		DocType:             DocTypeRestaurantReviewSummary,
		Title:               "Service",
		Content:             "Fast, friendly service.",
		ContentHash:         "sha256:abc",
		Embedding:           []float32{0.1, -0.2, 0.3},
		EmbeddingModel:      "qwen3-embedding:0.6b",
		EmbeddingDimensions: 1024,
		Metadata:            map[string]any{"source": "google_local_2021"},
		SourceRecordIDs:     []string{"gmap-1", "review-9"},
		SnapshotAt:          time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC),
		Version:             1,
		IsActive:            true,
	}
	var decoded KnowledgeDocument
	data, _ := json.Marshal(original)
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(original, decoded) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", decoded, original)
	}
}

func TestToEvidenceCarriesSourceAndSnapshot(t *testing.T) {
	doc := KnowledgeDocument{
		DocumentID:   "doc-1",
		RestaurantID: "r1",
		Scope:        ScopeEvidence,
		DocType:      DocTypeRestaurantReviewSummary,
		Title:        "Service",
		Content:      "Fast service.",
		Metadata:     map[string]any{"source": "google_local_2021"},
		SnapshotAt:   time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC),
	}
	ev := doc.ToEvidence(0.87)

	if ev.EvidenceID != "doc-1" || ev.RestaurantID != "r1" {
		t.Fatalf("identity not preserved: %+v", ev)
	}
	if ev.Source != "google_local_2021" {
		t.Fatalf("source not projected from metadata: %q", ev.Source)
	}
	if ev.SnapshotAt.IsZero() {
		t.Fatal("snapshot time must be preserved for citations")
	}
	if ev.Score != 0.87 {
		t.Fatalf("score = %v, want 0.87", ev.Score)
	}
}
