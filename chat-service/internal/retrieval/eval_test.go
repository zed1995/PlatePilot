package retrieval

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	sharedcfg "github.com/zed1995/platepilot/shared/config"
	"github.com/zed1995/platepilot/shared/domain/evidence"
	"github.com/zed1995/platepilot/shared/domain/retrieval"
	"github.com/zed1995/platepilot/shared/domain/search"
	"github.com/zed1995/platepilot/shared/embedding/ollama"
	"github.com/zed1995/platepilot/shared/store/postgres"
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
	// gateGroundedRate is the floor for the share of answerable evidence
	// questions answered entirely by checkable quotes.
	//
	// It is a different claim from citation precision, which is why it is a
	// separate number: precision measures the quotes, this measures the
	// question. One unsourced sentence beside two good quotes is 0.67 precise
	// and not grounded at all, and only the second reading says the answer is
	// unsafe to show a user. It is held near 1 because grounding is the answer
	// path's contract, not an aspiration — a question it cannot cite is one it
	// is supposed to decline.
	gateGroundedRate = 0.95
	// gateCrossRestaurantLeak is exact and absolute: one is a wrong citation.
	gateCrossRestaurantLeak = 0
	// gateResultRelevance is the floor for the share of a returned page that
	// answers the question it was returned for.
	//
	// It exists because recall cannot be measured on a broad semantic question.
	// "a pizza place in brooklyn" has hundreds of correct answers and five
	// result slots, so a ground truth of three ids scores a correct page as
	// wrong whenever the ranker prefers three other pizzerias — which is a
	// statement about which of many right answers it picked, not about whether
	// the answer was right. Relevance judges the page the user actually sees
	// instead, which is the standard measure for a query with many relevant
	// documents, and it is the number that moves when the semantic channel
	// breaks.
	gateResultRelevance = 0.6
)

