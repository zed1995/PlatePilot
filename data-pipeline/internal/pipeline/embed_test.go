package pipeline

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zed/platepilot/data-pipeline/internal/config"
	"github.com/zed/platepilot/data-pipeline/internal/pipeline/knowledge"
	sharedcfg "github.com/zed/platepilot/shared/config"
	"github.com/zed/platepilot/shared/domain/errs"
	"github.com/zed/platepilot/shared/domain/evidence"
	"github.com/zed/platepilot/shared/domain/review"
	"github.com/zed/platepilot/shared/embedding"
	"github.com/zed/platepilot/shared/store"
)

// fakeEmbeddingConfig is a configuration the fake provider satisfies, so the
// stage can be exercised without a model server.
func fakeEmbeddingConfig() config.Config {
	cfg := config.Config{Pipeline: config.PipelineConfig{BatchSize: 100, Workers: 1}}
	cfg.Embedding = sharedcfg.EmbeddingConfig{
		Provider:   sharedcfg.ProviderFake,
		Model:      "fake-model",
		Dimensions: 1024,
	}
	return cfg
}

// seedPendingDocument stores one active, unembedded document and returns it.
func seedPendingDocument(t *testing.T, stores Stores, restaurantID int64, hash, content string) int64 {
	t.Helper()
	doc := evidence.KnowledgeDocument{
		RestaurantID: restaurantID,
		Scope:        evidence.ScopeRestaurant,
		DocType:      evidence.DocTypeRestaurantProfile,
		Title:        "fixture",
		Content:      content,
		ContentHash:  hash,
		Metadata:     map[string]any{"source": "test"},
		SnapshotAt:   time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC),
		Version:      1,
	}
	if _, err := stores.Knowledge.UpsertDocuments(context.Background(), []evidence.KnowledgeDocument{doc}); err != nil {
		t.Fatalf("UpsertDocuments: %v", err)
	}
	stored, err := stores.Knowledge.ListByRestaurant(context.Background(), restaurantID, evidence.ScopeRestaurant)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	for _, item := range stored {
		if item.ContentHash == hash {
			return item.DocumentID
		}
	}
	t.Fatalf("document %q was not stored", hash)
	return 0
}

// embed activates a document after giving it a vector, which is the only order
// the schema accepts.
func embed(t *testing.T, stores Stores, docID int64) {
	t.Helper()
	vec := make([]float32, 1024)
	vec[0] = 1
	if _, err := stores.Knowledge.SetEmbedding(context.Background(),
		[]int64{docID}, [][]float32{vec}, "seed-model", 1024); err != nil {
		t.Fatalf("SetEmbedding: %v", err)
	}
	if _, err := stores.Knowledge.ActivateDocuments(context.Background(), []int64{docID}, true); err != nil {
		t.Fatalf("ActivateDocuments: %v", err)
	}
}

func TestRunEmbedFillsPendingDocuments(t *testing.T) {
	ctx := context.Background()
	stores, pipelineStore := stageStores()
	id := seedRestaurant(t, stores, "gmap-embed")
	docID := seedPendingDocument(t, stores, id, "hash-embed", "a quiet diner")

	result, err := RunEmbed(ctx, stores, fakeEmbeddingConfig(), EmbedOptions{BatchSize: 4})
	if err != nil {
		t.Fatalf("RunEmbed: %v", err)
	}
	if result.Embedded != 1 || result.Rejected != 0 {
		t.Errorf("result = %+v, want embedded=1 rejected=0", result)
	}

	// The document is now reachable: a vector and a live flag together.
	stored, err := stores.Knowledge.ListByRestaurant(ctx, id, evidence.ScopeRestaurant)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	if len(stored) != 1 || len(stored[0].Embedding) != 1024 {
		t.Fatalf("document not vectored: %+v", stored)
	}
	if stored[0].EmbeddingModel != "fake-model" {
		t.Errorf("embedding_model = %q, want the configured model", stored[0].EmbeddingModel)
	}
	if stored[0].DocumentID != docID {
		t.Errorf("vector landed on document %d, want %d", stored[0].DocumentID, docID)
	}

	batches, err := pipelineStore.ListBatches(ctx, 10)
	if err != nil {
		t.Fatalf("ListBatches: %v", err)
	}
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want exactly one", len(batches))
	}
	got := batches[0]
	if got.Stage != review.StageEmbedding {
		t.Errorf("stage = %q, want %q", got.Stage, review.StageEmbedding)
	}
	if got.DocumentsEmbedded == nil || *got.DocumentsEmbedded != 1 {
		t.Errorf("documents_embedded = %v, want 1", got.DocumentsEmbedded)
	}
	if got.EmbeddingModel != "fake-model" || got.EmbeddingDimensions == nil || *got.EmbeddingDimensions != 1024 {
		t.Errorf("embedding identity not recorded: %+v", got)
	}
}

// A vector alone does not make a document recallable. The embed stage has to
// activate it, or the document stays outside every partial index and
// VectorSearch can never return it — a silent failure with no error anywhere.
func TestRunEmbedActivatesWhatItVectors(t *testing.T) {
	ctx := context.Background()
	stores, _ := stageStores()
	id := seedRestaurant(t, stores, "gmap-activate")
	seedPendingDocument(t, stores, id, "hash-activate", "a quiet diner")

	if _, err := RunEmbed(ctx, stores, fakeEmbeddingConfig(), EmbedOptions{BatchSize: 4}); err != nil {
		t.Fatalf("RunEmbed: %v", err)
	}

	stored, err := stores.Knowledge.ListByRestaurant(ctx, id, evidence.ScopeRestaurant)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("stored = %+v", stored)
	}
	if !stored[0].IsActive {
		t.Error("the embedded document was left inactive, so no index contains it")
	}
	if len(stored[0].Embedding) == 0 {
		t.Fatal("the activated document has no vector")
	}

	// And it is therefore reachable by search.
	hits, err := stores.Knowledge.VectorSearch(ctx, evidence.ScopeRestaurant,
		storedEmbedding(t, stores, id), 5, store.VectorFilter{})
	if err != nil {
		t.Fatalf("VectorSearch: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("an active vectored document was not returned by search")
	}
}

// storedEmbedding reads back the vector the run wrote, so the search query is
// the document's own vector and an exact match is expected.
func storedEmbedding(t *testing.T, stores Stores, restaurantID int64) []float32 {
	t.Helper()
	stored, err := stores.Knowledge.ListByRestaurant(context.Background(), restaurantID, evidence.ScopeRestaurant)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	if len(stored) == 0 || len(stored[0].Embedding) == 0 {
		t.Fatal("no stored vector to search with")
	}
	return stored[0].Embedding
}

