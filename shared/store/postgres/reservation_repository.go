package postgres

import (
	"context"
	"strings"
	"time"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/reservation"
	"github.com/zed1995/platepilot/shared/store"
)

var _ store.ReservationRepository = (*ReservationRepository)(nil)

// ReservationRepository is the PostgreSQL store.ReservationRepository.
//
// slot_date and slot_time are date and time columns, and every statement reads
// them back through to_char so the domain keeps them as the ISO strings it
// models. Letting the driver hand back a time.Time would make the round trip
// depend on the session time zone, which is exactly the kind of implicit
// conversion that turns an 19:00 booking into an 18:00 one somewhere else.
type ReservationRepository struct {
	client *Client
}

// NewReservationRepository builds a reservation repository on an existing client.
func NewReservationRepository(client *Client) *ReservationRepository {
	return &ReservationRepository{client: client}
}

const slotColumns = `
	slot_id, restaurant_id,
	to_char(slot_date, 'YYYY-MM-DD') AS slot_date,
	to_char(slot_time, 'HH24:MI') AS slot_time,
	capacity, booked, policy_version, created_at`

const reservationColumns = `
	reservation_id, thread_id, user_id, restaurant_id, slot_id, party_size,
	status, hold_expires_at, idempotency_key, created_at, updated_at`

// prefixedReservationColumns is the same projection against an aliased table,
// for queries that join reservations with its slots.
const prefixedReservationColumns = `
	r.reservation_id, r.thread_id, r.user_id, r.restaurant_id, r.slot_id,
	r.party_size, r.status, r.hold_expires_at, r.idempotency_key,
	r.created_at, r.updated_at`

