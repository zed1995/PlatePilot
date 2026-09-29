package memory

import (
	"context"
	"testing"
	"time"

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/evidence"
)

func knowledgeFixture(id, restaurantID string, scope evidence.RetrievalScope, embedding []float32) evidence.KnowledgeDocument {
	return evidence.KnowledgeDocument{
		DocumentID:   id,
		RestaurantID: restaurantID,
		Scope:        scope,
		DocType:      evidence.DocTypeRestaurantReviewSummary,
		Title:        "Service",
		Content:      "Fast, friendly service.",
		Embedding:    embedding,
		Metadata:     map[string]any{"source": "google_local_2021"},
		SnapshotAt:   time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC),
		Version:      1,
		IsActive:     true,
	}
}

func TestKnowledgeFindEvidenceIsScopedToRestaurantAndScope(t *testing.T) {
	repo := NewKnowledgeRepository()
	ctx := context.Background()
	docs := []evidence.KnowledgeDocument{
		knowledgeFixture("ev-r1", "r1", evidence.ScopeEvidence, []float32{1, 0}),
		knowledgeFixture("profile-r1", "r1", evidence.ScopeRestaurant, []float32{1, 0}),
		knowledgeFixture("ev-r2", "r2", evidence.ScopeEvidence, []float32{1, 0}),
	}
	if err := repo.UpsertDocuments(ctx, docs); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := repo.FindEvidenceByRestaurant(ctx, "r1")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(got) != 1 || got[0].EvidenceID != "ev-r1" {
		t.Fatalf("evidence must be scoped to one restaurant and the evidence scope, got %+v", got)
	}
	if got[0].Source != "google_local_2021" || got[0].SnapshotAt.IsZero() {
		t.Fatalf("evidence must carry source and snapshot: %+v", got[0])
	}
}

func TestKnowledgeInactiveDocumentsAreExcluded(t *testing.T) {
	repo := NewKnowledgeRepository()
	ctx := context.Background()
	doc := knowledgeFixture("ev-1", "r1", evidence.ScopeEvidence, []float32{1, 0})
	doc.IsActive = false
	if err := repo.UpsertDocuments(ctx, []evidence.KnowledgeDocument{doc}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.FindEvidenceByRestaurant(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("inactive documents must not be retrievable, got %+v", got)
	}
}

func TestKnowledgeVectorSearchOrdersBySimilarityAndRespectsScope(t *testing.T) {
	repo := NewKnowledgeRepository()
	ctx := context.Background()
	docs := []evidence.KnowledgeDocument{
		knowledgeFixture("close", "r1", evidence.ScopeEvidence, []float32{1, 0}),
		knowledgeFixture("far", "r1", evidence.ScopeEvidence, []float32{0, 1}),
		knowledgeFixture("other-scope", "r1", evidence.ScopeRestaurant, []float32{1, 0}),
	}
	if err := repo.UpsertDocuments(ctx, docs); err != nil {
		t.Fatal(err)
	}

	got, err := repo.VectorSearch(ctx, evidence.ScopeEvidence, []float32{1, 0}, 10, nil)
	if err != nil {
		t.Fatalf("vector search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("scope filter returned %d docs, want 2", len(got))
	}
	if got[0].EvidenceID != "close" {
		t.Fatalf("closest document should rank first, got %s", got[0].EvidenceID)
	}
	if got[0].Score <= got[1].Score {
		t.Fatalf("scores must be descending: %v", []float64{got[0].Score, got[1].Score})
	}
}

func TestKnowledgeVectorSearchFilters(t *testing.T) {
	repo := NewKnowledgeRepository()
	ctx := context.Background()
	docs := []evidence.KnowledgeDocument{
		knowledgeFixture("a", "r1", evidence.ScopeEvidence, []float32{1, 0}),
		knowledgeFixture("b", "r2", evidence.ScopeEvidence, []float32{1, 0}),
	}
	if err := repo.UpsertDocuments(ctx, docs); err != nil {
		t.Fatal(err)
	}
	got, err := repo.VectorSearch(ctx, evidence.ScopeEvidence, []float32{1, 0}, 10, map[string]any{"restaurant_id": "r2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].EvidenceID != "b" {
		t.Fatalf("restaurant_id filter not applied: %+v", got)
	}
}

func TestKnowledgeVectorSearchValidatesInput(t *testing.T) {
	repo := NewKnowledgeRepository()
	if _, err := repo.VectorSearch(context.Background(), evidence.ScopeEvidence, nil, 5, nil); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("empty query vector should be rejected, got %v", err)
	}
	if _, err := repo.VectorSearch(context.Background(), "bogus", []float32{1}, 5, nil); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("unknown scope should be rejected, got %v", err)
	}
}

func TestKnowledgeUpsertDocumentsRequiresID(t *testing.T) {
	repo := NewKnowledgeRepository()
	err := repo.UpsertDocuments(context.Background(), []evidence.KnowledgeDocument{{RestaurantID: "r1"}})
	if errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("want invalid_argument, got %v", err)
	}
}