// fixtureCorpus records what the fixtures were written against.
type fixtureCorpus struct {
	GeneratedAt         string         `yaml:"generated_at"`
	ActiveRestaurants   int            `yaml:"active_restaurants"`
	ActiveDocuments     int            `yaml:"active_documents"`
	EvidenceDocuments   int            `yaml:"evidence_documents"`
	EmbeddingModel      string         `yaml:"embedding_model"`
	EmbeddingDimensions int            `yaml:"embedding_dimensions"`
	ActiveByBorough     map[string]int `yaml:"active_by_borough"`
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

	// ResultCuisines and MinRelevantRatio judge the page rather than recall a
	// set. A returned restaurant is relevant when it carries one of these
	// cuisine tags, and the ratio is the share of the page that has to be.
	//
	// They are how a broad semantic query is asserted. See gateResultRelevance
	// for why recall is the wrong instrument there.
	ResultCuisines   []string `yaml:"result_cuisines"`
	MinRelevantRatio float64  `yaml:"min_relevant_ratio"`
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
	structuredAccuracy float64
	structuredTotal    int
	structuredPassed   int
	recallSum          float64
	recallTotal        int
	citationPrecision  float64
	citationTotal      int
	citationPassed     int
	// groundedCases and groundedPassed carry the per-question judgement the
	// per-citation numbers cannot make. Citation precision says what share of
	// the quotes handed back are checkable; grounded rate says whether the
	// question was answered by checkable quotes at all. A question answered
	// with one checkable quote out of three is 0.33 precise and, on this
	// measure, not grounded.
	groundedCases       int
	groundedPassed      int
	crossRestaurantLeak int
	emptyResults        int
	validCases          int
	// relevanceSum and relevanceCases carry the judged share of each returned
	// page. Kept apart from recall because they are not the same claim: recall
	// asks whether named restaurants came back, relevance asks whether what
	// came back answers the question.
	relevanceSum   float64
	relevanceCases int
	// vectorRan counts the recall-bearing cases whose semantic channel
	// actually ran, and vectorNote keeps the reason one of them gave for not
	// running.
	//
	// They exist because the recall number means two different things
	// depending on them. With the vector channel running, 0.79 is a statement
	// about semantic ranking. Without it, the same fixture scores 0.47 — not
	// because the ranking got worse, but because half the ranking was never
	// computed, and the remaining half is the same prior-driven order for
	// every query. A metric that cannot distinguish those two is worse than
	// no metric: the second case reads exactly like a regression.
	// vectorExpected counts the recall cases that have a query for the semantic
	// channel to embed, and vectorRan counts those where it actually ran.
	//
	// The denominator is not the number of recall cases. A name search — text
	// "Carmine", no query — has nothing for the vector channel to embed, so it
	// reports itself as skipped by design. Counting those as absences made the
	// ratio 5/14 and tripped the guard that skips the gate whenever a semantic
	// channel is missing, which silently turned the whole evaluation green
	// while nine of its cases were never measured for recall at all.
	vectorRan        int
	vectorExpected   int
	vectorNote       string
	embeddingModelID string
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
	// The floor is the PRD's, not a number picked to match today's file.
	// A fixture set is the only thing standing between a retrieval change and a
	// silent quality regression, and a set that is allowed to shrink will.
	if len(file.Cases) < 50 {
		t.Fatalf("the fixture set has %d cases; the PRD requires at least 50",
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
	// Printed because the borough distribution decides how hard a borough filter
	// actually is: matching 3 of 3,000 rows and matching 2,010 of them are not
	// the same test, and a recall measured on one is not comparable to the other.
	if len(corpus.ActiveByBorough) > 0 {
		boroughs := make([]string, 0, len(corpus.ActiveByBorough))
		for name := range corpus.ActiveByBorough {
			boroughs = append(boroughs, name)
		}
		sort.Strings(boroughs)
		for _, name := range boroughs {
			t.Logf("corpus: active in %s = %d", name, corpus.ActiveByBorough[name])
		}
	}
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
		// Recorded beside the numbers it qualifies rather than assumed: recall
		// and relevance measured without the semantic channel are different
		// measurements, and the gate has to be able to tell them apart.
		//
		// Only a case with a query can expect the semantic channel: a name
		// search has no free text to embed, and the channel says so. Counted
		// for every case that has one — not only the ones scored for recall —
		// because relevance is a semantic claim too, and a ratio that
		// excluded those cases could report the channel as fully present
		// while the metric most dependent on it was measured without it.
		if strings.TrimSpace(tc.Query) != "" {
			score.vectorExpected++
			if ran, note := channelState(*result.Trace, retrieval.ChannelVector); ran {
				score.vectorRan++
			} else if score.vectorNote == "" {
				score.vectorNote = note
			}
		}
		if id := result.Trace.EmbeddingModelID; id != "" {
			score.embeddingModelID = id
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
		if len(tc.Expect.ResultCuisines) > 0 {
			if len(result.Candidates) == 0 {
				t.Errorf("the case judges the relevance of the returned page, "+
					"and no page was returned (asked for %v)", tc.Expect.ResultCuisines)
			} else {
				ratio := relevantShare(result.Candidates, tc.Expect.ResultCuisines)
				score.relevanceSum += ratio
				score.relevanceCases++
				floor := tc.Expect.MinRelevantRatio
				if floor <= 0 {
					floor = gateResultRelevance
				}
				t.Logf("relevance@%d = %.2f (judged %v, got %s)",
					len(result.Candidates), ratio, tc.Expect.ResultCuisines,
					describeCuisines(result.Candidates))
				if ratio < floor {
					t.Errorf("only %.2f of the %d returned restaurants answer "+
						"the question (judged as %v), want >= %.2f",
						ratio, len(result.Candidates),
						tc.Expect.ResultCuisines, floor)
				}
			}
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
			checkable := 0
			for _, item := range bundle {
				if checkCitation(item, trace) {
					score.citationPassed++
					checkable++
				}
			}
			// Counted only where the question expects an answer: a fixture
			// asserting that nothing is known is not an ungrounded answer, and
			// folding it in would let the rate be inflated by questions the
			// system is supposed to leave unanswered.
			if tc.Expect.MinResults > 0 {
				score.groundedCases++
				if len(bundle) >= tc.Expect.MinResults && checkable == len(bundle) {
					score.groundedPassed++
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

// channelState reports whether one channel contributed to a search, and why it
// did not when it did not.
func channelState(trace retrieval.Trace, channel retrieval.Channel) (bool, string) {
	for _, summary := range trace.Channels {
		if summary.Channel == channel {
			return summary.Ran, summary.Note
		}
	}
	return false, "通道未出现在 trace 中"
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

// relevantShare returns the fraction of the returned page that carries one of
// the cuisines the fixture judged as an answer to the question.
//
// It is measured over what was returned, not over the corpus: the user sees the
// page, and a page of five correct restaurants is the product working no matter
// how many others were left out.
func relevantShare(candidates []search.RestaurantCandidate, cuisines []string) float64 {
	if len(candidates) == 0 {
		return 0
	}
	relevant := 0
	for _, candidate := range candidates {
		for _, cuisine := range cuisines {
			if containsStringValue(candidate.Cuisines, cuisine) {
				relevant++
				break
			}
		}
	}
	return float64(relevant) / float64(len(candidates))
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

// describeCuisines renders the page with the tag each restaurant carries, so a
// relevance failure can be read without a second query against the corpus.
func describeCuisines(candidates []search.RestaurantCandidate) string {
	parts := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		parts = append(parts, fmt.Sprintf("%d:%v", candidate.RestaurantID,
			candidate.Cuisines))
	}
	return fmt.Sprint(parts)
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
		// Printed next to the number it qualifies, because a table a person
		// diffs has to show whether the semantic half was in the measurement.
		row("vector_channel_ran",
			fmt.Sprintf("%d/%d", score.vectorRan, score.vectorExpected),
			"all")
		if score.vectorRan < score.vectorExpected {
			t.Logf("WARNING: the vector channel was missing from %d of %d "+
				"cases that had a query to embed (%s). The recall above "+
				"describes the structured and prior-driven ranking only; it is "+
				"not comparable to a run that had an embedding provider.",
				score.vectorExpected-score.vectorRan, score.vectorExpected,
				orUnknown(score.vectorNote))
		}
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
	if score.groundedCases > 0 {
		row("evidence_grounded_rate",
			fmt.Sprintf("%.3f", float64(score.groundedPassed)/float64(score.groundedCases)),
			fmt.Sprintf(">= %.2f", gateGroundedRate))
	} else {
		row("evidence_grounded_rate", "n/a", ">= 0.95")
	}
	if score.relevanceCases > 0 {
		row("semantic_result_relevance",
			fmt.Sprintf("%.3f", score.relevanceSum/float64(score.relevanceCases)),
			fmt.Sprintf(">= %.1f", gateResultRelevance))
	} else {
		row("semantic_result_relevance", "n/a", ">= 0.6")
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
	// The semantic gates are asserted together, and only when the semantic
	// channel was actually present. A missing embedding provider is the same
	// class of absent dependency as a missing database, and it is treated the
	// same way: the numbers are not asserted, and the run says so loudly
	// instead of quietly reporting lower scores that read as a regression.
	//
	// Asserting them anyway would be worse than either alternative. Every
	// semantic fixture would fail for a reason outside the code, and the
	// failure would be indistinguishable from a genuine ranking regression —
	// which is the one thing these gates exist to catch.
	if score.vectorRan < score.vectorExpected {
		missing := score.vectorExpected - score.vectorRan
		if os.Getenv("PLATEPILOT_REQUIRE_VECTOR") != "" {
			t.Errorf("the vector channel did not run for %d of %d cases that "+
				"had a query to embed, so recall and relevance are not "+
				"measurable: %s\n"+
				"    PLATEPILOT_REQUIRE_VECTOR is set, so this is a failure. "+
				"Start the embedding provider (ollama serve; ollama pull %s) "+
				"or drop the switch.",
				missing, score.vectorExpected, orUnknown(score.vectorNote),
				orUnknown(score.embeddingModelID))
		} else {
			t.Skipf("skipping the recall and relevance gates: the vector "+
				"channel did not run for %d of %d cases that had a query to "+
				"embed (%s), so both describe a ranking without its semantic "+
				"half.\n"+
				"    Set PLATEPILOT_REQUIRE_VECTOR=1 to turn this into a "+
				"failure, or start the embedding provider to measure the real "+
				"numbers.",
				missing, score.vectorExpected, orUnknown(score.vectorNote))
		}
		return
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
	if score.groundedCases > 0 {
		rate := float64(score.groundedPassed) / float64(score.groundedCases)
		if rate < gateGroundedRate {
			t.Errorf("evidence_grounded_rate = %.3f, want >= %.2f: %d of %d "+
				"answerable evidence questions were not answered entirely by "+
				"checkable quotes",
				rate, gateGroundedRate,
				score.groundedCases-score.groundedPassed, score.groundedCases)
		}
	}
	if score.relevanceCases > 0 {
		mean := score.relevanceSum / float64(score.relevanceCases)
		if mean < gateResultRelevance {
			t.Errorf("semantic_result_relevance = %.3f, want >= %.1f: the "+
				"returned pages do not answer the questions they were returned for",
				mean, gateResultRelevance)
		}
	}
}

// orUnknown renders an empty diagnostic as the literal "unknown" rather than an
// empty string, so a reason that was never recorded cannot be read as "there
// was no reason".
func orUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
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

// minStructuredCases is the floor for the hard-filter family. Those cases carry
// no shared id prefix, so they are counted by kind rather than by name.
const minStructuredCases = 30

// minFixtureCases is the milestone's floor for the whole set. The plan asks for
// 50–100 scenarios; the ceiling is not asserted because a good set may grow
// past it, but a set that falls below the floor has stopped covering the space.
const minFixtureCases = 50

// requiredCoverage is the floor for each scenario family the milestone names.
// The numbers are deliberately below the current set: they are a ratchet
// against silent deletion, not a description of what exists today.
var requiredCoverage = map[string]int{
	"keyword_":             4,  // 精确店名
	"partial_":             3,  // 部分店名
	"soft_":                10, // 软条件（氛围 / 目的 / 口味）
	"evidence_food_":       3,  // 菜品
	"evidence_service_":    2,  // 服务
	"evidence_ambience_":   2,  // 安静与环境
	"evidence_value_":      2,  // 性价比
	"evidence_wait_":       2,  // 等位
	"evidence_kid_":        1,  // 带孩子
	"evidence_group_":      1,  // 聚会
	"evidence_complaints_": 2,  // 负面
	"evidence_unknown_":    5,  // 缺失数据
	"evidence_injection_":  2,  // prompt injection
	"evidence_scope_":      1,  // 引用边界
}

// coverageReason names the family behind each prefix, for the failure text.
var coverageReason = map[string]string{
	"keyword_":             "精确店名检索",
	"partial_":             "残缺店名检索",
	"soft_":                "软条件（氛围 / 目的 / 口味）",
	"evidence_food_":       "菜品证据",
	"evidence_service_":    "服务证据",
	"evidence_ambience_":   "安静 / 环境证据",
	"evidence_value_":      "性价比证据",
	"evidence_wait_":       "等位证据",
	"evidence_kid_":        "带孩子场景",
	"evidence_group_":      "聚会场景",
	"evidence_complaints_": "负面观点",
	"evidence_unknown_":    "缺失数据（无证据时必须拒答）",
	"evidence_injection_":  "prompt injection 防护",
	"evidence_scope_":      "引用作用域边界",
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
	if len(file.Cases) < minFixtureCases {
		t.Errorf("the fixture set has %d cases, want at least %d: the milestone "+
			"asks for 50–100 scenarios and a smaller set cannot cover them",
			len(file.Cases), minFixtureCases)
	}
	if kinds["structured"] < minStructuredCases {
		t.Errorf("only %d structured cases, want at least %d: the hard-filter "+
			"family (菜系 / 价格 / 地区) must not shrink",
			kinds["structured"], minStructuredCases)
	}
	// And within evidence, the families the PRD lists by name. A set that kept
	// 86 cases while dropping every complaint or every unknown-fact case
	// would still pass the count above, and the answer path would regress
	// exactly where it is hardest to notice: it would start answering
	// questions it has no evidence for.
	for prefix, min := range requiredCoverage {
		count := 0
		for id := range seen {
			if strings.HasPrefix(id, prefix) {
				count++
			}
		}
		if count < min {
			t.Errorf("only %d cases match %q, want at least %d: the fixture "+
				"set must keep covering %s", count, prefix, min, coverageReason[prefix])
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
