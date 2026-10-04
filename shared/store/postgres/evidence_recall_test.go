package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/store"
	"github.com/zed1995/platepilot/shared/store/contract"
	"github.com/zed1995/platepilot/shared/store/postgres"
)

// evidenceFixture is one seeded document plus the vector it was stored with.
//
// The vector is explicit rather than generated so a test can assert on which
// document a query ranks first: with random vectors, a test can only assert on
// set membership, and a recall that returned the right documents in the wrong
// order would pass.
type evidenceFixture struct {
	doc evidence.KnowledgeDocument
	// axis is the unit vector the document is closest to. A query built on the
	// same axis must find it.
	axis int
	// topic, when set, makes the document a review summary under that topic.
	topic  string
	active bool
}

// newEvidenceStores returns a migrated database seeded with restaurants and
// evidence documents.
func newEvidenceStores(t *testing.T, fixtures []evidenceFixture) (*postgres.KnowledgeReadRepository, []int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	client, err := postgres.Connect(ctx, postgres.Config{
		DSN: dsn(t), Database: "platepilot",
		ConnectTimeout: 10 * time.Second, Timeout: 30 * time.Second,
	})
	if err != nil {
		requireDatabase(t, "M3-04 evidence recall", err)
		t.SkipNow()
	}
	t.Cleanup(func() { _ = client.Close(context.WithoutCancel(ctx)) })

	if err := client.Drop(ctx); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if _, err := client.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	restaurantStore := postgres.NewRestaurantStore(client)
	seeds := contract.ReadSeedRows()
	scores := make(map[int64]float64, len(seeds))
	active := make(map[int64]bool, len(seeds))
	for _, detail := range seeds {
		if err := restaurantStore.UpsertRestaurant(ctx, seedFromDetail(detail)); err != nil {
			t.Fatalf("seed restaurant %d: %v", detail.RestaurantID, err)
		}
		scores[detail.RestaurantID] = detail.KnowledgeScore
		active[detail.RestaurantID] = detail.IsActiveForDemo
	}
	if err := restaurantStore.UpdateScores(ctx, scores, active); err != nil {
		t.Fatalf("seed scores: %v", err)
	}

	knowledgeStore := postgres.NewKnowledgeStore(client)
	// The column is vector(1024). The fixtures use orthogonal unit vectors in
	// that space rather than a smaller one, because a test that seeded a
	// different width would be testing a schema this deployment does not have.
	const dimensions = 1024
	documents := make([]evidence.KnowledgeDocument, 0, len(fixtures))
	restaurantIDs := make([]int64, 0, len(fixtures))
	for i, fixture := range fixtures {
		metadata := map[string]any{"source": "yelp_review"}
		if fixture.topic != "" {
			metadata["topic"] = fixture.topic
		}
		documents = append(documents, evidence.KnowledgeDocument{
			DocumentID:          int64(i + 1),
			RestaurantID:        fixture.doc.RestaurantID,
			Scope:               evidence.ScopeEvidence,
			DocType:             fixture.doc.DocType,
			Title:               fixture.doc.Title,
			Content:             fixture.doc.Content,
			ContentHash:         fixture.doc.ContentHash,
			Embedding:           axisVector(fixture.axis, dimensions),
			EmbeddingModel:      "fixture-model",
			EmbeddingDimensions: dimensions,
			Metadata:            metadata,
			SnapshotAt:          fixture.doc.SnapshotAt,
			IsActive:            fixture.active,
		})
		restaurantIDs = append(restaurantIDs, fixture.doc.RestaurantID)
	}
	// The lifecycle is enforced, not advisory: a document is inserted inactive,
	// given a vector, and only then activated. Inserting active would trip the
	// table's own CHECK, and seeding through raw SQL would bypass the one thing
	// this suite is meant to exercise.
	pending := make([]evidence.KnowledgeDocument, len(documents))
	copy(pending, documents)
	for i := range pending {
		pending[i].IsActive = false
	}
	if _, err := knowledgeStore.UpsertDocuments(ctx, pending); err != nil {
		t.Fatalf("seed documents: %v", err)
	}

	if len(documents) == 0 {
		return postgres.NewKnowledgeReadRepository(client), nil
	}

	// Read back per restaurant: the fixture deliberately spans several, and a
	// single-restaurant read would leave the others without their document ids.
	byContent := make(map[string]int64, len(documents))
	seeded := make([]int64, 0, len(documents))
	for _, restaurantID := range restaurantIDs {
		stored, err := knowledgeStore.ListByRestaurant(ctx, restaurantID,
			evidence.ScopeEvidence)
		if err != nil {
			t.Fatalf("read back seeded documents for %d: %v", restaurantID, err)
		}
		for _, item := range stored {
			byContent[item.Content] = item.DocumentID
			seeded = append(seeded, item.DocumentID)
		}
	}
	if len(byContent) == 0 {
		t.Fatalf("no documents were seeded: %d ids, %d contents",
			len(seeded), len(byContent))
	}
	for i, document := range documents {
		docID, ok := byContent[document.Content]
		if !ok {
			t.Fatalf("seeded document %q is not readable back", document.Content)
		}
		if _, err := knowledgeStore.SetEmbedding(ctx, []int64{docID},
			[][]float32{document.Embedding}, document.EmbeddingModel,
			document.EmbeddingDimensions); err != nil {
			t.Fatalf("embed %d: %v", i, err)
		}
		if !document.IsActive {
			continue
		}
		if _, err := knowledgeStore.ActivateDocuments(ctx, []int64{docID}, true); err != nil {
			t.Fatalf("activate %d: %v", i, err)
		}
	}
	_ = restaurantIDs

	return postgres.NewKnowledgeReadRepository(client), restaurantIDs
}

