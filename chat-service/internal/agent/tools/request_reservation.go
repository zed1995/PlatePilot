package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
	"github.com/zed1995/platepilot/chat-service/internal/hitl"
	ressvc "github.com/zed1995/platepilot/chat-service/internal/reservation"
	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/reservation"
	domaintool "github.com/zed1995/platepilot/shared/domain/tool"
)

// RequestReservationToolName is the tool name the model sees.
const RequestReservationToolName = "request_reservation"

const requestReservationSchema = `{
  "type": "object",
  "properties": {
    "restaurant_id": {
      "type": "integer",
      "description": "The restaurant to book"
    },
    "slot_id": {
      "type": "string",
      "description": "A slot id returned by get_availability for this restaurant and date"
    },
    "party_size": {
      "type": "integer",
      "description": "How many people the table is for"
    }
  },
  "required": ["restaurant_id", "slot_id", "party_size"],
  "additionalProperties": false
}`

type requestReservationArgs struct {
	RestaurantID int64  `json:"restaurant_id"`
	SlotID       string `json:"slot_id"`
	PartySize    int    `json:"party_size"`
}

// RequestReservationEntry builds the confirmation-gated booking tool.
//
// The entry declares ConfirmationRequired, so the reasoning loop never runs it:
// a call is recorded on the thread and the turn ends by asking the user to
// confirm the summary. The handler itself refuses to run without an approval on
// its context, which is the second half of the same guarantee — even if the gate
// were bypassed, the tool that writes has no way to be reached by a model.
func RequestReservationEntry(svc *ressvc.Service) toolreg.Entry {
	return toolreg.Entry{
		Spec: domaintool.ToolSpec{
			Name: RequestReservationToolName,
			Description: "Request a table. This does not book anything: it asks the user to confirm " +
				"the restaurant, time and party size first, and only their confirmation books it.",
			Parameters: json.RawMessage(requestReservationSchema),
			ReadOnly:   false,
		},
		Confirmation: toolreg.ConfirmationRequired,
		SummarizeApproval: func(ctx context.Context, raw json.RawMessage) (string, error) {
			args, err := decodeRequestReservation(raw)
			if err != nil {
				return "", err
			}
			return svc.Summary(ctx, confirmRequest(args, hitl.Approval{}))
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (domaintool.ToolResult, error) {
			args, err := decodeRequestReservation(raw)
			if err != nil {
				return domaintool.ToolResult{}, err
			}
			approval, ok := hitl.ApprovalFromContext(ctx)
			if !ok {
				// No approval means no user said yes. This is the one error in
				// the tool that must never be recoverable by retrying: the
				// model cannot talk its way into it, because there is nothing
				// it could pass that would satisfy the check.
				return domaintool.ToolResult{}, errs.New(errs.CodeAgentNoPendingAction,
					"request_reservation 只能在用户确认之后执行")
			}
			booked, err := svc.Confirm(ctx, confirmRequest(args, approval))
			if err != nil {
				return domaintool.ToolResult{}, err
			}
			data, err := json.Marshal(booked)
			if err != nil {
				return domaintool.ToolResult{}, errs.Wrap(errs.CodeInternal,
					"encode reservation", err)
			}
			return domaintool.ToolResult{
				Status:  domaintool.ToolStatusOK,
				Content: renderReservation(booked),
				Data:    data,
			}, nil
		},
	}
}

// confirmRequest joins what the model asked for with who approved it.
//
// The two halves are deliberately separate types: the arguments are the model's
// and the identity is the server's. Merging them into one struct at the tool
// boundary is the only place they meet, and it is where the idempotency key gets
// its request id — from the approval, never from the arguments.
func confirmRequest(args requestReservationArgs, approval hitl.Approval) ressvc.ConfirmRequest {
	return ressvc.ConfirmRequest{
		ThreadID:     approval.ThreadID,
		UserID:       approval.UserID,
		Action:       RequestReservationToolName,
		Arguments:    approval.Arguments,
		RequestID:    approval.RequestID,
		RestaurantID: args.RestaurantID,
		SlotID:       args.SlotID,
		PartySize:    args.PartySize,
	}
}

func decodeRequestReservation(raw json.RawMessage) (requestReservationArgs, error) {
	var args requestReservationArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return requestReservationArgs{}, errs.Wrap(errs.CodeValidationFailed,
			"decode request_reservation arguments", err)
	}
	return args, nil
}

// renderReservation reports the booking in the terms the summary used, so the
// answer after a confirmation reads as the same fact the user approved.
func renderReservation(booked reservation.Reservation) string {
	return fmt.Sprintf("预约已确认：编号 %s，餐厅 %d，时段 %s，%d 人，状态 %s。",
		booked.ReservationID, booked.RestaurantID, booked.SlotID,
		booked.PartySize, booked.Status)
}
