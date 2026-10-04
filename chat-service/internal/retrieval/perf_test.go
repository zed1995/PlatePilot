package retrieval

// The M6-05 latency measurement for the retrieval layer.
//
// Where the fixture suite asks "how correct", this asks "how fast, and where
// does the time go". It is gated behind PLATEPILOT_PERF rather than PLATEPILOT_REQUIRE_DB
// because a latency run is minutes of embedding calls, not seconds — a gate
// that ran on every `go test ./...` would tax every commit to buy a number
// nobody reads daily.
//
// The report keeps the embedding call beside the end-to-end search on
// purpose: with the vector channel on, the embedding round trip is the one
// network hop in the path, and "how much of the budget is the model" is the
// first question any p95 regression raises. Printing both turns that
// speculation into a subtraction.

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/zed1995/platepilot/shared/domain/retrieval"
)

// perfSamples controls how many timing samples the suite takes per measured
// operation. Three rounds over up to 24 queries keeps the whole run inside a
// couple of minutes while leaving the percentiles more than a handful of
// points to stand on.
const (
	perfQueryLimit   = 24
	perfQueryRounds  = 3
	perfEmbedSamples = 10
)

// TestRetrievalLatency measures search and embedding latency against the live
// corpus and reports p50/p95 with the bottleneck called out.
func TestRetrievalLatency(t *testing.T) {
	if os.Getenv("PLATEPILOT_PERF") == "" {
		t.Skip("skipping the latency run: set PLATEPILOT_PERF=1 (or `make perf`) " +
			"to measure against the live corpus")
	}

	file := loadFixtures(t)
	service := newEvalService(t)
	ctx := context.Background()

	// Every kind of query the fixtures cover gets into the sample set — up to
	// four cases per kind — so the percentiles describe the workload rather
	// than its most convenient slice.
	var queries []fixtureCase
	kindCount := map[string]int{}
	for _, tc := range file.Cases {
		if strings.TrimSpace(tc.Query) == "" {
			continue // a filter-only page has no free text to embed; measured below
		}
		if kindCount[tc.Kind] >= 4 {
			continue
		}
		kindCount[tc.Kind]++
		queries = append(queries, tc)
		if len(queries) >= perfQueryLimit {
			break
		}
	}
	if len(queries) == 0 {
		t.Fatal("the fixtures contain no query-bearing cases to measure")
	}

	var searchSamples []time.Duration
	warm := true
	for round := 0; round < perfQueryRounds; round++ {
		for _, tc := range queries {
			start := time.Now()
			result, err := service.Search(ctx, retrieval.Request{
				Query: tc.Query, Text: tc.Text, Filter: tc.Filter, TopK: 5,
			})
			elapsed := time.Since(start)
			if err != nil {
				t.Fatalf("search %q: %v", tc.Query, err)
			}
			if warm {
				// The first pass pays connection pools and plan caches; it is
				// warm-up, not a sample. Dropping it is what stops a cold
				// first query from wearing the p95 as a costume.
				warm = false
				continue
			}
			_ = result
			searchSamples = append(searchSamples, elapsed)
		}
	}

	vectorRan := false
	for _, tc := range queries {
		result, err := service.Search(ctx, retrieval.Request{
			Query: tc.Query, Filter: tc.Filter, TopK: 5,
		})
		if err != nil {
			continue
		}
		if ran, _ := channelState(*result.Trace, retrieval.ChannelVector); ran {
			vectorRan = true
			break
		}
	}

	embedSamples := measureEmbeddingLatency(t, ctx)
	reportLatency(t, "search end-to-end", searchSamples)
	if vectorRan {
		reportLatency(t, "embedding round-trip", embedSamples)
		callOutBottleneck(t, searchSamples, embedSamples)
	} else {
		t.Logf("vector channel did not run (no embedding provider reachable); " +
			"the search percentiles describe structured+keyword only")
	}
}

// measureEmbeddingLatency times the embedding round trip on its own, so the
// report can attribute search latency to the model rather than guess at it.
func measureEmbeddingLatency(t *testing.T, ctx context.Context) []time.Duration {
	t.Helper()
	client := embeddingForEval(t)
	if client == nil {
		return nil
	}
	var samples []time.Duration
	for i := 0; i < perfEmbedSamples; i++ {
		start := time.Now()
		if _, err := client.EmbedQuery(ctx, "安静的适合约会的布鲁克林意大利餐厅"); err != nil {
			t.Logf("embedding probe %d failed: %v", i, err)
			return samples
		}
		samples = append(samples, time.Since(start))
	}
	return samples
}

// reportLatency prints one operation's distribution.
func reportLatency(t *testing.T, label string, samples []time.Duration) {
	t.Helper()
	if len(samples) == 0 {
		t.Logf("%-22s  (no samples)", label)
		return
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	p50 := percentile(sorted, 0.50)
	p95 := percentile(sorted, 0.95)
	t.Logf("%-22s p50 %8.1fms  p95 %8.1fms  n=%d",
		label, ms(p50), ms(p95), len(samples))
}

// callOutBottleneck names where the time went instead of leaving two numbers
// for the reader to reconcile.
func callOutBottleneck(t *testing.T, search, embed []time.Duration) {
	t.Helper()
	if len(search) == 0 || len(embed) == 0 {
		return
	}
	searchP50 := percentile(sortDurations(search), 0.50)
	embedP50 := percentile(sortDurations(embed), 0.50)
	share := float64(embedP50) / float64(searchP50)
	if share > 1 {
		t.Logf("bottleneck: the embedding round trip alone is %.0f%% of the "+
			"median search — the vector channel, not the database, sets the floor", share*100)
		return
	}
	t.Logf("bottleneck: the embedding round trip is %.0f%% of the median search; "+
		"the database and fusion dominate", share*100)
}

func sortDurations(in []time.Duration) []time.Duration {
	sorted := append([]time.Duration(nil), in...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted
}

// percentile interpolates on the nearest-rank boundary; for the sample sizes
// here the difference from a fancy estimator is smaller than the noise.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)-1))
	return sorted[idx]
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
