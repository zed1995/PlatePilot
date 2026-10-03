package slots

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	chatport "github.com/zed1995/platepilot/shared/chat"
	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/conversation"
)

// DefaultTimeout bounds one structured extraction call.
//
// It is shorter than the chat provider's own timeout on purpose: extraction is
// an optional refinement of a turn that can proceed without it, and a user
// waiting on a model that is thinking about a slot list should be handed the
// rule-derived plan instead.
const DefaultTimeout = 10 * time.Second

// Defaults for the resolution knobs M5-03 consumes. They live here so that the
// whole "how the agent interprets language" surface has one configuration
// source, rather than one for extraction and another for disambiguation.
const (
	DefaultMaxClarifications    = 3
	DefaultResolveMinSimilarity = 0.55
	DefaultResolveAmbiguityGap  = 0.10
)

// Config holds the extraction and interpretation knobs.
type Config struct {
	// MaxClarifications bounds how many turns may be spent asking before the
	// agent proceeds on its best assumption.
	MaxClarifications int
	// ResolveMinSimilarity is the name-match similarity above which a named
	// restaurant is considered found.
	ResolveMinSimilarity float64
	// ResolveAmbiguityGap is the top1/top2 similarity difference below which a
	// match is considered ambiguous and the user is asked.
	ResolveAmbiguityGap float64
	// Timeout bounds one structured extraction call.
	Timeout time.Duration
}

// DefaultConfig returns the interpretation defaults.
func DefaultConfig() Config {
	return Config{
		MaxClarifications:    DefaultMaxClarifications,
		ResolveMinSimilarity: DefaultResolveMinSimilarity,
		ResolveAmbiguityGap:  DefaultResolveAmbiguityGap,
		Timeout:              DefaultTimeout,
	}
}

// withDefaults fills any zero value with its default, so a partially populated
// config cannot silently disable a bound.
func (c Config) withDefaults() Config {
	defaults := DefaultConfig()
	if c.MaxClarifications <= 0 {
		c.MaxClarifications = defaults.MaxClarifications
	}
	if c.ResolveMinSimilarity <= 0 {
		c.ResolveMinSimilarity = defaults.ResolveMinSimilarity
	}
	if c.ResolveAmbiguityGap <= 0 {
		c.ResolveAmbiguityGap = defaults.ResolveAmbiguityGap
	}
	if c.Timeout <= 0 {
		c.Timeout = defaults.Timeout
	}
	return c
}

// Deps builds an Extractor. Structured may be nil: the extractor then runs
// entirely on rules, which is a supported deployment rather than a degraded one.
type Deps struct {
	Structured chatport.StructuredOutputProvider
	Model      string
	Config     Config
}

// Extractor turns one user message into a Plan.
//
// The zero value is not usable; construct it with New. It holds no mutable
// state and is safe to share.
type Extractor struct {
	structured chatport.StructuredOutputProvider
	model      string
	cfg        Config
}

// New builds an extractor.
func New(deps Deps) *Extractor {
	return &Extractor{
		structured: deps.Structured,
		model:      deps.Model,
		cfg:        deps.Config.withDefaults(),
	}
}

// Config exposes the resolved configuration so the runner and the interpret
// endpoint read one set of knobs.
func (e *Extractor) Config() Config { return e.cfg }

// Extract interprets one user message.
//
// The returned error is never a modelling or parsing failure: every one of
// those degrades to a rule-derived plan, because refusing to answer because a
// slot could not be extracted would turn a partial understanding into no
// service at all. The error exists for one case only — the caller's context
// ended — so a canceled turn stops instead of searching on borrowed time.
// ThreadContext is what a thread already knows before this turn's message is
// read.
//
// It is a struct rather than more parameters because the two halves travel
// together: the checkpoint says what the thread is waiting for, and the
// candidate snapshot says what it is talking about, and a turn that read one
// without the other would answer a follow-up against a list it cannot name.
type ThreadContext struct {
	// Pending is the newest checkpoint, or nil for a fresh thread.
	Pending *conversation.Checkpoint
	// Candidates is the thread's current candidate snapshot, in position order.
	Candidates []conversation.Candidate
}

// SelectedRestaurantID is the restaurant the thread already pinned, or zero.
func (t ThreadContext) SelectedRestaurantID() int64 {
	if t.Pending == nil {
		return 0
	}
	return t.Pending.SelectedRestaurantID
}

// Extract turns one message into a plan.
func (e *Extractor) Extract(
	ctx context.Context, userInput string, thread ThreadContext,
) (Plan, error) {
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	started := time.Now()

	extraction, source, note := e.chooseExtraction(ctx, userInput, thread.Pending)
	plan := BuildPlan(userInput, extraction, source)
	if note != "" {
		plan.Warn(note)
	}
	// A reference is resolved before the pending slots are reconciled, because
	// "第二家" is exactly what fills a missing restaurant_id: the checkpoint asks
	// for one, and the candidate snapshot is where the answer comes from.
	applyReference(&plan, userInput, thread)
	plan.ExtractLatencyMS = time.Since(started).Milliseconds()
	applyPendingContext(&plan, thread.Pending)
	return plan, nil
}