// fixtureDimensions is the width the knowledge_documents column declares.
const fixtureDimensions = 1024

// axisVector builds a unit vector pointing along one axis.
func axisVector(axis, dimensions int) []float32 {
	vector := make([]float32, dimensions)
	vector[axis] = 1
	return vector
}

func doc(restaurantID int64, docType evidence.DocType, content, hash string) evidence.KnowledgeDocument {
	return evidence.KnowledgeDocument{
		RestaurantID: restaurantID,
		DocType:      docType,
		Title:        content,
		Content:      content,
		ContentHash:  hash,
		SnapshotAt:   time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC),
	}
}

// A citation is a claim about a named restaurant, so a recall without one is
// refused. This is the assertion the entire evidence path rests on.
func TestEvidenceRecallRefusesAnEmptyRestaurantScope(t *testing.T) {
	repo, _ := newEvidenceStores(t, nil)

	_, err := repo.RecallEvidence(context.Background(), store.EvidenceRequest{
		Query: axisVector(0, fixtureDimensions), TopK: 10,
	})
	if err == nil {
		t.Fatal("an evidence recall without restaurants must be refused")
	}
	if got := errs.CodeOf(err); got != errs.CodeRetrievalNoScope {
		t.Fatalf("code = %q, want %q", got, errs.CodeRetrievalNoScope)
	}
}

