package contract

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/reservation"
	"github.com/zed1995/platepilot/shared/store"
)

// runReservationRepositoryContract pins the mock reservation store both
// adapters must share.
//
// The rules that matter are the ones a Mock still has to get right: capacity is
// enforced atomically, a hold can be released, an idempotency key is unique, and
// expiring a hold returns its seats. A mock that oversells or duplicates is
// worse than no mock, because the confirmation flow is tested against it.
func runReservationRepositoryContract(t *testing.T, reservations store.ReservationRepository) {
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

	seed := func(restaurantID int64, date string, slotID string, at string, capacity int) reservation.Slot {
		t.Helper()
		slot := reservation.Slot{
			SlotID:        slotID,
			RestaurantID:  restaurantID,
			SlotDate:      date,
			SlotTime:      at,
			Capacity:      capacity,
			PolicyVersion: "policy-v1",
			CreatedAt:     now,
		}
		if err := reservations.EnsureSlots(ctx, []reservation.Slot{slot}); err != nil {
			t.Fatalf("EnsureSlots: %v", err)
		}
		return slot
	}

	t.Run("ensure_and_list_slots", func(t *testing.T) {
		const restaurantID = 9001
		const date = "2026-10-10"
		seed(restaurantID, date, "r9001-1915", "19:15", 8)
		seed(restaurantID, date, "r9001-1900", "19:00", 8)
		// A different day must not leak into the listing.
		seed(restaurantID, "2026-10-11", "r9001-1900-next", "19:00", 8)

		slots, err := reservations.ListSlots(ctx, restaurantID, date)
		if err != nil {
			t.Fatalf("ListSlots: %v", err)
		}
		if len(slots) != 2 {
			t.Fatalf("listed %d slots, want 2 (only the requested day)", len(slots))
		}
		// Time order, not slot_id order.
		if slots[0].SlotTime != "19:00" || slots[1].SlotTime != "19:15" {
			t.Fatalf("slots out of time order: %+v", slots)
		}
		if slots[0].Capacity != 8 || slots[0].Booked != 0 || slots[0].PolicyVersion != "policy-v1" {
			t.Fatalf("slot round trip lost fields: %+v", slots[0])
		}

		// Re-seeding must not reset capacity a live hold is holding.
		if _, err := reservations.HoldSlot(ctx, "r9001-1900", 3); err != nil {
			t.Fatalf("HoldSlot: %v", err)
		}
		seed(restaurantID, date, "r9001-1900", "19:00", 8)
		got, err := reservations.GetSlot(ctx, "r9001-1900")
		if err != nil {
			t.Fatalf("GetSlot: %v", err)
		}
		if got.Booked != 3 {
			t.Fatalf("re-seeding reset booked to %d, want 3", got.Booked)
		}

		if _, err := reservations.GetSlot(ctx, "no-such-slot"); !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("GetSlot missing: want ErrNotFound, got %v", err)
		}
		if _, err := reservations.ListSlots(ctx, 0, date); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("ListSlots without restaurant: want invalid_argument, got %v", err)
		}
	})

	t.Run("hold_respects_capacity", func(t *testing.T) {
		const restaurantID = 9002
		const date = "2026-10-12"
		seed(restaurantID, date, "r9002-1800", "18:00", 4)

		held, err := reservations.HoldSlot(ctx, "r9002-1800", 4)
		if err != nil {
			t.Fatalf("HoldSlot: %v", err)
		}
		if held.Booked != 4 || held.Remaining() != 0 {
			t.Fatalf("hold did not consume capacity: %+v", held)
		}
		// The slot is now full; another party must be refused, not oversold.
		_, err = reservations.HoldSlot(ctx, "r9002-1800", 1)
		if !errors.Is(err, errs.ErrReservationUnavailable) {
			t.Fatalf("over-capacity hold: want reservation_unavailable, got %v", err)
		}
		// Releasing returns the seats.
		if err := reservations.ReleaseSlot(ctx, "r9002-1800", 4); err != nil {
			t.Fatalf("ReleaseSlot: %v", err)
		}
		after, err := reservations.GetSlot(ctx, "r9002-1800")
		if err != nil {
			t.Fatalf("GetSlot: %v", err)
		}
		if after.Booked != 0 {
			t.Fatalf("release left booked=%d, want 0", after.Booked)
		}
		if _, err := reservations.HoldSlot(ctx, "no-such-slot", 1); !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("hold on missing slot: want ErrNotFound, got %v", err)
		}
		if _, err := reservations.HoldSlot(ctx, "r9002-1800", 0); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("hold with party_size 0: want invalid_argument, got %v", err)
		}
	})

	t.Run("reservation_round_trip_and_idempotency", func(t *testing.T) {
		const restaurantID = 9003
		const date = "2026-10-13"
		seed(restaurantID, date, "r9003-2000", "20:00", 10)

		expires := now.Add(10 * time.Minute)
		record := reservation.Reservation{
			ReservationID:  "res-1",
			ThreadID:       "thread-res",
			UserID:         "user-res",
			RestaurantID:   restaurantID,
			SlotID:         "r9003-2000",
			PartySize:      2,
			Status:         reservation.StatusHeld,
			HoldExpiresAt:  &expires,
			IdempotencyKey: "key-1",
			CreatedAt:      now,
		}
		if err := reservations.SaveReservation(ctx, record); err != nil {
			t.Fatalf("SaveReservation: %v", err)
		}
		got, err := reservations.GetReservation(ctx, "res-1")
		if err != nil {
			t.Fatalf("GetReservation: %v", err)
		}
		if got.Status != reservation.StatusHeld || got.PartySize != 2 ||
			got.HoldExpiresAt == nil || got.IdempotencyKey != "key-1" {
			t.Fatalf("reservation round trip lost fields: %+v", got)
		}

		byKey, err := reservations.GetByIdempotencyKey(ctx, "key-1")
		if err != nil {
			t.Fatalf("GetByIdempotencyKey: %v", err)
		}
		if byKey.ReservationID != "res-1" {
			t.Fatalf("key lookup returned %q, want res-1", byKey.ReservationID)
		}
		if _, err := reservations.GetByIdempotencyKey(ctx, "key-missing"); !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("unknown key: want ErrNotFound, got %v", err)
		}

		// Confirming updates in place; it must not mint a second row.
		got.Status = reservation.StatusConfirmed
		got.HoldExpiresAt = nil
		if err := reservations.SaveReservation(ctx, got); err != nil {
			t.Fatalf("SaveReservation (confirm): %v", err)
		}
		again, err := reservations.GetByIdempotencyKey(ctx, "key-1")
		if err != nil {
			t.Fatalf("GetByIdempotencyKey after confirm: %v", err)
		}
		if again.Status != reservation.StatusConfirmed || again.HoldExpiresAt != nil {
			t.Fatalf("confirm not persisted: %+v", again)
		}

		// A second reservation must not be able to reuse the key.
		other := record
		other.ReservationID = "res-2"
		other.SlotID = "r9003-2000"
		if err := reservations.SaveReservation(ctx, other); !errors.Is(err, errs.ErrConflict) {
			t.Fatalf("reused idempotency key: want conflict, got %v", err)
		}
		// An empty key is a request defect.
		noKey := record
		noKey.ReservationID = "res-3"
		noKey.IdempotencyKey = ""
		if err := reservations.SaveReservation(ctx, noKey); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("empty idempotency key: want invalid_argument, got %v", err)
		}
		if _, err := reservations.GetReservation(ctx, "res-missing"); !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("GetReservation missing: want ErrNotFound, got %v", err)
		}
	})

	t.Run("release_expired_holds_returns_seats", func(t *testing.T) {
		const restaurantID = 9004
		const date = "2026-10-14"
		seed(restaurantID, date, "r9004-1700", "17:00", 6)
		// Two holds of two seats each: one will expire, one will not.
		for i := 0; i < 2; i++ {
			if _, err := reservations.HoldSlot(ctx, "r9004-1700", 2); err != nil {
				t.Fatalf("HoldSlot %d: %v", i, err)
			}
		}
		past := now.Add(-time.Minute)
		future := now.Add(10 * time.Minute)
		for i, expiry := range []*time.Time{&past, &future} {
			held := reservation.Reservation{
				ReservationID:  fmt.Sprintf("exp-%d", i),
				ThreadID:       "thread-exp",
				RestaurantID:   restaurantID,
				SlotID:         "r9004-1700",
				PartySize:      2,
				Status:         reservation.StatusHeld,
				HoldExpiresAt:  expiry,
				IdempotencyKey: fmt.Sprintf("exp-key-%d", i),
				CreatedAt:      now,
			}
			if err := reservations.SaveReservation(ctx, held); err != nil {
				t.Fatalf("SaveReservation: %v", err)
			}
		}

		expired, err := reservations.ReleaseExpiredHolds(ctx, now)
		if err != nil {
			t.Fatalf("ReleaseExpiredHolds: %v", err)
		}
		if expired != 1 {
			t.Fatalf("expired %d holds, want 1", expired)
		}
		slot, err := reservations.GetSlot(ctx, "r9004-1700")
		if err != nil {
			t.Fatalf("GetSlot: %v", err)
		}
		if slot.Booked != 2 {
			t.Fatalf("booked = %d, want 2 (4 held minus the 2 expired)", slot.Booked)
		}
		nowExpired, err := reservations.GetReservation(ctx, "exp-0")
		if err != nil {
			t.Fatalf("GetReservation: %v", err)
		}
		if nowExpired.Status != reservation.StatusExpired {
			t.Fatalf("expired reservation status = %q, want expired", nowExpired.Status)
		}
		stillHeld, err := reservations.GetReservation(ctx, "exp-1")
		if err != nil {
			t.Fatalf("GetReservation (future): %v", err)
		}
		if stillHeld.Status != reservation.StatusHeld {
			t.Fatalf("future hold status = %q, want held", stillHeld.Status)
		}
		// A second sweep with nothing left to expire is a no-op.
		again, err := reservations.ReleaseExpiredHolds(ctx, now)
		if err != nil {
			t.Fatalf("ReleaseExpiredHolds (repeat): %v", err)
		}
		if again != 0 {
			t.Fatalf("second sweep expired %d, want 0", again)
		}
	})

	// The mock inventory's admin surface: a view that says where the demo's
	// seats went, and a reset that gives them back. A reset that left the
	// idempotency keys behind would replay yesterday's bookings as if they
	// were fresh writes, which is the one way a reset could lie.
	t.Run("list_and_reset_inventory", func(t *testing.T) {
		const restaurantID = 9005
		const demoDate = "2026-10-10"
		const otherDate = "2026-10-11"

		seed(restaurantID, demoDate, "r9005-1900", "19:00", 6)
		seed(restaurantID, demoDate, "r9005-1930", "19:30", 6)
		// A neighbouring day exists so a scoped reset cannot quietly become a
		// global one.
		seed(restaurantID, otherDate, "r9005-1900-next", "19:00", 6)

		// One confirmed booking on the demo date, one hold on the other date.
		if _, err := reservations.HoldSlot(ctx, "r9005-1900", 4); err != nil {
			t.Fatalf("HoldSlot (demo date): %v", err)
		}
		confirmed := reservation.Reservation{
			ReservationID:  "res-9005-a",
			RestaurantID:   restaurantID,
			SlotID:         "r9005-1900",
			PartySize:      4,
			Status:         reservation.StatusConfirmed,
			IdempotencyKey: "key-9005-a",
			CreatedAt:      now,
		}
		if err := reservations.SaveReservation(ctx, confirmed); err != nil {
			t.Fatalf("SaveReservation (demo date): %v", err)
		}
		if _, err := reservations.HoldSlot(ctx, "r9005-1900-next", 2); err != nil {
			t.Fatalf("HoldSlot (other date): %v", err)
		}
		held := reservation.Reservation{
			ReservationID:  "res-9005-b",
			RestaurantID:   restaurantID,
			SlotID:         "r9005-1900-next",
			PartySize:      2,
			Status:         reservation.StatusHeld,
			IdempotencyKey: "key-9005-b",
			CreatedAt:      now,
		}
		if err := reservations.SaveReservation(ctx, held); err != nil {
			t.Fatalf("SaveReservation (other date): %v", err)
		}

		// The view is scoped the same way the reset is: a date must not leak
		// the other day's rows, and an empty date spans all of them.
		dayRows, err := reservations.ListReservations(ctx, restaurantID, demoDate)
		if err != nil {
			t.Fatalf("ListReservations (scoped): %v", err)
		}
		if len(dayRows) != 1 || dayRows[0].ReservationID != "res-9005-a" {
			t.Fatalf("scoped listing = %+v, want only res-9005-a", dayRows)
		}
		allRows, err := reservations.ListReservations(ctx, restaurantID, "")
		if err != nil {
			t.Fatalf("ListReservations (all dates): %v", err)
		}
		if len(allRows) != 2 {
			t.Fatalf("unscoped listing = %+v, want both reservations", allRows)
		}

		// ListSlots must treat an empty date the same way: every date comes
		// back. This is the branch the inventory view's default scope walks,
		// and a '' date literal is a Postgres runtime error, not a filter.
		allSlots, err := reservations.ListSlots(ctx, restaurantID, "")
		if err != nil {
			t.Fatalf("ListSlots (all dates): %v", err)
		}
		if len(allSlots) != 3 {
			t.Fatalf("unscoped slot listing = %+v, want all 3 slots", allSlots)
		}
		daySlots, err := reservations.ListSlots(ctx, restaurantID, demoDate)
		if err != nil {
			t.Fatalf("ListSlots (scoped): %v", err)
		}
		if len(daySlots) != 2 {
			t.Fatalf("scoped slot listing = %+v, want the demo date's 2 slots", daySlots)
		}
		if _, err := reservations.ListReservations(ctx, 0, demoDate); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("ListReservations without restaurant: want invalid_argument, got %v", err)
		}

		// The scoped reset frees exactly the demo date and nothing else.
		// slotsReset counts only slots that actually carried bookings: the
		// never-booked 19:30 slot coming back clean is not news.
		slotsReset, removed, err := reservations.ResetInventory(ctx, restaurantID, demoDate)
		if err != nil {
			t.Fatalf("ResetInventory: %v", err)
		}
		if slotsReset != 1 {
			t.Fatalf("reset %d slots, want 1", slotsReset)
		}
		if removed != 1 {
			t.Fatalf("removed %d reservations, want 1", removed)
		}

		freed, err := reservations.GetSlot(ctx, "r9005-1900")
		if err != nil {
			t.Fatalf("GetSlot after reset: %v", err)
		}
		if freed.Booked != 0 || freed.Capacity != 6 {
			t.Fatalf("slot after reset = %+v, want 0/6", freed)
		}
		if _, err := reservations.GetReservation(ctx, "res-9005-a"); !errors.Is(err, errs.ErrNotFound) {
			t.Fatalf("reset reservation survived: %v", err)
		}
		untouched, err := reservations.GetReservation(ctx, "res-9005-b")
		if err != nil {
			t.Fatalf("GetReservation (other date): %v", err)
		}
		if untouched.Status != reservation.StatusHeld {
			t.Fatalf("other date's hold = %q, want held", untouched.Status)
		}
		next, err := reservations.GetSlot(ctx, "r9005-1900-next")
		if err != nil {
			t.Fatalf("GetSlot (other date): %v", err)
		}
		if next.Booked != 2 {
			t.Fatalf("other date's slot booked = %d, want 2", next.Booked)
		}

		// The spent idempotency key must be free again: saving a new
		// reservation under the old key is what a replayed demo does first.
		replayed := confirmed
		replayed.ReservationID = "res-9005-c"
		replayed.Status = reservation.StatusHeld
		replayed.IdempotencyKey = "key-9005-a"
		if err := reservations.SaveReservation(ctx, replayed); err != nil {
			t.Fatalf("SaveReservation with a reset key: %v", err)
		}

		// The unscoped reset sweeps the rest, including the untouched day.
		slotsReset, removed, err = reservations.ResetInventory(ctx, restaurantID, "")
		if err != nil {
			t.Fatalf("ResetInventory (all dates): %v", err)
		}
		if removed != 2 {
			t.Fatalf("unscoped reset removed %d reservations, want 2", removed)
		}
		if slotsReset != 1 {
			// Only the next date's slot still carries seats: the demo date's
			// slots were already clean, and the replayed row was saved without
			// holding capacity again.
			t.Fatalf("unscoped reset cleared %d slots, want 1", slotsReset)
		}
		// An invalid restaurant is rejected rather than treated as "nothing".
		if _, _, err := reservations.ResetInventory(ctx, 0, ""); errs.CodeOf(err) != errs.CodeInvalidArgument {
			t.Fatalf("ResetInventory without restaurant: want invalid_argument, got %v", err)
		}
	})
}
