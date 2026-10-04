package agent_test

// The M6-05 latency measurement for the agent runtime.
//
// The scripted provider takes the model out of the equation on purpose: what
// this measures is the runtime's own cost per turn — the graph, the tools, the
// checkpoint writes, the gate — which is the part this repository owns. A
// regression here cannot be excused by a slow model, because no model is
// involved.
//
// Gated behind PLATEPILOT_PERF like the retrieval run: it is seconds rather
// than minutes, but a percentile measured on every commit is a number nobody
// reads, and one measured nowhere is a number nobody has.

import (
	"os"
	"sort"
	"testing"
	"time"
)

// perfTurnsPerCase is how many times each fixture case runs. Ten cases times
// this many turns keeps the total run a couple of seconds while giving the
// percentiles real support.
const perfTurnsPerCase = 20

// sortDurations, percentile, and ms mirror the retrieval package's latency
// helpers. They are duplicated rather than shared because the two suites live
// in different packages and a helper grown into shared/ for two ten-line
// functions would be a dependency bought for nothing.
func sortDurations(in []time.Duration) []time.Duration {
	sorted := append([]time.Duration(nil), in...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[int(p*float64(len(sorted)-1))]
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// TestAgentTurnLatency measures full-turn latency for every eval case and
// reports per-case p50/p95 plus the runtime-wide picture.
//
// Per-case rows matter because the cases are not the same work: a chit-chat
// turn runs no tools, a booking turn runs the availability read and parks the
// write, a two-search turn runs the retrieval path twice. One aggregate would
// average those differences away; a table shows which slice of the runtime
// moved.
func TestAgentTurnLatency(t *testing.T) {
	if os.Getenv("PLATEPILOT_PERF") == "" {
		t.Skip("skipping the latency run: set PLATEPILOT_PERF=1 (or `make perf`) " +
			"to measure the runtime's own per-turn cost")
	}
	cases := loadAgentCases(t)

	type row struct {
		id      string
		p50, p95 time.Duration
		n       int
	}
	rows := make([]row, 0, len(cases))
	var all []time.Duration

	for _, c := range cases {
		var samples []time.Duration
		for i := 0; i < perfTurnsPerCase; i++ {
			start := time.Now()
			// runEvalCase returns only after the stream is drained, so the
			// clock covers the whole turn: the graph, the tools, the
			// persistence, and the confirmation decisions if the case has any.
			runEvalCase(t, c)
			samples = append(samples, time.Since(start))
		}
		sorted := sortDurations(samples)
		rows = append(rows, row{
			id:  c.ID,
			p50: percentile(sorted, 0.50),
			p95: percentile(sorted, 0.95),
			n:   len(samples),
		})
		all = append(all, sorted...)
	}

	for _, r := range rows {
		t.Logf("%-45s p50 %7.2fms  p95 %7.2fms  n=%d",
			r.id, ms(r.p50), ms(r.p95), r.n)
	}
	t.Logf("%-45s p50 %7.2fms  p95 %7.2fms  n=%d",
		"ALL TURNS", ms(percentile(sortDurations(all), 0.50)),
		ms(percentile(sortDurations(all), 0.95)), len(all))
}
