package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zed1995/platepilot/shared/domain/conversation"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// seedThreads puts three threads on the fake: two belonging to user-a (the
// second written later, so it is the newer of the two) and one belonging to
// user-b. The ownership split is the point of the fixture — the list endpoint's
// only rule is whose threads come back.
func seedThreads(fake *fakeChatService) {
	base := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	add := func(threadID, userID, title string, offset time.Duration, state conversation.State) {
		conv := conversation.Conversation{
			ThreadID:     threadID,
			UserID:       userID,
			Title:        title,
			CurrentState: state,
			CreatedAt:    base,
			UpdatedAt:    base.Add(offset),
		}
		fake.convs[threadID] = conv
		fake.threadOrder = append(fake.threadOrder, threadID)
	}
	add("thread-a-1", "user-a", "older", 0, conversation.StateCompleted)
	add("thread-a-2", "user-a", "newer", time.Minute, conversation.StateAwaitingClarification)
	add("thread-b-1", "user-b", "someone else", 2*time.Minute, conversation.StateCompleted)
}

type threadListBody struct {
	Conversations []struct {
		ThreadID     string `json:"thread_id"`
		UserID       string `json:"user_id"`
		Title        string `json:"title"`
		CurrentState string `json:"current_state"`
	} `json:"conversations"`
}

type candidateListBody struct {
	Candidates []struct {
		Position     int       `json:"position"`
		RestaurantID int64     `json:"restaurant_id"`
		Name         string    `json:"name"`
		Score        float64   `json:"score"`
		Reasons      []string  `json:"reasons"`
		SnapshotAt   time.Time `json:"snapshot_at"`
	} `json:"candidates"`
}

// ---------------------------------------------------------------------------
// GET /v1/conversations
// ---------------------------------------------------------------------------

// The list is scoped by the caller's identity, because that is the whole access
// model this surface has before M6 ships a principal. A list that answered with
// everybody's threads would read as a feature in a single-user deployment and
// as a data leak in every other one.
func TestListThreadsReturnsOnlyTheCallersThreads(t *testing.T) {
	fake := newFakeChatService()
	seedThreads(fake)
	base := startChatServer(t, fake)

	resp, raw := chatRequest(t, http.MethodGet, base+"/v1/conversations", nil,
		map[string]string{"X-User-ID": "user-a"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, raw)
	}
	var body threadListBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Conversations) != 2 {
		t.Fatalf("conversations = %d, want the two belonging to user-a: %s", len(body.Conversations), raw)
	}
	for _, conv := range body.Conversations {
		if conv.UserID != "user-a" {
			t.Fatalf("another user's thread came back: %+v", conv)
		}
	}
	if fake.listThreadCall.userID != "user-a" {
		t.Fatalf("service saw user %q", fake.listThreadCall.userID)
	}
}

// Newest-first ordering is asserted rather than assumed because the list is the
// only place a user picks up a thread they left waiting: the ordering is what
// puts "the one that is waiting for you" at the top.
func TestListThreadsReturnsNewestFirst(t *testing.T) {
	fake := newFakeChatService()
	seedThreads(fake)
	base := startChatServer(t, fake)

	_, raw := chatRequest(t, http.MethodGet, base+"/v1/conversations", nil,
		map[string]string{"X-User-ID": "user-a"})
	var body threadListBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Conversations) != 2 ||
		body.Conversations[0].ThreadID != "thread-a-2" ||
		body.Conversations[1].ThreadID != "thread-a-1" {
		t.Fatalf("order = %+v, want newest first", body.Conversations)
	}
	if state := body.Conversations[0].CurrentState; state != string(conversation.StateAwaitingClarification) {
		t.Fatalf("newest thread state = %q, want the waiting state visible in the list", state)
	}
}

// An anonymous list refuses instead of widening. Answering with everyone's
// threads when no identity was supplied is the failure this exists to prevent;
// it is also indistinguishable from a list that merely has no filter yet.
func TestListThreadsRefusesAnAnonymousCaller(t *testing.T) {
	fake := newFakeChatService()
	seedThreads(fake)
	base := startChatServer(t, fake)

	resp, raw := chatRequest(t, http.MethodGet, base+"/v1/conversations", nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", resp.StatusCode, raw)
	}
	if fake.listThreadCall.called != 0 {
		t.Fatal("an unscoped list reached the service")
	}
}

// The list shape varies deliberately: an empty page is a JSON array rather than
// null so a client can iterate without a special case. A user with no threads is
// the common case on a fresh browser.
func TestListThreadsAnswersWithAnEmptyArrayForANewUser(t *testing.T) {
	fake := newFakeChatService()
	seedThreads(fake)
	base := startChatServer(t, fake)

	_, raw := chatRequest(t, http.MethodGet, base+"/v1/conversations", nil,
		map[string]string{"X-User-ID": "user-nobody"})
	var body threadListBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Conversations == nil || len(body.Conversations) != 0 {
		t.Fatalf("conversations = %v, want an empty array", body.Conversations)
	}
	if !strings.Contains(string(raw), `"conversations":[]`) {
		t.Fatalf("empty list is not an array: %s", raw)
	}
}

