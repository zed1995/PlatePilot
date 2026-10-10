package contract

import (
	"context"
	"math"
	"testing"

	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/store"
)

// KnowledgeReadHarness bundles the read-side knowledge repository under test
// with the seeder that fills it.
//
// The seeder is part of the harness because the two adapters seed differently:
// the in-memory repository takes whole documents (embedding included) in one
// upsert, while the Postgres repository has no write path at all and must be
// fed through the write-side store — upsert, embed, activate, in that order,
// because the table refuses to activate a document without a vector.
type KnowledgeReadHarness struct {
	Repo store.KnowledgeRepository
	// Seed stores the documents and returns them as stored, with document ids
	// (and, where the store assigns them, restaurant ids) filled in. Order
	// follows the input, so cases address rows through the ContentHash keys of
	// the returned documents rather than by assuming any id.
	Seed func(t *testing.T, docs []evidence.KnowledgeDocument) []evidence.KnowledgeDocument
}

// KnowledgeReadFactory returns a fresh, empty harness for one subtest.
type KnowledgeReadFactory func(t *testing.T) KnowledgeReadHarness

// RunKnowledgeRead executes the read-side knowledge contract.
//
// Besides the rescore's own behaviour, the suite pins the digest boundary: the
// restaurant-level review digest is ranking fuel, never a citation, and no
// evidence read may ever return one. That assertion lives here rather than in
// an adapter-specific test because the boundary is a property of the read
// ports themselves — a new adapter that passed every other case but leaked
// digests would be confidently wrong about citations.
func RunKnowledgeRead(t *testing.T, newHarness KnowledgeReadFactory) {
	t.Helper()
	t.Run("ScorePoolByEmbedding", func(t *testing.T) { runScorePoolByEmbedding(t, newHarness(t)) })
	t.Run("DigestEvidenceBoundary", func(t *testing.T) { runDigestEvidenceBoundary(t, newHarness(t)) })
}

// Pool fixtures. The vectors live at the corpus width (vector(1024) in the
// table, so a narrower fixture would be rejected by the real store before the
// behaviour under test could run) and are chosen so the distances are
// distinct, hand-checkable, and spread wide enough that an approximate index
// that pruned anything would be visible: query=e0 against e0, 0.6·e0+0.8·e1
// and e1 gives distances 0, 0.4 and 1.
const poolDims = 1024

var (
	poolQuery = unitAt(0, 1)
	poolVecA  = unitAt(0, 1)
	poolVecB  = func() []float32 {
		v := unitAt(0, 0.6)
		v[1] = 0.8
		return v
	}()
	poolVecC   = unitAt(1, 1)
	poolHashes = []string{"pool-a", "pool-b", "pool-c"}
)

// unitAt returns a poolDims-dimensional unit vector with the given weight on
// dimension i.
func unitAt(i int, weight float32) []float32 {
	v := make([]float32, poolDims)
	v[i] = weight
	return v
}

// poolDocs builds one digest per placeholder restaurant id. The ids are
// placeholders: the harness may renumber them, and cases read the stored rows
// back rather than assuming.
func poolDocs(ids ...int64) []evidence.KnowledgeDocument {
	vectors := [][]float32{poolVecA, poolVecB, poolVecC}
	docs := make([]evidence.KnowledgeDocument, 0, len(ids))
	for i, id := range ids {
		doc := knowledgeDoc(id, evidence.DocTypeRestaurantReviewDigest,
			evidence.ScopeRestaurant, poolHashes[i], "digest "+poolHashes[i])
		doc.Embedding = vectors[i]
		doc.IsActive = true
		docs = append(docs, doc)
	}
	return docs
}