// applyReference folds a referring phrase into the plan.
//
// The resolved id goes on the plan rather than into a filter: it is a scope for
// the turn's evidence reads, and a filter by name would turn a near-miss into
// "no such restaurant" instead of a question.
func applyReference(plan *Plan, userInput string, thread ThreadContext) {
	if len(thread.Candidates) == 0 {
		return
	}
	reference, ok := ResolveReference(userInput, thread.Candidates, thread.SelectedRestaurantID())
	if !ok {
		return
	}
	if reference.RestaurantID == 0 {
		// Recognised but unsatisfiable. Saying so is better than picking the
		// nearest position: the user asked about a restaurant, not about a
		// number, and an answer about the wrong one is indistinguishable from a
		// right answer.
		plan.Warn(reference.Note)
		return
	}
	plan.SelectedRestaurantID = reference.RestaurantID
	plan.ReferenceNote = reference.Note
}

// chooseExtraction runs the model path and falls back to rules.
//
// The fallback is not an error path that happens to work; it is the contract.
// A deployment without a chat provider, a provider that is down, a response
// that is not JSON, and a response that is JSON but not an extraction all end
// the same way: the user's own sentence becomes the query, and the plan says
// so. The third return is the note that fallback wants on the plan.
func (e *Extractor) chooseExtraction(
	ctx context.Context, userInput string, pending *conversation.Checkpoint,
) (Extraction, string, string) {
	if e.structured == nil {
		return e.rulesExtraction(userInput), SourceRules, ""
	}
	extraction, err := e.modelExtraction(ctx, userInput, pending)
	if err != nil {
		note := "槽位抽取未使用模型（" + err.Error() + "），已按规则解析"
		return e.rulesExtraction(userInput), SourceRules, note
	}
	return extraction, SourceModel, ""
}

// modelExtraction asks the structured-output model for a slot list.
func (e *Extractor) modelExtraction(
	ctx context.Context, userInput string, pending *conversation.Checkpoint,
) (Extraction, error) {
	messages := []domainchat.ChatMessage{
		{Role: domainchat.RoleSystem, Content: extractionInstruction},
	}
	if context := pendingContext(pending); context != "" {
		messages = append(messages, domainchat.ChatMessage{
			Role:    domainchat.RoleSystem,
			Content: context,
		})
	}
	messages = append(messages, domainchat.ChatMessage{
		Role:    domainchat.RoleUser,
		Content: userInput,
	})

	callCtx, cancel := context.WithTimeout(ctx, e.cfg.Timeout)
	defer cancel()

	resp, err := e.structured.CompleteStructured(callCtx, domainchat.StructuredRequest{
		Messages: messages,
		Model:    e.model,
		Metadata: map[string]string{"task": "slot_extraction"},
	}, json.RawMessage(extractionSchema))
	if err != nil {
		return Extraction{}, err
	}
	var extraction Extraction
	if err := json.Unmarshal([]byte(resp.Content), &extraction); err != nil {
		return Extraction{}, err
	}
	// An empty content is valid JSON only by accident; an explicit check keeps
	// "the model answered with nothing" out of the same bucket as "the model
	// answered".
	if extraction.Intent == "" && extraction.Query == "" && len(extraction.Cuisines) == 0 &&
		len(extraction.PriceLevels) == 0 && extraction.MinRating == nil &&
		extraction.OpenNow == nil && extraction.Borough == "" &&
		extraction.Neighborhood == "" && len(extraction.SoftConditions) == 0 &&
		len(extraction.NamedRestaurants) == 0 {
		return Extraction{}, errEmptyExtraction
	}
	return extraction, nil
}

// errEmptyExtraction marks a response that parsed but carried no slot at all.
var errEmptyExtraction = errorString("model returned an extraction with no slots and no query")

type errorString string

func (e errorString) Error() string { return string(e) }

// rulesExtraction parses the sentence deterministically.
//
// It is deliberately conservative: every value it produces comes from a closed
// vocabulary, so a rule-derived plan can never contain a value the corpus does
// not understand. Everything it cannot recognize stays in Query, where the
// keyword and vector channels can still use it.
func (e *Extractor) rulesExtraction(userInput string) Extraction {
	extraction := Extraction{
		Query:            userInput,
		NamedRestaurants: scanQuotedNames(userInput),
	}
	if borough, _, ok := scanBorough(userInput); ok {
		extraction.Borough = borough
	}
	if neighborhood, _, ok := scanNeighborhood(userInput); ok {
		extraction.Neighborhood = neighborhood
	}
	extraction.Cuisines = scanCuisines(userInput)
	if levels, ok := scanPriceLevels(userInput); ok {
		extraction.PriceLevels = levels
	}
	extraction.MinRating = scanMinRating(userInput)
	extraction.OpenNow = scanOpenNow(userInput)
	extraction.SoftConditions = scanSoftConditions(userInput)
	extraction.Intent = string(classifyIntent(userInput, extraction))
	return extraction
}

