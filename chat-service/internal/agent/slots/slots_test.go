package slots

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/conversation"
	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/review"
	"github.com/zed1995/platepilot/shared/domain/search"
	"github.com/zed1995/platepilot/shared/testkit"
)

// rulesOnly is an extractor with no structured-output provider: the deployment
// that has no chat channel at all, which is the one the fallback exists for.
func rulesOnly() *Extractor { return New(Deps{}) }

// TestRuleExtractionSplitsHardAndSoftConditions is the core contract of this
// package: the same sentence, split the same way, in both languages.
//
// The split is what M5-02 and M5-03 build on. A soft word that lands in the
// hard filter narrows the search on a condition no column records, so the
// result looks filtered and is actually empty; a hard condition that lands in
// the soft pile stops filtering at all. Both failures are invisible in a result
// list, which is why the expectations are spelled out one case at a time.
func TestRuleExtractionSplitsHardAndSoftConditions(t *testing.T) {
	minRating := func(v float64) *float64 { return &v }
	cases := []struct {
		name         string
		input        string
		wantBorough  string
		wantNeighbor string
		wantCuisines []string
		wantPrices   []int
		wantRating   *float64
		wantOpenNow  *bool
		wantSoft     []string
		wantIntent   Intent
	}{
		{
			name:         "chinese hard conditions",
			input:        "曼哈顿中城 4 星以上意大利菜",
			wantBorough:  "manhattan",
			wantNeighbor: "Midtown",
			wantCuisines: []string{"italian"},
			wantRating:   minRating(4),
			wantIntent:   IntentDiscover,
		},
		{
			name:         "english with a price ceiling and a soft word",
			input:        "quiet ramen in Brooklyn under $$",
			wantBorough:  "brooklyn",
			wantCuisines: []string{"ramen"},
			wantPrices:   []int{1, 2},
			wantSoft:     []string{"ambience"},
			wantIntent:   IntentRecommend,
		},
		{
			name:         "soft-led japanese",
			input:        "帮我找适合约会的日料",
			wantCuisines: []string{"japanese", "sushi"},
			wantSoft:     []string{"ambience"},
			wantIntent:   IntentRecommend,
		},
		{
			name:       "bare price symbol",
			input:      "$",
			wantPrices: []int{1},
			wantIntent: IntentDiscover,
		},
		{
			name:       "bare decimal rating",
			input:      "4.5+",
			wantRating: minRating(4.5),
			wantIntent: IntentDiscover,
		},
		{
			name:         "bare neighborhood",
			input:        "中城",
			wantNeighbor: "Midtown",
			wantIntent:   IntentDiscover,
		},
		{
			name:       "empty query",
			input:      "",
			wantIntent: IntentDiscover,
		},
		{
			name:         "borough plus cuisine",
			input:        "布鲁克林的泰国菜",
			wantBorough:  "brooklyn",
			wantCuisines: []string{"thai"},
			wantIntent:   IntentDiscover,
		},
		{
			name:         "soft quiet cafe",
			input:        "想找个安静的咖啡馆",
			wantCuisines: []string{"cafe"},
			wantSoft:     []string{"ambience"},
			wantIntent:   IntentRecommend,
		},
		{
			name:         "soft no queue",
			input:        "不要排队的拉面",
			wantCuisines: []string{"ramen"},
			wantSoft:     []string{"wait"},
			wantIntent:   IntentRecommend,
		},
		{
			name:         "soft service with a price level",
			input:        "服务好的中餐，人均$$",
			wantCuisines: []string{"chinese"},
			wantPrices:   []int{2},
			wantSoft:     []string{"service"},
			wantIntent:   IntentRecommend,
		},
		{
			name:         "soft value",
			input:        "性价比高的墨西哥菜",
			wantCuisines: []string{"mexican"},
			wantSoft:     []string{"value"},
			wantIntent:   IntentRecommend,
		},
		{
			name:         "soft kid friendly",
			input:        "适合带孩子的意大利餐厅",
			wantCuisines: []string{"italian"},
			wantSoft:     []string{"kid_friendly"},
			wantIntent:   IntentRecommend,
		},
		{
			name:         "soft group friendly",
			input:        "适合聚会的火锅",
			wantCuisines: []string{"chinese", "hotpot"},
			wantSoft:     []string{"group_friendly"},
			wantIntent:   IntentRecommend,
		},
		{
			name:         "neighborhood with a price word",
			input:        "上西区的高档法国菜",
			wantNeighbor: "Upper West Side",
			wantCuisines: []string{"french"},
			wantPrices:   []int{3},
			wantIntent:   IntentDiscover,
		},
		{
			name:         "neighborhood with an inclusive ceiling",
			input:        "法拉盛 $$ 以内的中餐",
			wantNeighbor: "Flushing",
			wantCuisines: []string{"chinese"},
			wantPrices:   []int{1, 2},
			wantIntent:   IntentDiscover,
		},
		{
			name:         "bronx decimal rating",
			input:        "布朗克斯 4.5 星以上的披萨",
			wantBorough:  "bronx",
			wantCuisines: []string{"pizza"},
			wantRating:   minRating(4.5),
			wantIntent:   IntentDiscover,
		},
		{
			name:         "queens sichuan",
			input:        "皇后区的川菜",
			wantBorough:  "queens",
			wantCuisines: []string{"chinese", "sichuan"},
			wantIntent:   IntentDiscover,
		},
		{
			name:         "williamsburg vietnamese",
			input:        "威廉斯堡的越南粉",
			wantNeighbor: "Williamsburg",
			wantCuisines: []string{"vietnamese"},
			wantIntent:   IntentDiscover,
		},
		{
			name:         "open now",
			input:        "现在营业的寿司店",
			wantCuisines: []string{"sushi"},
			wantOpenNow:  boolPtr(true),
			wantIntent:   IntentDiscover,
		},
		{
			name:         "near me degrades to an area filter",
			input:        "附近安静的日料",
			wantCuisines: []string{"japanese", "sushi"},
			wantSoft:     []string{"ambience"},
			wantIntent:   IntentRecommend,
		},
		{
			name:         "korean in manhattan",
			input:        "韩国菜 曼哈顿",
			wantBorough:  "manhattan",
			wantCuisines: []string{"korean"},
			wantIntent:   IntentDiscover,
		},
		{
			name:       "no cuisine signal at all",
			input:      "不要辣",
			wantIntent: IntentDiscover,
		},
		{
			name:       "a head count is not a rating",
			input:      "3 人以上",
			wantIntent: IntentDiscover,
		},
		{
			name:         "two soft conditions",
			input:        "环境安静、服务好，适合聚会的川菜",
			wantCuisines: []string{"chinese", "sichuan"},
			wantSoft:     []string{"ambience", "group_friendly", "service"},
			wantIntent:   IntentRecommend,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := rulesOnly().Extract(context.Background(), tc.input, ThreadContext{})
			if err != nil {
				t.Fatalf("Extract(%q) returned an error: %v", tc.input, err)
			}
			if plan.Source != SourceRules {
				t.Fatalf("source = %q, want %q", plan.Source, SourceRules)
			}
			if plan.Intent != tc.wantIntent {
				t.Errorf("intent = %q, want %q", plan.Intent, tc.wantIntent)
			}
			if plan.HardFilters.Borough != tc.wantBorough {
				t.Errorf("borough = %q, want %q", plan.HardFilters.Borough, tc.wantBorough)
			}
			if plan.HardFilters.Neighborhood != tc.wantNeighbor {
				t.Errorf("neighborhood = %q, want %q", plan.HardFilters.Neighborhood, tc.wantNeighbor)
			}
			if !reflect.DeepEqual(plan.HardFilters.Cuisines, tc.wantCuisines) {
				t.Errorf("cuisines = %v, want %v", plan.HardFilters.Cuisines, tc.wantCuisines)
			}
			if !reflect.DeepEqual(plan.HardFilters.PriceLevels, tc.wantPrices) {
				t.Errorf("price levels = %v, want %v", plan.HardFilters.PriceLevels, tc.wantPrices)
			}
			if !equalFloatPtr(plan.HardFilters.MinRating, tc.wantRating) {
				t.Errorf("min rating = %v, want %v", formatFloatPtr(plan.HardFilters.MinRating), formatFloatPtr(tc.wantRating))
			}
			if !equalBoolPtr(plan.HardFilters.OpenNow, tc.wantOpenNow) {
				t.Errorf("open now = %v, want %v", plan.HardFilters.OpenNow, tc.wantOpenNow)
			}
			if !reflect.DeepEqual(plan.SoftTopics(), tc.wantSoft) {
				t.Errorf("soft topics = %v, want %v", plan.SoftTopics(), tc.wantSoft)
			}
			// The invariant the whole milestone rests on: no soft condition may
			// ever reach the hard filter.
			assertNoSoftConditionInFilter(t, plan)
		})
	}
}