// The cursor and page bound are forwarded rather than defaulted silently: a
// client paging through a long thread list has no other way to ask for less.
func TestListThreadsForwardsTheCursorAndBound(t *testing.T) {
	fake := newFakeChatService()
	seedThreads(fake)
	base := startChatServer(t, fake)

	_, raw := chatRequest(t, http.MethodGet, base+"/v1/conversations?limit=1&before_id=thread-a-2", nil,
		map[string]string{"X-User-ID": "user-a"})
	if resp := raw; !strings.Contains(string(resp), "thread-a-1") {
		t.Fatalf("the page below the cursor did not come back: %s", resp)
	}
	if fake.listThreadCall.limit != 1 || fake.listThreadCall.beforeID != "thread-a-2" {
		t.Fatalf("service saw %+v", fake.listThreadCall)
	}
}

// A collection GET must reach the list route rather than the parameterised
// read. Registration order decides which one answers, and the observable
// difference is whether the body is a list at all.
func TestListThreadsRouteIsNotShadowedByTheThreadRead(t *testing.T) {
	fake := newFakeChatService()
	seedThreads(fake)
	base := startChatServer(t, fake)

	_, raw := chatRequest(t, http.MethodGet, base+"/v1/conversations", nil,
		map[string]string{"X-User-ID": "user-a"})
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := decoded["conversations"]; !ok {
		t.Fatalf("response is not a thread list: %s", raw)
	}
	if _, shadowed := decoded["thread_id"]; shadowed {
		t.Fatalf("the collection route read one thread instead of listing: %s", raw)
	}
}

// ---------------------------------------------------------------------------
// GET /v1/conversations/:id/candidates
// ---------------------------------------------------------------------------

// Position travels beside the restaurant rather than being implied by array
// index: the UI renders it as the ordinal a follow-up says — "第二家" — and an
// index would renumber every bubble the moment one duplicate was dropped.
func TestListCandidatesCarriesTheSnapshotInPositionOrder(t *testing.T) {
	fake := newFakeChatService()
	base := startChatServer(t, fake)

	_, raw := chatRequest(t, http.MethodGet, base+"/v1/conversations/thread-9/candidates", nil, nil)
	var body candidateListBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Candidates) != 2 {
		t.Fatalf("candidates = %d, want 2: %s", len(body.Candidates), raw)
	}
	if body.Candidates[0].Position != 1 || body.Candidates[0].RestaurantID != 11 {
		t.Fatalf("first candidate = %+v", body.Candidates[0])
	}
	if body.Candidates[1].Position != 2 || body.Candidates[1].Name != "B Ramen" {
		t.Fatalf("second candidate = %+v", body.Candidates[1])
	}
}

// A ranking is not checkable without its reasons and its observation date. The
// reasons say why a restaurant ranked where it did; the snapshot time says how
// old the data behind it is. Without them the list reads as an unexplained and
// timeless ordering.
func TestListCandidatesCarriesReasonsAndTheSnapshotTime(t *testing.T) {
	fake := newFakeChatService()
	base := startChatServer(t, fake)

	_, raw := chatRequest(t, http.MethodGet, base+"/v1/conversations/thread-9/candidates", nil, nil)
	var body candidateListBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Candidates) != 2 {
		t.Fatalf("candidates = %d, want 2: %s", len(body.Candidates), raw)
	}
	first := body.Candidates[0]
	if len(first.Reasons) != 1 || first.Reasons[0] != "评论推断：安静（ambience）" {
		t.Fatalf("first candidate reasons = %v", first.Reasons)
	}
	if !first.SnapshotAt.Equal(candidateSnapshotAt) {
		t.Fatalf("snapshot_at = %v, want %v", first.SnapshotAt, candidateSnapshotAt)
	}
}

// A candidate whose snapshot the store never recorded must not come back dated
// year 1. An absent field is the honest answer; a zero timestamp is a date the
// data never had, and a client would render it.
func TestListCandidatesOmitsAnUnknownSnapshotTime(t *testing.T) {
	fake := newFakeChatService()
	base := startChatServer(t, fake)

	_, raw := chatRequest(t, http.MethodGet, base+"/v1/conversations/thread-no-snapshot/candidates", nil, nil)
	if strings.Contains(string(raw), "snapshot_at") {
		t.Fatalf("an unrecorded snapshot must be omitted, not sent as the zero time: %s", raw)
	}
	if !strings.Contains(string(raw), `"restaurant_id":11`) {
		t.Fatalf("the row itself must still be returned: %s", raw)
	}
}

