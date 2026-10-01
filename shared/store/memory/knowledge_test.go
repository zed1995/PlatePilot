package memory

import (
	"context"
	"testing"
	"time"

	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/evidence"
	"github.com/zed/platepilot/shared/store"
)

func knowledgeFixture(id int64, restaurantID int64, scope evidence.RetrievalScope, embedding []float32) evidence.KnowledgeDocument {
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
		knowledgeFixture(1, 1, evidence.ScopeEvidence, []float32{1, 0}),
		knowledgeFixture(2, 1, evidence.ScopeRestaurant, []float32{1, 0}),
		knowledgeFixture(3, 2, evidence.ScopeEvidence, []float32{1, 0}),
	}
	if err := repo.UpsertDocuments(ctx, docs); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := repo.FindEvidenceByRestaurant(ctx, 1, "")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(got) != 1 || got[0].EvidenceID != 1 {
		t.Fatalf("evidence must be scoped to one restaurant and the evidence scope, got %+v", got)
	}
	if got[0].Source != "google_local_2021" || got[0].SnapshotAt.IsZero() {
		t.Fatalf("evidence must carry source and snapshot: %+v", got[0])
	}
}

func TestKnowledgeInactiveDocumentsAreExcluded(t *testing.T) {
	repo := NewKnowledgeRepository()
	ctx := context.Background()
	doc := knowledgeFixture(1, 1, evidence.ScopeEvidence, []float32{1, 0})
	doc.IsActive = false
	if err := repo.UpsertDocuments(ctx, []evidence.KnowledgeDocument{doc}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.FindEvidenceByRestaurant(ctx, 1, "")
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
		knowledgeFixture(1, 1, evidence.ScopeEvidence, []float32{1, 0}),
		knowledgeFixture(2, 1, evidence.ScopeEvidence, []float32{0, 1}),
		knowledgeFixture(3, 1, evidence.ScopeRestaurant, []float32{1, 0}),
	}
	if err := repo.UpsertDocuments(ctx, docs); err != nil {
		t.Fatal(err)
	}

	got, err := repo.VectorSearch(ctx, store.VectorSearchRequest{
		Scope: evidence.ScopeEvidence, Query: []float32{1, 0}, TopK: 10,
	})
	if err != nil {
		t.Fatalf("vector search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("scope filter returned %d docs, want 2", len(got))
	}
	if got[0].DocumentID != 1 {
		t.Fatalf("closest document should rank first, got %d", got[0].DocumentID)
	}
	if got[0].Distance >= got[1].Distance {
		t.Fatalf("distance must be ascending: %v", []float64{got[0].Distance, got[1].Distance})
	}
}

func TestKnowledgeVectorSearchFilters(t *testing.T) {
	repo := NewKnowledgeRepository()
	ctx := context.Background()
	docs := []evidence.KnowledgeDocument{
		knowledgeFixture(1, 1, evidence.ScopeEvidence, []float32{1, 0}),
		knowledgeFixture(2, 2, evidence.ScopeEvidence, []float32{1, 0}),
	}
	if err := repo.UpsertDocuments(ctx, docs); err != nil {
		t.Fatal(err)
	}
	got, err := repo.VectorSearch(ctx, store.VectorSearchRequest{
		Scope: evidence.ScopeEvidence, Query: []float32{1, 0}, TopK: 10,
		RestaurantIDs: []int64{2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].DocumentID != 2 {
		t.Fatalf("RestaurantIDs filter not applied: %+v", got)
	}
}

func TestKnowledgeVectorSearchValidatesInput(t *testing.T) {
	repo := NewKnowledgeRepository()
	if _, err := repo.VectorSearch(context.Background(), store.VectorSearchRequest{
		Scope: evidence.ScopeEvidence, Query: nil, TopK: 5,
	}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("empty query vector should be rejected, got %v", err)
	}
	if _, err := repo.VectorSearch(context.Background(), store.VectorSearchRequest{
		Scope: "bogus", Query: []float32{1}, TopK: 5,
	}); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("unknown scope should be rejected, got %v", err)
	}
}

func TestKnowledgeUpsertDocumentsRequiresID(t *testing.T) {
	repo := NewKnowledgeRepository()
	err := repo.UpsertDocuments(context.Background(), []evidence.KnowledgeDocument{{RestaurantID: 1}})
	if errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("want invalid_argument, got %v", err)
	}
}