// TestEmptyPlanReportsItself pins the one answer that must never be presented as
// a result: a plan that would search for nothing.
func TestEmptyPlanReportsItself(t *testing.T) {
	plan, err := rulesOnly().Extract(context.Background(), "", ThreadContext{})
	if err != nil {
		t.Fatalf("Extract returned an error: %v", err)
	}
	if !plan.IsEmpty() {
		t.Fatalf("an empty query must produce an empty plan, got %+v", plan)
	}
	if plan.Query != "" {
		t.Fatalf("query = %q, want empty", plan.Query)
	}
}

// TestQueryFallsBackToTheUserSentence covers the plan that has conditions but
// no query text of its own: the keyword and vector channels still need the
// user's words, and dropping them would silently narrow the search to the
// structured channel.
func TestQueryFallsBackToTheUserSentence(t *testing.T) {
	plan := BuildPlan("曼哈顿的意大利菜", Extraction{}, SourceModel)
	if plan.Query != "曼哈顿的意大利菜" {
		t.Fatalf("query = %q, want the user sentence", plan.Query)
	}
}

// TestSoftWordsAreStrippedFromHardFilters is the negative path the milestone
// calls out explicitly: a model that files "安静" under cuisines must not have
// that taken at face value.
func TestSoftWordsAreStrippedFromHardFilters(t *testing.T) {
	plan := BuildPlan("安静的意大利餐厅", Extraction{
		Intent:   "discover",
		Cuisines: []string{"italian", "安静", "氛围好"},
	}, SourceModel)

	if got := plan.HardFilters.Cuisines; !reflect.DeepEqual(got, []string{"italian"}) {
		t.Fatalf("cuisines = %v, want only italian", got)
	}
	if got := plan.SoftTopics(); !reflect.DeepEqual(got, []string{review.TopicAmbience}) {
		t.Fatalf("soft topics = %v, want [ambience]", got)
	}
	if len(plan.SoftTexts()) != 2 {
		t.Fatalf("soft texts = %v, want both moved values", plan.SoftTexts())
	}
	if !hasWarningContaining(plan.Warnings, "评论推断条件") {
		t.Fatalf("the move must be recorded, warnings = %v", plan.Warnings)
	}
	assertNoSoftConditionInFilter(t, plan)
}

