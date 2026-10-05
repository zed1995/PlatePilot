package agent_test

// The M6-03 agent evaluation. Where the slice tests each pin one property of
// one node, this suite runs whole turns against the full runtime — every tool
// registered, the approval gate live, the checkpoint store real — and scores
// them as a set, so a regression shows up as a metric moving rather than as
// one test going red.
//
// The cases live in testdata/agent_cases.yaml and follow the same rule the
// retrieval fixtures established: an expectation is a statement about the
// runtime, written before any run, never copied from one. The scripted
// provider stands in for the model so the suite is offline and deterministic;
// when a real model is wired in, the scripts are dropped and the expectations
// are asserted unchanged.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/testkit"

	"github.com/zed1995/platepilot/chat-service/internal/agent"
	"github.com/zed1995/platepilot/chat-service/internal/agent/slots"
	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
	"github.com/zed1995/platepilot/chat-service/internal/agent/tools"
	"github.com/zed1995/platepilot/chat-service/internal/hitl"
	"github.com/zed1995/platepilot/chat-service/internal/memorywrite"
	"github.com/zed1995/platepilot/chat-service/internal/retrieval"
)

// ---- the fixture schema ---------------------------------------------------

type evalScriptStep struct {
	Call string         `yaml:"call"`
	Args map[string]any `yaml:"args"`
	Say  string         `yaml:"say"`
}

type evalExpect struct {
	ToolsInvoked         []string `yaml:"tools_invoked"`
	ToolsForbidden       []string `yaml:"tools_forbidden"`
	ConfirmationRequired bool     `yaml:"confirmation_required"`
	ConfirmationCount    int      `yaml:"confirmation_count"`
	FinalState           string   `yaml:"final_state"`
	AnswerContains       []string `yaml:"answer_contains"`
	BookingWrites        *int     `yaml:"booking_writes"`
}

type evalCase struct {
	ID        string           `yaml:"id"`
	UserInput string           `yaml:"user_input"`
	Script    []evalScriptStep `yaml:"script"`
	Decide    []string         `yaml:"decide"`
	Expect    evalExpect       `yaml:"expect"`
}

type evalFixture struct {
	Corpus struct {
		GeneratedAt string `yaml:"generated_at"`
		Provider    string `yaml:"provider"`
	} `yaml:"corpus"`
	Cases []evalCase `yaml:"cases"`
}

// ---- the offline harness --------------------------------------------------

// evalHarness is one case's world: three seeded restaurants, every tool
// registered over in-memory stores, and the approval gate wired to the same
// registry the turn runs through — so a decision exercises the real write path.
type evalHarness struct {
	repo          *countingReservations
	conversations *testkit.ConversationRepository
	registry      *toolreg.Registry
	runner        *agent.Runner
	decider       *hitl.Service
}

