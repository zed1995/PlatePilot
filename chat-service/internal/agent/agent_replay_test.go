package agent_test

// The M6-04 replay. The eval suite proves the runtime meets its expectations
// once; the replay asks the other quality question: does the *same* fixed
// input produce the *same* run, and can two model configurations be compared
// on that basis?
//
// Both halves run through the same offline harness as the eval suite, so a
// diff is a statement about the runtime or the provider script, never about
// the network. The comparison axis is a provider instance: today the
// "models" are scripted, and when a real model is wired in the same diff
// machinery compares its two runs unchanged — which is why the comparison
// reads observations, not provider internals.

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// diffObservations returns one line per property the two runs disagree on.
// The property list is the observation itself: what ran, what the gate asked,
// where the thread ended, what was written, what the user read.
func diffObservations(a, b evalObservation) []string {
	var diffs []string
	add := func(format string, args ...any) {
		diffs = append(diffs, fmt.Sprintf(format, args...))
	}

	if !sameStringSet(a.invoked, b.invoked) {
		add("tools: %v vs %v", a.invoked, b.invoked)
	}
	if a.asks != b.asks {
		add("confirmations: %d vs %d", a.asks, b.asks)
	}
	if a.answer != b.answer {
		add("answer: %q vs %q", a.answer, b.answer)
	}
	if a.finalState != b.finalState {
		add("final state: %q vs %q", a.finalState, b.finalState)
	}
	if a.bookings != b.bookings {
		add("bookings: %d vs %d", a.bookings, b.bookings)
	}
	if a.decideError != b.decideError {
		add("decision error: %q vs %q", a.decideError, b.decideError)
	}
	return diffs
}

// TestFixedRequestReplayRepeatsIdentically replays every eval case from the
// same fixed input on an independent harness and requires the runs to be
// observationally identical.
//
// The bar is deliberately strict. These runs are scripted and single-threaded,
// so any difference means the runtime reads something outside the request —
// map order, wall clock, leftover state — and a runtime like that cannot be
// benchmarked or trusted to replay a production incident.
func TestFixedRequestReplayRepeatsIdentically(t *testing.T) {
	cases := loadAgentCases(t)

	var replayed []string
	for _, c := range cases {
		first := runEvalCase(t, c)
		second := runEvalCase(t, c)
		if diffs := diffObservations(first, second); len(diffs) > 0 {
			t.Errorf("case %s replayed differently:\n    %s",
				c.ID, strings.Join(diffs, "\n    "))
			continue
		}
		replayed = append(replayed, c.ID)
	}
	t.Logf("%d/%d cases replayed identically", len(replayed), len(cases))
}

// TestReplayComparesModelConfigurations exercises the comparison axis the
// acceptance criterion calls 多模型比较: the same fixed requests run against
// two provider configurations, and the diff report names exactly where they
// diverge.
//
// The two "models" here are scripts — one that answers a search from evidence
// of the corpus and one that stalls on a clarification — so the test can
// assert the diff is complete and no more: a comparison that under-reports
// would hide a model difference, and one that over-reports would cry wolf on
// identical models.
func TestReplayComparesModelConfigurations(t *testing.T) {
	byID := map[string]evalCase{}
	for _, c := range loadAgentCases(t) {
		byID[c.ID] = c
	}

	// Identical configurations: every case must come back with an empty diff.
	t.Run("identical providers produce no diffs", func(t *testing.T) {
		for _, c := range loadAgentCases(t) {
			a := runEvalCase(t, c)
			b := runEvalCase(t, c)
			if diffs := diffObservations(a, b); len(diffs) > 0 {
				t.Errorf("case %s: identical inputs diffed: %v", c.ID, diffs)
			}
		}
	})

	// A divergent configuration: the same user input, but the "model" answers
	// the search with different prose and never runs the second search. The
	// diff must name the answer and the tools — and nothing else, since the
	// gate, the state and the store were untouched.
	t.Run("divergent providers diff on exactly the changed properties", func(t *testing.T) {
		cases := []evalCase{byID["route_search_then_answer"]}
		if len(cases) != 1 || cases[0].ID == "" {
			t.Fatal("route_search_then_answer missing from the fixture")
		}
		base := cases[0]

		variant := base
		variant.ID = base.ID + "-variant"
		// The "model" never runs the tool and stalls on a clarification
		// instead: the diff must then name the tools and the answer — and
		// nothing else, since the gate, the state and the store were
		// untouched.
		variant.Script = []evalScriptStep{
			{Say: "能告诉我你想吃什么菜系吗？"},
		}

		obsBase := runEvalCase(t, base)
		obsVariant := runEvalCase(t, variant)
		diffs := diffObservations(obsBase, obsVariant)

		if len(diffs) != 2 {
			t.Fatalf("diff = %d lines, want exactly the 2 changed properties:\n%s",
				len(diffs), strings.Join(diffs, "\n"))
		}
		joined := strings.Join(diffs, "\n")
		for _, want := range []string{"answer:", "tools:"} {
			if !strings.Contains(joined, want) {
				t.Errorf("diff does not mention %q:\n%s", want, joined)
			}
		}

		report := replayDiffReport(cases, []evalObservation{obsBase}, []evalObservation{obsVariant})
		for _, line := range report {
			t.Logf("%s", line)
		}
	})
}

// replayDiffReport renders a case-by-case comparison the way a multi-model
// run would be read: one block per differing case, then a count. Sorted by
// case id so two reports of the same data are byte-identical. runA and runB
// are parallel to cases: one observation per case, in fixture order.
func replayDiffReport(cases []evalCase, runA, runB []evalObservation) []string {
	if len(runA) != len(cases) || len(runB) != len(cases) {
		panic("replayDiffReport: observations must be parallel to cases")
	}
	obsA := map[string]evalObservation{}
	obsB := map[string]evalObservation{}
	for i, c := range cases {
		obsA[c.ID] = runA[i]
		obsB[c.ID] = runB[i]
	}
	ids := make([]string, 0, len(cases))
	for _, c := range cases {
		ids = append(ids, c.ID)
	}
	sort.Strings(ids)

	lines := []string{fmt.Sprintf("replay comparison over %d fixed requests", len(cases))}
	differing := 0
	for _, id := range ids {
		diffs := diffObservations(obsA[id], obsB[id])
		if len(diffs) == 0 {
			continue
		}
		differing++
		lines = append(lines, fmt.Sprintf("  %s:", id))
		for _, d := range diffs {
			lines = append(lines, "    "+d)
		}
	}
	lines = append(lines, fmt.Sprintf("  %d/%d requests differ", differing, len(cases)))
	return lines
}
