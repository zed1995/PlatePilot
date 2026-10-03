package httpapi

import (
	"context"
	"encoding/json"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/zed1995/platepilot/chat-service/internal/httperr"
	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/requestctx"
)

// Confirm route values. They are the wire contract for POST
// /v1/conversations/:id/confirm and are matched, not parsed: an unknown
// decision is a request defect, and treating it as "confirm" because it was not
// "cancel" would execute a write on a typo.
const (
	DecisionConfirm = "confirm"
	DecisionCancel  = "cancel"
)

// confirmActionRequest is the decision endpoint's body.
type confirmActionRequest struct {
	Decision string `json:"decision"`
}

func (r confirmActionRequest) Validate() error {
	switch trimSpace(r.Decision) {
	case DecisionConfirm, DecisionCancel:
		return nil
	case "":
		return errs.New(errs.CodeInvalidArgument,
			"decision is required and must be \"confirm\" or \"cancel\"")
	default:
		return errs.Newf(errs.CodeInvalidArgument,
			"decision must be \"confirm\" or \"cancel\" (got %q)", r.Decision)
	}
}

// ConfirmInput identifies the thread and the answer to its pending action.
type ConfirmInput struct {
	ThreadID string
	UserID   string
	TraceID  string
	Decision string
}

// ConfirmResult is the decided outcome.
type ConfirmResult struct {
	ThreadID      string
	Decision      string
	PendingAction string
	// State is the thread's state after the decision.
	State string
	// Output is the approved action's own payload, absent when the user
	// cancelled. It is passed through unchanged: the caller of this endpoint is
	// the same client that would have received it from a tool call, and
	// reshaping it here would invent a second contract for one fact.
	Output json.RawMessage
	// Summary is the sentence the user approved, echoed so the response stands
	// on its own in a client's history.
	Summary string
	// Replayed reports that an identical confirmation had already been decided.
	Replayed bool
	// Message is the human-readable outcome.
	Message string
}

// confirmResponse is the wire shape of a decision.
type confirmResponse struct {
	ThreadID      string          `json:"thread_id"`
	Decision      string          `json:"decision"`
	PendingAction string          `json:"pending_action,omitempty"`
	State         string          `json:"state"`
	Result        json.RawMessage `json:"result,omitempty"`
	Summary       string          `json:"summary,omitempty"`
	Replayed      bool            `json:"replayed,omitempty"`
	Message       string          `json:"message"`
}

// ConfirmHandler answers POST /v1/conversations/:id/confirm.
//
// It is a decision endpoint rather than a message: the confirmation is a
// yes/no about a specific request, and routing it through the chat endpoint
// would make it depend on the model reading "确认" correctly. It also has no
// stream — a decision produces one outcome, and the client is waiting for it
// rather than watching a turn.
func ConfirmHandler(svc ChatService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		var body confirmActionRequest
		if err := BindAndValidate(c, &body); err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		rc, _ := requestctx.FromContext(ctx)
		out, err := svc.ConfirmAction(ctx, ConfirmInput{
			ThreadID: c.Param("id"),
			UserID:   userIDFromContext(ctx),
			TraceID:  rc.TraceID,
			Decision: trimSpace(body.Decision),
		})
		if err != nil {
			httperr.Write(ctx, c, err)
			return
		}
		c.JSON(200, toConfirmResponse(out))
	}
}

func toConfirmResponse(out ConfirmResult) confirmResponse {
	return confirmResponse{
		ThreadID:      out.ThreadID,
		Decision:      out.Decision,
		PendingAction: out.PendingAction,
		State:         out.State,
		Result:        out.Output,
		Summary:       out.Summary,
		Replayed:      out.Replayed,
		Message:       out.Message,
	}
}