// TestInvalidBoroughSurvivesToValidation pins the deliberate non-behaviour: an
// unrecognised borough is passed through so the retrieval layer can reject it
// as retrieval_invalid_filter. Dropping it here would turn "that is not a
// borough" into "no restaurants matched", and the two are indistinguishable
// once the value is gone.
func TestInvalidBoroughSurvivesToValidation(t *testing.T) {
	plan := BuildPlan("atlantis 的意大利菜", Extraction{
		Intent:   "discover",
		Borough:  "Atlantis",
		Cuisines: []string{"italian"},
	}, SourceModel)

	if plan.HardFilters.Borough != "atlantis" {
		t.Fatalf("borough = %q, want atlantis to survive normalization", plan.HardFilters.Borough)
	}
	err := plan.HardFilters.Validate()
	if code := errs.CodeOf(err); code != errs.CodeRetrievalInvalidFilter {
		t.Fatalf("Validate() error code = %q, want %q (error: %v)",
			code, errs.CodeRetrievalInvalidFilter, err)
	}
}

// TestNeighborhoodMovedOutOfTheBoroughField covers the model putting a商圈 in
// the borough field. It is a correctable mistake rather than an invalid one:
// the user named a place, and the address filter expresses it exactly.
func TestNeighborhoodMovedOutOfTheBoroughField(t *testing.T) {
	plan := BuildPlan("中城的日料", Extraction{
		Intent:  "discover",
		Borough: "中城",
	}, SourceModel)

	if plan.HardFilters.Borough != "" {
		t.Fatalf("borough = %q, want it cleared", plan.HardFilters.Borough)
	}
	if plan.HardFilters.Neighborhood != "Midtown" {
		t.Fatalf("neighborhood = %q, want Midtown", plan.HardFilters.Neighborhood)
	}
	if !hasWarningContaining(plan.Warnings, "商圈而非行政区") {
		t.Fatalf("the move must be recorded, warnings = %v", plan.Warnings)
	}
}

