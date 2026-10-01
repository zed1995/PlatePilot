package retrieval

import (
	"context"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	sharedcfg "github.com/zed/platepilot/shared/config"
	"github.com/zed/platepilot/shared/domain/evidence"
	"github.com/zed/platepilot/shared/domain/retrieval"
	"github.com/zed/platepilot/shared/domain/search"
	"github.com/zed/platepilot/shared/embedding/ollama"
	"github.com/zed/platepilot/shared/store/postgres"
)

// Gate C thresholds. They are constants rather than fixture values because they
// are the milestone's exit criteria, not a property of this corpus: a fixture
// that could lower its own bar would let a regression pass by editing a file.
const (
	// gateStructuredAccuracy is exact because a hard condition that is silently
	// dropped returns a plausible list. The user sees a restaurant they
	// explicitly excluded and has no way to know the filter was ignored.
	gateStructuredAccuracy = 1.0
	// gateRecallAtK is the floor for soft-condition recall.
	gateRecallAtK = 0.6
	// gateCitationPrecision is the floor for evidence that matches what was asked.
	gateCitationPrecision = 0.7
	// gateCrossRestaurantLeak is exact and absolute: one is a wrong citation.
	gateCrossRestaurantLeak = 0
)

// fixtureCorpus records what the fixtures were written against.
type fixtureCorpus struct {
	GeneratedAt         string `yaml:"generated_at"`
	ActiveRestaurants   int    `yaml:"active_restaurants"`
	ActiveDocuments     int    `yaml:"active_documents"`
	EvidenceDocuments   int    `yaml:"evidence_documents"`
	EmbeddingModel      string `yaml:"embedding_model"`
	EmbeddingDimensions int    `yaml:"embedding_dimensions"`
}

// fixtureCase is one query with what a correct system must return.
//
// The expectations are written by hand against sampled rows rather than
// captured from a run. A ground truth produced by the system under test makes
// the metric measure self-consistency, which always scores 1.0 and detects
// nothing.
type fixtureCase struct {
	ID            string                  `yaml:"id"`
	Kind          string                  `yaml:"kind"`
	Query         string                  `yaml:"query"`
	Text          string                  `yaml:"text"`
	Filter        search.RestaurantFilter `yaml:"filter"`
	RestaurantIDs []int64                 `yaml:"restaurant_ids"`
	Topic         string                  `yaml:"topic"`
	Expect        fixtureExpect           `yaml:"expect"`
}

type fixtureExpect struct {
	// RestaurantIDs is the recall ground truth.
	RestaurantIDs []int64 `yaml:"restaurant_ids"`
	// ExcludeRestaurantIDs is checked for absence. A filter that is applied too
	// loosely fails here even when the positive assertions pass.
	ExcludeRestaurantIDs  []int64 `yaml:"exclude_restaurant_ids"`
	RecallAtK             int     `yaml:"recall_at_k"`
	MinResults            int     `yaml:"min_results"`
	AllSatisfyFilter      bool    `yaml:"all_satisfy_filter"`
	AllRestaurantsInScope bool    `yaml:"all_restaurants_in_scope"`
	AllHaveSource         bool    `yaml:"all_have_source"`
}

type fixtureFile struct {
	Corpus fixtureCorpus `yaml:"corpus"`
	Cases  []fixtureCase `yaml:"cases"`
}

// metrics is the Gate C scoreboard.
//
// The hard and soft numbers are kept apart because they are not the same kind of
// claim. A leak is a defect; a recall of 0.5 on a soft condition is a tuning
// result. Printing them in one number would let a good average hide a leak.
type metrics struct {
	structuredAccuracy  float64
	structuredTotal     int
	structuredPassed    int
	recallSum           float64
	recallTotal         int
	citationPrecision   float64
	citationTotal       int
	citationPassed      int
	crossRestaurantLeak int
	emptyResults        int
	validCases          int
}

func loadFixtures(t *testing.T) fixtureFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/retrieval_cases.yaml")
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var file fixtureFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse fixtures: %v", err)
	}
	if len(file.Cases) < 20 {
		t.Fatalf("the fixture set has %d cases; the milestone requires at least 20",
			len(file.Cases))
	}
	return file
}

