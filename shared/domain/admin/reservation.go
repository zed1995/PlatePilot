package admin

import (
	"github.com/zed1995/platepilot/shared/domain/reservation"
)

// The mock inventory views. The reservation feature is a demo: its whole point
// is that a demo can be replayed, and these DTOs are how an operator sees what
// a replay would spend and what a reset would give back.
//
// They reuse the reservation domain types directly rather than projecting
// copies: the JSON tags are already the wire contract, and a second struct
// would be one more place for the two to drift.

// InventoryView is one restaurant's bookable inventory as the admin surface
// serves it: the slots for the scope, and the reservations currently spending
// those seats. Both collections are arrays, never null.
type InventoryView struct {
	RestaurantID int64  `json:"restaurant_id"`
	// Date is the ISO date the view was scoped to, empty when it spans every
	// date the store holds for the restaurant.
	Date         string                    `json:"date,omitempty"`
	Slots        []reservation.Slot        `json:"slots"`
	Reservations []reservation.Reservation `json:"reservations"`
}

// InventoryResetResult reports what one reset did, so the caller can tell
// "nothing to reset" from "it worked and here is what came back".
type InventoryResetResult struct {
	RestaurantID int64  `json:"restaurant_id"`
	Date         string `json:"date,omitempty"`
	// SlotsReset counts only slots that were actually carrying bookings; a
	// pristine slot is not news.
	SlotsReset int `json:"slots_reset"`
	// ReservationsRemoved counts the reservations the reset deleted, idempotency
	// keys included.
	ReservationsRemoved int `json:"reservations_removed"`
}
