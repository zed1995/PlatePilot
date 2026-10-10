package memory_test

import (
	"context"
	"testing"

	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/domain/search"
	"github.com/zed1995/platepilot/shared/store/contract"
	"github.com/zed1995/platepilot/shared/store/memory"
)

// TestMemoryReadStoresSatisfyContract proves the in-memory read side honours the
// same behaviour contract the Postgres adapter must satisfy. Without it the
// double would drift, and every application test built on it would be testing
// the double's behaviour rather than the system's.
func TestMemoryReadStoresSatisfyContract(t *testing.T) {
	contract.RunRead(t, func(t *testing.T) contract.ReadStores {
		repo := memory.NewRestaurantRepository()
		ctx := context.Background()
		for _, row := range contract.ReadSeedRows() {
			if err := repo.Upsert(ctx, row); err != nil {
				t.Fatalf("seed: %v", err)
			}
		}
		return contract.ReadStores{
			Restaurants: repo,
			Knowledge:   memory.NewKnowledgeRepository(),
		}
	})
}

// TestMemoryKnowledgeReadContract runs the read-side knowledge contract — the
// pool rescore and the digest citation boundary — against the in-memory
// repository, so the Postgres adapter has a double to agree with.
func TestMemoryKnowledgeReadContract(t *testing.T) {
	contract.RunKnowledgeRead(t, func(t *testing.T) contract.KnowledgeReadHarness {
		repo := memory.NewKnowledgeRepository()
		next := int64(0)
		return contract.KnowledgeReadHarness{
			Repo: repo,
			Seed: func(t *testing.T, docs []evidence.KnowledgeDocument) []evidence.KnowledgeDocument {
				t.Helper()
				stored := make([]evidence.KnowledgeDocument, len(docs))
				for i, doc := range docs {
					if doc.DocumentID == 0 {
						next++
						doc.DocumentID = next
					}
					stored[i] = doc
				}
				if err := repo.UpsertDocuments(context.Background(), stored); err != nil {
					t.Fatalf("seed: %v", err)
				}
				return stored
			},
		}
	})
}

// The prior is read back per candidate, so GetByID has to carry it. A double
// that returned a zero score here would quietly flatten every fused ranking.
func TestMemoryRestaurantDetailCarriesThePrior(t *testing.T) {
	repo := memory.NewRestaurantRepository()
	row := contract.ReadSeedRows()[0]
	row.KnowledgeScore = 0.77
	if err := repo.Upsert(context.Background(), row); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := repo.GetByID(context.Background(), row.RestaurantID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.KnowledgeScore != 0.77 {
		t.Fatalf("knowledge_score = %v, want 0.77", got.KnowledgeScore)
	}
	if !got.IsActiveForDemo {
		t.Fatal("the demo flag must survive; the corpus filter depends on it")
	}
}

// MatchByText on the double scores by token overlap, not trigram similarity.
// That is enough to keep the contract honest about shape, but the double must
// not claim a similarity scale it does not implement.
func TestMemoryMatchByTextStaysInTheSimilarityRange(t *testing.T) {
	repo := memory.NewRestaurantRepository()
	ctx := context.Background()
	for _, row := range contract.ReadSeedRows() {
		if err := repo.Upsert(ctx, row); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	got, err := repo.MatchByText(ctx, "casa verdi", 10)
	if err != nil {
		t.Fatalf("match: %v", err)
	}
	for _, candidate := range got {
		if candidate.Score < 0 || candidate.Score > 1 {
			t.Fatalf("score %v is outside the documented [0,1] similarity range", candidate.Score)
		}
	}
}

// A candidate recalled by two channels keeps whichever projection carries more
// detail, so a channel that never read the address cannot erase it.
func TestMemoryCandidateProjectionKeepsTheFields(t *testing.T) {
	repo := memory.NewRestaurantRepository()
	ctx := context.Background()
	row := contract.ReadSeedRows()[0]
	if err := repo.Upsert(ctx, row); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := repo.Search(ctx, search.SearchQuery{Filter: search.RestaurantFilter{Borough: "manhattan"}})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("want candidates")
	}
	for _, candidate := range got {
		if candidate.Name == "" || candidate.SnapshotAt.IsZero() {
			t.Fatalf("candidate lost a field the response promises: %+v", candidate)
		}
	}
}