// TestUnknownSoftTopicIsRederived keeps an unactionable topic out of the recall.
func TestUnknownSoftTopicIsRederived(t *testing.T) {
	plan := BuildPlan("安静一点", Extraction{
		Intent:         "recommend",
		SoftConditions: []SoftCondition{{Text: "安静一点", Topic: "vibes"}},
	}, SourceModel)

	if got := plan.SoftTopics(); !reflect.DeepEqual(got, []string{review.TopicAmbience}) {
		t.Fatalf("soft topics = %v, want the topic re-derived as ambience", got)
	}
}

// TestNearMeIsRecordedAsALimitation covers the honest degradation: with no
// geocoder there is no centre to measure from, so the request narrows the area
// and says so.
func TestNearMeIsRecordedAsALimitation(t *testing.T) {
	plan := BuildPlan("我附近的日料", Extraction{}, SourceRules)
	if !hasWarningContaining(plan.Warnings, "地理编码器") {
		t.Fatalf("warnings = %v, want the proximity limitation", plan.Warnings)
	}
	if !hasWarningContaining(plan.Warnings, "降级") {
		t.Fatalf("warnings = %v, want it described as a degradation", plan.Warnings)
	}
}

// TestExtractFallsBackToRulesWithoutAProvider is the deployment contract: no
// chat channel means rules, not an error.
func TestExtractFallsBackToRulesWithoutAProvider(t *testing.T) {
	plan, err := New(Deps{}).Extract(context.Background(), "布鲁克林的泰国菜", ThreadContext{})
	if err != nil {
		t.Fatalf("Extract returned an error: %v", err)
	}
	if plan.Source != SourceRules {
		t.Fatalf("source = %q, want %q", plan.Source, SourceRules)
	}
	if plan.HardFilters.Borough != "brooklyn" {
		t.Fatalf("borough = %q, want brooklyn", plan.HardFilters.Borough)
	}
	if plan.IsEmpty() {
		t.Fatal("a rule-derived plan must still carry the user's words")
	}
}