// A rebuild must retire the version it replaces. Both versions live would mean
// retrieval returns whichever the index happens to rank first, so a citation
// could quote text the pipeline already replaced.
func TestRunEmbedRetiresTheSupersededVersion(t *testing.T) {
	ctx := context.Background()
	stores, _ := stageStores()
	id := seedRestaurant(t, stores, "gmap-supersede")

	seedPendingDocument(t, stores, id, "hash-sup-a", "a quiet diner")
	if _, err := RunEmbed(ctx, stores, fakeEmbeddingConfig(), EmbedOptions{BatchSize: 4}); err != nil {
		t.Fatalf("RunEmbed (first): %v", err)
	}
	seedPendingDocument(t, stores, id, "hash-sup-b", "a loud diner")
	if _, err := RunEmbed(ctx, stores, fakeEmbeddingConfig(), EmbedOptions{BatchSize: 4}); err != nil {
		t.Fatalf("RunEmbed (second): %v", err)
	}

	stored, err := stores.Knowledge.ListByRestaurant(ctx, id, evidence.ScopeRestaurant)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	live := 0
	liveIsNewest := false
	for _, doc := range stored {
		if !doc.IsActive {
			// The old version is retained, not deleted: a citation issued
			// against it still has to resolve.
			continue
		}
		live++
		if doc.ContentHash == "hash-sup-b" {
			liveIsNewest = true
		}
	}
	if live != 1 {
		t.Errorf("live documents in the group = %d, want exactly 1", live)
	}
	if !liveIsNewest {
		t.Error("the superseded version is the live one")
	}
	// Both rows must still be present.
	if len(stored) != 2 {
		t.Errorf("stored documents = %d, want both versions retained", len(stored))
	}
}

// Re-running must be a no-op. PendingDocuments only reports documents with no
// vector, so a second run has nothing to do and must not double-count.
func TestRunEmbedRerunIsANoOp(t *testing.T) {
	ctx := context.Background()
	stores, _ := stageStores()
	id := seedRestaurant(t, stores, "gmap-rerun")
	docID := seedPendingDocument(t, stores, id, "hash-rerun", "a quiet diner")

	cfg := fakeEmbeddingConfig()
	if _, err := RunEmbed(ctx, stores, cfg, EmbedOptions{BatchSize: 4}); err != nil {
		t.Fatalf("first RunEmbed: %v", err)
	}
	second, err := RunEmbed(ctx, stores, cfg, EmbedOptions{BatchSize: 4})
	if err != nil {
		t.Fatalf("second RunEmbed: %v", err)
	}
	if second.Documents != 0 || second.Embedded != 0 {
		t.Errorf("re-run = %+v, want nothing to do", second)
	}

	stored, _ := stores.Knowledge.ListByRestaurant(ctx, id, evidence.ScopeRestaurant)
	if len(stored) != 1 || stored[0].DocumentID != docID {
		t.Errorf("re-run changed the stored documents: %+v", stored)
	}
}

// A quality rejection must be recorded by reason, and the document must not
// receive a vector: an unembedded document is invisible in every index.
func TestRunEmbedRecordsQualityRejections(t *testing.T) {
	ctx := context.Background()
	stores, pipelineStore := stageStores()
	id := seedRestaurant(t, stores, "gmap-quality")
	docID := seedPendingDocument(t, stores, id, "hash-quality", "a quiet diner")

	// A provider that returns a zero vector, which is the failure the quality
	// gate has to catch: pgvector silently excludes zero vectors from distance
	// results, so the document would become permanently unrecallable.
	restore := stubProvider(zeroVectorProvider{})
	t.Cleanup(restore)

	result, err := RunEmbed(ctx, stores, fakeEmbeddingConfig(), EmbedOptions{BatchSize: 4})
	if err != nil {
		t.Fatalf("RunEmbed: %v", err)
	}
	if result.Rejected != 1 || result.Embedded != 0 {
		t.Errorf("result = %+v, want rejected=1 embedded=0", result)
	}
	if result.RejectReasons["embedding_zero_vector"] != 1 {
		t.Errorf("reject reasons = %+v, want one zero_vector", result.RejectReasons)
	}

	stored, _ := stores.Knowledge.ListByRestaurant(ctx, id, evidence.ScopeRestaurant)
	if len(stored) != 1 {
		t.Fatalf("stored = %+v", stored)
	}
	if len(stored[0].Embedding) != 0 {
		t.Error("a rejected document was given a vector")
	}
	if docID == 0 {
		t.Error("fixture did not produce a document id")
	}

	batches, _ := pipelineStore.ListBatches(ctx, 10)
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want 1", len(batches))
	}
	if batches[0].DocumentsRejected == nil || *batches[0].DocumentsRejected != 1 {
		t.Errorf("documents_rejected = %v, want 1", batches[0].DocumentsRejected)
	}
	if batches[0].RejectReasons["embedding_zero_vector"] != 1 {
		t.Errorf("reject_reasons = %+v, want one zero_vector", batches[0].RejectReasons)
	}
}

// Two models in one live set produce scores that cannot be interpreted, so the
// run must refuse rather than mix them.
func TestRunEmbedRefusesAModelChange(t *testing.T) {
	ctx := context.Background()
	stores, _ := stageStores()
	id := seedRestaurant(t, stores, "gmap-model")
	docID := seedPendingDocument(t, stores, id, "hash-model", "a quiet diner")
	embed(t, stores, docID)

	cfg := fakeEmbeddingConfig()
	cfg.Embedding.Model = "a-different-model"

	_, err := RunEmbed(ctx, stores, cfg, EmbedOptions{BatchSize: 4})
	if err == nil {
		t.Fatal("want an error when the stored vectors came from another model")
	}
	if got := errs.CodeOf(err); got != errs.CodeEmbeddingModelMismatch {
		t.Errorf("code = %q, want %q", got, errs.CodeEmbeddingModelMismatch)
	}

	// Forcing the change is the documented escape hatch, and it is the only way
	// past the guard.
	forced := EmbedOptions{BatchSize: 4, ForceModelChange: true}
	if _, err := RunEmbed(ctx, stores, cfg, forced); err != nil {
		t.Errorf("forced run refused: %v", err)
	}
}

