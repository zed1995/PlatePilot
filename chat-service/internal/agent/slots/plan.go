// Package slots turns one natural-language user message into a structured,
// executable plan: the intent, the hard filters a search can enforce
// deterministically, and the soft conditions only reviews can support.
//
// It is the single place that decides "what did the user actually ask for", and
// every later M5 slice consumes its output. That makes one property
// non-negotiable: the decision has to be inspectable and reproducible without a
// model. Whenever the configured chat provider is missing, unreachable, or
// returns something unusable, Extract still returns a plan whose Source is
// "rules" and whose query text goes through the keyword and vector channels. A
// slot that could not be extracted is a smaller plan, never an error and never
// a 500 — a plan that cannot be built is a plan the user never gets a
// restaurant from.
//
// The hard/soft split is a type boundary rather than a convention. Soft
// conditions live in their own field and never enter search.RestaurantFilter,
// so a soft condition that leaked into the hard filter would be a compile-time
// shape mismatch at the point of use rather than a silently empty result.
package slots

import (
	"context"
	"strings"

	"github.com/zed1995/platepilot/shared/domain/retrieval"
	"github.com/zed1995/platepilot/shared/domain/search"
)

// Intent is what the user is trying to do this turn. The set is closed: it
// drives routing, and an unknown value has no route.
type Intent string

const (
	// IntentDiscover looks for restaurants, led by hard conditions.
	IntentDiscover Intent = "discover"
	// IntentRecommend looks for restaurants, led by soft conditions ("安静").
	IntentRecommend Intent = "recommend"
	// IntentRestaurantQA asks about a restaurant the user named.
	IntentRestaurantQA Intent = "restaurant_qa"
	// IntentReservation books a table. It is only actionable when the
	// reservation surface is switched on.
	IntentReservation Intent = "reservation"
	// IntentChitChat needs no restaurant tools at all.
	IntentChitChat Intent = "chit_chat"
)

// DefaultIntent is the intent a plan falls back to when nothing else can be
// established. It is deliberately the one that still searches: a misread
// question that returns candidates is recoverable, while a misread question
// that returns nothing looks like the corpus is empty.
const DefaultIntent = IntentDiscover

var allIntents = []Intent{
	IntentDiscover, IntentRecommend, IntentRestaurantQA, IntentReservation, IntentChitChat,
}

// AllIntents lists every intent in a stable order.
func AllIntents() []Intent {
	out := make([]Intent, len(allIntents))
	copy(out, allIntents)
	return out
}

// Valid reports whether the intent is one of the known ones.
func (i Intent) Valid() bool {
	for _, candidate := range allIntents {
		if candidate == i {
			return true
		}
	}
	return false
}

// Extraction sources, recorded on every plan so an offline check can tell a
// model-derived plan from a rule-derived one without re-running anything.
const (
	// SourceModel means the structured-output model produced the extraction.
	SourceModel = "model"
	// SourceRules means the deterministic parser did, because no model was
	// available or its output was unusable.
	SourceRules = "rules"
)

// Slot names reported in Plan.MissingSlots. The set is closed so a caller can
// switch on it without defending against arbitrary strings.
const (
	SlotBorough      = "borough"
	SlotCuisine      = "cuisine"
	SlotPriceLevel   = "price_level"
	SlotDate         = "date"
	SlotPartySize    = "party_size"
	SlotRestaurantID = "restaurant_id"
)

// SoftCondition is a user requirement that no column can answer.
//
// It is an alias rather than a second declaration: the same pair has to reach
// the retrieval request, the ranking trace, and the answer prompt, and two
// structurally identical types on either side of that path would need a copy at
// every hop. The type is declared beside the request field it feeds, and the
// name is kept here because this is the layer that produces it.
type SoftCondition = retrieval.SoftCondition