// EnsureSlots inserts the slots that do not exist yet, in one statement.
func (r *ReservationRepository) EnsureSlots(ctx context.Context, slots []reservation.Slot) error {
	if len(slots) == 0 {
		return nil
	}
	ids := make([]string, 0, len(slots))
	restaurantIDs := make([]int64, 0, len(slots))
	dates := make([]string, 0, len(slots))
	times := make([]string, 0, len(slots))
	capacities := make([]int32, 0, len(slots))
	booked := make([]int32, 0, len(slots))
	policies := make([]string, 0, len(slots))
	created := make([]time.Time, 0, len(slots))
	for _, slot := range slots {
		if strings.TrimSpace(slot.SlotID) == "" {
			return errs.New(errs.CodeInvalidArgument, "slot_id is required")
		}
		if slot.Capacity <= 0 {
			return errs.Newf(errs.CodeInvalidArgument,
				"slot %q capacity must be positive (got %d)", slot.SlotID, slot.Capacity)
		}
		createdAt := slot.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		ids = append(ids, slot.SlotID)
		restaurantIDs = append(restaurantIDs, slot.RestaurantID)
		dates = append(dates, slot.SlotDate)
		times = append(times, slot.SlotTime)
		capacities = append(capacities, int32(slot.Capacity))
		booked = append(booked, int32(slot.Booked))
		policies = append(policies, slot.PolicyVersion)
		created = append(created, createdAt)
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	_, err := r.client.pool.Exec(ctx, `
		INSERT INTO reservation_slots (
			slot_id, restaurant_id, slot_date, slot_time,
			capacity, booked, policy_version, created_at
		)
		SELECT s.slot_id, s.restaurant_id, s.slot_date::date, s.slot_time::time,
		       s.capacity, s.booked, s.policy_version, s.created_at
		FROM unnest($1::text[], $2::bigint[], $3::text[], $4::text[],
		            $5::int[], $6::int[], $7::text[], $8::timestamptz[])
		     AS s(slot_id, restaurant_id, slot_date, slot_time,
		          capacity, booked, policy_version, created_at)
		ON CONFLICT (slot_id) DO NOTHING`,
		ids, restaurantIDs, dates, times, capacities, booked, policies, created)
	if err != nil {
		return operationError("postgres: ensure reservation slots", err)
	}
	return nil
}

// ListSlots returns one restaurant's slots for one date, in time order.
func (r *ReservationRepository) ListSlots(ctx context.Context, restaurantID int64, date string) ([]reservation.Slot, error) {
	if restaurantID <= 0 {
		return nil, errs.New(errs.CodeInvalidArgument, "restaurant_id must be positive")
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	// An empty date spans every date; a present one filters to that day. The
	// scope is assembled rather than parameterized because '' is not a valid
	// date literal, and "all dates" and "one date" are different predicates.
	scope := `WHERE restaurant_id = $1`
	args := []any{restaurantID}
	if date != "" {
		scope += ` AND slot_date = $2::date`
		args = append(args, date)
	}

	rows, err := r.client.pool.Query(ctx, `
		SELECT `+slotColumns+`
		FROM reservation_slots
		`+scope+`
		ORDER BY slot_time ASC, slot_id ASC`,
		args...)
	if err != nil {
		return nil, operationError("postgres: list reservation slots", err)
	}
	defer rows.Close()

	out := make([]reservation.Slot, 0)
	for rows.Next() {
		slot, err := scanSlot(rows.Scan)
		if err != nil {
			return nil, operationError("postgres: scan reservation slot", err)
		}
		out = append(out, slot)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate reservation slots", err)
	}
	return out, nil
}

// GetSlot returns one slot by id.
func (r *ReservationRepository) GetSlot(ctx context.Context, slotID string) (reservation.Slot, error) {
	if strings.TrimSpace(slotID) == "" {
		return reservation.Slot{}, errs.New(errs.CodeInvalidArgument, "slot_id is required")
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	slot, err := scanSlot(r.client.pool.QueryRow(ctx, `
		SELECT `+slotColumns+`
		FROM reservation_slots WHERE slot_id = $1`, slotID).Scan)
	if err != nil {
		if pgErrNoRows(err) {
			return reservation.Slot{}, errs.Newf(errs.CodeNotFound, "slot %q not found", slotID)
		}
		return reservation.Slot{}, operationError("postgres: get reservation slot", err)
	}
	return slot, nil
}

// HoldSlot reserves seats when the slot has room.
//
// The capacity check lives in the UPDATE's own predicate, so the read and the
// increment are one atomic statement: two concurrent holds for the last seat
// race on the row, and the loser's statement matches nothing rather than both
// seeing room.
func (r *ReservationRepository) HoldSlot(ctx context.Context, slotID string, partySize int) (reservation.Slot, error) {
	if partySize <= 0 {
		return reservation.Slot{}, errs.New(errs.CodeInvalidArgument, "party_size must be positive")
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	slot, err := scanSlot(r.client.pool.QueryRow(ctx, `
		UPDATE reservation_slots
		SET booked = booked + $2
		WHERE slot_id = $1 AND booked + $2 <= capacity
		RETURNING `+slotColumns, slotID, partySize).Scan)
	switch {
	case err == nil:
		return slot, nil
	case pgErrNoRows(err):
		// Distinguish "no such slot" from "no room". The distinction is what
		// lets a caller report a missing slot id as a defect and a full slot as
		// a state the user can act on (pick another time).
		if _, lookupErr := r.GetSlot(ctx, slotID); lookupErr != nil {
			return reservation.Slot{}, lookupErr
		}
		return reservation.Slot{}, errs.Newf(errs.CodeReservationUnavailable,
			"slot %s has no room for a party of %d", slotID, partySize)
	default:
		return reservation.Slot{}, operationError("postgres: hold reservation slot", err)
	}
}

// ReleaseSlot returns seats to a slot.
func (r *ReservationRepository) ReleaseSlot(ctx context.Context, slotID string, partySize int) error {
	if partySize <= 0 {
		return errs.New(errs.CodeInvalidArgument, "party_size must be positive")
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	tag, err := r.client.pool.Exec(ctx, `
		UPDATE reservation_slots
		SET booked = GREATEST(booked - $2, 0)
		WHERE slot_id = $1`, slotID, partySize)
	if err != nil {
		return operationError("postgres: release reservation slot", err)
	}
	if tag.RowsAffected() == 0 {
		return errs.Newf(errs.CodeNotFound, "slot %q not found", slotID)
	}
	return nil
}

// SaveReservation inserts or updates a reservation, preserving created_at.
func (r *ReservationRepository) SaveReservation(ctx context.Context, res reservation.Reservation) error {
	if strings.TrimSpace(res.ReservationID) == "" {
		return errs.New(errs.CodeInvalidArgument, "reservation_id is required")
	}
	if strings.TrimSpace(res.IdempotencyKey) == "" {
		return errs.New(errs.CodeInvalidArgument, "idempotency_key is required")
	}
	createdAt := res.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	updatedAt := res.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	_, err := r.client.pool.Exec(ctx, `
		INSERT INTO reservations (
			reservation_id, thread_id, user_id, restaurant_id, slot_id, party_size,
			status, hold_expires_at, idempotency_key, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (reservation_id) DO UPDATE SET
			thread_id       = EXCLUDED.thread_id,
			user_id         = EXCLUDED.user_id,
			restaurant_id   = EXCLUDED.restaurant_id,
			slot_id         = EXCLUDED.slot_id,
			party_size      = EXCLUDED.party_size,
			status          = EXCLUDED.status,
			hold_expires_at = EXCLUDED.hold_expires_at,
			idempotency_key = EXCLUDED.idempotency_key,
			created_at      = reservations.created_at,
			updated_at      = EXCLUDED.updated_at`,
		res.ReservationID, res.ThreadID, res.UserID, res.RestaurantID, res.SlotID, res.PartySize,
		string(res.Status), res.HoldExpiresAt, res.IdempotencyKey, createdAt, updatedAt)
	if err != nil {
		return operationError("postgres: save reservation", err)
	}
	return nil
}

// GetReservation returns one reservation by id.
func (r *ReservationRepository) GetReservation(ctx context.Context, reservationID string) (reservation.Reservation, error) {
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	res, err := scanReservation(r.client.pool.QueryRow(ctx, `
		SELECT `+reservationColumns+`
		FROM reservations WHERE reservation_id = $1`, reservationID).Scan)
	if err != nil {
		if pgErrNoRows(err) {
			return reservation.Reservation{}, errs.Newf(errs.CodeNotFound,
				"reservation %q not found", reservationID)
		}
		return reservation.Reservation{}, operationError("postgres: get reservation", err)
	}
	return res, nil
}

// GetByIdempotencyKey returns the reservation a key already produced.
func (r *ReservationRepository) GetByIdempotencyKey(ctx context.Context, key string) (reservation.Reservation, error) {
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	res, err := scanReservation(r.client.pool.QueryRow(ctx, `
		SELECT `+reservationColumns+`
		FROM reservations WHERE idempotency_key = $1`, key).Scan)
	if err != nil {
		if pgErrNoRows(err) {
			return reservation.Reservation{}, errs.Newf(errs.CodeNotFound,
				"no reservation for idempotency key %q", key)
		}
		return reservation.Reservation{}, operationError("postgres: get reservation by key", err)
	}
	return res, nil
}

// ReleaseExpiredHolds expires held reservations whose TTL passed and returns
// their seats.
func (r *ReservationRepository) ReleaseExpiredHolds(ctx context.Context, now time.Time) (int, error) {
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	tx, err := r.client.pool.Begin(ctx)
	if err != nil {
		return 0, operationError("postgres: begin release expired holds", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	rows, err := tx.Query(ctx, `
		UPDATE reservations
		SET status = 'expired', updated_at = now()
		WHERE status = 'held' AND hold_expires_at IS NOT NULL AND hold_expires_at <= $1
		RETURNING slot_id, party_size`, now)
	if err != nil {
		return 0, operationError("postgres: expire holds", err)
	}
	type release struct {
		slotID    string
		partySize int32
	}
	var releases []release
	for rows.Next() {
		var item release
		if err := rows.Scan(&item.slotID, &item.partySize); err != nil {
			rows.Close()
			return 0, operationError("postgres: scan expired hold", err)
		}
		releases = append(releases, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, operationError("postgres: iterate expired holds", err)
	}
	rows.Close()

	for _, item := range releases {
		if _, err := tx.Exec(ctx, `
			UPDATE reservation_slots
			SET booked = GREATEST(booked - $2, 0)
			WHERE slot_id = $1`, item.slotID, item.partySize); err != nil {
			return 0, operationError("postgres: release expired slot", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, operationError("postgres: commit release expired holds", err)
	}
	return len(releases), nil
}

// ListReservations returns the reservations against one restaurant's
// inventory, newest first. An empty date spans every date. The join through
// reservation_slots is what scopes a reservation by the calendar date of the
// seat it spent — the reservations table itself has no date column.
func (r *ReservationRepository) ListReservations(
	ctx context.Context, restaurantID int64, date string,
) ([]reservation.Reservation, error) {
	if restaurantID <= 0 {
		return nil, errs.New(errs.CodeInvalidArgument, "restaurant_id must be positive")
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	scope := `WHERE r.restaurant_id = $1`
	args := []any{restaurantID}
	if date != "" {
		scope += ` AND s.slot_date = $2::date`
		args = append(args, date)
	}

	rows, err := r.client.pool.Query(ctx, `
		SELECT `+prefixedReservationColumns+`
		FROM reservations r
		JOIN reservation_slots s ON s.slot_id = r.slot_id
		`+scope+`
		ORDER BY r.created_at DESC, r.reservation_id ASC`, args...)
	if err != nil {
		return nil, operationError("postgres: list reservations", err)
	}
	defer rows.Close()

	out := make([]reservation.Reservation, 0)
	for rows.Next() {
		res, err := scanReservation(rows.Scan)
		if err != nil {
			return nil, operationError("postgres: scan reservation", err)
		}
		out = append(out, res)
	}
	if err := rows.Err(); err != nil {
		return nil, operationError("postgres: iterate reservations", err)
	}
	return out, nil
}

// ResetInventory restores the mock inventory in one transaction: the matching
// slots return to pristine capacity and the reservations that spent those
// seats are deleted, which also frees their idempotency keys (the unique
// index lives on the row, so deleting the row deletes the key).
func (r *ReservationRepository) ResetInventory(
	ctx context.Context, restaurantID int64, date string,
) (slotsReset int, removed int, err error) {
	if restaurantID <= 0 {
		return 0, 0, errs.New(errs.CodeInvalidArgument, "restaurant_id must be positive")
	}
	ctx, cancel := r.client.withTimeout(ctx)
	defer cancel()

	tx, err := r.client.pool.Begin(ctx)
	if err != nil {
		return 0, 0, operationError("postgres: begin reset inventory", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	scope := `WHERE restaurant_id = $1`
	args := []any{restaurantID}
	if date != "" {
		scope += ` AND slot_date = $2::date`
		args = append(args, date)
	}

	onlyUnspent := ` AND booked > 0`
	tag, err := tx.Exec(ctx, `UPDATE reservation_slots SET booked = 0 `+scope+onlyUnspent, args...)
	if err != nil {
		return 0, 0, operationError("postgres: reset reservation slots", err)
	}
	slotsReset = int(tag.RowsAffected())

	delScope := `AND s.restaurant_id = $1`
	delArgs := []any{restaurantID}
	if date != "" {
		delScope += ` AND s.slot_date = $2::date`
		delArgs = append(delArgs, date)
	}
	tag, err = tx.Exec(ctx, `
		DELETE FROM reservations r
		USING reservation_slots s
		WHERE s.slot_id = r.slot_id `+delScope, delArgs...)
	if err != nil {
		return 0, 0, operationError("postgres: delete reset reservations", err)
	}
	removed = int(tag.RowsAffected())

	if err := tx.Commit(ctx); err != nil {
		return 0, 0, operationError("postgres: commit reset inventory", err)
	}
	return slotsReset, removed, nil
}

// scanSlot reads one reservation_slots row.
func scanSlot(scan func(dest ...any) error) (reservation.Slot, error) {
	var slot reservation.Slot
	if err := scan(&slot.SlotID, &slot.RestaurantID, &slot.SlotDate, &slot.SlotTime,
		&slot.Capacity, &slot.Booked, &slot.PolicyVersion, &slot.CreatedAt); err != nil {
		return reservation.Slot{}, err
	}
	return slot, nil
}

// scanReservation reads one reservations row.
func scanReservation(scan func(dest ...any) error) (reservation.Reservation, error) {
	var res reservation.Reservation
	if err := scan(&res.ReservationID, &res.ThreadID, &res.UserID, &res.RestaurantID,
		&res.SlotID, &res.PartySize, &res.Status, &res.HoldExpiresAt,
		&res.IdempotencyKey, &res.CreatedAt, &res.UpdatedAt); err != nil {
		return reservation.Reservation{}, err
	}
	return res, nil
}
