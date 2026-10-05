package tools

import (
	"encoding/json"
	"testing"

	"github.com/zed1995/platepilot/shared/domain/errs"
)

// A booking whose restaurant or slot this turn never saw is refused. The point
// is the "empty set" case: a schema validates the type of the id, not its
// provenance, so an injected model could otherwise book at an invented id.
func TestValidateWriteArgumentsRefusesUnknownIds(t *testing.T) {
	domain := ValueDomain{
		RestaurantIDs: map[int64]struct{}{7: {}},
		SlotIDs:       map[string]struct{}{"slot-1900": {}},
	}
	cases := []struct {
		name string
		args string
		ok   bool
	}{
		{name: "known restaurant and slot", args: `{"restaurant_id":7,"slot_id":"slot-1900","party_size":2}`, ok: true},
		{name: "unknown restaurant", args: `{"restaurant_id":99,"slot_id":"slot-1900","party_size":2}`},
		{name: "unknown slot", args: `{"restaurant_id":7,"slot_id":"slot-0300","party_size":2}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateWriteArguments(RequestReservationToolName, json.RawMessage(tc.args), domain)
			if tc.ok && err != nil {
				t.Fatalf("a grounded call was refused: %v", err)
			}
			if !tc.ok {
				if err == nil {
					t.Fatal("expected a refusal")
				}
				if code := errs.CodeOf(err); code != errs.CodeValidationFailed {
					t.Fatalf("code = %q, want %q", code, errs.CodeValidationFailed)
				}
			}
		})
	}
}

// A turn that saw nothing cannot legitimately book anything: an empty domain is
// a refusal, not a wildcard.
func TestValidateWriteArgumentsRefusesWhenNothingWasSeen(t *testing.T) {
	err := ValidateWriteArguments(
		RequestReservationToolName,
		json.RawMessage(`{"restaurant_id":7,"slot_id":"slot-1900","party_size":2}`),
		ValueDomain{},
	)
	if err == nil {
		t.Fatal("an empty value domain accepted a booking")
	}
}

// A read tool is not checked: a lookup that returns nothing is harmless.
func TestValidateWriteArgumentsIgnoresReadTools(t *testing.T) {
	err := ValidateWriteArguments(
		GetAvailabilityToolName,
		json.RawMessage(`{"restaurant_id":99,"date":"2026-10-10","party_size":2}`),
		ValueDomain{},
	)
	if err != nil {
		t.Fatalf("a read tool was gated: %v", err)
	}
}
