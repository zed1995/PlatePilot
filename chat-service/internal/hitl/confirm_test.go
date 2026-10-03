package hitl_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/zed1995/platepilot/shared/domain/conversation"
	"github.com/zed1995/platepilot/shared/domain/errs"
	domaintool "github.com/zed1995/platepilot/shared/domain/tool"
	"github.com/zed1995/platepilot/shared/testkit"

	"github.com/zed1995/platepilot/chat-service/internal/hitl"
)

const (
	fixtureThread = "thread-1"
	fixtureUser   = "user-1"
	fixtureAction = "request_reservation"
	fixtureArgs   = `{"restaurant_id":7,"slot_id":"r7-2026-10-10-1900","party_size":2}`
	fixtureReqID  = "req-1"
)

var fixtureNow = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

// countingRunner is the tool runner behind a counter. The counter is the whole
// point: the assertions these tests make are "did the tool run at all", and a
// runner that recorded nothing could not distinguish a cancelled decision from
// one that quietly wrote.
type countingRunner struct {
	mu     sync.Mutex
	calls  []domaintool.ToolCall
	grants []hitl.Approval
	// result overrides what the tool answers. Nil means success.
	result *domaintool.ToolResult
	// onInvoke runs while the tool is "working", which is the window a
	// competing writer for the same thread would occupy.
	onInvoke func(ctx context.Context)
}

func (r *countingRunner) Invoke(ctx context.Context, call domaintool.ToolCall) domaintool.ToolResult {
	r.mu.Lock()
	r.calls = append(r.calls, call)
	if approval, ok := hitl.ApprovalFromContext(ctx); ok {
		r.grants = append(r.grants, approval)
	}
	override := r.result
	hook := r.onInvoke
	r.mu.Unlock()

	if hook != nil {
		hook(ctx)
	}
	if override != nil {
		out := *override
		out.CallID = call.ID
		out.Name = call.Name
		return out
	}
	return domaintool.ToolResult{
		CallID:  call.ID,
		Name:    call.Name,
		Status:  domaintool.ToolStatusOK,
		Content: "预约已确认：编号 res-1",
		Data:    json.RawMessage(`{"reservation_id":"res-1","status":"confirmed"}`),
	}
}

func (r *countingRunner) invocationCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *countingRunner) lastGrant(t *testing.T) hitl.Approval {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.grants) == 0 {
		t.Fatal("the tool ran without an approval on its context")
	}
	return r.grants[len(r.grants)-1]
}

// harness is a thread parked on a write, with the stores the decision reads.
type harness struct {
	service *hitl.Service
	runner  *countingRunner
	repo    *testkit.ConversationRepository
}

// newHarness parks one write on a thread and returns the service that decides
// it. It is the shared opening of nearly every test below, and it goes through
// the store rather than setting a field so the reads these tests exercise are
// the real ones.
func newHarness(t *testing.T, state conversation.State) *harness {
	t.Helper()
	return newHarnessParked(t, state, true)
}

