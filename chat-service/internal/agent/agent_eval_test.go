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

func newEvalHarness(t *testing.T, provider *scriptedProvider) *evalHarness {
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
	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 4}, agent.Deps{
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

// runOne executes the case end to end: the turn, then any confirmation
// decisions, and returns everything the expectations are checked against.
type evalObservation struct {
	invoked     []string
	asks        int
	answer      string
	finalState  string
	bookings    int
	decideError string
}

func runEvalCase(t *testing.T, c evalCase) evalObservation {
	t.Helper()
	ctx := context.Background()
	h := newEvalHarness(t, &scriptedProvider{
		supportTools: true,
		toolResps:    c.scriptedResps(),
	})

	threadID := "thread-" + c.ID
	result, stream := h.runner.Run(ctx, agent.TurnInput{
		ThreadID:  threadID,
		UserID:    "user-1",
		UserInput: c.UserInput,
	})
	events := drain(stream)

	obs := evalObservation{asks: len(confirmationAsks(events))}
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
func TestAgentEvalSuite(t *testing.T) {
	fixtureCases := loadAgentCases(t)
	if len(fixtureCases) == 0 {
		t.Fatal("the fixture declares no cases")
	}

	var (
		toolOK, confirmOK, noDuplicate, e2eOK int
		failed                                []string
	)
	for _, c := range fixtureCases {
		c := c
		obs := runEvalCase(t, c)
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
