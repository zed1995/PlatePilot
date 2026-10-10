package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zed1995/platepilot/data-pipeline/internal/config"
	"github.com/zed1995/platepilot/data-pipeline/internal/pipeline/knowledge"
	chatport "github.com/zed1995/platepilot/shared/chat"
	"github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/domain/review"
)

// validDigestText clears the output gate: long enough, and free of every
// forbidden pattern.
var validDigestText = strings.Repeat("这家店的汤头浓郁，服务也周到，适合朋友聚餐，周末人多需要等位。", 6)

// fakeDigestChat records what the stage asked for so the cache-hit test can
// assert the model was not called. It picks its answer per request by looking
// at the serialized input bundle, which names the restaurant.
type fakeDigestChat struct {
	mu      sync.Mutex
	calls   int
	err     error
	lastReq chat.ChatRequest
	respond func(req chat.ChatRequest) string
}

func (f *fakeDigestChat) Complete(ctx context.Context, req chat.ChatRequest) (chat.ChatResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastReq = req
	if f.err != nil {
		return chat.ChatResponse{}, f.err
	}
	return chat.ChatResponse{
		Message: chat.ChatMessage{Role: chat.RoleAssistant, Content: f.respond(req)},
	}, nil
}

func (f *fakeDigestChat) Stream(ctx context.Context, req chat.ChatRequest) (chatport.ChatStream, error) {
	return nil, errors.New("fakeDigestChat: streaming is not part of the digest contract")
}

func (f *fakeDigestChat) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// digestTestConfig returns an enabled digest configuration with a rate high
// enough that the limiter never slows a test down.
func digestTestConfig(promptVersion string) config.Config {
	cfg := testConfig()
	cfg.Digest = config.DigestConfig{
		Enabled:       true,
		PromptVersion: promptVersion,
		ChatBaseURL:   "http://digest.test/v1",
		ChatAPIKey:    "test-key",
		ChatModel:     "digest-model-x",
		Timeout:       time.Second,
		Concurrency:   2,
		RateQPS:       10000,
		BatchSize:     10,
		MaxRetries:    0,
	}
	return cfg
}

// useFakeChatProvider swaps the provider constructor for the duration of one
// test and restores it afterwards, so the other tests in the package are not
// affected by the substitution.
func useFakeChatProvider(t *testing.T, fake *fakeDigestChat) {
	t.Helper()
	previous := newChatProvider
	newChatProvider = func(config.Config) (chatport.ChatProvider, error) { return fake, nil }
	t.Cleanup(func() { newChatProvider = previous })
}

func digestOptions() DigestOptions {
	return DigestOptions{BatchSize: 10}
}

// The stage is exported for embedders; a caller that bypasses the CLI gate
// must still be refused, or a disabled stage would silently spend API budget.
func TestRunBuildDigestsRequiresEnabled(t *testing.T) {
	ctx := context.Background()
	stores, pipelineStore := stageStores()
	cfg := testConfig()
	cfg.Digest.PromptVersion = knowledge.DigestLLMVersion

	if _, err := RunBuildDigests(ctx, stores, cfg, digestOptions()); err == nil {
		t.Fatal("want an error when DIGEST_ENABLED is false")
	}
	batches, _ := pipelineStore.ListBatches(ctx, 10)
	if len(batches) != 0 {
		t.Errorf("a refused run wrote %d batch rows", len(batches))
	}
}