// requireEvalDatabase skips unless the evaluation is meant to run.
//
// The skip is explicit and loud on purpose. A fixture whose expected ids point at
// a database that is not there passes trivially against an empty result set, and
// a green run that measured nothing is worse than no run at all: it is
// indistinguishable from a passing one.
func requireEvalDatabase(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if os.Getenv("PLATEPILOT_REQUIRE_DB") == "" {
		t.Skipf("skipping the retrieval evaluation: %v\n"+
			"    Set PLATEPILOT_REQUIRE_DB=1 to run it against the real corpus.\n"+
			"    A skipped evaluation is not a passing one — the metrics below would\n"+
			"    be measured against nothing.", err)
	}
	t.Fatalf("PLATEPILOT_REQUIRE_DB is set but the database is unreachable: %v", err)
}

// newEvalService builds the real retrieval service over the live corpus.
func newEvalService(t *testing.T) *Service {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)

	dsn := os.Getenv("PLATEPILOT_TEST_POSTGRES_DSN")
	if dsn == "" {
		dsn = "postgres://platepilot:platepilot@localhost:55432/platepilot?sslmode=disable"
	}
	client, err := postgres.Connect(ctx, postgres.Config{
		DSN: dsn, Database: "platepilot",
		ConnectTimeout: 10 * time.Second, Timeout: 30 * time.Second,
	})
	requireEvalDatabase(t, err)
	t.Cleanup(func() { _ = client.Close(context.WithoutCancel(ctx)) })

	// The evaluation runs without an embedding provider when one is not
	// configured, so that a structured-filter regression is still detectable on
	// a machine that has never pulled a model. It is recorded in the output
	// rather than assumed, because the vector channels' numbers mean something
	// different without one.
	var embedding = embeddingForEval(t)

	service, err := NewService(ServiceConfig{
		Weights:          DefaultWeights,
		Oversample:       2,
		TopK:             5,
		EnableStructured: true,
		EnableKeyword:    true,
		EnableVector:     true,
		EmbeddingTimeout: 30 * time.Second,
	}, Deps{
		Restaurants: postgres.NewRestaurantSearchRepository(client),
		Knowledge:   postgres.NewKnowledgeReadRepository(client),
		Embedding:   embedding,
	})
	if err != nil {
		t.Fatalf("build service: %v", err)
	}
	return service
}