func runScorePoolByEmbedding(t *testing.T, h KnowledgeReadHarness) {
	ctx := context.Background()

	// Three restaurants with digests, one restaurant whose digest exists but is
	// inactive, one restaurant with no digest at all, and one evidence-scope
	// row that shares the digest's doc_type to prove the scope is honored.
	stored := h.Seed(t, poolDocs(1, 2, 3))
	byHash := map[string]evidence.KnowledgeDocument{}
	for _, doc := range stored {
		byHash[doc.ContentHash] = doc
	}
	a, b, c := byHash["pool-a"], byHash["pool-b"], byHash["pool-c"]

	inactive := knowledgeDoc(a.RestaurantID, evidence.DocTypeRestaurantReviewDigest,
		evidence.ScopeRestaurant, "pool-a-inactive", "inactive digest")
	inactive.Embedding = unitAt(3, 1)
	h.Seed(t, []evidence.KnowledgeDocument{inactive})

	evidenceRow := knowledgeDoc(a.RestaurantID, evidence.DocTypeRestaurantReviewDigest,
		evidence.ScopeEvidence, "pool-a-evidence-scope", "evidence scope row")
	evidenceRow.Embedding = poolVecA
	evidenceRow.IsActive = true
	h.Seed(t, []evidence.KnowledgeDocument{evidenceRow})

	req := store.ScorePoolRequest{
		Scope:         evidence.ScopeRestaurant,
		Query:         poolQuery,
		RestaurantIDs: []int64{a.RestaurantID, b.RestaurantID, c.RestaurantID, a.RestaurantID + 999},
		DocType:       evidence.DocTypeRestaurantReviewDigest,
	}
	hits, err := h.Repo.ScorePoolByEmbedding(ctx, req)
	if err != nil {
		t.Fatalf("ScorePoolByEmbedding: %v", err)
	}

	// Completeness is the port's reason to exist: the pool named four ids, one
	// of which has no digest, and the answer is exactly the three that do. An
	// approximate walk that post-filtered the id set would silently drop rows
	// here — which is the failure the rescore exists to prevent.
	if len(hits) != 3 {
		t.Fatalf("got %d hits, want exactly one per digest-bearing restaurant", len(hits))
	}
	seen := map[int64]int{}
	for _, hit := range hits {
		seen[hit.RestaurantID]++
	}
	for id := range seen {
		if seen[id] != 1 {
			t.Errorf("restaurant %d returned %d times, want one document per restaurant", id, seen[id])
		}
	}

	// Order is distance ascending, and the distances are the exact cosines the
	// fixture vectors determine — not an approximation of them.
	wantOrder := []evidence.KnowledgeDocument{a, b, c}
	for i, want := range wantOrder {
		if hits[i].RestaurantID != want.RestaurantID {
			t.Errorf("hit %d = restaurant %d, want %d", i, hits[i].RestaurantID, want.RestaurantID)
		}
	}
	wantDistance := map[string]float64{
		"pool-a": 0,
		"pool-b": 0.4, // 1 - 0.6
		"pool-c": 1,
	}
	for _, hit := range hits {
		want, ok := wantDistance[hit.ContentHash]
		if !ok {
			t.Errorf("unexpected content hash %q in result", hit.ContentHash)
			continue
		}
		if math.Abs(hit.Distance-want) > 1e-6 {
			t.Errorf("distance for %s = %v, want %v", hit.ContentHash, hit.Distance, want)
		}
	}

	// The same pool and query return the same list, in the same order.
	first := hits
	again, err := h.Repo.ScorePoolByEmbedding(ctx, req)
	if err != nil {
		t.Fatalf("ScorePoolByEmbedding (second call): %v", err)
	}
	if len(again) != len(first) {
		t.Fatalf("second call returned %d hits, want %d", len(again), len(first))
	}
	for i := range again {
		if again[i].RestaurantID != first[i].RestaurantID || again[i].Distance != first[i].Distance {
			t.Errorf("second call hit %d = (%d, %v), want (%d, %v)",
				i, again[i].RestaurantID, again[i].Distance, first[i].RestaurantID, first[i].Distance)
		}
	}

	// A restaurant whose only digest is inactive is answered as absent, not
	// with the retired row.
	inactiveOnly := knowledgeDoc(b.RestaurantID+50, evidence.DocTypeRestaurantReviewDigest,
		evidence.ScopeRestaurant, "pool-inactive-only", "only version inactive")
	inactiveOnly.Embedding = poolVecA
	h.Seed(t, []evidence.KnowledgeDocument{inactiveOnly})
	hits, err = h.Repo.ScorePoolByEmbedding(ctx, store.ScorePoolRequest{
		Scope:         evidence.ScopeRestaurant,
		Query:         poolQuery,
		RestaurantIDs: []int64{inactiveOnly.RestaurantID},
		DocType:       evidence.DocTypeRestaurantReviewDigest,
	})
	if err != nil {
		t.Fatalf("ScorePoolByEmbedding (inactive only): %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("inactive digest was returned: %d hits", len(hits))
	}

	// Requests the store cannot execute faithfully are refused.
	if _, err := h.Repo.ScorePoolByEmbedding(ctx, store.ScorePoolRequest{
		Scope: evidence.ScopeRestaurant, RestaurantIDs: []int64{a.RestaurantID},
		DocType: evidence.DocTypeRestaurantReviewDigest,
	}); err == nil {
		t.Error("ScorePoolByEmbedding accepted an empty query vector")
	}
	if _, err := h.Repo.ScorePoolByEmbedding(ctx, store.ScorePoolRequest{
		Scope: evidence.ScopeRestaurant, Query: poolQuery,
		DocType: evidence.DocTypeRestaurantReviewDigest,
	}); err == nil {
		t.Error("ScorePoolByEmbedding accepted an empty restaurant set")
	}
	if _, err := h.Repo.ScorePoolByEmbedding(ctx, store.ScorePoolRequest{
		Scope: "nonsense", Query: poolQuery, RestaurantIDs: []int64{a.RestaurantID},
		DocType: evidence.DocTypeRestaurantReviewDigest,
	}); err == nil {
		t.Error("ScorePoolByEmbedding accepted an unknown scope")
	}
	if _, err := h.Repo.ScorePoolByEmbedding(ctx, store.ScorePoolRequest{
		Scope: evidence.ScopeRestaurant, Query: poolQuery, RestaurantIDs: []int64{a.RestaurantID},
	}); err == nil {
		t.Error("ScorePoolByEmbedding accepted an empty doc_type")
	}
}

// runDigestEvidenceBoundary pins the citable boundary: the review digest lives
// in the restaurant scope and can therefore never leak through an evidence
// read, whatever the query asks for.
func runDigestEvidenceBoundary(t *testing.T, h KnowledgeReadHarness) {
	ctx := context.Background()

	digest := knowledgeDoc(1, evidence.DocTypeRestaurantReviewDigest,
		evidence.ScopeRestaurant, "boundary-digest", "digest text")
	digest.Embedding = poolVecA
	digest.IsActive = true
	quotable := knowledgeDoc(1, evidence.DocTypeRestaurantReviewSummary,
		evidence.ScopeEvidence, "boundary-summary", "summary text")
	quotable.Embedding = poolVecA
	quotable.IsActive = true
	stored := h.Seed(t, []evidence.KnowledgeDocument{digest, quotable})
	var digestID, summaryID int64
	for _, doc := range stored {
		if doc.Scope == evidence.ScopeRestaurant {
			digestID = doc.DocumentID
		} else {
			summaryID = doc.DocumentID
		}
	}
	restaurantID := stored[0].RestaurantID

	evidenceDocs, err := h.Repo.FindEvidenceByRestaurant(ctx, restaurantID, "")
	if err != nil {
		t.Fatalf("FindEvidenceByRestaurant: %v", err)
	}
	for _, doc := range evidenceDocs {
		if doc.DocType == evidence.DocTypeRestaurantReviewDigest {
			t.Error("FindEvidenceByRestaurant returned the review digest")
		}
	}
	if len(evidenceDocs) != 1 || evidenceDocs[0].EvidenceID != summaryID {
		t.Errorf("FindEvidenceByRestaurant = %d docs (want exactly the summary), first id %d",
			len(evidenceDocs), firstID(evidenceDocs))
	}

	recalled, err := h.Repo.RecallEvidence(ctx, store.EvidenceRequest{
		RestaurantIDs: []int64{restaurantID},
		Query:         poolQuery,
		TopK:          10,
	})
	if err != nil {
		t.Fatalf("RecallEvidence: %v", err)
	}
	for _, doc := range recalled {
		if doc.DocType == evidence.DocTypeRestaurantReviewDigest {
			t.Error("RecallEvidence returned the review digest")
		}
	}

	byIDs, err := h.Repo.FindEvidenceByIDs(ctx, []int64{digestID, summaryID})
	if err != nil {
		t.Fatalf("FindEvidenceByIDs: %v", err)
	}
	if len(byIDs) != 1 || byIDs[0].EvidenceID != summaryID {
		t.Errorf("FindEvidenceByIDs = %d docs, want only the summary", len(byIDs))
	}
}

func firstID(docs []evidence.Evidence) int64 {
	if len(docs) == 0 {
		return 0
	}
	return docs[0].EvidenceID
}