// newHarnessParked builds the same harness with or without a parked write, so a
// test about the no-pending-action refusal is not accidentally given one.
func newHarnessParked(t *testing.T, state conversation.State, park bool) *harness {
	t.Helper()
	ctx := context.Background()
	repo := testkit.NewConversationRepository()
	if err := repo.Upsert(ctx, conversation.Conversation{
		ThreadID:     fixtureThread,
		UserID:       fixtureUser,
		CurrentState: state,
		CreatedAt:    fixtureNow,
		UpdatedAt:    fixtureNow,
	}); err != nil {
		t.Fatalf("seed conversation: %v", err)
	}
	checkpoint := conversation.Checkpoint{
		ThreadID:  fixtureThread,
		Version:   1,
		State:     state,
		CreatedAt: fixtureNow,
	}
	if park {
		checkpoint.PendingAction = fixtureAction
		checkpoint.PendingToolCallID = fixtureReqID
		checkpoint.PendingArguments = json.RawMessage(fixtureArgs)
	}
	if err := repo.SaveCheckpoint(ctx, checkpoint); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}

	runner := &countingRunner{}
	service, err := hitl.NewService(hitl.Config{
		Checkpoints: repo,
		Tools:       runner,
		Clock:       func() time.Time { return fixtureNow },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return &harness{service: service, runner: runner, repo: repo}
}

// ---- the two things a decision can be -------------------------------------

// Confirming runs the tool exactly once and settles the thread.
func TestConfirmingRunsTheApprovedToolOnceAndCompletesTheThread(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, conversation.StateAwaitingConfirmation)

	outcome, err := h.service.Decide(ctx, fixtureThread, hitl.DecisionConfirm)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if outcome.Cancelled || outcome.Replayed {
		t.Fatalf("a first confirmation reported cancelled=%v replayed=%v", outcome.Cancelled, outcome.Replayed)
	}
	if outcome.State != conversation.StateCompleted {
		t.Fatalf("state = %q, want completed", outcome.State)
	}
	if outcome.Action != fixtureAction {
		t.Fatalf("action = %q, want %q", outcome.Action, fixtureAction)
	}
	if h.runner.invocationCount() != 1 {
		t.Fatalf("tool invocations = %d, want 1", h.runner.invocationCount())
	}
	if outcome.Result == nil || outcome.Result.Status != domaintool.ToolStatusOK {
		t.Fatalf("outcome carries no successful result: %+v", outcome.Result)
	}

	// The tool is handed the server-owned identity, not anything the model
	// supplied: the request id is what an idempotency key commits to.
	grant := h.runner.lastGrant(t)
	if grant.ThreadID != fixtureThread || grant.UserID != fixtureUser {
		t.Fatalf("grant names %s/%s, want %s/%s", grant.ThreadID, grant.UserID, fixtureThread, fixtureUser)
	}
	if grant.RequestID != fixtureReqID {
		t.Fatalf("grant request id = %q, want the parked id %q", grant.RequestID, fixtureReqID)
	}
	if string(grant.Arguments) != fixtureArgs {
		t.Fatalf("grant arguments = %s, want the approved arguments verbatim", grant.Arguments)
	}

	checkpoint, err := h.repo.LoadCheckpoint(ctx, fixtureThread)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if checkpoint.State != conversation.StateCompleted {
		t.Fatalf("persisted state = %q, want completed", checkpoint.State)
	}
	if checkpoint.Version <= 1 {
		t.Fatalf("checkpoint version = %d, want the write to have advanced it", checkpoint.Version)
	}
}

// Cancelling runs nothing and returns the thread to idle.
//
// "Runs nothing" is the assertion that matters. A cancel that had to undo a
// hold would mean the gate had already let the write through.
func TestCancellingRunsNothingAndClearsThePendingAction(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, conversation.StateAwaitingConfirmation)

	outcome, err := h.service.Decide(ctx, fixtureThread, hitl.DecisionCancel)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !outcome.Cancelled {
		t.Fatal("a cancel did not report itself as one")
	}
	if outcome.Result != nil {
		t.Fatalf("a cancel produced a result: %+v", outcome.Result)
	}
	if outcome.State != conversation.StateIdle {
		t.Fatalf("state = %q, want idle", outcome.State)
	}
	if h.runner.invocationCount() != 0 {
		t.Fatalf("a cancel ran the tool %d times", h.runner.invocationCount())
	}

	checkpoint, err := h.repo.LoadCheckpoint(ctx, fixtureThread)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if checkpoint.PendingAction != "" || checkpoint.PendingToolCallID != "" || len(checkpoint.PendingArguments) != 0 {
		t.Fatalf("the pending block survived a cancel: %+v", checkpoint)
	}
	if checkpoint.State != conversation.StateIdle {
		t.Fatalf("persisted state = %q, want idle", checkpoint.State)
	}
	conv, err := h.repo.Get(ctx, fixtureThread)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if conv.CurrentState != conversation.StateIdle {
		t.Fatalf("thread state = %q, want idle", conv.CurrentState)
	}
}

// ---- refusals -------------------------------------------------------------

