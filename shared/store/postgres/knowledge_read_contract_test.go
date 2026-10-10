package postgres_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/store/contract"
	"github.com/zed1995/platepilot/shared/store/postgres"
)

// TestKnowledgeReadContract runs the read-side knowledge contract — the exact
// pool rescore and the digest citation boundary — against the real store.
//
// The seeder goes through the write-side store rather than raw SQL (same
// discipline as the read contract): upsert, embed, activate, in that order,
// because the table refuses to activate a document without a vector and the
// read side must never see a row the pipeline could not have produced.
func TestKnowledgeReadContract(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := postgres.Connect(ctx, postgres.Config{
		DSN: dsn(t), Database: "platepilot",
		ConnectTimeout: 10 * time.Second, Timeout: 30 * time.Second,
	})
	if err != nil {
		requireDatabase(t, "knowledge read contract", err)
		t.SkipNow()
	}
	t.Cleanup(func() { _ = client.Close(context.WithoutCancel(ctx)) })

	restaurants := postgres.NewRestaurantStore(client)
	knowledge := postgres.NewKnowledgeStore(client)
	repo := postgres.NewKnowledgeReadRepository(client)

	contract.RunKnowledgeRead(t, func(t *testing.T) contract.KnowledgeReadHarness {
		seedCtx := context.Background()
		// Each subtest rebuilds the schema: the subtests share one database, and
		// a fixture one leaves behind (an evidence-scope digest row, say) would
		// answer another's reads. The in-memory double is fresh per subtest by
		// construction; this is the same isolation, paid for in milliseconds.
		if err := client.Drop(seedCtx); err != nil {
			t.Fatalf("Drop: %v", err)
		}
		if _, err := client.Migrate(seedCtx); err != nil {
			t.Fatalf("Migrate: %v", err)
		}
		return contract.KnowledgeReadHarness{
			Repo: repo,
			Seed: func(t *testing.T, docs []evidence.KnowledgeDocument) []evidence.KnowledgeDocument {
				t.Helper()
				// knowledge_documents.restaurant_id is a foreign key, so every
				// placeholder id the fixture names becomes a real parent row.
				parents := map[int64]int64{}
				for _, doc := range docs {
					if _, done := parents[doc.RestaurantID]; done {
						continue
					}
					parent := contract.RestaurantFixture(
						"contract-kread-"+strconv.FormatInt(doc.RestaurantID, 10),
						"Knowledge Read Parent "+strconv.FormatInt(doc.RestaurantID, 10),
						time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC))
					if err := restaurants.UpsertRestaurant(seedCtx, parent); err != nil {
						t.Fatalf("UpsertRestaurant: %v", err)
					}
					created, err := restaurants.GetBySourceRecordID(seedCtx, parent.SourceRecordID)
					if err != nil {
						t.Fatalf("GetBySourceRecordID: %v", err)
					}
					parents[doc.RestaurantID] = created.ID
				}
				var activeWanted []bool
				for i := range docs {
					docs[i].RestaurantID = parents[docs[i].RestaurantID]
					// Documents are inserted inactive and go live only once
					// vectored — the same lifecycle the pipeline drives. The
					// fixture's own flag is captured first: after the rewrite
					// below it no longer says what the caller asked for.
					activeWanted = append(activeWanted, docs[i].IsActive)
					docs[i].IsActive = false
				}
				if _, err := knowledge.UpsertDocuments(seedCtx, docs); err != nil {
					t.Fatalf("UpsertDocuments: %v", err)
				}

				// Resolve the assigned document ids by content hash per
				// (restaurant, scope), then embed and activate.
				type groupKey struct {
					restaurantID int64
					scope        evidence.RetrievalScope
				}
				groups := map[groupKey]bool{}
				for _, doc := range docs {
					groups[groupKey{doc.RestaurantID, doc.Scope}] = true
				}
				idByHash := map[string]int64{}
				for key := range groups {
					stored, err := knowledge.ListByRestaurant(seedCtx, key.restaurantID, key.scope)
					if err != nil {
						t.Fatalf("ListByRestaurant: %v", err)
					}
					for _, doc := range stored {
						idByHash[doc.ContentHash] = doc.DocumentID
					}
				}
				var ids []int64
				var vectors [][]float32
				var toActivate []int64
				for i, doc := range docs {
					id, ok := idByHash[doc.ContentHash]
					if !ok {
						t.Fatalf("document %q was not stored", doc.ContentHash)
					}
					if len(doc.Embedding) == 0 {
						continue
					}
					ids = append(ids, id)
					vectors = append(vectors, doc.Embedding)
					if activeWanted[i] {
						toActivate = append(toActivate, id)
					}
				}
				if len(ids) > 0 {
					if _, err := knowledge.SetEmbedding(seedCtx, ids, vectors, "test-model", len(vectors[0])); err != nil {
						t.Fatalf("SetEmbedding: %v", err)
					}
					if len(toActivate) > 0 {
						if _, err := knowledge.ActivateDocuments(seedCtx, toActivate, true); err != nil {
							t.Fatalf("ActivateDocuments: %v", err)
						}
					}
				}

				out := make([]evidence.KnowledgeDocument, len(docs))
				for i, doc := range docs {
					doc.DocumentID = idByHash[doc.ContentHash]
					out[i] = doc
				}
				return out
			},
		}
	})
}
