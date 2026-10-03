// Package conversation defines conversation and checkpoint DTOs.
package conversation

import (
	"encoding/json"
	"time"
)

// State is the persisted business state of a thread. Eino manages the runtime
// graph, but recovery always reads this state rather than trusting memory.
type State string

const (
	StateIdle                  State = "idle"
	StateAwaitingClarification State = "awaiting_clarification"
	StateAwaitingConfirmation  State = "awaiting_confirmation"
	StateCompleted             State = "completed"
	StateFailed                State = "failed"
)

// Conversation is thread metadata.
type Conversation struct {
	ThreadID      string    `json:"thread_id"`
	UserID        string    `json:"user_id,omitempty"`
	Title         string    `json:"title,omitempty"`
	CurrentState  State     `json:"current_state"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	LastMessageAt time.Time `json:"last_message_at"`
}

// Checkpoint is the recoverable state of a thread.
type Checkpoint struct {
	ThreadID      string   `json:"thread_id"`
	Version       int64    `json:"version"`
	State         State    `json:"state"`
	PendingAction string   `json:"pending_action,omitempty"`
	MissingSlots  []string `json:"missing_slots,omitempty"`
	// These reference rows the database owns, so they carry the database id
	// type. ThreadID stays a string: it is minted by the transport layer, not by
	// an identity column.
	EvidenceIDs          []int64   `json:"evidence_ids,omitempty"`
	SelectedRestaurantID int64     `json:"selected_restaurant_id,omitempty"`
	CreatedAt            time.Time `json:"created_at"`

	// PendingToolCallID and PendingArguments carry an action the thread has
	// parked until the user confirms it.
	//
	// The model may request a write, but only the user may authorise one, and
	// the request has to outlive the process that produced it: a confirmation
	// arrives as a later HTTP call, possibly after a restart. Storing the
	// pending call beside the checkpoint is what makes "the model asked, the
	// user has not answered yet" a durable fact rather than a runtime variable.
	//
	// PendingArguments is the approved call's arguments verbatim, because a
	// confirmation replays the call the user was shown — a digest would name it
	// without being able to run it. Nothing secret belongs in a tool's
	// arguments for this reason: whatever is here is stored in the clear and is
	// replayed on approval.
	//
	// PendingToolCallID is minted server-side when the call is parked, so it
	// names one approval attempt. Anything derived from it — an idempotency key,
	// say — is therefore stable across the retries of that attempt and different
	// for the next request on the same thread.
	PendingToolCallID string          `json:"pending_tool_call_id,omitempty"`
	PendingArguments  json.RawMessage `json:"pending_arguments,omitempty"`

	// ClarificationCount is how many consecutive clarification rounds this
	// thread has had without reaching an answer.
	//
	// It is persisted rather than counted in memory because each round happens
	// in a different request: a thread that asks "which Katz's?" three times
	// does so across three turns, and an in-process counter would restart at
	// zero every time — which is exactly the loop the cap exists to break.
	//
	// It resets to zero on any turn that ends without a pending clarification,
	// so a thread that clarifies, resolves, and later clarifies again gets a
	// fresh budget rather than inheriting a spent one.
	ClarificationCount int `json:"clarification_count,omitempty"`
}