// A thread with nothing parked cannot be confirmed, and it says so specifically
// rather than reporting success for a decision that decided nothing.
func TestDecidingAThreadWithNoPendingActionIsRefused(t *testing.T) {
	ctx := context.Background()
	h := newHarnessParked(t, conversation.StateIdle, false)

	if _, err := h.service.Decide(ctx, fixtureThread, hitl.DecisionConfirm); err == nil {
		t.Fatal("expected a refusal")
	} else if code := errs.CodeOf(err); code != errs.CodeAgentNoPendingAction {
		t.Fatalf("code = %q, want %q", code, errs.CodeAgentNoPendingAction)
	}
	if h.runner.invocationCount() != 0 {
		t.Fatal("a refused decision ran the tool")
	}
}

func TestDecidingWithAnUnknownAnswerIsRefused(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, conversation.StateAwaitingConfirmation)

	for _, decision := range []hitl.Decision{"", "yes", "CONFIRM", "确认"} {
		t.Run(string(decision), func(t *testing.T) {
			if _, err := h.service.Decide(ctx, fixtureThread, decision); err == nil {
				t.Fatal("expected a refusal")
			} else if code := errs.CodeOf(err); code != errs.CodeInvalidArgument {
				t.Fatalf("code = %q, want %q", code, errs.CodeInvalidArgument)
			}
		})
	}
	if h.runner.invocationCount() != 0 {
		t.Fatal("an unknown decision ran the tool")
	}
}

// A pending action recorded outside the awaiting state is a writer bug, and
// deciding it would be guessing which of the two fields to believe.
func TestAPendingActionInTheWrongStateIsNotDecided(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, conversation.StateAwaitingClarification)

	if _, err := h.service.Decide(ctx, fixtureThread, hitl.DecisionConfirm); err == nil {
		t.Fatal("expected a refusal")
	} else if code := errs.CodeOf(err); code != errs.CodeInternal {
		t.Fatalf("code = %q, want %q", code, errs.CodeInternal)
	}
	if h.runner.invocationCount() != 0 {
		t.Fatal("an inconsistent checkpoint was decided anyway")
	}
}

// A tool that fails leaves the thread's pending action alone, because the write
// did not happen and clearing it would lose the request the user was making.
func TestAFailedToolKeepsThePendingActionAndReportsTheReason(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, conversation.StateAwaitingConfirmation)
	h.runner.result = &domaintool.ToolResult{
		Status: domaintool.ToolStatusError,
		Error: errs.New(errs.CodeReservationUnavailable,
			"slot r7-2026-10-10-1900 has no room"),
	}

	_, err := h.service.Decide(ctx, fixtureThread, hitl.DecisionConfirm)
	if err == nil {
		t.Fatal("expected the tool's failure to surface")
	}
	if code := errs.CodeOf(err); code != errs.CodeReservationUnavailable {
		t.Fatalf("code = %q, want the tool's own code %q", code, errs.CodeReservationUnavailable)
	}

	checkpoint, loadErr := h.repo.LoadCheckpoint(ctx, fixtureThread)
	if loadErr != nil {
		t.Fatalf("LoadCheckpoint: %v", loadErr)
	}
	if checkpoint.PendingAction != fixtureAction {
		t.Fatalf("pending action = %q, want it kept after a failed write", checkpoint.PendingAction)
	}
	if checkpoint.State != conversation.StateAwaitingConfirmation {
		t.Fatalf("state = %q, want the thread still awaiting confirmation", checkpoint.State)
	}
}

// ---- retries --------------------------------------------------------------

// A duplicated confirmation is a retry of a request whose response was lost. It
// has to answer with the booking it already made, not with a second one and not
// with "nothing is pending" for a booking that exists.
func TestARepeatedConfirmationReplaysTheSameBooking(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, conversation.StateAwaitingConfirmation)

	first, err := h.service.Decide(ctx, fixtureThread, hitl.DecisionConfirm)
	if err != nil {
		t.Fatalf("first Decide: %v", err)
	}
	second, err := h.service.Decide(ctx, fixtureThread, hitl.DecisionConfirm)
	if err != nil {
		t.Fatalf("second Decide: %v", err)
	}
	if !second.Replayed {
		t.Fatal("the second confirmation did not report itself as a replay")
	}
	if first.Cancelled || second.Cancelled {
		t.Fatal("a confirm was recorded as a cancel")
	}
	if !sameResult(t, first.Result, second.Result) {
		t.Fatalf("the replay answered differently:\n first=%s\nsecond=%s",
			resultData(first.Result), resultData(second.Result))
	}
	// The tool is invoked again on purpose: idempotency is a requirement of the
	// policy, and re-running is what makes the answered body the tool's real
	// output rather than a remembered copy that could drift from it. The
	// assertion is therefore that the *grant is identical*, which is what lets
	// the tool resolve both invocations to one row.
	if h.runner.invocationCount() != 2 {
		t.Fatalf("tool invocations = %d, want 2 (the replay re-invokes)", h.runner.invocationCount())
	}
	grants := h.runner.grants
	if grants[0].RequestID != grants[1].RequestID {
		t.Fatalf("the replay carried a different request id: %q then %q",
			grants[0].RequestID, grants[1].RequestID)
	}
}