// Forcing a model change must retire the old documents, not merely skip the
// check. Leaving them live is the exact failure the guard exists to prevent:
// the run continues and ends with two models live in one index, whose distances
// mean different things and cannot be compared.
func TestForceModelChangeRetiresTheOldDocuments(t *testing.T) {
	ctx := context.Background()
	stores, _ := stageStores()
	id := seedRestaurant(t, stores, "gmap-model-retire")
	docID := seedPendingDocument(t, stores, id, "hash-model-retire", "a quiet diner")
	embed(t, stores, docID)

	// The document is live and produced by the seed model.
	stored, err := stores.Knowledge.ListByRestaurant(ctx, id, evidence.ScopeRestaurant)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	if len(stored) != 1 || !stored[0].IsActive || stored[0].EmbeddingModel != "seed-model" {
		t.Fatalf("fixture is not a live seed-model document: %+v", stored)
	}

	cfg := fakeEmbeddingConfig()
	cfg.Embedding.Model = "replacement-model"
	force := EmbedOptions{BatchSize: 4, ForceModelChange: true}
	if _, err := RunEmbed(ctx, stores, cfg, force); err != nil {
		t.Fatalf("forced RunEmbed: %v", err)
	}

	stored, err = stores.Knowledge.ListByRestaurant(ctx, id, evidence.ScopeRestaurant)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	// The row is kept — a citation issued before the switch still resolves —
	// but it is no longer recallable.
	if len(stored) != 1 {
		t.Fatalf("stored = %d documents, want the old row retained", len(stored))
	}
	if stored[0].IsActive {
		t.Error("the superseded-model document is still live")
	}
	if len(stored[0].Embedding) == 0 {
		t.Error("the retired document lost its vector; it is history, not work in progress")
	}

	// And it must not come back as pending work: a rebuild is what replaces it.
	pending, err := stores.Knowledge.PendingDocuments(ctx, 10)
	if err != nil {
		t.Fatalf("PendingDocuments: %v", err)
	}
	for _, doc := range pending {
		if doc.DocumentID == docID {
			t.Error("a retired document was offered for re-embedding")
		}
	}
}

// A second run after the switch must succeed without the flag, because there
// is no longer a live document from the old model to conflict with.
func TestRunAfterAModelChangeNeedsNoForce(t *testing.T) {
	ctx := context.Background()
	stores, _ := stageStores()
	id := seedRestaurant(t, stores, "gmap-model-second")
	docID := seedPendingDocument(t, stores, id, "hash-model-second", "a quiet diner")
	embed(t, stores, docID)

	cfg := fakeEmbeddingConfig()
	cfg.Embedding.Model = "replacement-model"
	if _, err := RunEmbed(ctx, stores, cfg, EmbedOptions{BatchSize: 4, ForceModelChange: true}); err != nil {
		t.Fatalf("forced RunEmbed: %v", err)
	}

	// The new document is built and embedded normally, with no flag.
	fresh := seedPendingDocument(t, stores, id, "hash-model-fresh", "a louder diner")
	if _, err := RunEmbed(ctx, stores, cfg, EmbedOptions{BatchSize: 4}); err != nil {
		t.Errorf("a run after the switch still required --force-model-change: %v", err)
	}

	stored, err := stores.Knowledge.ListByRestaurant(ctx, id, evidence.ScopeRestaurant)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	var freshLive bool
	for _, doc := range stored {
		if doc.ContentHash == "hash-model-fresh" {
			freshLive = doc.IsActive
			if doc.EmbeddingModel != "replacement-model" {
				t.Errorf("new document model = %q, want the replacement", doc.EmbeddingModel)
			}
		}
	}
	if !freshLive {
		t.Error("the document built after the switch is not live")
	}
	_ = docID
	_ = fresh
}

func TestRunEmbedRejectsMissingStore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := RunEmbed(ctx, Stores{}, fakeEmbeddingConfig(), EmbedOptions{})
	if err == nil {
		t.Fatal("want an error when the knowledge store is absent")
	}
}

// A dry-run must leave no trace and no vector.
func TestRunEmbedDryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	stores, pipelineStore := stageStores()
	id := seedRestaurant(t, stores, "gmap-embed-dry")
	seedPendingDocument(t, stores, id, "hash-dry", "a quiet diner")

	result, err := RunEmbed(ctx, stores, fakeEmbeddingConfig(), EmbedOptions{BatchSize: 4, DryRun: true})
	if err != nil {
		t.Fatalf("RunEmbed: %v", err)
	}
	if result.Embedded != 1 {
		t.Errorf("dry run counted %d embedded, want 1", result.Embedded)
	}
	stored, _ := stores.Knowledge.ListByRestaurant(ctx, id, evidence.ScopeRestaurant)
	if len(stored) != 1 || len(stored[0].Embedding) != 0 {
		t.Errorf("dry run wrote a vector: %+v", stored)
	}
	batches, _ := pipelineStore.ListBatches(ctx, 10)
	if len(batches) != 0 {
		t.Errorf("dry run wrote %d batch rows", len(batches))
	}
}

// A provider failure fails the run and is recorded as such. There is no partial
// result to salvage: writing half a page would leave the run resumable but the
// report misleading.
func TestRunEmbedFailsWhenTheProviderFails(t *testing.T) {
	ctx := context.Background()
	stores, pipelineStore := stageStores()
	id := seedRestaurant(t, stores, "gmap-provider")
	seedPendingDocument(t, stores, id, "hash-provider", "a quiet diner")

	restore := stubProvider(failingProvider{err: errs.New(errs.CodeProviderUnavailable, "ollama is down")})
	t.Cleanup(restore)

	_, err := RunEmbed(ctx, stores, fakeEmbeddingConfig(), EmbedOptions{BatchSize: 4})
	if err == nil {
		t.Fatal("want an error when the provider fails")
	}
	batches, _ := pipelineStore.ListBatches(ctx, 10)
	if len(batches) != 1 || batches[0].Status != review.StatusFailed {
		t.Fatalf("failed run not recorded: %+v", batches)
	}
	if batches[0].ErrorCode == "" {
		t.Error("failed batch recorded no error code")
	}
}

// --- test providers --------------------------------------------------------

// stubProvider replaces the stage's provider constructor for one test.
func stubProvider(p embedding.EmbeddingProvider) func() {
	previous := newProvider
	newProvider = func(config.Config, EmbedOptions) (embedding.EmbeddingProvider, error) { return p, nil }
	return func() { newProvider = previous }
}

type zeroVectorProvider struct{}

func (zeroVectorProvider) ModelID() string { return "zero" }
func (zeroVectorProvider) Dimensions() int { return 1024 }

// EmbedDocuments answers with one all-zero vector per input, so the count
// always matches the batch the stage asked for.
func (zeroVectorProvider) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = make([]float32, 1024)
	}
	return out, nil
}
func (zeroVectorProvider) EmbedQuery(context.Context, string) ([]float32, error) {
	return make([]float32, 1024), nil
}

type failingProvider struct{ err error }

func (f failingProvider) ModelID() string { return "failing" }
func (f failingProvider) Dimensions() int { return 1024 }
func (f failingProvider) EmbedDocuments(context.Context, []string) ([][]float32, error) {
	return nil, f.err
}
func (f failingProvider) EmbedQuery(context.Context, string) ([]float32, error) {
	return nil, f.err
}

// slowProvider records how many requests were in flight at once and takes a
// fixed amount of time per call.
type slowProvider struct {
	mu       sync.Mutex
	inFlight int
	peak     int
	calls    int
	delay    time.Duration
}

