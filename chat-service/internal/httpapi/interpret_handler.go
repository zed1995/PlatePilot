package httpapi

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/zed1995/platepilot/chat-service/internal/httperr"
	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/search"
)

// SlotInterpreter is the read-only slot-extraction surface.
//
// It exists so the understanding layer can be exercised on its own. The point is
// not convenience: without it, the only way to check what the agent thinks a
// sentence means is to run a whole turn — model, tools, retrieval, composition —
// and then infer the interpretation from a final answer. A layer whose
// correctness decides every downstream slice deserves to be observable on its
// own, and a debugging client should be able to ask "what did you understand?"
// without creating a thread or spending a tool call.
type SlotInterpreter interface {
	Interpret(ctx context.Context, text string) (InterpretResult, error)
}

// InterpretRequest is the inbound body of an interpretation.
type InterpretRequest struct {
	Text string `json:"text"`
}

// Validate rejects an empty sentence.
//
// Unlike a search, an interpretation has no degraded form worth returning: a
// plan built from nothing is the empty plan, and answering with it would look
// exactly like a successfully understood question that happened to constrain
// nothing. The length bound is the same one a chat message carries, so a client
// cannot use this endpoint to probe a size the chat surface would refuse.
func (r InterpretRequest) Validate() error {
	text := strings.TrimSpace(r.Text)
	if text == "" {
		return errs.New(errs.CodeInvalidArgument, "text must not be empty")
	}
	if utf8.RuneCountInString(text) > maxMessageRunes {
		return errs.Newf(errs.CodeInvalidArgument,
			"text must be at most %d characters", maxMessageRunes)
	}
	return nil
}

// InterpretSoftCondition is one requirement that only reviews can support.
//
// Text is the user's own phrase so the condition can be quoted back; Topic is
// the review topic it maps onto, which is the part a recall can act on.
type InterpretSoftCondition struct {
	Text  string `json:"text"`
	Topic string `json:"topic"`
}

// InterpretResult is the plan projection this transport returns.
//
// It mirrors the agent's plan field for field rather than re-deriving anything,
// because a projection that computed its own answer could disagree with the one
// the turn actually used. HardFilters is the deterministic filter, and it is a
// separate field from SoftConditions on purpose: a client that merged the two
// would reproduce the exact confusion this layer exists to prevent.
type InterpretResult struct {
	Intent               string                   `json:"intent"`
	Query                string                   `json:"query,omitempty"`
	HardFilters          search.RestaurantFilter  `json:"hard_filters"`
	SoftConditions       []InterpretSoftCondition `json:"soft_conditions"`
	NamedRestaurants     []string                 `json:"named_restaurants"`
	SelectedRestaurantID int64                    `json:"selected_restaurant_id,omitempty"`
	MissingSlots         []string                 `json:"missing_slots"`
	NeedClarification    bool                     `json:"need_clarification"`
	// Source is "model" or "rules". It is part of the response rather than a log
	// line because whether the understanding came from the model is the first
	// thing a reader checking an odd result needs to know.
	Source string `json:"source"`
	// ExtractLatencyMS makes the extraction visible as a cost, which is the
	// first number M6-05 will need.
	ExtractLatencyMS int64    `json:"extract_latency_ms,omitempty"`
	Warnings         []string `json:"warnings,omitempty"`
}

// InterpretHandler answers POST /v1/restaurants/interpret.
//
// The route is read-only in the strongest sense: it creates no thread, writes no
// row, and spends no tool call. That is what makes it usable as a probe.
func InterpretHandler(service SlotInterpreter) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		var body InterpretRequest
		if err := BindAndValidate(c, &body); err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		result, err := service.Interpret(ctx, strings.TrimSpace(body.Text))
		if err != nil {
			httperr.Write(ctx, c, err)
			return
		}
		// Empty collections are serialised as [] rather than null so a client
		// can iterate the response without a nil check for the common case of a
		// sentence that carried no soft condition.
		if result.SoftConditions == nil {
			result.SoftConditions = []InterpretSoftCondition{}
		}
		if result.NamedRestaurants == nil {
			result.NamedRestaurants = []string{}
		}
		if result.MissingSlots == nil {
			result.MissingSlots = []string{}
		}
		c.JSON(200, result)
	}
}
