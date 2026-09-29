// Package conversation defines conversation and checkpoint DTOs.
package conversation

import "time"

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
	ThreadID             string    `json:"thread_id"`
	Version              int64     `json:"version"`
	State                State     `json:"state"`
	PendingAction        string    `json:"pending_action,omitempty"`
	MissingSlots         []string  `json:"missing_slots,omitempty"`
	EvidenceIDs          []string  `json:"evidence_ids,omitempty"`
	SelectedRestaurantID string    `json:"selected_restaurant_id,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
}