// TestExtractFallsBackToRulesOnUnusableOutput covers the model answering with
// something that is not an extraction at all.
func TestExtractFallsBackToRulesOnUnusableOutput(t *testing.T) {
	cases := []struct {
		name     string
		provider *testkit.MockChatProvider
	}{
		{
			name: "provider error",
			provider: &testkit.MockChatProvider{
				Err: errors.New("upstream is down"),
			},
		},
		{
			name: "not json",
			provider: &testkit.MockChatProvider{
				StructuredResponse: domainchat.StructuredResponse{Content: "抱歉，我无法回答。"},
			},
		},
		{
			name: "json without slots",
			provider: &testkit.MockChatProvider{
				StructuredResponse: domainchat.StructuredResponse{Content: "{}"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := New(Deps{Structured: tc.provider}).Extract(
				context.Background(), "布鲁克林的泰国菜", ThreadContext{})
			if err != nil {
				t.Fatalf("Extract must not fail on a model problem: %v", err)
			}
			if plan.Source != SourceRules {
				t.Fatalf("source = %q, want %q", plan.Source, SourceRules)
			}
			if plan.HardFilters.Borough != "brooklyn" {
				t.Fatalf("borough = %q, want the rule path to have recovered it",
					plan.HardFilters.Borough)
			}
			if !hasWarningContaining(plan.Warnings, "未使用模型") {
				t.Fatalf("warnings = %v, want the fallback recorded", plan.Warnings)
			}
		})
	}
}

// TestExtractUsesTheModelWhenItAnswers covers the happy path and the fact that
// the model is asked exactly once per turn.
func TestExtractUsesTheModelWhenItAnswers(t *testing.T) {
	payload := Extraction{
		Intent:         "recommend",
		Query:          "安静点的意餐",
		Borough:        "曼哈顿",
		Cuisines:       []string{"意餐"},
		PriceLevels:    []int{3},
		SoftConditions: []SoftCondition{{Text: "安静点", Topic: review.TopicAmbience}},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	provider := &testkit.MockChatProvider{
		StructuredResponse: domainchat.StructuredResponse{Content: string(encoded)},
	}

	plan, err := New(Deps{Structured: provider}).Extract(context.Background(), "安静点的意餐", ThreadContext{})
	if err != nil {
		t.Fatalf("Extract returned an error: %v", err)
	}
	if plan.Source != SourceModel {
		t.Fatalf("source = %q, want %q", plan.Source, SourceModel)
	}
	if len(provider.StructuredCalls) != 1 {
		t.Fatalf("structured calls = %d, want exactly 1 per turn", len(provider.StructuredCalls))
	}
	if plan.HardFilters.Borough != "manhattan" {
		t.Errorf("borough = %q, want the model's 曼哈顿 canonicalized", plan.HardFilters.Borough)
	}
	if !reflect.DeepEqual(plan.HardFilters.Cuisines, []string{"italian"}) {
		t.Errorf("cuisines = %v, want the model's 意餐 canonicalized", plan.HardFilters.Cuisines)
	}
	if plan.Intent != IntentRecommend {
		t.Errorf("intent = %q, want recommend", plan.Intent)
	}
	assertNoSoftConditionInFilter(t, plan)
}

// TestExtractIncludesPendingStateInThePrompt keeps a follow-up readable: without
// the open slot the model cannot tell a new question from an answer to the last
// one.
func TestExtractIncludesPendingStateInThePrompt(t *testing.T) {
	provider := &testkit.MockChatProvider{
		StructuredResponse: domainchat.StructuredResponse{Content: `{"intent":"discover","query":"第二家"}`},
	}
	pending := &conversation.Checkpoint{
		State:         conversation.StateAwaitingClarification,
		PendingAction: "resolve_restaurant",
		MissingSlots:  []string{SlotRestaurantID},
	}
	plan, err := New(Deps{Structured: provider}).Extract(
		context.Background(), "第二家安静吗", ThreadContext{Pending: pending})
	if err != nil {
		t.Fatalf("Extract returned an error: %v", err)
	}
	if len(provider.StructuredCalls) != 1 {
		t.Fatalf("structured calls = %d, want 1", len(provider.StructuredCalls))
	}
	var transcript strings.Builder
	for _, message := range provider.StructuredCalls[0].Messages {
		transcript.WriteString(message.Content)
		transcript.WriteString("\n")
	}
	if !strings.Contains(transcript.String(), SlotRestaurantID) {
		t.Fatalf("the open slot must reach the model, prompt was:\n%s", transcript.String())
	}
	// The slot was not filled by "第二家", so the thread stays open.
	if !plan.NeedClarification {
		t.Fatal("an unfilled pending slot must keep the thread asking")
	}
	if !reflect.DeepEqual(plan.MissingSlots, []string{SlotRestaurantID}) {
		t.Fatalf("missing slots = %v, want [%s]", plan.MissingSlots, SlotRestaurantID)
	}
}

// TestPendingSlotIsClearedWhenFilled is the other half: once the user supplies
// what was missing, the thread stops asking.
func TestPendingSlotIsClearedWhenFilled(t *testing.T) {
	plan := BuildPlan("意大利菜", Extraction{Intent: "discover", Cuisines: []string{"italian"}}, SourceRules)
	applyPendingContext(&plan, &conversation.Checkpoint{MissingSlots: []string{SlotCuisine}})
	if plan.NeedClarification {
		t.Fatalf("a filled slot must clear the clarification, plan = %+v", plan)
	}
	if len(plan.MissingSlots) != 0 {
		t.Fatalf("missing slots = %v, want none", plan.MissingSlots)
	}
}

// TestExtractHonoursContextCancellation is the only reason Extract returns an
// error at all: a canceled turn must stop rather than search on borrowed time.
func TestExtractHonoursContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := rulesOnly().Extract(ctx, "意大利菜", ThreadContext{}); err == nil {
		t.Fatal("a canceled context must be reported")
	}
}

// TestExtractionSchemaIsValidAndTracksTheTopicVocabulary keeps the model-facing
// schema from drifting away from the shared domain vocabulary. A topic the
// schema offers but the domain does not know is a filter that matches nothing.
func TestExtractionSchemaIsValidAndTracksTheTopicVocabulary(t *testing.T) {
	raw := ExtractionSchema()
	var schema struct {
		Required             []string       `json:"required"`
		AdditionalProperties bool           `json:"additionalProperties"`
		Properties           map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if schema.AdditionalProperties {
		t.Fatal("additionalProperties must be false for a strict schema")
	}
	// Every property must be required: a strict-schema provider rejects a
	// schema with optional properties outright, and a rejected schema would push
	// every extraction onto the rule path silently.
	for name := range schema.Properties {
		if !containsString(schema.Required, name) {
			t.Errorf("property %q is not listed in required", name)
		}
	}
	for _, name := range schema.Required {
		if _, ok := schema.Properties[name]; !ok {
			t.Errorf("required names %q which has no property", name)
		}
	}

	soft, ok := schema.Properties["soft_conditions"].(map[string]any)
	if !ok {
		t.Fatal("soft_conditions is missing from the schema")
	}
	items, ok := soft["items"].(map[string]any)
	if !ok {
		t.Fatal("soft_conditions has no item schema")
	}
	props, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatal("soft_conditions items have no properties")
	}
	topic, ok := props["topic"].(map[string]any)
	if !ok {
		t.Fatal("soft_conditions items have no topic")
	}
	enum, ok := topic["enum"].([]any)
	if !ok {
		t.Fatal("topic is not an enum")
	}
	got := make([]string, 0, len(enum))
	for _, value := range enum {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("topic enum contains a non-string: %v", value)
		}
		got = append(got, text)
	}
	if !reflect.DeepEqual(got, review.CanonicalTopics()) {
		t.Fatalf("topic enum = %v, want the shared vocabulary %v", got, review.CanonicalTopics())
	}
}

