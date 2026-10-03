package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/zed1995/platepilot/shared/domain/errs"
)

// confirmResponseBody mirrors the decision endpoint's wire shape for assertions.
type confirmResponseBody struct {
	ThreadID      string          `json:"thread_id"`
	Decision      string          `json:"decision"`
	PendingAction string          `json:"pending_action"`
	State         string          `json:"state"`
	Result        json.RawMessage `json:"result"`
	Summary       string          `json:"summary"`
	Replayed      bool            `json:"replayed"`
	Message       string          `json:"message"`
}

// The endpoint is a bound route, not a message: the decision it carries has to
// reach the service as the thread's id plus the answer, and the response has to
// stand on its own in a client's history.
func TestConfirmEndpointPassesTheDecisionThrough(t *testing.T) {
	fake := newFakeChatService()
	base := startChatServer(t, fake)

	resp, raw := chatRequest(t, http.MethodPost, base+"/v1/conversations/thread-9/confirm",
		map[string]string{"decision": "confirm"},
		map[string]string{"X-User-ID": "user-7"})

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, raw)
	}
	var body confirmResponseBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.ThreadID != "thread-9" || body.Decision != "confirm" {
		t.Fatalf("response = %+v", body)
	}
	if body.State != "completed" {
		t.Fatalf("state = %q, want completed", body.State)
	}
	if len(body.Result) == 0 {
		t.Fatal("the approved action's own payload did not survive the hop")
	}
	if body.Summary == "" || body.Message == "" {
		t.Fatalf("the response does not stand on its own: %+v", body)
	}

	// The request id came from the middleware and reached the service, so the
	// decision is traceable to the request that made it.
	if fake.lastConfirm.ThreadID != "thread-9" || fake.lastConfirm.Decision != "confirm" {
		t.Fatalf("service saw %+v", fake.lastConfirm)
	}
	if fake.lastConfirm.UserID != "user-7" {
		t.Fatalf("service saw user %q, want user-7", fake.lastConfirm.UserID)
	}
	if fake.lastConfirm.TraceID == "" {
		t.Fatal("the decision reached the service with no trace id")
	}
}

// An unknown decision is a request defect. It is matched, not parsed: treating
// "cnofirm" as a confirm because it is not "cancel" would execute a write on a
// typo, which is the one mistake this endpoint must not make.
func TestConfirmEndpointMatchesTheDecisionRatherThanParsingIt(t *testing.T) {
	fake := newFakeChatService()
	base := startChatServer(t, fake)

	for _, decision := range []string{"maybe", "yes", "CONFIRM", "true"} {
		t.Run(decision, func(t *testing.T) {
			resp, raw := chatRequest(t, http.MethodPost, base+"/v1/conversations/thread-9/confirm",
				map[string]string{"decision": decision}, nil)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, body %s", resp.StatusCode, raw)
			}
			if fake.confirmCalled != 0 {
				t.Fatal("a malformed decision reached the service")
			}
		})
	}
}

func TestConfirmEndpointRequiresADecision(t *testing.T) {
	fake := newFakeChatService()
	base := startChatServer(t, fake)

	resp, raw := chatRequest(t, http.MethodPost, base+"/v1/conversations/thread-9/confirm",
		map[string]string{}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", resp.StatusCode, raw)
	}
	if fake.confirmCalled != 0 {
		t.Fatal("an empty decision reached the service")
	}
}

// A cancel is a decision the service is asked to make, not one the transport
// makes on its behalf.
func TestConfirmEndpointCarriesACancel(t *testing.T) {
	fake := newFakeChatService()
	base := startChatServer(t, fake)

	resp, raw := chatRequest(t, http.MethodPost, base+"/v1/conversations/thread-9/confirm",
		map[string]string{"decision": "cancel"}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, raw)
	}
	if fake.lastConfirm.Decision != "cancel" {
		t.Fatalf("service saw decision %q", fake.lastConfirm.Decision)
	}
}

// The service's error code decides the status. A slot that is gone is a 409 and
// a thread with nothing pending is a 400, and a transport that flattened either
// into a 500 would leave a client unable to tell "retry" from "start over".
func TestConfirmEndpointMapsTheServicesErrorCode(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"capacity is gone", errs.New(errs.CodeReservationUnavailable, "时段已满"),
			http.StatusConflict},
		{"nothing pending", errs.New(errs.CodeAgentNoPendingAction, "没有待确认的动作"),
			http.StatusBadRequest},
		{"thread was written by another request", errs.New(errs.CodeConflict, "请重试"),
			http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeChatService()
			fake.confirmErr = tc.err
			base := startChatServer(t, fake)

			resp, raw := chatRequest(t, http.MethodPost, base+"/v1/conversations/thread-9/confirm",
				map[string]string{"decision": "confirm"}, nil)
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d, body %s", resp.StatusCode, tc.want, raw)
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatalf("decode error body: %v", err)
			}
			if body.Error.Code != string(errs.CodeOf(tc.err)) {
				t.Fatalf("body code = %q, want %q", body.Error.Code, errs.CodeOf(tc.err))
			}
		})
	}
}

// The decision endpoint is a decision, not a turn: it has no stream, and a
// client that sent Accept: text/event-stream must still get one JSON answer.
func TestConfirmEndpointAnswersJsonNotAStream(t *testing.T) {
	fake := newFakeChatService()
	base := startChatServer(t, fake)

	resp, raw := chatRequest(t, http.MethodPost, base+"/v1/conversations/thread-9/confirm",
		map[string]string{"decision": "confirm"},
		map[string]string{"Accept": "text/event-stream"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); ct == "" || ct[:16] != "application/json" {
		t.Fatalf("content type = %q, want application/json", ct)
	}
	if raw[0] != '{' {
		t.Fatalf("body is not a JSON object: %s", raw)
	}
}
