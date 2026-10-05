package agent_test

// The M6-05 latency measurement for the agent runtime, extended by OPT-01 to
// cover the second answer path.
//
// The scripted provider takes the model out of the equation on purpose: what
// this measures is the runtime's own cost per turn — the graph, the tools, the
// checkpoint writes, the gate — which is the part this repository owns. A
// regression here cannot be excused by a slow model, because no model is
// involved.
//
// Two clocks are read per turn, and the difference between them is the whole
// point of streaming:
//
//   - first text: how long until the client could start reading an answer;
//   - whole turn: how long until the turn was over.
//
// Streaming only ever moves the first number. A run that reports the two as
// equal on every case is a run where the answer arrived in one lump, which is
// what a batched transport delivers and what this harness is here to catch.
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

// perfTurnsPerCase is how many times each fixture case runs per answer path.
// Eleven cases times two paths times this many turns keeps the total run a few
// seconds while giving the percentiles real support.
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

// perfPath is one answer path under measurement.
//
// The chunk delay is what makes comparing the two mean anything: an
// instantaneous provider completes either path in microseconds, so first-text
// latency would measure the fake rather than the runtime's decision about when
// to publish. The delay is a fixed decode cost that the one-shot path sits
// behind and the streamed path publishes ahead of — which isolates the
// runtime's own contribution to the latency the user feels.
type perfPath struct {
	name string
	opts evalOptions
}

var perfPaths = []perfPath{
	{name: "one-shot", opts: evalOptions{ChunkDelay: evalPacingDelay}},
	{name: "streamed", opts: evalOptions{Streaming: true, ChunkDelay: evalPacingDelay}},
}

// perfRow is one case's percentiles under one path.
type perfRow struct {
	id                 string
	firstP50, firstP95 time.Duration
	turnP50, turnP95   time.Duration
	n                  int
}

// perfResult is one path's whole table, plus the pooled samples the aggregate
// row is computed from.
type perfResult struct {
	name  string
	rows  []perfRow
	first []time.Duration
	turn  []time.Duration
}

// TestAgentTurnLatency measures full-turn latency and time-to-first-text for
// every eval case, under both answer paths, and reports per-case p50/p95.
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

	results := make([]perfResult, 0, len(perfPaths))
	for _, path := range perfPaths {
		results = append(results, measurePath(t, cases, path))
	}

	for _, result := range results {
		t.Logf("--- answers: %s ---", result.name)
		for _, row := range result.rows {
			logPerfRow(t, row)
		}
		logPerfRow(t, perfRow{
			id:       "ALL TURNS",
			firstP50: percentile(sortDurations(result.first), 0.50),
			firstP95: percentile(sortDurations(result.first), 0.95),
			turnP50:  percentile(sortDurations(result.turn), 0.50),
			turnP95:  percentile(sortDurations(result.turn), 0.95),
			n:        len(result.turn),
		})
	}

	logAnswerPathEffect(t, results)
}

// measurePath runs the whole fixture under one answer path.
func measurePath(t *testing.T, cases []evalCase, path perfPath) perfResult {
	t.Helper()
	result := perfResult{name: path.name, rows: make([]perfRow, 0, len(cases))}
	for _, c := range cases {
		var firsts, turns []time.Duration
		for i := 0; i < perfTurnsPerCase; i++ {
			start := time.Now()
			// runEvalCaseWith returns only after the turn is over, so this clock
			// covers the whole turn: the graph, the tools, the persistence, and
			// the confirmation decisions if the case has any. The first-text
			// stamp is taken inside the run, which is the only place it exists —
			// once the turn has ended, every frame is equally old.
			obs := runEvalCaseWith(t, c, path.opts)
			turns = append(turns, time.Since(start))
			if obs.firstText > 0 {
				firsts = append(firsts, obs.firstText)
			}
		}
		sortedFirst := sortDurations(firsts)
		sortedTurn := sortDurations(turns)
		result.rows = append(result.rows, perfRow{
			id:       c.ID,
			firstP50: percentile(sortedFirst, 0.50),
			firstP95: percentile(sortedFirst, 0.95),
			turnP50:  percentile(sortedTurn, 0.50),
			turnP95:  percentile(sortedTurn, 0.95),
			n:        len(turns),
		})
		result.first = append(result.first, sortedFirst...)
		result.turn = append(result.turn, sortedTurn...)
	}
	return result
}

func logPerfRow(t *testing.T, row perfRow) {
	t.Helper()
	t.Logf("%-45s first p50 %7.2fms  p95 %7.2fms | turn p50 %7.2fms  p95 %7.2fms  n=%d",
		row.id, ms(row.firstP50), ms(row.firstP95), ms(row.turnP50), ms(row.turnP95), row.n)
}

// logAnswerPathEffect prints each case's time-to-first-text under both paths
// side by side.
//
// Every case but the grounded one runs identical code under both paths, so its
// two numbers are the turn's own cost measured twice and the small difference
// between them is the streaming machinery's overhead. The grounded case is
// where the paths part, and the streamed number should be visibly smaller —
// that gap is what the user gets back, and reading it off one row is the
// difference between a measurement and a pair of tables somebody has to
// subtract by hand.
func logAnswerPathEffect(t *testing.T, results []perfResult) {
	t.Helper()
	if len(results) != 2 {
		return
	}
	oneShot, streamed := results[0], results[1]
	byID := make(map[string]perfRow, len(streamed.rows))
	for _, row := range streamed.rows {
		byID[row.id] = row
	}

	t.Logf("--- time to first answer text, %s vs %s ---", oneShot.name, streamed.name)
	for _, base := range oneShot.rows {
		other, ok := byID[base.id]
		if !ok {
			continue
		}
		t.Logf("%-45s %7.2fms -> %7.2fms  (%+.2fms)",
			base.id, ms(base.firstP50), ms(other.firstP50),
			ms(other.firstP50-base.firstP50))
	}
}