// The rules baseline is the zero-model path: it must produce and store a
// digest without ever constructing a provider, and a re-run must be a pure
// cache skip.
func TestRunBuildDigestsRulesVersionNeverCallsModel(t *testing.T) {
	ctx := context.Background()
	stores, _ := stageStores()
	id := seedRestaurant(t, stores, "gmap-digest-rules")
	seedReviews(t, stores, id, 40)

	var providerCalls int
	previous := newChatProvider
	newChatProvider = func(config.Config) (chatport.ChatProvider, error) {
		providerCalls++
		return nil, errors.New("rules version must not build a chat provider")
	}
	t.Cleanup(func() { newChatProvider = previous })

	cfg := digestTestConfig(knowledge.DigestRulesVersion)
	cfg.Digest.ChatBaseURL = ""
	cfg.Digest.ChatAPIKey = ""
	cfg.Digest.ChatModel = ""
	first, err := RunBuildDigests(ctx, stores, cfg, digestOptions())
	if err != nil {
		t.Fatalf("RunBuildDigests: %v", err)
	}
	if first.Inserted != 1 || first.Restaurants != 1 {
		t.Errorf("first run = %+v, want one restaurant and one insert", first)
	}
	if providerCalls != 0 {
		t.Fatalf("rules version constructed %d chat providers", providerCalls)
	}

	docs, err := stores.Knowledge.ListByRestaurant(ctx, id, evidence.ScopeRestaurant)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	var digest *evidence.KnowledgeDocument
	for i, doc := range docs {
		if doc.DocType == evidence.DocTypeRestaurantReviewDigest {
			digest = &docs[i]
		}
	}
	if digest == nil {
		t.Fatal("no digest document was stored")
	}
	if digest.Metadata["prompt_version"] != knowledge.DigestRulesVersion {
		t.Errorf("prompt_version = %v, want %q", digest.Metadata["prompt_version"], knowledge.DigestRulesVersion)
	}
	if digest.Metadata["model_id"] != "" {
		t.Errorf("rules digest recorded a model id: %v", digest.Metadata["model_id"])
	}

	second, err := RunBuildDigests(ctx, stores, cfg, digestOptions())
	if err != nil {
		t.Fatalf("second RunBuildDigests: %v", err)
	}
	if second.Inserted != 0 || second.Skipped != 1 {
		t.Errorf("re-run = %+v, want a cache skip with no insert", second)
	}
	if providerCalls != 0 {
		t.Errorf("re-run constructed %d chat providers", providerCalls)
	}
}

// The cache is the mechanism that keeps a re-run free: once a digest with the
// same bundle hash, prompt version, and model is stored, the model must not be
// called again.
func TestRunBuildDigestsCacheHitSkipsModel(t *testing.T) {
	ctx := context.Background()
	stores, _ := stageStores()
	id := seedRestaurant(t, stores, "gmap-digest-cache")
	seedReviews(t, stores, id, 40)

	fake := &fakeDigestChat{respond: func(chat.ChatRequest) string { return validDigestText }}
	useFakeChatProvider(t, fake)
	cfg := digestTestConfig(knowledge.DigestLLMVersion)

	first, err := RunBuildDigests(ctx, stores, cfg, digestOptions())
	if err != nil {
		t.Fatalf("first RunBuildDigests: %v", err)
	}
	if first.Inserted != 1 {
		t.Errorf("first run inserted %d, want 1", first.Inserted)
	}
	if calls := fake.callCount(); calls != 1 {
		t.Fatalf("model called %d times on a cold cache, want 1", calls)
	}

	second, err := RunBuildDigests(ctx, stores, cfg, digestOptions())
	if err != nil {
		t.Fatalf("second RunBuildDigests: %v", err)
	}
	if second.Inserted != 0 || second.Skipped != 1 {
		t.Errorf("re-run = %+v, want one cache skip", second)
	}
	if calls := fake.callCount(); calls != 1 {
		t.Errorf("cache hit still called the model (now %d times), want it untouched", calls)
	}
}