// newEvalHarness builds one case's world. opts selects how the composer is
// driven, which is the only part of the runtime that has two paths.
func newEvalHarness(t *testing.T, provider *scriptedProvider, opts evalOptions) *evalHarness {
	t.Helper()
	ctx := context.Background()

	// The corpus both the searcher and the name resolver read. One store for
	// both is the point: a restaurant the search can see is one the resolver
	// can pin, and two stores would let the fixtures drift apart.
	restaurants := testkit.NewRestaurantRepository()
	for _, detail := range []struct {
		id       int64
		name     string
		borough  string
		cuisines []string
	}{
		{7, "Joe's Pizza", "manhattan", []string{"italian", "pizza"}},
		{11, "Brooklyn Brick Oven", "brooklyn", []string{"italian", "pizza"}},
		{12, "Queens Curry House", "queens", []string{"indian"}},
	} {
		if err := restaurants.Upsert(ctx, rankedDetail(
			detail.id, detail.name, detail.borough, detail.cuisines, 2, 4.5,
		)); err != nil {
			t.Fatalf("seed restaurant %d: %v", detail.id, err)
		}
	}
	searchService, err := retrieval.NewService(retrieval.ServiceConfig{
		Weights:          retrieval.DefaultWeights,
		Oversample:       2,
		TopK:             10,
		EnableStructured: true,
		EnableKeyword:    true,
		EnableVector:     false,
	}, retrieval.Deps{
		Restaurants: restaurants,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("build retrieval service: %v", err)
	}

	memories := testkit.NewMemoryRepository()
	memoryService, err := memorywrite.NewService(memories)
	if err != nil {
		t.Fatalf("memorywrite.NewService: %v", err)
	}

	repo := newCountingReservations()
	reservationService := reservationService(t, repo)

	registry := toolreg.New(0)
	register(t, registry, tools.SearchRestaurantsEntry(searchService))
	register(t, registry, tools.ResolveRestaurantEntry(restaurants, tools.ResolveConfig{}))
	register(t, registry, tools.RestaurantEvidenceEntry(&followUpEvidence{}))
	register(t, registry, tools.GetAvailabilityEntry(reservationService))
	register(t, registry, tools.RequestReservationEntry(reservationService))
	register(t, registry, tools.SaveMemoryEntry(memoryService))

	conversations := testkit.NewConversationRepository()
	runner, err := agent.NewRunner(agent.Config{
		MaxToolRounds:   4,
		AnswerStreaming: opts.Streaming,
	}, agent.Deps{
		Chat:          provider,
		ToolCalling:   provider,
		Registry:      registry,
		Extractor:     slots.New(slots.Deps{}),
		Conversations: conversations,
		Memories:      memories,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	decider, err := hitl.NewService(hitl.Config{
		Checkpoints: conversations,
		Tools:       registry,
	})
	if err != nil {
		t.Fatalf("hitl.NewService: %v", err)
	}
	return &evalHarness{
		repo:          repo,
		conversations: conversations,
		registry:      registry,
		runner:        runner,
		decider:       decider,
	}
}

// scriptedResps turns the case's script into the provider's response queue:
// one tool call per `call` step, one assistant turn per `say` step, in order.
func (c evalCase) scriptedResps() []domainchat.ToolCallResponse {
	var resps []domainchat.ToolCallResponse
	step := 0
	for _, s := range c.Script {
		if s.Call != "" {
			step++
			args, err := json.Marshal(s.Args)
			if err != nil {
				panic(fmt.Sprintf("case %s: encode args for %s: %v", c.ID, s.Call, err))
			}
			resps = append(resps, toolCallResponse(fmt.Sprintf("c%d", step), s.Call, string(args)))
			continue
		}
		resps = append(resps, assistantText(s.Say))
	}
	return resps
}

// composerAnswer is the sentence the script says the composer produced.
//
// A turn makes up to two model calls: the plan's tool-calling round, and — when
// the tools gathered citable evidence — the composer's grounded answer. The
// script models both with one list, and the composer is the last of the two, so
// the final `say` is the composer's. A case that never gathers evidence never
// reaches the composer and never consumes it.
func (c evalCase) composerAnswer() string {
	for i := len(c.Script) - 1; i >= 0; i-- {
		if c.Script[i].Say != "" {
			return c.Script[i].Say
		}
	}
	return ""
}

// evalChunkRunes is how much text one scripted stream chunk carries. It is
// small enough that a single published line spans several chunks — which is the
// case the composer's line buffer exists for — and large enough that the chunk
// count stays readable when a test fails.
const evalChunkRunes = 12

// chunkText splits a scripted answer into the chunks a streaming provider would
// emit. Byte-for-byte the chunks concatenate to the input, which is the property
// the composer's delta equality rests on.
func chunkText(text string) []domainchat.ChatChunk {
	runes := []rune(text)
	chunks := make([]domainchat.ChatChunk, 0, len(runes)/evalChunkRunes+1)
	for start := 0; start < len(runes); start += evalChunkRunes {
		end := min(start+evalChunkRunes, len(runes))
		chunk := domainchat.ChatChunk{Delta: string(runes[start:end])}
		if end == len(runes) {
			chunk.FinishReason = domainchat.FinishReasonStop
		}
		chunks = append(chunks, chunk)
	}
	return chunks
}

// evalOptions selects how the harness feeds the parts of a turn a model would
// produce.
type evalOptions struct {
	// Streaming runs the composer through its streaming path. It is a switch
	// rather than the only mode because the four gates have to hold under both:
	// an answer that validates one way and ships another is precisely the
	// failure streaming could introduce, and a gate that only ever ran one path
	// could not see it.
	Streaming bool
	// ChunkDelay paces the scripted stream, one wait per chunk. It is zero
	// everywhere except the perf harness: with an instantaneous stream the
	// runtime's own contribution to first-token latency is unmeasurable.
	ChunkDelay time.Duration
}

// runOne executes the case end to end: the turn, then any confirmation
// decisions, and returns everything the expectations are checked against.
type evalObservation struct {
	invoked    []string
	asks       int
	answer     string
	finalState string
	bookings   int
	// firstText is how long the turn ran before the client saw any answer
	// text. It is the one number that separates the two answer paths, so it is
	// an observation rather than a metric only the perf harness reads.
	firstText time.Duration
	// deltas and replaced say which route the answer took — published in
	// pieces, or published once and then corrected. The gate metrics cannot
	// see the difference, and a suite that cannot see it cannot tell a
	// streamed run from a one-shot one wearing the flag.
	deltas      int
	replaced    bool
	decideError string
}

// runEvalCase runs one case the way the gates grade it: one-shot answers.
func runEvalCase(t *testing.T, c evalCase) evalObservation {
	t.Helper()
	return runEvalCaseWith(t, c, evalOptions{})
}

func runEvalCaseWith(t *testing.T, c evalCase, opts evalOptions) evalObservation {
	t.Helper()
	ctx := context.Background()
	provider := &scriptedProvider{
		supportTools: true,
		toolResps:    c.scriptedResps(),
		chunkDelay:   opts.ChunkDelay,
	}
	// The composer's answer is queued on both channels: as a whole completion,
	// and — when streaming is on — as the chunk script it is read from instead.
	// Nothing else in the harness asks Complete, so an entry a case never
	// reaches is an entry nothing pops.
	if answer := c.composerAnswer(); answer != "" {
		provider.completeResps = append(provider.completeResps, domainchat.ChatResponse{
			Message:      domainchat.ChatMessage{Role: domainchat.RoleAssistant, Content: answer},
			FinishReason: domainchat.FinishReasonStop,
		})
		// Both paths pay the same decode cost, so comparing them compares the
		// moment of publication rather than the speed of the fake. The
		// completion is charged for every chunk the stream would have carried,
		// which is why the count is computed before the mode is applied.
		chunks := chunkText(answer)
		provider.completeDelay = opts.ChunkDelay * time.Duration(len(chunks))
		if opts.Streaming {
			provider.chunks = chunks
		}
	}
	h := newEvalHarness(t, provider, opts)

	threadID := "thread-" + c.ID
	started := time.Now()

	// RunLive, not Run, because that is what serves a client: the SSE handler
	// forwards each frame as the graph produces it. Going through the batched
	// entry point here would make every observation about *timing* — which is
	// half of what the two answer paths differ on — a measurement of when the
	// turn ended.
	var (
		events    []agent.Event
		firstText time.Duration
		deltas    int
		replaced  bool
	)
	result, err := h.runner.RunLive(ctx, agent.TurnInput{
		ThreadID:  threadID,
		UserID:    "user-1",
		UserInput: c.UserInput,
	}, func(ev agent.Event) error {
		switch {
		case ev.Type == agent.EventDelta && ev.Delta != "":
			deltas++
			if firstText == 0 {
				firstText = time.Since(started)
			}
		case ev.Type == agent.EventAnswerReplace:
			replaced = true
		}
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("case %s: %v", c.ID, err)
	}

	obs := evalObservation{
		asks:      len(confirmationAsks(events)),
		firstText: firstText,
		deltas:    deltas,
		replaced:  replaced,
	}
	seen := map[string]bool{}
	for _, ev := range events {
		if ev.Type == agent.EventToolStart {
			if !seen[ev.Tool] {
				seen[ev.Tool] = true
				obs.invoked = append(obs.invoked, ev.Tool)
			}
		}
	}
	if result.Answer != nil {
		obs.answer = result.Answer.Text
	}

	for _, d := range c.Decide {
		if _, err := h.decider.Decide(ctx, threadID, hitl.Decision(d)); err != nil {
			obs.decideError = fmt.Sprintf("%s: %v", d, err)
		}
	}
	// Read after the decisions: the write a confirmation authorises is part of
	// the case, and counting it before the user answers would call every
	// approval a no-op.
	obs.bookings = h.repo.holds

	checkpoint, err := h.conversations.LoadCheckpoint(ctx, threadID)
	if err != nil {
		obs.finalState = "unreachable"
	} else {
		obs.finalState = string(checkpoint.State)
	}
	return obs
}

// ---- the checks -----------------------------------------------------------

// sameStringSet compares sets, because a turn that searched twice met the same
// expectation as one that searched once.
func sameStringSet(want, got []string) bool {
	if len(want) == 0 && len(got) == 0 {
		return true
	}
	set := make(map[string]int, len(want))
	for _, w := range want {
		set[w]++
	}
	for _, g := range got {
		set[g]--
		if set[g] < 0 {
			return false
		}
	}
	for _, n := range set {
		if n != 0 {
			return false
		}
	}
	return true
}

// checkCase returns the case's failures against one observation, grouped so
// the metrics can be reported per property rather than only as a total.
func checkCase(c evalCase, obs evalObservation) (toolFail, confirmFail, fail []string) {
	add := func(dst *[]string, format string, args ...any) {
		*dst = append(*dst, fmt.Sprintf(format, args...))
	}

	if !sameStringSet(c.Expect.ToolsInvoked, obs.invoked) {
		add(&toolFail, "实际调用 %v，期望 %v", obs.invoked, c.Expect.ToolsInvoked)
	}
	for _, banned := range c.Expect.ToolsForbidden {
		for _, got := range obs.invoked {
			if got == banned {
				add(&toolFail, "禁用工具 %q 被调用", banned)
			}
		}
	}

	if c.Expect.ConfirmationRequired {
		if obs.asks != c.Expect.ConfirmationCount {
			add(&confirmFail, "确认请求数 %d，期望 %d", obs.asks, c.Expect.ConfirmationCount)
		}
	} else if obs.asks != 0 {
		add(&confirmFail, "不该有确认请求，实际 %d 次", obs.asks)
	}
	if c.Expect.BookingWrites != nil && obs.bookings != *c.Expect.BookingWrites {
		add(&confirmFail, "订单写入 %d 次，期望 %d 次", obs.bookings, *c.Expect.BookingWrites)
	}
	if c.Expect.FinalState != "" && obs.finalState != c.Expect.FinalState {
		add(&fail, "结束状态 %q，期望 %q", obs.finalState, c.Expect.FinalState)
	}
	for _, want := range c.Expect.AnswerContains {
		if !strings.Contains(obs.answer, want) {
			add(&fail, "回答缺少 %q（实际：%q）", want, obs.answer)
		}
	}
	if obs.decideError != "" {
		add(&fail, "确认决策出错：%s", obs.decideError)
	}

	fail = append(fail, toolFail...)
	fail = append(fail, confirmFail...)
	return toolFail, confirmFail, fail
}

// ---- the suite ------------------------------------------------------------

// loadAgentCases reads and parses the fixture. Shared by the eval suite and
// the replay tests so both always grade the same fixed requests.
func loadAgentCases(t *testing.T) []evalCase {
	t.Helper()
	data, err := os.ReadFile("testdata/agent_cases.yaml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture evalFixture
	if err := yaml.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("the fixture declares no cases")
	}
	return fixture.Cases
}

// TestAgentEvalSuite runs every case in testdata/agent_cases.yaml and scores
// four metrics over the set. A metric below 1.0 fails the suite: these are
// gates, not dashboards — the point of the offline harness is that there is no
// acceptable level of "the model talked its way past the approval gate".
//
// The suite runs twice, once per answer path. The second run is what makes the
// gates statements about the *runtime* rather than about one of its two ways of
// producing an answer: a streamed turn publishes text before it has been
// validated, so it is the configuration where a citation violation could reach
// the user, and a suite that only graded the one-shot path would grade the
// configuration that cannot fail that way.
func TestAgentEvalSuite(t *testing.T) {
	fixtureCases := loadAgentCases(t)
	if len(fixtureCases) == 0 {
		t.Fatal("the fixture declares no cases")
	}

	for _, config := range []struct {
		name string
		opts evalOptions
	}{
		{"one-shot answers", evalOptions{}},
		{"streamed answers", evalOptions{Streaming: true}},
	} {
		config := config
		t.Run(config.name, func(t *testing.T) {
			scoreAgentEval(t, fixtureCases, config.opts)
		})
	}
}

// scoreAgentEval grades one configuration over the whole fixture.
func scoreAgentEval(t *testing.T, fixtureCases []evalCase, opts evalOptions) {
	t.Helper()
	var (
		toolOK, confirmOK, noDuplicate, e2eOK int
		failed                                []string
	)
	for _, c := range fixtureCases {
		c := c
		obs := runEvalCaseWith(t, c, opts)
		toolFail, confirmFail, allFail := checkCase(c, obs)

		if len(toolFail) == 0 {
			toolOK++
		}
		if len(confirmFail) == 0 {
			confirmOK++
		}
		if c.Expect.BookingWrites == nil || obs.bookings <= *c.Expect.BookingWrites {
			noDuplicate++
		}
		if len(allFail) == 0 {
			e2eOK++
		} else {
			failed = append(failed, fmt.Sprintf("  %s:\n    %s",
				c.ID, strings.Join(allFail, "\n    ")))
		}
	}

	n := len(fixtureCases)
	rate := func(passed int) float64 { return float64(passed) / float64(n) }
	t.Logf("agent eval over %d offline cases:", n)
	t.Logf("  tool_selection_accuracy     %.3f", rate(toolOK))
	t.Logf("  confirmation_safety_rate    %.3f", rate(confirmOK))
	t.Logf("  duplicate_reservation_rate  %.3f", rate(noDuplicate))
	t.Logf("  end_to_end_success_rate     %.3f", rate(e2eOK))

	for _, metric := range []struct {
		name  string
		value float64
	}{
		{"tool_selection_accuracy", rate(toolOK)},
		{"confirmation_safety_rate", rate(confirmOK)},
		{"duplicate_reservation_rate", rate(noDuplicate)},
		{"end_to_end_success_rate", rate(e2eOK)},
	} {
		if metric.value < 1.0 {
			t.Errorf("%s = %.3f, want 1.000 (%d/%d cases passed)",
				metric.name, metric.value, int(metric.value*float64(n)), n)
		}
	}
	if len(failed) > 0 {
		t.Errorf("failing cases:\n%s", strings.Join(failed, "\n"))
	}
}

// ---- the two answer paths are not the same path ---------------------------

// evalPacingDelay stands in for the decode rate. Without a paced stream both
// answer paths deliver their text within the same microsecond — the scripted
// provider answers instantly — and time-to-first-text would measure nothing.
// The delay is not a model; it is a fixed cost the runtime either sits behind
// (one-shot) or publishes ahead of (streamed), which is exactly the difference
// being measured.
const evalPacingDelay = 20 * time.Millisecond

// TestTheStreamedConfigurationReallyStreams keeps the suite's second run
// honest.
//
// Two sets of 1.000 gates prove nothing about streaming if the second run took
// the one-shot path: a flag that never reached the runner would leave the suite
// green and the streamed configuration untested. So the observation records how
// the answer actually arrived, and this reads it.
func TestTheStreamedConfigurationReallyStreams(t *testing.T) {
	grounded := evalCaseByID(t, "ground_answer_cites_the_evidence_it_was_given")

	oneShot := runEvalCaseWith(t, grounded, evalOptions{ChunkDelay: evalPacingDelay})
	if oneShot.deltas != 1 || oneShot.replaced {
		t.Fatalf("one-shot published %d deltas, replaced=%v; want one delta and no replacement",
			oneShot.deltas, oneShot.replaced)
	}

	streamed := runEvalCaseWith(t, grounded, evalOptions{
		Streaming:  true,
		ChunkDelay: evalPacingDelay,
	})
	if streamed.deltas < 2 {
		t.Fatalf("streamed published %d deltas; the answer must arrive in pieces",
			streamed.deltas)
	}
	// The composer guarantees the concatenated deltas equal the validated text,
	// so a replacement here would mean the guarantee broke.
	if streamed.replaced {
		t.Fatal("the deltas already summed to the validated answer, so nothing needed replacing")
	}
	if streamed.answer != oneShot.answer {
		t.Fatalf("the two paths disagree on the answer:\n  one-shot: %q\n  streamed: %q",
			oneShot.answer, streamed.answer)
	}
	// The first line reaches the client after three chunks instead of six, which
	// is the whole of OPT-01: the same text, earlier, because it is published as
	// it is generated.
	if streamed.firstText >= oneShot.firstText {
		t.Fatalf("first text after %v streamed vs %v one-shot; streaming must publish earlier",
			streamed.firstText, oneShot.firstText)
	}
}

// evalCaseByID returns the fixture case with that id.
func evalCaseByID(t *testing.T, id string) evalCase {
	t.Helper()
	for _, c := range loadAgentCases(t) {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("case %q is missing from the fixture", id)
	return evalCase{}
}