// classifyIntent routes a turn from what was actually recognized.
func classifyIntent(userInput string, extraction Extraction) Intent {
	lowered := strings.ToLower(userInput)
	switch {
	case hasAny(lowered, reservationKeywords):
		return IntentReservation
	case hasAny(lowered, questionKeywords):
		return IntentRestaurantQA
	case hasAny(lowered, chitChatKeywords) && len(extraction.SoftConditions) == 0 &&
		len(extraction.Cuisines) == 0 && extraction.Borough == "":
		return IntentChitChat
	}
	// A stated soft condition makes the turn a recommendation regardless of how
	// many hard conditions accompany it: the soft condition is the part the
	// ranking has to interpret, and that is the routing decision that matters.
	if len(extraction.SoftConditions) > 0 {
		return IntentRecommend
	}
	return IntentDiscover
}

var (
	reservationKeywords = []string{"预约", "订位", "订座", "预订", "定位", "book a table", "reservation", "reserve"}
	questionKeywords    = []string{"怎么样", "怎么样？", "如何", "好不好", "评价", "推荐菜", "招牌菜", "几点", "营业时间", "在哪儿", "在哪里", "地址", "how is", "what about"}
	chitChatKeywords    = []string{"你好", "您好", "谢谢", "多谢", "在吗", "早上好", "晚上好", "hello", "hi ", "thanks", "thank you"}
)

// scanQuotedNames extracts names the user put in quotes.
//
// It is intentionally narrow. Without a named-entity model, guessing that a
// capitalized phrase is a restaurant name would misresolve ordinary words, and
// a wrong resolution is worse than none: it answers about a restaurant the user
// did not ask about. Quoting is an explicit signal, so it is the only one the
// rules path trusts.
func scanQuotedNames(text string) []string {
	pairs := [][2]string{
		{"「", "」"}, {"『", "』"}, {"《", "》"}, {`"`, `"`}, {"“", "”"}, {"'", "'"},
	}
	var names []string
	for _, pair := range pairs {
		rest := text
		for {
			open := strings.Index(rest, pair[0])
			if open < 0 {
				break
			}
			after := rest[open+len(pair[0]):]
			closeAt := strings.Index(after, pair[1])
			if closeAt < 0 {
				break
			}
			name := strings.TrimSpace(after[:closeAt])
			if name != "" {
				names = append(names, name)
			}
			rest = after[closeAt+len(pair[1]):]
		}
	}
	return names
}

// pendingContext renders the recoverable thread state for the model.
//
// A follow-up like "第二家安静吗" is unreadable without it, and a clarification
// turn is unanswerable without knowing which slot is still open. Passing it as
// a system message keeps the extraction stateless from the model's perspective
// while still letting one turn depend on the last.
func pendingContext(pending *conversation.Checkpoint) string {
	if pending == nil {
		return ""
	}
	var parts []string
	if pending.State != "" {
		parts = append(parts, "线程当前状态："+string(pending.State))
	}
	if len(pending.MissingSlots) > 0 {
		parts = append(parts, "上一轮尚缺的槽位："+strings.Join(pending.MissingSlots, ", "))
	}
	if pending.PendingAction != "" {
		parts = append(parts, "尚未完成的动作："+pending.PendingAction)
	}
	if pending.SelectedRestaurantID != 0 {
		parts = append(parts, "上一轮锁定的餐厅 id："+itoa(pending.SelectedRestaurantID))
	}
	if len(parts) == 0 {
		return ""
	}
	return "会话上下文（供理解指代使用，不是本轮的新条件）：\n" + strings.Join(parts, "\n")
}

// applyPendingContext carries what the previous turn left open into this plan.
//
// A slot the new message filled is dropped from the missing set; anything left
// over keeps the thread in its pending state. Resolving a reference is not done
// here — that is a deterministic pass over the persisted candidates (M5-05) —
// but noting that the thread was already waiting is what lets the graph decide
// between asking again and continuing.
func applyPendingContext(plan *Plan, pending *conversation.Checkpoint) {
	if pending == nil || len(pending.MissingSlots) == 0 {
		return
	}
	missing := make([]string, 0, len(pending.MissingSlots))
	for _, slot := range pending.MissingSlots {
		if slotFilled(plan, slot) {
			continue
		}
		missing = append(missing, slot)
	}
	if len(missing) == 0 {
		return
	}
	plan.MissingSlots = normalizeSlots(append(plan.MissingSlots, missing...))
	plan.NeedClarification = true
}

// slotFilled reports whether a plan now supplies a previously missing slot.
func slotFilled(plan *Plan, slot string) bool {
	switch slot {
	case SlotBorough:
		return plan.HardFilters.Borough != ""
	case SlotCuisine:
		return len(plan.HardFilters.Cuisines) > 0
	case SlotPriceLevel:
		return len(plan.HardFilters.PriceLevels) > 0
	case SlotRestaurantID:
		return plan.SelectedRestaurantID != 0 || len(plan.NamedRestaurants) > 0
	default:
		return false
	}
}

func itoa(value int64) string { return strconv.FormatInt(value, 10) }
