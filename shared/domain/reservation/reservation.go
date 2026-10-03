// Package reservation defines the mock reservation DTOs.
//
// The capability is intentionally small: it models a single restaurant's
// capacity per time slot, a hold that expires, and an idempotent confirmation.
// It exists so the agent has one write path whose only route to the database is
// a server-side confirmation, which is what makes "the model may request, only
// the user may authorise" a property of the code rather than of a prompt.
//
// The package is part of the pure domain layer and depends only on the standard
// library.
package reservation

import "time"

// Status is the lifecycle of a reservation.
//
// held and confirmed are distinct on purpose. A hold is what the user is asked
// to approve: it spends capacity but is not yet a booking. Confirming promotes
// the hold; the transition is one-way, because "I confirmed it, then it went
// back to held" is not a state a user can reason about.
type Status string

const (
	// StatusHeld is a capacity reservation awaiting user confirmation.
	StatusHeld Status = "held"
	// StatusConfirmed is a booking the user approved.
	StatusConfirmed Status = "confirmed"
	// StatusCancelled is a hold or booking the user declined or abandoned.
	StatusCancelled Status = "cancelled"
	// StatusExpired is a hold whose TTL ran out before confirmation.
	StatusExpired Status = "expired"
)

// Slot is one bookable time at one restaurant.
type Slot struct {
	SlotID       string `json:"slot_id"`
	RestaurantID int64  `json:"restaurant_id"`
	// SlotDate is an ISO calendar date, "2006-01-02".
	SlotDate string `json:"slot_date"`
	// SlotTime is a 24-hour local time, "15:04".
	SlotTime      string    `json:"slot_time"`
	Capacity      int       `json:"capacity"`
	Booked        int       `json:"booked"`
	PolicyVersion string    `json:"policy_version"`
	CreatedAt     time.Time `json:"created_at"`
}

// Remaining is how many seats the slot can still take.
func (s Slot) Remaining() int {
	remaining := s.Capacity - s.Booked
	if remaining < 0 {
		return 0
	}
	return remaining
}

// CanHold reports whether the slot has room for a party.
func (s Slot) CanHold(partySize int) bool {
	return partySize > 0 && s.Remaining() >= partySize
}

// Reservation is one party's booking at one slot.
type Reservation struct {
	ReservationID string `json:"reservation_id"`
	ThreadID      string `json:"thread_id"`
	UserID        string `json:"user_id"`
	RestaurantID  int64  `json:"restaurant_id"`
	SlotID        string `json:"slot_id"`
	PartySize     int    `json:"party_size"`
	Status        Status `json:"status"`
	// HoldExpiresAt bounds how long an unconfirmed hold blocks capacity. It is
	// nil once the reservation leaves the held state.
	HoldExpiresAt *time.Time `json:"hold_expires_at,omitempty"`
	// IdempotencyKey is derived server-side from the thread, the action, the
	// arguments, and the id of the request the user approved. It is unique in
	// the store, which is what turns a repeated confirmation into the same
	// booking instead of a second one.
	//
	// The request id rather than the checkpoint version is the ingredient that
	// makes this survive a retry: recording the outcome of a confirmation
	// advances the version, so a key derived from it could never be recomputed
	// on the retry that idempotency exists for.
	IdempotencyKey string    `json:"idempotency_key"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// IsActive reports whether the reservation still occupies capacity.
func (r Reservation) IsActive() bool {
	return r.Status == StatusHeld || r.Status == StatusConfirmed
}