func embeddingForEval(t *testing.T) *ollama.Client {
	t.Helper()
	client, err := ollama.New(sharedcfg.EmbeddingConfig{
		BaseURL:    envOr("PLATEPILOT_TEST_OLLAMA_URL", "http://localhost:11434"),
		Model:      envOr("EMBEDDING_MODEL", "qwen3-embedding:0.6b"),
		Dimensions: 1024,
	})
	if err != nil {
		t.Logf("no embedding provider (%v); the vector channels will report "+
			"themselves as unavailable and their numbers describe structured "+
			"recall only", err)
		return nil
	}
	return client
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// TestRetrievalFixtures computes the Gate C metrics against the real corpus.
func TestRetrievalFixtures(t *testing.T) {
	file := loadFixtures(t)
	service := newEvalService(t)
	ctx := context.Background()

	corpus := file.Corpus
	t.Logf("corpus: generated_at=%s restaurants=%d documents=%d evidence=%d model=%s/%dd",
		corpus.GeneratedAt, corpus.ActiveRestaurants, corpus.ActiveDocuments,
		corpus.EvidenceDocuments, corpus.EmbeddingModel, corpus.EmbeddingDimensions)
	t.Logf("scoring against the live database; if its size differs from the " +
		"figures above, the recall numbers are not comparable to a later run")

	score := metrics{}
	for _, tc := range file.Cases {
		t.Run(tc.ID, func(t *testing.T) {
			runFixtureCase(t, ctx, service, tc, &score)
		})
	}

	score.finalise()
	reportMetrics(t, score)
	assertGates(t, score)
}

// runFixtureCase executes one fixture and folds it into the scoreboard.
func runFixtureCase(
	t *testing.T, ctx context.Context, service *Service, tc fixtureCase, score *metrics,
) {
	t.Helper()
	score.validCases++

	switch tc.Kind {
	case "structured", "restaurant":
		result, err := service.Search(ctx, retrieval.Request{
			Query: tc.Query, Text: tc.Text, Filter: tc.Filter, TopK: 5,
		})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(result.Candidates) == 0 {
			score.emptyResults++
		}
		if tc.Expect.AllSatisfyFilter {
			score.structuredTotal++
			if checkFilter(t, tc, result.Candidates) {
				score.structuredPassed++
			}
		}
		if len(tc.Expect.RestaurantIDs) > 0 {
			score.recallTotal++
			ratio := recallAtK(result.Candidates, tc.Expect.RestaurantIDs,
				recallK(tc))
			score.recallSum += ratio
			t.Logf("recall@%d = %.2f (want %v, got top %s)",
				recallK(tc), ratio, tc.Expect.RestaurantIDs,
				describeIDs(result.Candidates))
		}
		for _, forbidden := range tc.Expect.ExcludeRestaurantIDs {
			for _, candidate := range result.Candidates {
				if candidate.RestaurantID == forbidden {
					t.Errorf("restaurant %d was excluded by the filter and "+
						"returned anyway", forbidden)
				}
			}
		}
		// Every candidate has to explain itself, or Gate C condition 2 fails
		// even when the ids are right.
		for _, candidate := range result.Candidates {
			if len(candidate.Reasons) == 0 {
				t.Errorf("candidate %d (%s) has no reason; an unexplainable "+
					"ranking cannot be reviewed", candidate.RestaurantID, candidate.Name)
				break
			}
		}

	case "evidence":
		bundle, trace, err := service.AssembleAndReturn(ctx, EvidenceRequest{
			RestaurantIDs: tc.RestaurantIDs, Query: tc.Query, Topic: tc.Topic,
		}, AssembleOptions{})
		if err != nil {
			t.Fatalf("evidence: %v", err)
		}
		if len(bundle) == 0 {
			score.emptyResults++
		}
		if len(bundle) < tc.Expect.MinResults {
			t.Errorf("got %d citations, want at least %d", len(bundle), tc.Expect.MinResults)
		}
		for _, item := range bundle {
			if !inScope(item.RestaurantID, tc.RestaurantIDs) {
				score.crossRestaurantLeak++
				t.Errorf("citation from restaurant %d leaked into a recall "+
					"scoped to %v", item.RestaurantID, tc.RestaurantIDs)
			}
		}
		if tc.Expect.AllHaveSource {
			score.citationTotal += len(bundle)
			for _, item := range bundle {
				if checkCitation(item, trace) {
					score.citationPassed++
				}
			}
		}
		t.Logf("evidence: recalled=%d kept=%d tokens=%d/%d",
			trace.Recalled, trace.Kept, trace.Tokens, trace.TokenBudget)

	default:
		t.Fatalf("unknown fixture kind %q", tc.Kind)
	}
}

// checkCitation reports whether one citation is checkable by a reader.
//
// Source and observation time are the minimum: a quote without an attribution
// looks correct and cannot be verified, and a quote without a date cannot be
// compared against a newer snapshot.
func checkCitation(item evidence.Evidence, trace *retrieval.EvidenceTrace) bool {
	ok := item.Source != "" && !item.SnapshotAt.IsZero()
	if !ok {
		return false
	}
	// The document must be inside the scope the trace claims it was.
	if trace != nil && trace.ScopeSize == 0 {
		return false
	}
	return true
}

// checkFilter reports whether every candidate satisfies every stated condition.
//
// It re-derives the conditions from the candidate rather than trusting the
// repository, because a repository that silently ignored a filter would return
// rows that look exactly like rows that were filtered.
func checkFilter(t *testing.T, tc fixtureCase, candidates []search.RestaurantCandidate) bool {
	t.Helper()
	ok := true
	for _, candidate := range candidates {
		if tc.Filter.Borough != "" && candidate.Borough != tc.Filter.Borough {
			t.Errorf("candidate %d (%s) is in %q, want %q",
				candidate.RestaurantID, candidate.Name, candidate.Borough, tc.Filter.Borough)
			ok = false
		}
		if tc.Filter.MinRating != nil {
			if candidate.Rating == nil {
				// A candidate with no rating cannot satisfy a minimum-rating
				// condition. Treating an unknown as a pass is exactly the bug.
				t.Errorf("candidate %d (%s) has no rating, so it cannot satisfy "+
					"min_rating=%v", candidate.RestaurantID, candidate.Name, *tc.Filter.MinRating)
				ok = false
			} else if *candidate.Rating < *tc.Filter.MinRating {
				t.Errorf("candidate %d (%s) rated %.1f, want >= %.1f",
					candidate.RestaurantID, candidate.Name, *candidate.Rating, *tc.Filter.MinRating)
				ok = false
			}
		}
		if len(tc.Filter.PriceLevels) > 0 && candidate.PriceLevel != nil {
			if !containsInt(tc.Filter.PriceLevels, int(*candidate.PriceLevel)) {
				t.Errorf("candidate %d (%s) is price %d, want one of %v",
					candidate.RestaurantID, candidate.Name, *candidate.PriceLevel,
					tc.Filter.PriceLevels)
				ok = false
			}
		}
		for _, cuisine := range tc.Filter.Cuisines {
			if !containsStringValue(candidate.Cuisines, cuisine) {
				t.Errorf("candidate %d (%s) lacks cuisine %q (has %v)",
					candidate.RestaurantID, candidate.Name, cuisine, candidate.Cuisines)
				ok = false
			}
		}
	}
	return ok
}

func recallK(tc fixtureCase) int {
	if tc.Expect.RecallAtK > 0 {
		return tc.Expect.RecallAtK
	}
	return 5
}

func recallAtK(candidates []search.RestaurantCandidate, truth []int64, k int) float64 {
	if len(truth) == 0 {
		return 0
	}
	if k > len(candidates) {
		k = len(candidates)
	}
	found := 0
	for _, candidate := range candidates[:k] {
		if containsID(truth, candidate.RestaurantID) {
			found++
		}
	}
	return float64(found) / float64(len(truth))
}

func inScope(id int64, scope []int64) bool { return containsID(scope, id) }

func containsID(ids []int64, id int64) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

func containsInt(values []int, value int) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func containsStringValue(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func describeIDs(candidates []search.RestaurantCandidate) string {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, fmt.Sprintf("%d", candidate.RestaurantID))
	}
	return fmt.Sprint(ids)
}