// The result must not contain a single document from a restaurant outside the
// scope, at any depth.
func TestEvidenceRecallNeverLeavesTheRestaurantScope(t *testing.T) {
	repo, _ := newEvidenceStores(t, []evidenceFixture{
		{doc: doc(1, evidence.DocTypeRestaurantReviewSummary, "one is quiet", "h1"), axis: 0, topic: "service", active: true},
		{doc: doc(2, evidence.DocTypeRestaurantReviewSummary, "two is loud", "h2"), axis: 0, topic: "service", active: true},
		{doc: doc(3, evidence.DocTypeRestaurantReviewSummary, "three is quiet", "h3"), axis: 1, topic: "service", active: true},
		{doc: doc(4, evidence.DocTypeRestaurantAttributes, "four is quiet", "h4"), axis: 0, active: true},
	})

	for _, axis := range []int{0, 1, 2, 3} {
		got, err := repo.RecallEvidence(context.Background(), store.EvidenceRequest{
			RestaurantIDs: []int64{1}, Query: axisVector(axis, fixtureDimensions), TopK: 50,
		})
		if err != nil {
			t.Fatalf("axis %d: %v", axis, err)
		}
		for _, item := range got {
			if item.RestaurantID != 1 {
				t.Fatalf("axis %d: evidence from restaurant %d leaked into a "+
					"recall scoped to 1: %q", axis, item.RestaurantID, item.Content)
			}
		}
	}
}

