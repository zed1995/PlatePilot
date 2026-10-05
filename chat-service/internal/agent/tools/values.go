package tools

import (
	"encoding/json"

	"github.com/zed1995/platepilot/shared/domain/errs"
)

// ValueDomain is what one turn has legitimately seen: the restaurant ids its
// search produced or its name resolution settled on (plus any restaurant an
// availability lookup named), and the slot ids its availability lookups
// returned.
//
// It exists so a write tool's arguments can be checked against this turn's own
// facts rather than against the schema's types. A JSON Schema proves
// `restaurant_id` is an integer; it cannot prove the integer is a restaurant the
// user was ever shown. An injected model that could pick an arbitrary id could
// request a booking at a place that never appeared in the conversation — the
// "legitimate-looking bad action" the schema cannot exclude.
type ValueDomain struct {
	// RestaurantIDs is the set of restaurants this turn resolved or returned.
	RestaurantIDs map[int64]struct{}
	// SlotIDs is the set of slot ids this turn's availability calls returned.
	SlotIDs map[string]struct{}
}

// ValidateWriteArguments refuses a write whose arguments name something the turn
// never saw. Reads are not checked: a lookup that returns nothing is harmless,
// and gating it would turn a question into an error.
//
// The check is deliberately strict about an empty set. A turn that never saw a
// restaurant cannot legitimately book one, and treating "nothing known" as "no
// restriction" would be exactly the hole this closes.
func ValidateWriteArguments(name string, args json.RawMessage, domain ValueDomain) error {
	switch name {
	case RequestReservationToolName:
		var decoded requestReservationArgs
		if err := json.Unmarshal(args, &decoded); err != nil {
			return errs.Wrap(errs.CodeValidationFailed,
				"decode request_reservation arguments", err)
		}
		if _, ok := domain.RestaurantIDs[decoded.RestaurantID]; !ok {
			return errs.Newf(errs.CodeValidationFailed,
				"restaurant_id %d 不在本轮候选或已解析的餐厅里", decoded.RestaurantID)
		}
		if _, ok := domain.SlotIDs[decoded.SlotID]; !ok {
			return errs.Newf(errs.CodeValidationFailed,
				"slot_id %q 不是本轮 get_availability 返回的时段", decoded.SlotID)
		}
	}
	return nil
}