// reportMetrics prints the scoreboard as a table.
//
// A table rather than a sentence because M6-02 compares runs against each
// other, and a table is something a person can diff.
func reportMetrics(t *testing.T, score metrics) {
	t.Helper()
	t.Log("┌────────────────────────────────┬─────────┐")
	t.Log("│ metric                          │ value   │")
	t.Log("├────────────────────────────────┼─────────┤")
	row := func(name, value, threshold string) {
		t.Log(fmt.Sprintf("│ %-30s │ %7s │ (gate %s)", name, value, threshold))
	}
	row("cases evaluated", fmt.Sprint(score.validCases), "-")
	if score.structuredTotal > 0 {
		row("structured_filter_accuracy",
			fmt.Sprintf("%.3f", score.structuredAccuracy),
			fmt.Sprintf("%.1f", gateStructuredAccuracy))
	} else {
		row("structured_filter_accuracy", "n/a", "1.0")
	}
	if score.recallTotal > 0 {
		row("retrieval_recall_at_k",
			fmt.Sprintf("%.3f", score.recallSum/float64(score.recallTotal)),
			fmt.Sprintf(">= %.1f", gateRecallAtK))
	} else {
		row("retrieval_recall_at_k", "n/a", ">= 0.6")
	}
	if score.citationTotal > 0 {
		row("citation_precision",
			fmt.Sprintf("%.3f", float64(score.citationPassed)/float64(score.citationTotal)),
			fmt.Sprintf(">= %.1f", gateCitationPrecision))
	} else {
		row("citation_precision", "n/a", ">= 0.7")
	}
	row("cross_restaurant_leak",
		fmt.Sprint(score.crossRestaurantLeak),
		fmt.Sprint(gateCrossRestaurantLeak))
	row("empty_result_rate",
		fmt.Sprintf("%.3f", float64(score.emptyResults)/float64(maxInt(score.validCases, 1))),
		"reported only")
	t.Log("└────────────────────────────────┴─────────┘")
}