// The generation contract pins temperature at zero; a test that cannot see the
// request cannot verify it, so the fake records it.
func TestRunBuildDigestsSendsTemperatureZero(t *testing.T) {
	ctx := context.Background()
	stores, _ := stageStores()
	id := seedRestaurant(t, stores, "gmap-digest-temp")
	seedReviews(t, stores, id, 40)

	fake := &fakeDigestChat{respond: func(chat.ChatRequest) string { return validDigestText }}
	useFakeChatProvider(t, fake)

	if _, err := RunBuildDigests(ctx, stores, digestTestConfig(knowledge.DigestLLMVersion), digestOptions()); err != nil {
		t.Fatalf("RunBuildDigests: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.lastReq.Temperature == nil || *fake.lastReq.Temperature != 0 {
		t.Errorf("request temperature = %v, want an explicit zero", fake.lastReq.Temperature)
	}
	if fake.lastReq.Model != "digest-model-x" {
		t.Errorf("request model = %q, want the configured digest model", fake.lastReq.Model)
	}
}

// One restaurant's bad model output must cost that restaurant only: the rest
// of the page continues, the rejected text never reaches the store, and the
// failure is countable in the report.
func TestRunBuildDigestsIsolatesRejectedOutput(t *testing.T) {
	ctx := context.Background()
	stores, _ := stageStores()
	good := seedRestaurant(t, stores, "gmap-digest-good")
	bad := seedRestaurant(t, stores, "gmap-digest-bad")
	seedReviews(t, stores, good, 40)
	seedReviews(t, stores, bad, 40)

	fake := &fakeDigestChat{respond: func(req chat.ChatRequest) string {
		// The user message is the serialized bundle, which names the restaurant.
		if strings.Contains(req.Messages[1].Content, "gmap-digest-bad") {
			return validDigestText + " 详情见 https://example.com/deal"
		}
		return validDigestText
	}}
	useFakeChatProvider(t, fake)

	result, err := RunBuildDigests(ctx, stores, digestTestConfig(knowledge.DigestLLMVersion), digestOptions())
	if err != nil {
		t.Fatalf("RunBuildDigests: %v", err)
	}
	if result.Failed != 1 || result.Inserted != 1 {
		t.Errorf("result = %+v, want exactly one failure and one insert", result)
	}
	if result.RejectReasons["digest_output_invalid"] != 1 {
		t.Errorf("reject reasons = %v, want one digest_output_invalid", result.RejectReasons)
	}
	docs, err := stores.Knowledge.ListByRestaurant(ctx, bad, evidence.ScopeRestaurant)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	for _, doc := range docs {
		if doc.DocType == evidence.DocTypeRestaurantReviewDigest {
			t.Error("a restaurant whose output failed validation must not get a stored digest")
		}
	}
}

// A provider failure costs the restaurants that hit it and nothing else: the
// run continues, the failure is counted, and no half-generated digest is
// stored.
func TestRunBuildDigestsIsolatesProviderErrors(t *testing.T) {
	ctx := context.Background()
	stores, _ := stageStores()
	healthy := seedRestaurant(t, stores, "gmap-digest-healthy")
	broken := seedRestaurant(t, stores, "gmap-digest-broken")
	seedReviews(t, stores, healthy, 40)
	seedReviews(t, stores, broken, 40)

	fake := &fakeDigestChat{err: errors.New("transport blew up")}
	useFakeChatProvider(t, fake)

	result, err := RunBuildDigests(ctx, stores, digestTestConfig(knowledge.DigestLLMVersion), digestOptions())
	if err != nil {
		t.Fatalf("RunBuildDigests: %v", err)
	}
	if result.Failed != 2 {
		t.Errorf("failed = %d, want both restaurants isolated and counted", result.Failed)
	}
	if result.RejectReasons["digest_provider_error"] != 2 {
		t.Errorf("reject reasons = %v, want two digest_provider_error", result.RejectReasons)
	}
	for _, id := range []int64{healthy, broken} {
		docs, listErr := stores.Knowledge.ListByRestaurant(ctx, id, evidence.ScopeRestaurant)
		if listErr != nil {
			t.Fatalf("ListByRestaurant: %v", listErr)
		}
		for _, doc := range docs {
			if doc.DocType == evidence.DocTypeRestaurantReviewDigest {
				t.Errorf("restaurant %d stored a digest despite the provider error", id)
			}
		}
	}
}

// A restaurant with no usable review text is a normal outcome: skipped and
// counted, never an error, never a digest.
func TestRunBuildDigestsSkipsRestaurantsWithoutMaterial(t *testing.T) {
	ctx := context.Background()
	stores, _ := stageStores()
	withReviews := seedRestaurant(t, stores, "gmap-digest-material")
	empty := seedRestaurant(t, stores, "gmap-digest-empty")
	seedReviews(t, stores, withReviews, 40)

	fake := &fakeDigestChat{respond: func(chat.ChatRequest) string { return validDigestText }}
	useFakeChatProvider(t, fake)

	result, err := RunBuildDigests(ctx, stores, digestTestConfig(knowledge.DigestLLMVersion), digestOptions())
	if err != nil {
		t.Fatalf("RunBuildDigests: %v", err)
	}
	if result.NoMaterial != 1 {
		t.Errorf("no_material = %d, want 1", result.NoMaterial)
	}
	if result.Failed != 0 || result.Inserted != 1 {
		t.Errorf("result = %+v, want one insert and no failure", result)
	}
	docs, err := stores.Knowledge.ListByRestaurant(ctx, empty, evidence.ScopeRestaurant)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("a restaurant with no review material got %d documents", len(docs))
	}
}

