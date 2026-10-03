package app

import (
	"context"

	"github.com/zed1995/platepilot/chat-service/internal/agent/slots"
	"github.com/zed1995/platepilot/chat-service/internal/httpapi"
)

// slotInterpreter adapts the slot extractor onto the transport's read-only
// interpretation contract.
//
// It is the thinnest possible adapter on purpose. The projection from a plan to
// the response is mechanical, and anything cleverer — filling a missing slot,
// guessing an intent — would mean the endpoint reported an interpretation the
// turn would not have used, which is worse than reporting none.
type slotInterpreter struct {
	extractor *slots.Extractor
}

// newSlotInterpreter builds the adapter. The extractor is never nil in a
// deployment: with no chat provider it still parses, which is precisely the
// deployment the endpoint is most useful in.
func newSlotInterpreter(extractor *slots.Extractor) *slotInterpreter {
	return &slotInterpreter{extractor: extractor}
}

// Interpret answers one standalone interpretation request.
//
// Pending thread state is deliberately not consulted: this endpoint is a probe
// of the understanding layer, and a probe whose answer depended on a thread
// would be unreproducible — the same sentence would interpret differently
// depending on when it was asked.
func (s *slotInterpreter) Interpret(ctx context.Context, text string) (httpapi.InterpretResult, error) {
	plan, err := s.extractor.Extract(ctx, text, slots.ThreadContext{})
	if err != nil {
		return httpapi.InterpretResult{}, err
	}
	return toInterpretResult(plan), nil
}

// toInterpretResult projects a plan onto the transport DTO.
func toInterpretResult(plan slots.Plan) httpapi.InterpretResult {
	result := httpapi.InterpretResult{
		Intent:               string(plan.Intent),
		Query:                plan.Query,
		HardFilters:          plan.HardFilters,
		NamedRestaurants:     plan.NamedRestaurants,
		SelectedRestaurantID: plan.SelectedRestaurantID,
		MissingSlots:         plan.MissingSlots,
		NeedClarification:    plan.NeedClarification,
		Source:               plan.Source,
		ExtractLatencyMS:     plan.ExtractLatencyMS,
		Warnings:             plan.Warnings,
	}
	for _, condition := range plan.SoftConditions {
		result.SoftConditions = append(result.SoftConditions, httpapi.InterpretSoftCondition{
			Text:  condition.Text,
			Topic: condition.Topic,
		})
	}
	return result
}