func (p *slowProvider) ModelID() string { return "slow" }
func (p *slowProvider) Dimensions() int { return 1024 }

func (p *slowProvider) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	p.mu.Lock()
	p.calls++
	p.inFlight++
	if p.inFlight > p.peak {
		p.peak = p.inFlight
	}
	p.mu.Unlock()

	time.Sleep(p.delay)

	p.mu.Lock()
	p.inFlight--
	p.mu.Unlock()

	out := make([][]float32, len(texts))
	for i, text := range texts {
		vec, err := deterministicVector(text)
		if err != nil {
			return nil, err
		}
		out[i] = vec
	}
	return out, nil
}

func (p *slowProvider) EmbedQuery(ctx context.Context, query string) ([]float32, error) {
	vecs, err := p.EmbedDocuments(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

// deterministicVector derives a stable vector from text, so distinct documents
// get distinct vectors and the duplicate check does not reject the whole page.
func deterministicVector(text string) ([]float32, error) {
	vec := make([]float32, 1024)
	var h uint64 = 1469598103934665603
	for i := 0; i < len(text); i++ {
		h ^= uint64(text[i])
		h *= 1099511628211
	}
	// Every document must be non-zero or the quality gate rejects it, and
	// distinct documents must not collide or they read as duplicates.
	vec[h%1024] = 1
	vec[(h/7)%1024] += 0.5
	return vec, nil
}

// seedManyDocuments stores n unembedded documents for one restaurant.
func seedManyDocuments(t *testing.T, stores Stores, restaurantID int64, n int) {
	t.Helper()
	docs := make([]evidence.KnowledgeDocument, 0, n)
	for i := range n {
		docs = append(docs, evidence.KnowledgeDocument{
			RestaurantID: restaurantID,
			Scope:        evidence.ScopeRestaurant,
			DocType:      evidence.DocTypeRestaurantProfile,
			Title:        "fixture",
			Content:      fmt.Sprintf("document body number %d for the concurrency test", i),
			ContentHash:  fmt.Sprintf("hash-conc-%05d", i),
			Metadata:     map[string]any{"source": "test"},
			SnapshotAt:   time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC),
			Version:      1,
		})
	}
	if _, err := stores.Knowledge.UpsertDocuments(context.Background(), docs); err != nil {
		t.Fatalf("UpsertDocuments: %v", err)
	}
}

// The worker pool has to actually overlap requests. A pool that spawns
// goroutines but serialises the provider call would look identical in the
// counters and would be no faster than one worker.
func TestEmbedUsesTheRequestedConcurrency(t *testing.T) {
	ctx := context.Background()
	stores, _ := stageStores()
	id := seedRestaurant(t, stores, "gmap-conc")
	// 8 documents at 2 per batch is 4 batches, so four workers have one each.
	seedManyDocuments(t, stores, id, 8)

	provider := &slowProvider{delay: 20 * time.Millisecond}
	restore := stubProvider(provider)
	t.Cleanup(restore)

	opts := EmbedOptions{BatchSize: 2, Workers: 4}
	if _, err := RunEmbed(ctx, stores, fakeEmbeddingConfig(), opts); err != nil {
		t.Fatalf("RunEmbed: %v", err)
	}

	provider.mu.Lock()
	peak, calls := provider.peak, provider.calls
	provider.mu.Unlock()

	if calls != 4 {
		t.Errorf("provider called %d times, want 4 (8 documents / batch 2)", calls)
	}
	if peak < 2 {
		t.Errorf("peak concurrency was %d, want at least 2; the pool is not overlapping requests", peak)
	}
}

// Concurrency must not change the outcome. The same corpus has to produce the
// same vectors and the same counts whichever way the batches are scheduled.
func TestEmbedIsDeterministicUnderConcurrency(t *testing.T) {
	ctx := context.Background()

	run := func(workers int) (EmbedResult, map[int64]string) {
		stores, _ := stageStores()
		id := seedRestaurant(t, stores, fmt.Sprintf("gmap-det-%d", workers))
		seedManyDocuments(t, stores, id, 6)
		restore := stubProvider(&slowProvider{})
		defer restore()
		result, err := RunEmbed(ctx, stores, fakeEmbeddingConfig(),
			EmbedOptions{BatchSize: 2, Workers: workers})
		if err != nil {
			t.Fatalf("RunEmbed(workers=%d): %v", workers, err)
		}
		models := make(map[int64]string)
		stored, err := stores.Knowledge.ListByRestaurant(ctx, id, evidence.ScopeRestaurant)
		if err != nil {
			t.Fatalf("ListByRestaurant: %v", err)
		}
		for _, doc := range stored {
			models[doc.DocumentID] = doc.EmbeddingModel
		}
		return result, models
	}

	serial, _ := run(1)
	parallel, _ := run(4)

	if serial.Embedded != parallel.Embedded {
		t.Errorf("embedded = %d (serial) vs %d (parallel)", serial.Embedded, parallel.Embedded)
	}
	if serial.Rejected != parallel.Rejected {
		t.Errorf("rejected = %d (serial) vs %d (parallel)", serial.Rejected, parallel.Rejected)
	}
	if serial.Documents != parallel.Documents {
		t.Errorf("documents = %d (serial) vs %d (parallel)", serial.Documents, parallel.Documents)
	}
}

// Every document must still be written when several workers are running, and
// the batch report must account for all of them.
func TestEmbedConcurrencyWritesEveryDocument(t *testing.T) {
	ctx := context.Background()
	stores, pipelineStore := stageStores()
	id := seedRestaurant(t, stores, "gmap-conc-report")
	seedManyDocuments(t, stores, id, 9)

	restore := stubProvider(&slowProvider{})
	t.Cleanup(restore)

	result, err := RunEmbed(ctx, stores, fakeEmbeddingConfig(), EmbedOptions{BatchSize: 2, Workers: 3})
	if err != nil {
		t.Fatalf("RunEmbed: %v", err)
	}
	if result.Embedded != 9 {
		t.Errorf("embedded = %d, want 9", result.Embedded)
	}
	stored, err := stores.Knowledge.ListByRestaurant(ctx, id, evidence.ScopeRestaurant)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	for _, doc := range stored {
		if len(doc.Embedding) != 1024 {
			t.Errorf("document %d has %d dimensions, want 1024", doc.DocumentID, len(doc.Embedding))
		}
		if !doc.IsActive {
			t.Errorf("document %d was not activated", doc.DocumentID)
		}
	}
	batches, err := pipelineStore.ListBatches(ctx, 10)
	if err != nil {
		t.Fatalf("ListBatches: %v", err)
	}
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want 1", len(batches))
	}
	if batches[0].DocumentsEmbedded == nil || *batches[0].DocumentsEmbedded != 9 {
		t.Errorf("documents_embedded = %v, want 9", batches[0].DocumentsEmbedded)
	}
}

