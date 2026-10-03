package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
	ressvc "github.com/zed1995/platepilot/chat-service/internal/reservation"
	"github.com/zed1995/platepilot/shared/domain/errs"
	domaintool "github.com/zed1995/platepilot/shared/domain/tool"
)

// GetAvailabilityToolName is the tool name the model sees.
const GetAvailabilityToolName = "get_availability"

const getAvailabilitySchema = `{
  "type": "object",
  "properties": {
    "restaurant_id": {
      "type": "integer",
      "description": "The restaurant to ask about, from search_restaurants or resolve_restaurant"
    },
    "date": {
      "type": "string",
      "description": "Calendar date in YYYY-MM-DD form"
    },
    "party_size": {
      "type": "integer",
      "description": "How many people the table is for"
    }
  },
  "required": ["restaurant_id", "date", "party_size"],
  "additionalProperties": false
}`

type getAvailabilityArgs struct {
	RestaurantID int64  `json:"restaurant_id"`
	Date         string `json:"date"`
	PartySize    int    `json:"party_size"`
}

// GetAvailabilityEntry builds the read-only availability lookup.
//
// It is registered only when the reservation capability is switched on: a
// deployment without it has no inventory to read, and a tool that always
// answered "nothing available" would be worse than one the model cannot see.
func GetAvailabilityEntry(svc *ressvc.Service) toolreg.Entry {
	return toolreg.Entry{
		Spec: domaintool.ToolSpec{
			Name: GetAvailabilityToolName,
			Description: "List the bookable times for one restaurant on one date that can seat a party. " +
				"Use it before requesting a reservation: the slot_id it returns is the only id " +
				"request_reservation accepts.",
			Parameters: json.RawMessage(getAvailabilitySchema),
			ReadOnly:   true,
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (domaintool.ToolResult, error) {
			var args getAvailabilityArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return domaintool.ToolResult{}, errs.Wrap(errs.CodeValidationFailed,
					"decode get_availability arguments", err)
			}
			result, err := svc.Availability(ctx, ressvc.AvailabilityRequest{
				RestaurantID: args.RestaurantID,
				Date:         args.Date,
				PartySize:    args.PartySize,
			})
			if err != nil {
				return domaintool.ToolResult{}, err
			}
			data, err := json.Marshal(result)
			if err != nil {
				return domaintool.ToolResult{}, errs.Wrap(errs.CodeInternal,
					"encode availability result", err)
			}
			return domaintool.ToolResult{
				Status:  domaintool.ToolStatusOK,
				Content: renderAvailability(result),
				Data:    data,
			}, nil
		},
	}
}

// renderAvailability summarises the answer for the model.
//
// An empty list is stated as a fact about the day rather than as an error: the
// honest next step is a different time or date, and a tool error would invite
// the model to retry the same question.
func renderAvailability(result ressvc.AvailabilityResult) string {
	if len(result.Slots) == 0 {
		return fmt.Sprintf("餐厅 %d 在 %s 没有能坐下 %d 人的时段。",
			result.RestaurantID, result.Date, result.PartySize)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "餐厅 %d 在 %s 可订 %d 人的时段：\n",
		result.RestaurantID, result.Date, result.PartySize)
	for _, slot := range result.Slots {
		fmt.Fprintf(&b, "- %s（slot_id=%s，余 %d/%d 位，政策 %s）\n",
			slot.SlotTime, slot.SlotID, slot.Remaining, slot.Capacity, slot.PolicyVersion)
	}
	b.WriteString("预订必须由用户确认后才会生效。")
	return b.String()
}
