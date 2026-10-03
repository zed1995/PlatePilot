package memory

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/reservation"
	"github.com/zed1995/platepilot/shared/idgen"
)

// ReservationRepository is an in-memory store.ReservationRepository.
type ReservationRepository struct {
	mu           sync.Mutex
	slots        map[string]reservation.Slot
	reservations map[string]reservation.Reservation
	byKey        map[string]string
}

// NewReservationRepository returns an empty in-memory reservation repository.
func NewReservationRepository() *ReservationRepository {
	return &ReservationRepository{
		slots:        make(map[string]reservation.Slot),
		reservations: make(map[string]reservation.Reservation),
		byKey:        make(map[string]string),
	}
}

// EnsureSlots creates the slots that do not exist yet and leaves the rest
// untouched.
func (r *ReservationRepository) EnsureSlots(_ context.Context, slots []reservation.Slot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, slot := range slots {
		if strings.TrimSpace(slot.SlotID) == "" {
			return errs.New(errs.CodeInvalidArgument, "slot_id is required")
		}
		if _, exists := r.slots[slot.SlotID]; exists {
			// An existing slot keeps its booked count: re-seeding an inventory
			// must not erase capacity a live hold is holding.
			continue
		}
		if slot.CreatedAt.IsZero() {
			slot.CreatedAt = time.Now().UTC()
		}
		r.slots[slot.SlotID] = slot
	}
	return nil
}

// ListSlots returns one restaurant's slots for one date, in time order.
func (r *ReservationRepository) ListSlots(_ context.Context, restaurantID int64, date string) ([]reservation.Slot, error) {
	if restaurantID <= 0 {
		return nil, errs.New(errs.CodeInvalidArgument, "restaurant_id must be positive")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]reservation.Slot, 0)
	for _, slot := range r.slots {
		if slot.RestaurantID == restaurantID && slot.SlotDate == date {
			out = append(out, slot)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SlotTime != out[j].SlotTime {
			return out[i].SlotTime < out[j].SlotTime
		}
		return out[i].SlotID < out[j].SlotID
	})
	return out, nil
}

// GetSlot returns one slot by id.
func (r *ReservationRepository) GetSlot(_ context.Context, slotID string) (reservation.Slot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	slot, ok := r.slots[slotID]
	if !ok {
		return reservation.Slot{}, errs.Newf(errs.CodeNotFound, "slot %q not found", slotID)
	}
	return slot, nil
}

// HoldSlot reserves seats when the slot has room.
func (r *ReservationRepository) HoldSlot(_ context.Context, slotID string, partySize int) (reservation.Slot, error) {
	if partySize <= 0 {
		return reservation.Slot{}, errs.New(errs.CodeInvalidArgument, "party_size must be positive")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	slot, ok := r.slots[slotID]
	if !ok {
		return reservation.Slot{}, errs.Newf(errs.CodeNotFound, "slot %q not found", slotID)
	}
	if !slot.CanHold(partySize) {
		return reservation.Slot{}, errs.Newf(errs.CodeReservationUnavailable,
			"slot %s has %d seats left and the party is %d", slotID, slot.Remaining(), partySize)
	}
	slot.Booked += partySize
	r.slots[slotID] = slot
	return slot, nil
}

// ReleaseSlot returns seats to a slot.
func (r *ReservationRepository) ReleaseSlot(_ context.Context, slotID string, partySize int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	slot, ok := r.slots[slotID]
	if !ok {
		return errs.Newf(errs.CodeNotFound, "slot %q not found", slotID)
	}
	slot.Booked -= partySize
	if slot.Booked < 0 {
		slot.Booked = 0
	}
	r.slots[slotID] = slot
	return nil
}

// SaveReservation inserts or updates a reservation, enforcing the idempotency
// key's uniqueness.
func (r *ReservationRepository) SaveReservation(_ context.Context, res reservation.Reservation) error {
	if strings.TrimSpace(res.ReservationID) == "" {
		res.ReservationID = idgen.NewUUID()
	}
	if strings.TrimSpace(res.IdempotencyKey) == "" {
		return errs.New(errs.CodeInvalidArgument, "idempotency_key is required")
	}
	now := time.Now().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	if existingID, taken := r.byKey[res.IdempotencyKey]; taken && existingID != res.ReservationID {
		return errs.Newf(errs.CodeConflict,
			"idempotency key %q already belongs to reservation %q", res.IdempotencyKey, existingID)
	}
	if existing, ok := r.reservations[res.ReservationID]; ok {
		if res.CreatedAt.IsZero() {
			res.CreatedAt = existing.CreatedAt
		}
	} else if res.CreatedAt.IsZero() {
		res.CreatedAt = now
	}
	res.UpdatedAt = now
	r.reservations[res.ReservationID] = res
	r.byKey[res.IdempotencyKey] = res.ReservationID
	return nil
}

// GetReservation returns one reservation by id.
func (r *ReservationRepository) GetReservation(_ context.Context, reservationID string) (reservation.Reservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	res, ok := r.reservations[reservationID]
	if !ok {
		return reservation.Reservation{}, errs.Newf(errs.CodeNotFound,
			"reservation %q not found", reservationID)
	}
	return res, nil
}

// GetByIdempotencyKey returns the reservation a key already produced.
func (r *ReservationRepository) GetByIdempotencyKey(_ context.Context, key string) (reservation.Reservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	reservationID, ok := r.byKey[key]
	if !ok {
		return reservation.Reservation{}, errs.Newf(errs.CodeNotFound,
			"no reservation for idempotency key %q", key)
	}
	res, ok := r.reservations[reservationID]
	if !ok {
		return reservation.Reservation{}, errs.Newf(errs.CodeNotFound,
			"reservation %q not found", reservationID)
	}
	return res, nil
}

// ReleaseExpiredHolds expires held reservations whose TTL passed.
func (r *ReservationRepository) ReleaseExpiredHolds(_ context.Context, now time.Time) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	expired := 0
	for id, res := range r.reservations {
		if res.Status != reservation.StatusHeld || res.HoldExpiresAt == nil {
			continue
		}
		if res.HoldExpiresAt.After(now) {
			continue
		}
		if slot, ok := r.slots[res.SlotID]; ok {
			slot.Booked -= res.PartySize
			if slot.Booked < 0 {
				slot.Booked = 0
			}
			r.slots[res.SlotID] = slot
		}
		res.Status = reservation.StatusExpired
		res.UpdatedAt = now
		r.reservations[id] = res
		expired++
	}
	return expired, nil
}