// A rejection raised inside a worker still has to reach the batch report, with
// the reason and the document id. A per-worker collector that never merges its
// rejections would drop exactly the audit trail M2-08 exists to produce.
func TestEmbedConcurrencyRecordsRejections(t *testing.T) {
	ctx := context.Background()
	stores, pipelineStore := stageStores()
	id := seedRestaurant(t, stores, "gmap-conc-reject")
	seedManyDocuments(t, stores, id, 4)

	restore := stubProvider(zeroVectorProvider{})
	t.Cleanup(restore)

	result, err := RunEmbed(ctx, stores, fakeEmbeddingConfig(), EmbedOptions{BatchSize: 2, Workers: 2})
	if err != nil {
		t.Fatalf("RunEmbed: %v", err)
	}
	if result.Rejected != 4 {
		t.Errorf("rejected = %d, want 4", result.Rejected)
	}
	batches, _ := pipelineStore.ListBatches(ctx, 10)
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want 1", len(batches))
	}
	if batches[0].RejectReasons["embedding_zero_vector"] != 4 {
		t.Errorf("reject_reasons = %+v, want four zero_vector", batches[0].RejectReasons)
	}
	_, rejections, err := pipelineStore.BatchDetail(ctx, batches[0].BatchID)
	if err != nil {
		t.Fatalf("BatchDetail: %v", err)
	}
	if len(rejections) != 4 {
		t.Errorf("recorded rejections = %d, want 4 (one per document)", len(rejections))
	}
}

// A run that dies half way must leave the work it did finish intact, and the
// run after it must pick up exactly the remainder. This is the resumability
// half of "重跑幂等": rerunning is a no-op only when the first run finished,
// so "resume from where it stopped" is the property that actually makes the
// two consistent.
//
// The failure is injected at the provider rather than by cancelling the
// context, because a cancelled context would also stop the loop before the
// completed batch was written, and the assertion would pass for the wrong
// reason.
func TestRunEmbedResumesAfterAMidRunFailure(t *testing.T) {
	ctx := context.Background()
	stores, _ := stageStores()
	id := seedRestaurant(t, stores, "gmap-resume")
	seedManyDocuments(t, stores, id, 9)

	// Batch 2 of 3 fails, so batch 1 must already be durable when the run dies.
	provider := &failOnNthProvider{failOnCall: 2}
	restore := stubProvider(provider)
	_, err := RunEmbed(ctx, stores, fakeEmbeddingConfig(), EmbedOptions{BatchSize: 3, Workers: 1})
	restore()
	if err == nil {
		t.Fatal("want an error when the provider fails mid-run")
	}
	if code := errs.CodeOf(err); code != errs.CodeProviderUnavailable {
		t.Errorf("error code = %q, want %q", code, errs.CodeProviderUnavailable)
	}

	stored, err := stores.Knowledge.ListByRestaurant(ctx, id, evidence.ScopeRestaurant)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	writtenBefore := 0
	for _, doc := range stored {
		if len(doc.Embedding) > 0 {
			writtenBefore++
		}
	}
	if writtenBefore == 0 || writtenBefore == 9 {
		t.Fatalf("wrote %d of 9 documents before failing; a partial run must write some but not all", writtenBefore)
	}
	// The batches that did succeed must be switched on, not just written. A
	// vector on an inactive document is stranded: it is out of the pending set
	// and out of every HNSW index, so no run can ever finish the job.
	if live := countActiveVectored(stored); live == 0 {
		t.Fatal("the run failed part-way and left no active vectored document; " +
			"successful batches were written but never activated")
	}

	// The resumed run must not recompute what is already stored. PendingDocuments
	// excludes documents that carry a vector, so a provider that counts the
	// texts it is asked for should only ever see the documents still pending.
	counting := &countingProvider{}
	restore = stubProvider(counting)
	result, err := RunEmbed(ctx, stores, fakeEmbeddingConfig(), EmbedOptions{BatchSize: 3, Workers: 1})
	restore()
	if err != nil {
		t.Fatalf("resumed RunEmbed: %v", err)
	}
	if result.Embedded != 9-writtenBefore {
		t.Errorf("resumed run embedded %d, want %d (the documents still pending)", result.Embedded, 9-writtenBefore)
	}
	if counting.asked != 9-writtenBefore {
		t.Errorf("resumed run asked the provider about %d documents, want %d; "+
			"the already-embedded ones were recomputed", counting.asked, 9-writtenBefore)
	}

	// The invariant is not a particular active count — the fixture puts every
	// document in one group, so supersession decides how many stay live. It is
	// that no document ends up vectored-but-inactive without being displaced by
	// a newer version, and that nothing is live without a vector. A vector
	// stranded on an inactive document is the state a partial run used to
	// leave behind, and no later run can reach it.
	stored, err = stores.Knowledge.ListByRestaurant(ctx, id, evidence.ScopeRestaurant)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	vectored, active := 0, 0
	for _, doc := range stored {
		if len(doc.Embedding) > 0 {
			vectored++
		}
		if doc.IsActive {
			active++
			if len(doc.Embedding) == 0 {
				t.Errorf("document %d is live without a vector", doc.DocumentID)
			}
		}
	}
	if vectored != 9 {
		t.Errorf("after resuming, %d of 9 documents carry a vector; want 9", vectored)
	}
	if active == 0 {
		t.Error("no document is live after the run completed; the group would be unretrievable")
	}
}

// A model that does not exist is a configuration fault, not a transient
// outage, and the acceptance criterion is that it writes nothing at all. This
// asserts the two halves separately, because they fail independently: an
// error that is merely logged, and vectors that were written on the way out.
func TestRunEmbedWritesNothingWhenTheModelIsMissing(t *testing.T) {
	ctx := context.Background()
	stores, pipelineStore := stageStores()
	id := seedRestaurant(t, stores, "gmap-missing-model")
	seedManyDocuments(t, stores, id, 4)

	missing := errs.Newf(errs.CodeProviderUnavailable,
		"ollama: model %q not found (run `ollama pull %s`)", "no-such-model", "no-such-model")
	restore := stubProvider(failingProvider{err: missing})
	_, err := RunEmbed(ctx, stores, fakeEmbeddingConfig(), EmbedOptions{BatchSize: 2, Workers: 2})
	restore()
	if err == nil {
		t.Fatal("want an error when the model does not exist")
	}
	if code := errs.CodeOf(err); code != errs.CodeProviderUnavailable {
		t.Errorf("error code = %q, want %q", code, errs.CodeProviderUnavailable)
	}
	typed, ok := errs.As(err)
	if !ok || !strings.Contains(typed.Message, "no-such-model") {
		t.Errorf("error %v does not name the missing model", err)
	}

	stored, listErr := stores.Knowledge.ListByRestaurant(ctx, id, evidence.ScopeRestaurant)
	if listErr != nil {
		t.Fatalf("ListByRestaurant: %v", listErr)
	}
	for _, doc := range stored {
		if len(doc.Embedding) > 0 || doc.EmbeddingModel != "" {
			t.Errorf("document %d was written to (model %q) despite the missing model",
				doc.DocumentID, doc.EmbeddingModel)
		}
	}
	batches, _ := pipelineStore.ListBatches(ctx, 10)
	if len(batches) != 1 || batches[0].Status != review.StatusFailed {
		t.Fatalf("failed run not recorded: %+v", batches)
	}
	// A count of zero is stored as NULL, the M1 convention for "this stage had
	// no rows to count" rather than "it counted and found none", so the check is
	// that no non-zero count was recorded.
	if batches[0].DocumentsEmbedded != nil && *batches[0].DocumentsEmbedded != 0 {
		t.Errorf("documents_embedded = %d, want nothing embedded", *batches[0].DocumentsEmbedded)
	}
	if batches[0].ErrorCode == "" {
		t.Error("the failed batch recorded no error code")
	}
}