// TestInstructionSeparatesHardFromSoft guards the prompt itself. The boundary is
// enforced in code, but the prompt is what keeps the model from producing
// nonsense that the code then has to undo.
func TestInstructionSeparatesHardFromSoft(t *testing.T) {
	text := Instruction()
	for _, required := range []string{"hard conditions", "Soft conditions", "ambience", "quiet", "reviews"} {
		if !strings.Contains(text, required) {
			t.Errorf("the extraction prompt must mention %q", required)
		}
	}
}

// TestFilterMatchesRejectsASoftOnlyCandidate is the slice-level invariant in
// miniature: a candidate recalled by a soft channel must still satisfy every
// hard condition, and a candidate missing the data cannot pass by default.
func TestFilterMatchesRejectsASoftOnlyCandidate(t *testing.T) {
	plan, err := rulesOnly().Extract(context.Background(), "曼哈顿 4 星以上的意大利菜", ThreadContext{})
	if err != nil {
		t.Fatalf("Extract returned an error: %v", err)
	}
	rating := 4.6
	lowRating := 3.2

	passing := search.RestaurantCandidate{
		RestaurantID: 1, Name: "Trattoria", Borough: "manhattan",
		Cuisines: []string{"italian"}, Rating: &rating,
	}
	if !plan.FilterMatches(passing) {
		t.Fatal("a candidate satisfying every hard condition must match")
	}
	for name, candidate := range map[string]search.RestaurantCandidate{
		"wrong borough": {
			RestaurantID: 2, Borough: "brooklyn", Cuisines: []string{"italian"}, Rating: &rating,
		},
		"low rating": {
			RestaurantID: 3, Borough: "manhattan", Cuisines: []string{"italian"}, Rating: &lowRating,
		},
		"unknown rating": {
			RestaurantID: 4, Borough: "manhattan", Cuisines: []string{"italian"},
		},
		"wrong cuisine": {
			RestaurantID: 5, Borough: "manhattan", Cuisines: []string{"pizza"}, Rating: &rating,
		},
	} {
		if plan.FilterMatches(candidate) {
			t.Errorf("%s must not match", name)
		}
	}
}