// A thread that never searched has zero candidates, and that is a real answer
// rather than an error: it is how a client can say honestly that there is no
// "第二家" to point at.
func TestListCandidatesAnswersWithAnEmptyArrayWhenThereIsNoSnapshot(t *testing.T) {
	fake := newFakeChatService()
	base := startChatServer(t, fake)

	for _, threadID := range []string{"thread-empty-candidates", "thread-nil-candidates"} {
		t.Run(threadID, func(t *testing.T) {
			_, raw := chatRequest(t, http.MethodGet, base+"/v1/conversations/"+threadID+"/candidates", nil, nil)
			if !strings.Contains(string(raw), `"candidates":[]`) {
				t.Fatalf("empty snapshot is not an array: %s", raw)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DELETE /v1/conversations/:id
// ---------------------------------------------------------------------------

// A delete answers 204 and the thread is gone from the caller's list — the two
// halves of the same claim, because a 204 that left the row in place would look
// identical from the client until the next reload.
func TestDeleteThreadRemovesItFromTheCallersList(t *testing.T) {
	fake := newFakeChatService()
	seedThreads(fake)
	base := startChatServer(t, fake)

	resp, raw := chatRequest(t, http.MethodDelete, base+"/v1/conversations/thread-a-1", nil,
		map[string]string{"X-User-ID": "user-a"})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, body %s", resp.StatusCode, raw)
	}
	if len(raw) != 0 {
		t.Fatalf("204 carried a body: %q", raw)
	}

	_, listed := chatRequest(t, http.MethodGet, base+"/v1/conversations", nil,
		map[string]string{"X-User-ID": "user-a"})
	var body threadListBody
	if err := json.Unmarshal(listed, &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Conversations) != 1 || body.Conversations[0].ThreadID != "thread-a-2" {
		t.Fatalf("list after delete = %+v, want only the surviving thread", body.Conversations)
	}
}

// The header is the whole authorization model this surface has, so it is
// required rather than optional: a delete that ran without one would remove
// whatever thread id it was handed, from whoever happened to own it.
func TestDeleteThreadRefusesAnAnonymousCaller(t *testing.T) {
	fake := newFakeChatService()
	seedThreads(fake)
	base := startChatServer(t, fake)

	resp, raw := chatRequest(t, http.MethodDelete, base+"/v1/conversations/thread-a-1", nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", resp.StatusCode, raw)
	}
	if len(fake.deleteThreadCalls) != 0 {
		t.Fatalf("an unscoped delete reached the service: %+v", fake.deleteThreadCalls)
	}
	if _, err := fake.GetThread(context.Background(), "thread-a-1"); err != nil {
		t.Fatalf("the thread was removed by a request that should not have run: %v", err)
	}
}

// Somebody else's thread is not_found and stays put. A 403 would tell the
// caller the id exists, which is the one thing an unauthenticated surface must
// not answer.
func TestDeleteThreadDoesNotRemoveAnotherUsersThread(t *testing.T) {
	fake := newFakeChatService()
	seedThreads(fake)
	base := startChatServer(t, fake)

	resp, raw := chatRequest(t, http.MethodDelete, base+"/v1/conversations/thread-a-1", nil,
		map[string]string{"X-User-ID": "user-b"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, body %s", resp.StatusCode, raw)
	}
	if _, err := fake.GetThread(context.Background(), "thread-a-1"); err != nil {
		t.Fatalf("another user's thread was removed: %v", err)
	}
}

// The identity reaches the service rather than being dropped on the way: a
// handler that forwarded only the thread id would delete anybody's thread, and
// this is the assertion that fails when it does.
func TestDeleteThreadForwardsTheCallersIdentity(t *testing.T) {
	fake := newFakeChatService()
	seedThreads(fake)
	base := startChatServer(t, fake)

	chatRequest(t, http.MethodDelete, base+"/v1/conversations/thread-a-2", nil,
		map[string]string{"X-User-ID": "user-a"})

	if len(fake.deleteThreadCalls) != 1 {
		t.Fatalf("delete reached the service %d times, want 1", len(fake.deleteThreadCalls))
	}
	call := fake.deleteThreadCalls[0]
	if call.userID != "user-a" || call.threadID != "thread-a-2" {
		t.Fatalf("service saw %+v, want user-a deleting thread-a-2", call)
	}
}

// A thread that was never there is not_found, matching the read routes: an id
// the caller mistyped must not read as "already deleted".
func TestDeleteThreadReportsAnUnknownThread(t *testing.T) {
	fake := newFakeChatService()
	seedThreads(fake)
	base := startChatServer(t, fake)

	resp, raw := chatRequest(t, http.MethodDelete, base+"/v1/conversations/thread-nobody-has", nil,
		map[string]string{"X-User-ID": "user-a"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, body %s", resp.StatusCode, raw)
	}
}

// A mistyped thread is not_found rather than an empty list, matching the
// transcript: a typo must never look like a thread whose data simply did not
// load.
func TestListCandidatesReportsAnUnknownThread(t *testing.T) {
	fake := newFakeChatService()
	base := startChatServer(t, fake)

	resp, raw := chatRequest(t, http.MethodGet, base+"/v1/conversations/missing-thread/candidates", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, body %s", resp.StatusCode, raw)
	}
}