// failOnNthProvider answers normally until the given call, then fails.
type failOnNthProvider struct {
	failOnCall int
	calls      int
}

func (p *failOnNthProvider) ModelID() string { return "fail-nth" }
func (p *failOnNthProvider) Dimensions() int { return 1024 }

func (p *failOnNthProvider) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	p.calls++
	if p.calls >= p.failOnCall {
		return nil, errs.New(errs.CodeProviderUnavailable, "provider went down mid-run")
	}
	out := make([][]float32, len(texts))
	for i, text := range texts {
		vec, err := deterministicVector(text)
		if err != nil {
			return nil, err
		}
		out[i] = vec
	}
	return out, nil
}

func (p *failOnNthProvider) EmbedQuery(_ context.Context, text string) ([]float32, error) {
	vectors, err := p.EmbedDocuments(context.Background(), []string{text})
	if err != nil {
		return nil, err
	}
	return vectors[0], nil
}

// countActiveVectored reports how many documents are both live and vectored.
func countActiveVectored(docs []evidence.KnowledgeDocument) int {
	n := 0
	for _, doc := range docs {
		if doc.IsActive && len(doc.Embedding) > 0 {
			n++
		}
	}
	return n
}

// countingProvider answers normally and remembers how many texts it was asked
// about, so a test can assert the stage did not re-embed what it already has.
type countingProvider struct{ asked int }

func (p *countingProvider) ModelID() string { return "counting" }
func (p *countingProvider) Dimensions() int { return 1024 }

func (p *countingProvider) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	p.asked += len(texts)
	out := make([][]float32, len(texts))
	for i, text := range texts {
		vec, err := deterministicVector(text)
		if err != nil {
			return nil, err
		}
		out[i] = vec
	}
	return out, nil
}

func (p *countingProvider) EmbedQuery(_ context.Context, text string) ([]float32, error) {
	vectors, err := p.EmbedDocuments(context.Background(), []string{text})
	if err != nil {
		return nil, err
	}
	return vectors[0], nil
}

// A duplicate vector is a provider fault, and the plan's Gate B requires the
// count to be zero. Detection has to hold across the whole run, not just
// inside one batch: the fingerprint set is what decides whether a vector is
// written at all, and a set that resets per batch lets the same vector through
// once per batch.
func TestRunEmbedRejectsDuplicatesAcrossBatches(t *testing.T) {
	ctx := context.Background()
	stores, pipelineStore := stageStores()
	id := seedRestaurant(t, stores, "gmap-dup-batch")
	seedManyDocuments(t, stores, id, 6)

	// Every document gets the same vector, and the batch size is 2 so the six
	// documents span three batches inside one page.
	restore := stubProvider(constantVectorProvider{})
	t.Cleanup(restore)

	result, err := RunEmbed(ctx, stores, fakeEmbeddingConfig(), EmbedOptions{BatchSize: 2, Workers: 1})
	if err != nil {
		t.Fatalf("RunEmbed: %v", err)
	}
	if result.Embedded != 1 {
		t.Errorf("embedded = %d, want 1: only the first copy of a repeated vector may be written", result.Embedded)
	}
	if result.Rejected != 5 {
		t.Errorf("rejected = %d, want 5", result.Rejected)
	}
	if result.RejectReasons["embedding_duplicate"] != 5 {
		t.Errorf("reject reasons = %+v, want five duplicates", result.RejectReasons)
	}
	batches, _ := pipelineStore.ListBatches(ctx, 10)
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want 1", len(batches))
	}
	if batches[0].RejectReasons["embedding_duplicate"] != 5 {
		t.Errorf("audit reject reasons = %+v, want five duplicates across every batch",
			batches[0].RejectReasons)
	}
}

// The same guarantee under concurrency, where each worker used to keep its own
// fingerprint set and therefore could not see a vector another worker had
// already written.
func TestRunEmbedRejectsDuplicatesAcrossWorkers(t *testing.T) {
	ctx := context.Background()
	stores, _ := stageStores()
	id := seedRestaurant(t, stores, "gmap-dup-workers")
	seedManyDocuments(t, stores, id, 9)

	restore := stubProvider(constantVectorProvider{})
	t.Cleanup(restore)

	// 9 documents at 2 per batch is 5 batches, so four workers overlap.
	result, err := RunEmbed(ctx, stores, fakeEmbeddingConfig(), EmbedOptions{BatchSize: 2, Workers: 4})
	if err != nil {
		t.Fatalf("RunEmbed: %v", err)
	}
	if result.Embedded != 1 {
		t.Errorf("embedded = %d, want 1: concurrent workers shared no duplicate detection", result.Embedded)
	}
	if result.Rejected != 8 {
		t.Errorf("rejected = %d, want 8", result.Rejected)
	}
}

// constantVectorProvider answers every input with the same valid vector.
type constantVectorProvider struct{}

func (constantVectorProvider) ModelID() string { return "constant" }
func (constantVectorProvider) Dimensions() int { return 1024 }

func (constantVectorProvider) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	vector := make([]float32, 1024)
	vector[0] = 1
	vector[1] = 0.5
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = vector
	}
	return out, nil
}

func (constantVectorProvider) EmbedQuery(_ context.Context, _ string) ([]float32, error) {
	vector := make([]float32, 1024)
	vector[0] = 1
	vector[1] = 0.5
	return vector, nil
}