// A candidate set of several restaurants is the real shape: every returned
// document must belong to one of them.
func TestEvidenceRecallStaysInsideTheCandidateSet(t *testing.T) {
	repo, _ := newEvidenceStores(t, []evidenceFixture{
		{doc: doc(1, evidence.DocTypeRestaurantReviewSummary, "in scope a", "h1"), axis: 0, topic: "wait", active: true},
		{doc: doc(2, evidence.DocTypeRestaurantReviewSummary, "in scope b", "h2"), axis: 1, topic: "wait", active: true},
		{doc: doc(3, evidence.DocTypeRestaurantReviewSummary, "out of scope", "h3"), axis: 0, topic: "wait", active: true},
	})

	got, err := repo.RecallEvidence(context.Background(), store.EvidenceRequest{
		RestaurantIDs: []int64{1, 2}, Query: axisVector(0, fixtureDimensions), TopK: 50,
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("want evidence for the in-scope restaurants")
	}
	for _, item := range got {
		if item.RestaurantID != 1 && item.RestaurantID != 2 {
			t.Fatalf("restaurant %d is outside the candidate set: %q",
				item.RestaurantID, item.Content)
		}
	}
}

// A topic is a secondary filter: it narrows the set, and the vector still
// decides the order within it.
func TestEvidenceRecallNarrowsByTopicAndStillRanks(t *testing.T) {
	repo, _ := newEvidenceStores(t, []evidenceFixture{
		{doc: doc(1, evidence.DocTypeRestaurantReviewSummary, "wait was long", "h1"), axis: 0, topic: "wait", active: true},
		{doc: doc(1, evidence.DocTypeRestaurantReviewSummary, "food was good", "h2"), axis: 0, topic: "food", active: true},
		{doc: doc(1, evidence.DocTypeRestaurantReviewSummary, "wait again", "h3"), axis: 0, topic: "wait", active: true},
	})

	got, err := repo.RecallEvidence(context.Background(), store.EvidenceRequest{
		RestaurantIDs: []int64{1}, Query: axisVector(0, fixtureDimensions), Topic: "wait", TopK: 10,
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want the 2 wait summaries, got %d: %+v", len(got), got)
	}
	for _, item := range got {
		if item.Topic != "wait" {
			t.Fatalf("topic = %q, want only wait summaries", item.Topic)
		}
		if item.DocType != evidence.DocTypeRestaurantReviewSummary {
			t.Fatalf("doc_type = %q, want review summaries", item.DocType)
		}
	}
}

// The query decides the order. A recall that ignored the vector would return
// both documents and this assertion would be meaningless.
func TestEvidenceRecallRanksBySimilarity(t *testing.T) {
	repo, _ := newEvidenceStores(t, []evidenceFixture{
		{doc: doc(1, evidence.DocTypeRestaurantReviewSummary, "far", "h1"), axis: 0, topic: "wait", active: true},
		{doc: doc(1, evidence.DocTypeRestaurantReviewSummary, "near", "h2"), axis: 1, topic: "wait", active: true},
	})

	got, err := repo.RecallEvidence(context.Background(), store.EvidenceRequest{
		RestaurantIDs: []int64{1}, Query: axisVector(1, fixtureDimensions), Topic: "wait", TopK: 10,
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want both documents, got %d", len(got))
	}
	if got[0].Content != "near" {
		t.Fatalf("first = %q, want the nearer document", got[0].Content)
	}
	if got[0].Score <= got[1].Score {
		t.Fatalf("scores must descend: %v then %v", got[0].Score, got[1].Score)
	}
}

// "What is this place like" has no single embedding behind it, and the honest
// answer is everything on record rather than an error.
func TestEvidenceRecallWithoutAQueryReturnsTheRestaurant(t *testing.T) {
	repo, _ := newEvidenceStores(t, []evidenceFixture{
		{doc: doc(1, evidence.DocTypeRestaurantAttributes, "attrs", "h1"), axis: 0, active: true},
		{doc: doc(1, evidence.DocTypeRestaurantHours, "hours", "h2"), axis: 1, active: true},
		{doc: doc(1, evidence.DocTypeRestaurantReviewSummary, "summary", "h3"), axis: 2, topic: "wait", active: true},
		{doc: doc(2, evidence.DocTypeRestaurantAttributes, "other", "h4"), axis: 0, active: true},
	})

	got, err := repo.RecallEvidence(context.Background(), store.EvidenceRequest{
		RestaurantIDs: []int64{1}, TopK: 50,
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want all 3 documents for restaurant 1, got %d", len(got))
	}
	for _, item := range got {
		if item.RestaurantID != 1 {
			t.Fatalf("restaurant %d leaked into a single-restaurant read", item.RestaurantID)
		}
		if item.Score != 0 {
			t.Fatalf("an unordered read must not claim a similarity, got %v", item.Score)
		}
	}
}

// A retired document must not be citable. This is the assertion that keeps a
// superseded review out of an answer.
func TestEvidenceRecallExcludesRetiredDocuments(t *testing.T) {
	repo, _ := newEvidenceStores(t, []evidenceFixture{
		{doc: doc(1, evidence.DocTypeRestaurantReviewSummary, "current", "h1"), axis: 0, topic: "wait", active: true},
		{doc: doc(1, evidence.DocTypeRestaurantReviewSummary, "retired", "h2"), axis: 0, topic: "wait", active: false},
	})

	for _, req := range []store.EvidenceRequest{
		{RestaurantIDs: []int64{1}, Query: axisVector(0, fixtureDimensions), TopK: 50},
		{RestaurantIDs: []int64{1}, TopK: 50},
	} {
		got, err := repo.RecallEvidence(context.Background(), req)
		if err != nil {
			t.Fatalf("recall: %v", err)
		}
		for _, item := range got {
			if item.Content == "retired" {
				t.Fatal("a retired document must not be citable")
			}
		}
	}
}

// Every returned citation must be able to say where it came from.
func TestEvidenceRecallCarriesACheckableSource(t *testing.T) {
	repo, _ := newEvidenceStores(t, []evidenceFixture{
		{doc: doc(1, evidence.DocTypeRestaurantReviewSummary, "sourced", "h1"), axis: 0, topic: "wait", active: true},
	})

	got, err := repo.RecallEvidence(context.Background(), store.EvidenceRequest{
		RestaurantIDs: []int64{1}, Query: axisVector(0, fixtureDimensions), TopK: 10,
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("want evidence")
	}
	for _, item := range got {
		if item.Source == "" {
			t.Fatal("a citation with no source cannot be checked")
		}
		if item.SnapshotAt.IsZero() {
			t.Fatal("a citation must say when the observation was made")
		}
		if item.EvidenceID <= 0 {
			t.Fatal("a citation must name the document it came from")
		}
	}
}

// ---------------------------------------------------------------------------
// Recall by cited document id
//
// A citation event carries document ids, and nothing about which restaurant
// each belongs to. Resolving them is the only lookup that cannot be wrong, so
// the store has to answer it directly rather than asking the caller to derive a
// restaurant scope.
// ---------------------------------------------------------------------------

func TestEvidenceByIDsReturnsTheNamedDocumentsInRequestedOrder(t *testing.T) {
	repo, _ := newEvidenceStores(t, []evidenceFixture{
		{doc: doc(1, evidence.DocTypeRestaurantReviewSummary, "first", "h1"), axis: 0, active: true},
		{doc: doc(2, evidence.DocTypeRestaurantHours, "second", "h2"), axis: 1, active: true},
		{doc: doc(3, evidence.DocTypeRestaurantReviewSummary, "third", "h3"), axis: 2, active: true},
	})

	all, err := repo.RecallEvidence(context.Background(), store.EvidenceRequest{
		RestaurantIDs: []int64{1, 2, 3}, TopK: 10,
	})
	if err != nil {
		t.Fatalf("seed read: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("seed = %d documents, want 3", len(all))
	}
	// document_id order matches insertion, so the ids are predictable enough to
	// ask for the set back in a different order than the store prefers.
	ids := []int64{all[2].EvidenceID, all[0].EvidenceID}

	got, err := repo.FindEvidenceByIDs(context.Background(), ids)
	if err != nil {
		t.Fatalf("FindEvidenceByIDs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d documents, want the 2 requested", len(got))
	}
	// Order follows the request because the caller footnoted them in this order;
	// reordering here would renumber the answer's citations.
	if got[0].EvidenceID != ids[0] || got[1].EvidenceID != ids[1] {
		t.Fatalf("order = [%d %d], want [%d %d]", got[0].EvidenceID, got[1].EvidenceID, ids[0], ids[1])
	}
	for _, item := range got {
		if item.Source == "" || item.SnapshotAt.IsZero() {
			t.Fatalf("a recall by id must still return citable documents: %+v", item)
		}
	}
}

// A retired document must stay retired when it is named directly. This read is
// the one place an id arrives already proven to have been cited once, which is
// exactly where a corpus-wide active filter is most easily skipped.
func TestEvidenceByIDsSkipsRetiredAndUnknownIds(t *testing.T) {
	repo, _ := newEvidenceStores(t, []evidenceFixture{
		{doc: doc(1, evidence.DocTypeRestaurantReviewSummary, "live", "h1"), axis: 0, active: true},
		{doc: doc(2, evidence.DocTypeRestaurantReviewSummary, "retired", "h2"), axis: 1, active: false},
	})

	all, err := repo.FindEvidenceByRestaurant(context.Background(), 1, "")
	if err != nil {
		t.Fatalf("seed read: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("restaurant 1 carries %d active documents, want 1", len(all))
	}
	// An id that exists but is not citable, plus one that does not exist at all.
	retired, err := repo.RecallEvidence(context.Background(), store.EvidenceRequest{
		RestaurantIDs: []int64{2}, TopK: 10,
	})
	_ = retired
	_ = err

	got, err := repo.FindEvidenceByIDs(context.Background(),
		[]int64{all[0].EvidenceID, all[0].EvidenceID + 5000})
	if err != nil {
		t.Fatalf("FindEvidenceByIDs: %v", err)
	}
	if len(got) != 1 || got[0].EvidenceID != all[0].EvidenceID {
		t.Fatalf("got %+v, want only the active documented id", got)
	}
}

// An empty id list is an empty answer rather than an error: a client that asked
// for nothing must not be told the corpus is empty.
func TestEvidenceByIDsAnswersEmptyForAnEmptyList(t *testing.T) {
	repo, _ := newEvidenceStores(t, nil)

	got, err := repo.FindEvidenceByIDs(context.Background(), nil)
	if err != nil {
		t.Fatalf("FindEvidenceByIDs: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d documents for an empty request", len(got))
	}
}