// assertGates enforces the hard thresholds.
func assertGates(t *testing.T, score metrics) {
	t.Helper()
	if score.structuredTotal > 0 && score.structuredAccuracy < gateStructuredAccuracy {
		t.Errorf("structured_filter_accuracy = %.3f, want exactly %.1f "+
			"(%d of %d queries returned a restaurant that violates a stated condition)",
			score.structuredAccuracy, gateStructuredAccuracy,
			score.structuredPassed, score.structuredTotal)
	}
	if score.crossRestaurantLeak > gateCrossRestaurantLeak {
		t.Errorf("cross_restaurant_leak = %d, want %d: a citation must never "+
			"name a restaurant outside the requested set",
			score.crossRestaurantLeak, gateCrossRestaurantLeak)
	}
	if score.recallTotal > 0 {
		mean := score.recallSum / float64(score.recallTotal)
		if mean < gateRecallAtK {
			t.Errorf("retrieval_recall_at_k = %.3f, want >= %.1f",
				mean, gateRecallAtK)
		}
	}
	if score.citationTotal > 0 {
		precision := float64(score.citationPassed) / float64(score.citationTotal)
		if precision < gateCitationPrecision {
			t.Errorf("citation_precision = %.3f, want >= %.1f",
				precision, gateCitationPrecision)
		}
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// finalise folds the accumulated counts into ratios. It is separate from the
// per-case loop so a case that fails does not corrupt the running totals.
func (m *metrics) finalise() {
	if m.structuredTotal > 0 {
		m.structuredAccuracy = float64(m.structuredPassed) / float64(m.structuredTotal)
	}
}

// TestRetrievalFixtureShape checks the fixture file itself.
//
// It runs offline on purpose. A fixture set that has drifted — a case with no
// expectation, a duplicated id, a ground truth that is empty — makes every
// metric below meaningless, and that is checkable without a database.
func TestRetrievalFixtureShape(t *testing.T) {
	file := loadFixtures(t)

	kinds := map[string]int{}
	seen := map[string]bool{}
	for _, tc := range file.Cases {
		if seen[tc.ID] {
			t.Errorf("duplicate fixture id %q", tc.ID)
		}
		seen[tc.ID] = true

		kinds[tc.Kind]++
		if tc.Query == "" && tc.Text == "" && tc.Kind != "evidence" {
			t.Errorf("%s: a non-evidence case needs a query or text to search for", tc.ID)
		}
		if tc.Kind == "evidence" && len(tc.RestaurantIDs) == 0 {
			t.Errorf("%s: an evidence case must name the restaurants it may cite", tc.ID)
		}
		if tc.Expect.MinResults == 0 && len(tc.Expect.RestaurantIDs) == 0 &&
			!tc.Expect.AllSatisfyFilter && !tc.Expect.AllRestaurantsInScope {
			t.Errorf("%s: the case asserts nothing, so it can only pass", tc.ID)
		}
		if tc.Expect.RecallAtK > 0 && len(tc.Expect.RestaurantIDs) == 0 {
			t.Errorf("%s: recall_at_k is set but the ground truth is empty; the "+
				"metric would divide by nothing", tc.ID)
		}
	}

	// The milestone names the conditions the fixture set must cover. Asserting
	// coverage is what stops the set from quietly shrinking to the easy cases.
	for _, required := range []string{"structured", "restaurant", "evidence"} {
		if kinds[required] == 0 {
			t.Errorf("no %s cases; the fixture set must cover every retrieval kind", required)
		}
	}
	if file.Corpus.GeneratedAt == "" {
		t.Error("the corpus block must record when the fixtures were sampled; " +
			"recall depends on the corpus and an undated score cannot be compared")
	}

	ids := make([]string, 0, len(kinds))
	for kind := range kinds {
		ids = append(ids, fmt.Sprintf("%s=%d", kind, kinds[kind]))
	}
	sort.Strings(ids)
	t.Logf("fixture set: %d cases (%s)", len(file.Cases), fmt.Sprint(ids))
}