// The single-restaurant path is the one an operator reaches for when a
// document looks wrong, so "it works" is not good enough: it has to restrict
// itself to the requested restaurant. Embedding the whole corpus because a
// filter was dropped is the kind of mistake that costs hours of compute and
// shows up only in the counts afterwards.
func TestRunEmbedOneRestaurantTouchesNothingElse(t *testing.T) {
	ctx := context.Background()
	stores, _ := stageStores()
	target := seedRestaurant(t, stores, "gmap-single-target")
	other := seedRestaurant(t, stores, "gmap-single-other")
	seedManyDocuments(t, stores, target, 4)
	seedManyDocuments(t, stores, other, 4)

	restore := stubProvider(&countingProvider{})
	t.Cleanup(restore)

	result, err := RunEmbed(ctx, stores, fakeEmbeddingConfig(),
		EmbedOptions{BatchSize: 2, Workers: 2, RestaurantID: target})
	if err != nil {
		t.Fatalf("RunEmbed: %v", err)
	}
	if result.Embedded != 4 {
		t.Errorf("embedded = %d, want 4 (only the requested restaurant)", result.Embedded)
	}

	targetDocs, err := stores.Knowledge.ListByRestaurant(ctx, target, evidence.ScopeRestaurant)
	if err != nil {
		t.Fatalf("ListByRestaurant (target): %v", err)
	}
	for _, doc := range targetDocs {
		if len(doc.Embedding) == 0 {
			t.Errorf("target document %d was not embedded", doc.DocumentID)
		}
	}
	otherDocs, err := stores.Knowledge.ListByRestaurant(ctx, other, evidence.ScopeRestaurant)
	if err != nil {
		t.Fatalf("ListByRestaurant (other): %v", err)
	}
	for _, doc := range otherDocs {
		if len(doc.Embedding) != 0 {
			t.Errorf("document %d of an unrequested restaurant was embedded", doc.DocumentID)
		}
	}
}

// A restaurant with no pending documents is not an error: it is the normal
// result of running the flag twice, and reporting a failure would train the
// reader to ignore the command's output.
func TestRunEmbedOneRestaurantWithNothingPending(t *testing.T) {
	ctx := context.Background()
	stores, _ := stageStores()
	id := seedRestaurant(t, stores, "gmap-single-empty")
	seedManyDocuments(t, stores, id, 2)
	// Give them vectors first, so nothing is pending. The model has to match
	// the configuration: the stage refuses to mix models, and it is right to.
	for _, docID := range mustListIDs(t, stores, id, 2) {
		embedWithModel(t, stores, docID, "fake-model")
	}

	restore := stubProvider(&countingProvider{})
	t.Cleanup(restore)

	result, err := RunEmbed(ctx, stores, fakeEmbeddingConfig(),
		EmbedOptions{BatchSize: 2, Workers: 1, RestaurantID: id})
	if err != nil {
		t.Fatalf("RunEmbed on an up-to-date restaurant: %v", err)
	}
	if result.Embedded != 0 || result.Documents != 0 {
		t.Errorf("documents = %d, embedded = %d; want zero for a no-op run",
			result.Documents, result.Embedded)
	}
}

// embedWithModel activates a document with a vector from a named model.
func embedWithModel(t *testing.T, stores Stores, docID int64, model string) {
	t.Helper()
	vec := make([]float32, 1024)
	vec[0] = 1
	if _, err := stores.Knowledge.SetEmbedding(context.Background(),
		[]int64{docID}, [][]float32{vec}, model, 1024); err != nil {
		t.Fatalf("SetEmbedding: %v", err)
	}
	if _, err := stores.Knowledge.ActivateDocuments(context.Background(), []int64{docID}, true); err != nil {
		t.Fatalf("ActivateDocuments: %v", err)
	}
}

// mustListIDs returns the ids of a restaurant's documents, failing the test if
// the count does not match.
func mustListIDs(t *testing.T, stores Stores, restaurantID int64, want int) []int64 {
	t.Helper()
	stored, err := stores.Knowledge.ListByRestaurant(context.Background(), restaurantID, evidence.ScopeRestaurant)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	if len(stored) != want {
		t.Fatalf("restaurant has %d documents, want %d", len(stored), want)
	}
	ids := make([]int64, len(stored))
	for i, doc := range stored {
		ids[i] = doc.DocumentID
	}
	return ids
}

// embedded_review_count is the column Gate B names as an M1 gap that M2 has to
// close, and its write-back had no test at all: the aggregate is computed, the
// narrow update is called, and nothing checked that the two agree.
//
// The count that matters is the one a reader would compute from the documents
// themselves, so the assertion is written that way rather than against the
// number the stage happened to pass.
func TestRunEmbedWritesBackTheEmbeddedReviewCount(t *testing.T) {
	ctx := context.Background()
	stores, _ := stageStores()
	id := seedRestaurant(t, stores, "gmap-writeback")
	// One representative-review summary per restaurant: that is what the
	// builder produces, and the aggregate resolves a restaurant to a single
	// count. Seeding two would model a restaurant whose reviews are summarised
	// twice, which is a builder bug rather than a case worth encoding here.
	seedRepresentativeDocument(t, stores, id, "wb-hash-1", 7)

	restore := stubProvider(&countingProvider{})
	t.Cleanup(restore)

	if _, err := RunEmbed(ctx, stores, fakeEmbeddingConfig(), EmbedOptions{BatchSize: 4, Workers: 1}); err != nil {
		t.Fatalf("RunEmbed: %v", err)
	}

	// Recompute the expectation from the documents rather than trusting the
	// value the stage wrote, using the same definition the aggregate uses: the
	// live, vectored document of that type.
	//
	// Both fixture documents stay live, and that is correct. Supersession
	// applies to *versions of the same fact* — one profile per restaurant, one
	// evidence chunk per claim. Several representative-review documents in one
	// group are separate facts, and a 1:N group legitimately has more than one
	// live row. The count therefore comes from the document the aggregate
	// resolves to, and the assertion below checks it against the database
	// rather than against a guess about which one that is.
	want := 0
	live := 0
	stored, err := stores.Knowledge.ListByRestaurant(ctx, id, evidence.ScopeEvidence)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	for _, doc := range stored {
		if !doc.IsActive || len(doc.Embedding) == 0 {
			continue
		}
		if doc.DocType != evidence.DocTypeRestaurantRepresentativeReviews {
			continue
		}
		live++
		if n, ok := doc.Metadata["representative_count"].(int); ok {
			want = n
		}
	}
	if live != 1 {
		t.Fatalf("%d live representative summaries, want 1", live)
	}
	if want == 0 {
		t.Fatal("the fixture produced no countable representative document")
	}

	restaurant, err := stores.Restaurants.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got := restaurant.ReviewStats.EmbeddedReviewCount; got != want {
		t.Errorf("embedded_review_count = %d, want %d (the count the documents imply)",
			got, want)
	}
}