// A dry run must not write: no digest rows, no batch audit row.
func TestRunBuildDigestsDryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	stores, pipelineStore := stageStores()
	id := seedRestaurant(t, stores, "gmap-digest-dry")
	seedReviews(t, stores, id, 40)

	fake := &fakeDigestChat{respond: func(chat.ChatRequest) string { return validDigestText }}
	useFakeChatProvider(t, fake)

	opts := digestOptions()
	opts.DryRun = true
	result, err := RunBuildDigests(ctx, stores, digestTestConfig(knowledge.DigestLLMVersion), opts)
	if err != nil {
		t.Fatalf("RunBuildDigests: %v", err)
	}
	if result.Inserted != 1 {
		t.Errorf("dry run counted %d inserts, want the would-be write", result.Inserted)
	}
	docs, err := stores.Knowledge.ListByRestaurant(ctx, id, evidence.ScopeRestaurant)
	if err != nil {
		t.Fatalf("ListByRestaurant: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("dry run wrote %d documents", len(docs))
	}
	batches, _ := pipelineStore.ListBatches(ctx, 10)
	if len(batches) != 0 {
		t.Errorf("dry run wrote %d batch rows", len(batches))
	}
}

// The stage must record its audit batch like every other stage, with the
// documents it built.
func TestRunBuildDigestsRecordsABatch(t *testing.T) {
	ctx := context.Background()
	stores, pipelineStore := stageStores()
	id := seedRestaurant(t, stores, "gmap-digest-audit")
	seedReviews(t, stores, id, 40)

	fake := &fakeDigestChat{respond: func(chat.ChatRequest) string { return validDigestText }}
	useFakeChatProvider(t, fake)

	if _, err := RunBuildDigests(ctx, stores, digestTestConfig(knowledge.DigestLLMVersion), digestOptions()); err != nil {
		t.Fatalf("RunBuildDigests: %v", err)
	}
	batches, err := pipelineStore.ListBatches(ctx, 10)
	if err != nil {
		t.Fatalf("ListBatches: %v", err)
	}
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want one", len(batches))
	}
	got := batches[0]
	if got.Stage != stageDigests {
		t.Errorf("stage = %q, want %q", got.Stage, stageDigests)
	}
	if got.Status != review.StatusSucceeded {
		t.Errorf("status = %q, want succeeded", got.Status)
	}
	if got.DocumentsBuilt == nil || *got.DocumentsBuilt != 1 {
		t.Errorf("documents_built = %v, want 1", got.DocumentsBuilt)
	}
}

func TestParseDigestOptionsDefaultsAndFlags(t *testing.T) {
	cfg := testConfig()
	cfg.Digest.BatchSize = 25

	opts, err := ParseDigestOptions(nil, cfg)
	if err != nil {
		t.Fatalf("ParseDigestOptions: %v", err)
	}
	if opts.BatchSize != 25 {
		t.Errorf("batch = %d, want the configured 25", opts.BatchSize)
	}

	opts, err = ParseDigestOptions([]string{"--limit=5", "--batch=2", "--restaurant-id=9", "--dry-run"}, cfg)
	if err != nil {
		t.Fatalf("ParseDigestOptions: %v", err)
	}
	if opts.Limit != 5 || opts.BatchSize != 2 || opts.RestaurantID != 9 || !opts.DryRun {
		t.Errorf("flags parsed wrong: %+v", opts)
	}

	for _, args := range [][]string{{"--nope"}, {"--limit=x"}, {"--batch="}} {
		if _, err := ParseDigestOptions(args, cfg); err == nil {
			t.Errorf("ParseDigestOptions(%v) accepted bad input", args)
		}
	}
}