// TestConfigDefaultsAreApplied keeps a partially filled configuration from
// disabling a bound.
func TestConfigDefaultsAreApplied(t *testing.T) {
	extractor := New(Deps{Config: Config{MaxClarifications: 7}})
	cfg := extractor.Config()
	if cfg.MaxClarifications != 7 {
		t.Errorf("MaxClarifications = %d, want the supplied 7", cfg.MaxClarifications)
	}
	if cfg.ResolveMinSimilarity != DefaultResolveMinSimilarity {
		t.Errorf("ResolveMinSimilarity = %v, want the default", cfg.ResolveMinSimilarity)
	}
	if cfg.Timeout <= 0 {
		t.Error("Timeout must have a default")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// assertNoSoftConditionInFilter is the invariant M5-02 states as "软条件绝不进
// RestaurantFilter", checked structurally rather than by inspection.
func assertNoSoftConditionInFilter(t *testing.T, plan Plan) {
	t.Helper()
	soft := make(map[string]struct{})
	for _, condition := range plan.SoftConditions {
		soft[normalizeKey(condition.Text)] = struct{}{}
	}
	if len(soft) == 0 {
		return
	}
	filter := plan.HardFilters
	candidates := append([]string{filter.Borough, filter.Neighborhood}, filter.Cuisines...)
	for _, value := range candidates {
		if _, leaked := soft[normalizeKey(value)]; leaked {
			t.Fatalf("soft condition %q leaked into the hard filter: %+v", value, filter)
		}
	}
}

func hasWarningContaining(warnings []string, fragment string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, fragment) {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func boolPtr(v bool) *bool { return &v }

func equalBoolPtr(got, want *bool) bool {
	if got == nil || want == nil {
		return got == want
	}
	return *got == *want
}

func equalFloatPtr(got, want *float64) bool {
	if got == nil || want == nil {
		return got == want
	}
	return *got == *want
}

func formatFloatPtr(value *float64) string {
	if value == nil {
		return "<nil>"
	}
	return strconv.FormatFloat(*value, 'g', -1, 64)
}