// Cancelling after a completed confirmation is asking to undo a booking through
// an endpoint that cannot express it. Reporting success would tell the user
// something untrue.
func TestCancellingAfterACompletedConfirmationIsRefused(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, conversation.StateAwaitingConfirmation)

	if _, err := h.service.Decide(ctx, fixtureThread, hitl.DecisionConfirm); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	before := h.runner.invocationCount()

	_, err := h.service.Decide(ctx, fixtureThread, hitl.DecisionCancel)
	if err == nil {
		t.Fatal("expected the late cancel to be refused")
	}
	if code := errs.CodeOf(err); code != errs.CodeAgentNoPendingAction {
		t.Fatalf("code = %q, want %q", code, errs.CodeAgentNoPendingAction)
	}
	if h.runner.invocationCount() != before {
		t.Fatal("the refused cancel still ran the tool")
	}
	checkpoint, loadErr := h.repo.LoadCheckpoint(ctx, fixtureThread)
	if loadErr != nil {
		t.Fatalf("LoadCheckpoint: %v", loadErr)
	}
	if checkpoint.State != conversation.StateCompleted {
		t.Fatalf("state = %q, want the completed booking left alone", checkpoint.State)
	}
}

// Two confirmations racing on one thread are two writers for one version. The
// loser is told `conflict` rather than silently re-deciding an action that has
// already been decided.
//
// The race is staged deterministically: the runner lets a competing writer claim
// the thread while the tool is running, which is exactly the window a real second
// request would occupy. What must not happen is the loser reporting a booking
// the thread does not record.
func TestAConfirmationThatLosesTheThreadReportsAConflict(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, conversation.StateAwaitingConfirmation)

	h.runner.onInvoke = func(ctx context.Context) {
		// Another turn on this thread finished first and wrote version 2.
		winner, err := h.repo.LoadCheckpoint(ctx, fixtureThread)
		if err != nil {
			t.Fatalf("LoadCheckpoint: %v", err)
		}
		winner.Version++
		winner.CreatedAt = fixtureNow
		if err := h.repo.SaveCheckpoint(ctx, winner); err != nil {
			t.Fatalf("the competing writer was refused: %v", err)
		}
	}

	_, err := h.service.Decide(ctx, fixtureThread, hitl.DecisionConfirm)
	if err == nil {
		t.Fatal("the losing confirmation reported success")
	}
	if code := errs.CodeOf(err); code != errs.CodeConflict {
		t.Fatalf("code = %q, want %q", code, errs.CodeConflict)
	}
	if h.runner.invocationCount() != 1 {
		t.Fatalf("tool invocations = %d, want 1", h.runner.invocationCount())
	}

	// The winner's checkpoint is intact: the loser did not overwrite it.
	checkpoint, loadErr := h.repo.LoadCheckpoint(ctx, fixtureThread)
	if loadErr != nil {
		t.Fatalf("LoadCheckpoint: %v", loadErr)
	}
	if checkpoint.Version != 2 {
		t.Fatalf("checkpoint version = %d, want the winner's 2", checkpoint.Version)
	}
}

// ---- helpers --------------------------------------------------------------

func sameResult(t *testing.T, a, b *domaintool.ToolResult) bool {
	t.Helper()
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return string(a.Data) == string(b.Data) && a.Status == b.Status
}

func resultData(result *domaintool.ToolResult) string {
	if result == nil {
		return "<nil>"
	}
	return string(result.Data)
}