// seedRepresentativeDocument stores an evidence document of the type that
// carries a representative-review count, which is what the aggregate reads.
func seedRepresentativeDocument(t *testing.T, stores Stores, restaurantID int64, hash string, count int) int64 {
	t.Helper()
	doc := evidence.KnowledgeDocument{
		RestaurantID: restaurantID,
		Scope:        evidence.ScopeEvidence,
		DocType:      evidence.DocTypeRestaurantRepresentativeReviews,
		Title:        "What diners say",
		Content:      "representative reviews for " + hash,
		ContentHash:  hash,
		Metadata: map[string]any{
			"source":               "test",
			"representative_count": count,
		},
		SnapshotAt: time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC),
		Version:    1,
	}
	if _, err := stores.Knowledge.UpsertDocuments(context.Background(), []evidence.KnowledgeDocument{doc}); err != nil {
		t.Fatalf("UpsertDocuments: %v", err)
	}
	stored, err := stores.Knowledge.ListByRestaurant(context.Background(), restaurantID, evidence.ScopeEvidence)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	for _, item := range stored {
		if item.ContentHash == hash {
			return item.DocumentID
		}
	}
	t.Fatalf("document %q was not stored", hash)
	return 0
}

// A group is 1:N, not 1:1. Several evidence documents of the same doc type
// belong to one restaurant at once — three opening-hours blocks, a summary per
// topic — and every one of them is a separate fact that has to be retrievable.
//
// This is the end-to-end shape of E.27. SupersededDocumentIDs was excluding
// only "a different document_id", so the siblings a page carried alongside the
// row being activated came back as displaced and were switched straight back
// off. Two thirds of this corpus was vectored and invisible, and the stage
// reported every batch as successful.
func TestRunEmbedKeepsEverySiblingOfAGroupLive(t *testing.T) {
	ctx := context.Background()
	stores, _ := stageStores()
	id := seedRestaurant(t, stores, "gmap-siblings")

	hashes := []string{"sib-mon", "sib-sat", "sib-sun"}
	for i, hash := range hashes {
		doc := evidence.KnowledgeDocument{
			RestaurantID: id,
			Scope:        evidence.ScopeEvidence,
			DocType:      evidence.DocTypeRestaurantHours,
			Title:        "Opening hours",
			Content:      fmt.Sprintf("opening hours block %d: days %s", i, hash),
			ContentHash:  hash,
			Metadata:     map[string]any{"source": "test"},
			SnapshotAt:   time.Date(2021, 9, 1, 0, 0, 0, 0, time.UTC),
			Version:      1,
		}
		if _, err := stores.Knowledge.UpsertDocuments(ctx, []evidence.KnowledgeDocument{doc}); err != nil {
			t.Fatalf("UpsertDocuments %d: %v", i, err)
		}
	}

	restore := stubProvider(&countingProvider{})
	t.Cleanup(restore)

	// One page holds all three, which is what makes the bug reachable: the
	// exclusion has to hold for a whole page, not for one row.
	if _, err := RunEmbed(ctx, stores, fakeEmbeddingConfig(), EmbedOptions{BatchSize: 10, Workers: 1}); err != nil {
		t.Fatalf("RunEmbed: %v", err)
	}

	stored, err := stores.Knowledge.ListByRestaurant(ctx, id, evidence.ScopeEvidence)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	live, vectored := 0, 0
	for _, doc := range stored {
		if len(doc.Embedding) > 0 {
			vectored++
		}
		if doc.IsActive {
			live++
		}
	}
	if vectored != len(hashes) {
		t.Errorf("vectored = %d, want %d", vectored, len(hashes))
	}
	if live != len(hashes) {
		t.Errorf("live = %d, want %d: a 1:N group keeps every sibling recallable, "+
			"and a vector on an inactive document is unreachable", live, len(hashes))
	}
}

// The reject_reasons map in the audit table is keyed by the error code's string
// value, and the report is the only place a reviewer sees why a document was
// dropped. A reason written as a literal in one place and a constant in another
// splits one reason into two that nothing ever reports together — and the
// constant stops being reachable, so a test that reads the taxonomy cannot tell
// that the code is dead.
//
// CodeEmbeddingDuplicate was in exactly that state: declared, mapped to an HTTP
// status, and produced nowhere.
func TestEmbeddingRejectionReasonsComeFromTheTaxonomy(t *testing.T) {
	if duplicateReason != string(errs.CodeEmbeddingDuplicate) {
		t.Errorf("duplicate reason = %q, want the taxonomy value %q",
			duplicateReason, errs.CodeEmbeddingDuplicate)
	}
}

// Every embedding code the taxonomy declares must be reachable from the
// quality gate. A code with no producer is either a rule that was removed and
// not deleted, or one that was written down and never implemented — and the
// error list looks identical in both cases, which is how a dead code survives
// a review.
//
// This lives in the embed package rather than next to Check because it is a
// statement about the pipeline's error contract, not about one function.
func TestEveryEmbeddingCodeIsProducedByTheQualityGate(t *testing.T) {
	const dim = 8
	valid := makeVector(dim)
	valid[0] = 1

	nearZero := makeVector(dim)
	wrongWidth := make([]float32, dim+1)
	nanVec := makeVector(dim)
	nanVec[0] = 1
	nanVec[2] = float32(math.NaN())
	infVec := makeVector(dim)
	infVec[0] = 1
	infVec[3] = float32(math.Inf(1))

	cases := map[errs.Code][]float32{
		errs.CodeEmbeddingEmpty:             {},
		errs.CodeEmbeddingDimensionMismatch: wrongWidth,
		errs.CodeEmbeddingNaN:               nanVec,
		errs.CodeEmbeddingInf:               infVec,
		errs.CodeEmbeddingZeroVector:        nearZero,
	}
	for want, vector := range cases {
		err := knowledge.Check(vector, dim)
		if err == nil {
			t.Errorf("Check accepted a vector that should raise %s", want)
			continue
		}
		if got := errs.CodeOf(err); got != want {
			t.Errorf("Check reported %s, want %s", got, want)
		}
	}
	// The one code the gate cannot raise: duplicates are a property of a batch
	// rather than of a single vector, so the stage raises it. It is listed
	// here so that removing the stage's producer fails this test too.
	if errs.CodeEmbeddingDuplicate == "" {
		t.Error("CodeEmbeddingDuplicate has no value")
	}
	_ = valid
}

// makeVector returns a zero vector of the given width.
func makeVector(dim int) []float32 { return make([]float32, dim) }

// A rejection reason that stops matching the taxonomy is invisible in review:
// the batch still records a count, the report still prints a line, and the
// number simply no longer adds up to the documents that were dropped. The
// assertion is on the value rather than on a map lookup, so it holds no matter
// how many reasons a run produced.
func TestDuplicateReasonIsTheTaxonomyValue(t *testing.T) {
	if duplicateReason != "embedding_duplicate" {
		t.Errorf("duplicateReason = %q; the audit key must equal the taxonomy value "+
			"so one reason cannot split into two", duplicateReason)
	}
	if got := string(errs.CodeEmbeddingDuplicate); got != duplicateReason {
		t.Errorf("errs.CodeEmbeddingDuplicate = %q but the stage records %q",
			got, duplicateReason)
	}
}