// Plan is one turn's executable interpretation of the user's message.
type Plan struct {
	Intent Intent `json:"intent"`
	// Query is the free text handed to the keyword and vector channels. It
	// survives extraction intact so that even a plan with no slots at all still
	// searches for something.
	Query string `json:"query,omitempty"`

	// HardFilters holds only deterministic conditions. Soft conditions live in
	// SoftConditions and must never be copied here.
	HardFilters search.RestaurantFilter `json:"hard_filters"`

	SoftConditions []SoftCondition `json:"soft_conditions,omitempty"`

	// NamedRestaurants are the restaurants the user referred to by name. They
	// are not filters: resolving a name to an id is a separate, disambiguating
	// step, and filtering by a typed name would turn a near-miss into "no such
	// restaurant" instead of a clarification question.
	NamedRestaurants []string `json:"named_restaurants,omitempty"`

	// SelectedRestaurantID is set by reference resolution (M5-05), not by
	// extraction. Zero means the turn did not pin a single restaurant.
	SelectedRestaurantID int64 `json:"selected_restaurant_id,omitempty"`
	// ReferenceNote explains how a reference was resolved, in the user's
	// language, so an answer can say which restaurant it understood.
	ReferenceNote string `json:"reference_note,omitempty"`

	// MissingSlots names what would have to be supplied to answer, and
	// NeedClarification asks the graph to stop and ask rather than guess.
	MissingSlots      []string `json:"missing_slots,omitempty"`
	NeedClarification bool     `json:"need_clarification"`

	// Source records whether the model or the rules produced this plan.
	Source string `json:"source"`
	// ExtractLatencyMS is how long extraction took, so M5-04 can account for it.
	ExtractLatencyMS int64 `json:"extract_latency_ms,omitempty"`
	// Warnings records every degradation the extraction had to make.
	Warnings []string `json:"warnings,omitempty"`
}

// Warn appends a degradation note, keeping the first occurrence only.
func (p *Plan) Warn(message string) {
	p.Warnings = appendWarning(p.Warnings, message)
}

// Warnf appends a formatted degradation note.
func (p *Plan) Warnf(format string, args ...any) {
	p.Warn(formatWarning(format, args...))
}

// HasHardFilters reports whether the plan constrains anything deterministically.
func (p Plan) HasHardFilters() bool { return !p.HardFilters.IsEmpty() }

// IsEmpty reports whether the plan would search for nothing at all. It is what
// lets a caller refuse a turn that has neither text nor a condition, instead of
// presenting the corpus in prior order as an answer.
func (p Plan) IsEmpty() bool {
	return strings.TrimSpace(p.Query) == "" && p.HardFilters.IsEmpty() &&
		len(p.SoftConditions) == 0 && len(p.NamedRestaurants) == 0
}

// FilterMatches reports whether a candidate satisfies every hard condition of
// the plan. It exists so a slice-level test can assert the invariant the whole
// milestone rests on — every returned candidate satisfies every hard condition
// — using the same code the fusion layer uses.
func (p Plan) FilterMatches(candidate search.RestaurantCandidate) bool {
	return p.HardFilters.Matches(candidate)
}

// SoftTopics returns the distinct review topics the plan asked about, sorted.
// Callers use it to label results ("评论推断：安静（ambience）") without walking
// the condition list themselves. A plan with no soft condition returns nil, so
// "asked for nothing" and "asked for an empty list" do not look different.
func (p Plan) SoftTopics() []string {
	seen := make(map[string]struct{}, len(p.SoftConditions))
	var out []string
	for _, condition := range p.SoftConditions {
		if condition.Topic == "" {
			continue
		}
		if _, ok := seen[condition.Topic]; ok {
			continue
		}
		seen[condition.Topic] = struct{}{}
		out = append(out, condition.Topic)
	}
	sortStrings(out)
	return out
}

// SoftTexts returns the distinct user phrases behind the soft conditions, in
// the order the user wrote them.
func (p Plan) SoftTexts() []string {
	seen := make(map[string]struct{}, len(p.SoftConditions))
	var out []string
	for _, condition := range p.SoftConditions {
		text := strings.TrimSpace(condition.Text)
		if text == "" {
			continue
		}
		if _, ok := seen[text]; ok {
			continue
		}
		seen[text] = struct{}{}
		out = append(out, text)
	}
	return out
}

// ---------------------------------------------------------------------------
// Per-turn plan, carried on the context
// ---------------------------------------------------------------------------

// planKey is the private context key for the turn's plan.
type planKey struct{}

// WithPlan attaches the turn's plan to a context.
//
// The plan travels on the context rather than as a rewritten tool argument
// because of where it has to be applied. A tool's arguments are validated inside
// the registry's Invoke before any handler sees them, and a tool whose arguments
// were rewritten first would let a malformed model call be repaired into a valid
// one instead of being reported. Carrying the plan alongside the call keeps that
// ordering intact: the model's arguments are validated exactly as produced, and
// only then does the tool fill in what the model left out.
func WithPlan(ctx context.Context, plan Plan) context.Context {
	return context.WithValue(ctx, planKey{}, plan)
}

// PlanFromContext returns the turn's plan, if one was attached.
func PlanFromContext(ctx context.Context) (Plan, bool) {
	plan, ok := ctx.Value(planKey{}).(Plan)
	return plan, ok
}
