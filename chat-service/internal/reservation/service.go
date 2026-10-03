// Package reservation implements the mock reservation flow's server-side rules.
//
// It sits between the confirmation gate and the store: the gate decides that a
// write may not happen yet, the confirm endpoint decides that the user approved
// it, and this package decides what approving it actually means. Capacity is
// checked atomically, a hold expires, and a repeated confirmation resolves to
// the booking it already made rather than to a second one.
//
// The capability is a Mock and the package says so by being small: dinner
// service slots generated from a fixed template, no cancellation, no
// rescheduling, no party-size policy beyond "there must be room". Modelling more
// would produce a booking system nobody asked for while leaving the property
// that matters — the model cannot write, only the user can — exactly as it is.
package reservation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/reservation"
	"github.com/zed1995/platepilot/shared/domain/search"
	"github.com/zed1995/platepilot/shared/idgen"
	"github.com/zed1995/platepilot/shared/store"
)

// Slot inventory. A day's slots are generated from this template the first time
// the day is read, which is what lets the mock serve any date without an import
// step and without a table anyone has to keep in sync with reality.
var (
	// serviceTimes is a fixed dinner service, every half hour.
	serviceTimes = []string{
		"17:00", "17:30", "18:00", "18:30", "19:00", "19:30",
		"20:00", "20:30", "21:00", "21:30",
	}
	// defaultCapacity is seats per slot, before any booking.
	defaultCapacity = 8
	// dateLayout is the ISO calendar date every slot key uses.
	dateLayout = "2006-01-02"
)

// Defaults for the service configuration.
const (
	// DefaultHoldTTL bounds a hold. Ten minutes is long enough for a user to
	// read the summary and answer, and short enough that a hold abandoned by a
	// crash does not sit on the seats for the evening.
	DefaultHoldTTL = 10 * time.Minute
	// DefaultPolicyVersion identifies the policy a booking was made under, so a
	// summary can say which rules the user agreed to.
	DefaultPolicyVersion = "mock-v1"
)

// Config holds the service's knobs.
type Config struct {
	// HoldTTL bounds how long a hold exists before its seats are returned.
	HoldTTL time.Duration
	// PolicyVersion is recorded on every slot and quoted in the approval
	// summary.
	PolicyVersion string
	// Clock is injectable so a test can decide what "now" is. Nil means the
	// wall clock.
	Clock func() time.Time
}

// RestaurantNames is the one restaurant fact a confirmation summary needs.
//
// It is a narrow interface rather than the whole restaurant store because a
// summary has exactly one question — what is this restaurant called — and a
// package that could reach the rest of the store would eventually do so.
type RestaurantNames interface {
	GetByID(ctx context.Context, restaurantID int64) (search.RestaurantDetail, error)
}

// Deps are the ports the service runs on.
type Deps struct {
	Reservations store.ReservationRepository
	Restaurants  RestaurantNames
}

// Service carries the reservation rules.
type Service struct {
	cfg   Config
	deps  Deps
	newID func() string
}

// NewService builds the service.
func NewService(cfg Config, deps Deps) (*Service, error) {
	if deps.Reservations == nil {
		return nil, errs.New(errs.CodeInvalidArgument, "reservation service requires a reservation store")
	}
	if cfg.HoldTTL <= 0 {
		cfg.HoldTTL = DefaultHoldTTL
	}
	if strings.TrimSpace(cfg.PolicyVersion) == "" {
		cfg.PolicyVersion = DefaultPolicyVersion
	}
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	return &Service{cfg: cfg, deps: deps, newID: idgen.NewUUID}, nil
}

// AvailabilityRequest asks what a party can book.
type AvailabilityRequest struct {
	RestaurantID int64
	// Date is an ISO calendar date, "2006-01-02".
	Date      string
	PartySize int
}

// AvailableSlot is one bookable time, described by what a user chooses between.
type AvailableSlot struct {
	SlotID        string `json:"slot_id"`
	SlotTime      string `json:"slot_time"`
	Remaining     int    `json:"remaining"`
	Capacity      int    `json:"capacity"`
	PolicyVersion string `json:"policy_version"`
}

// AvailabilityResult is one restaurant's bookable times on one date.
type AvailabilityResult struct {
	RestaurantID int64           `json:"restaurant_id"`
	Date         string          `json:"date"`
	PartySize    int             `json:"party_size"`
	Slots        []AvailableSlot `json:"slots"`
}

// Availability lists the times a party can actually book.
//
// It sweeps expired holds first. Without that, a hold whose owner never came
// back would keep its seats out of the answer until something else happened to
// release them, and the user would be told the restaurant is full when it is
// not — a wrong answer with no way for them to tell.
func (s *Service) Availability(
	ctx context.Context, req AvailabilityRequest,
) (AvailabilityResult, error) {
	if req.RestaurantID <= 0 {
		return AvailabilityResult{}, errs.New(errs.CodeInvalidArgument,
			"get_availability requires a restaurant id")
	}
	if _, err := time.Parse(dateLayout, req.Date); err != nil {
		return AvailabilityResult{}, errs.Newf(errs.CodeValidationFailed,
			"date must be an ISO calendar date (YYYY-MM-DD), got %q", req.Date)
	}
	if req.PartySize <= 0 {
		return AvailabilityResult{}, errs.New(errs.CodeInvalidArgument,
			"party_size must be greater than zero")
	}

	now := s.now()
	if _, err := s.deps.Reservations.ReleaseExpiredHolds(ctx, now); err != nil {
		return AvailabilityResult{}, err
	}
	if err := s.ensureSlots(ctx, req.RestaurantID, req.Date, now); err != nil {
		return AvailabilityResult{}, err
	}
	slots, err := s.deps.Reservations.ListSlots(ctx, req.RestaurantID, req.Date)
	if err != nil {
		return AvailabilityResult{}, err
	}

	// Only slots that can hold this party are listed. The filter is what makes
	// the result an answer to "when can we eat" rather than "what exists": a
	// 19:00 that is full is not an option, and offering it would send the user
	// to a confirmation that can only fail.
	available := make([]AvailableSlot, 0, len(slots))
	for _, slot := range slots {
		if !slot.CanHold(req.PartySize) {
			continue
		}
		available = append(available, AvailableSlot{
			SlotID:        slot.SlotID,
			SlotTime:      slot.SlotTime,
			Remaining:     slot.Remaining(),
			Capacity:      slot.Capacity,
			PolicyVersion: slot.PolicyVersion,
		})
	}
	return AvailabilityResult{
		RestaurantID: req.RestaurantID,
		Date:         req.Date,
		PartySize:    req.PartySize,
		Slots:        available,
	}, nil
}

// ConfirmRequest is one approved booking request.
type ConfirmRequest struct {
	// ThreadID, Action and RequestID identify the approval. They are the
	// server-side ingredients of the idempotency key.
	ThreadID string
	UserID   string
	Action   string
	// Arguments is the approved argument JSON, verbatim.
	Arguments json.RawMessage
	// RequestID names the parked request the user approved.
	RequestID string

	RestaurantID int64
	SlotID       string
	PartySize    int
}

// Confirm executes an approved booking request.
//
// The booking is written in two steps on purpose. The hold lands first, with
// its idempotency key, and is then promoted to confirmed. That ordering is what
// makes the operation survive being interrupted: a hold that never got promoted
// still owns its key and still holds its seats, so a retry finds it by key and
// promotes it, and a crash that is never retried is cleaned up by the TTL
// sweep. Writing the confirmed row directly would leave nothing for a retry to
// recognise and a lost response would mean a lost booking.
func (s *Service) Confirm(ctx context.Context, req ConfirmRequest) (reservation.Reservation, error) {
	if req.ThreadID == "" || req.Action == "" || req.RequestID == "" {
		return reservation.Reservation{}, errs.New(errs.CodeInternal,
			"a confirmed reservation needs an identified approval")
	}
	if req.RestaurantID <= 0 || req.SlotID == "" || req.PartySize <= 0 {
		return reservation.Reservation{}, errs.New(errs.CodeValidationFailed,
			"request_reservation requires restaurant_id, slot_id and party_size")
	}
	key := IdempotencyKey(req)

	// A retry of a confirmation whose response was lost lands here and gets the
	// booking it already made. This is the only thing standing between a flaky
	// network and a party holding two tables.
	if existing, err := s.deps.Reservations.GetByIdempotencyKey(ctx, key); err == nil {
		return existing, nil
	} else if !errors.Is(err, errs.ErrNotFound) {
		return reservation.Reservation{}, err
	}

	now := s.now()
	if _, err := s.deps.Reservations.ReleaseExpiredHolds(ctx, now); err != nil {
		return reservation.Reservation{}, err
	}
	// The slot has to belong to the restaurant the user was shown. A slot id
	// from another restaurant would otherwise book a table the summary never
	// mentioned — and the summary is what the user approved.
	slot, err := s.deps.Reservations.GetSlot(ctx, req.SlotID)
	if err != nil {
		return reservation.Reservation{}, err
	}
	if slot.RestaurantID != req.RestaurantID {
		return reservation.Reservation{}, errs.Newf(errs.CodeValidationFailed,
			"slot %q belongs to restaurant %d, not %d",
			req.SlotID, slot.RestaurantID, req.RestaurantID)
	}

	if _, err := s.deps.Reservations.HoldSlot(ctx, req.SlotID, req.PartySize); err != nil {
		// Capacity is gone. Nothing was written, so the thread can offer the
		// user the remaining times instead.
		return reservation.Reservation{}, err
	}

	expires := now.Add(s.cfg.HoldTTL)
	held := reservation.Reservation{
		ReservationID:  s.newID(),
		ThreadID:       req.ThreadID,
		UserID:         req.UserID,
		RestaurantID:   req.RestaurantID,
		SlotID:         req.SlotID,
		PartySize:      req.PartySize,
		Status:         reservation.StatusHeld,
		HoldExpiresAt:  &expires,
		IdempotencyKey: key,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.deps.Reservations.SaveReservation(ctx, held); err != nil {
		// Another confirmation claimed this key between the lookup and the
		// write. Its row is the booking — give the seats this attempt took back
		// and answer with it.
		_ = s.deps.Reservations.ReleaseSlot(ctx, req.SlotID, req.PartySize)
		if existing, lookupErr := s.deps.Reservations.GetByIdempotencyKey(ctx, key); lookupErr == nil {
			return existing, nil
		}
		return reservation.Reservation{}, err
	}

	confirmed := held
	confirmed.Status = reservation.StatusConfirmed
	confirmed.HoldExpiresAt = nil
	confirmed.UpdatedAt = now
	if err := s.deps.Reservations.SaveReservation(ctx, confirmed); err != nil {
		return reservation.Reservation{}, err
	}
	return confirmed, nil
}

// Summary renders the sentence a user is asked to approve.
//
// It is the product's final summary (PRD §3.4): the restaurant by name, the date
// and time, the party size, and the policy version the booking is made under.
// The restaurant name is looked up rather than taken from the arguments because
// a summary that identified a restaurant by its database id would ask the user
// to approve something they have never seen.
func (s *Service) Summary(
	ctx context.Context, req ConfirmRequest,
) (string, error) {
	slot, err := s.deps.Reservations.GetSlot(ctx, req.SlotID)
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("restaurant_id=%d", req.RestaurantID)
	if s.deps.Restaurants != nil {
		restaurant, err := s.deps.Restaurants.GetByID(ctx, req.RestaurantID)
		switch {
		case err == nil:
			name = restaurant.Name
			if restaurant.Borough != "" {
				name += "（" + restaurant.Borough + "）"
			}
		case errors.Is(err, errs.ErrNotFound):
			// The restaurant is gone but the user is still holding a summary
			// for it; naming it by id is worse than saying so.
			name = fmt.Sprintf("餐厅 %d（已无法读取名称）", req.RestaurantID)
		default:
			return "", err
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "确认预约：%s\n", name)
	fmt.Fprintf(&b, "时间：%s %s\n", slot.SlotDate, slot.SlotTime)
	fmt.Fprintf(&b, "人数：%d 人\n", req.PartySize)
	fmt.Fprintf(&b, "政策版本：%s\n", slot.PolicyVersion)
	b.WriteString("确认后立即占座；如不需要请选择取消。")
	return b.String(), nil
}

// IdempotencyKey derives the key a confirmation is remembered by.
//
// It is derived, never supplied: a client that could choose the key could book
// twice by choosing differently, and the model certainly cannot — it does not
// know the request id. The request id is the ingredient that makes the key name
// one approval attempt, which is what a retry reproduces and a second, genuinely
// new request does not.
func IdempotencyKey(req ConfirmRequest) string {
	digest := sha256.Sum256(req.Arguments)
	raw := strings.Join([]string{
		req.ThreadID, req.Action, req.RequestID, hex.EncodeToString(digest[:]),
	}, "\n")
	sum := sha256.Sum256([]byte(raw))
	return "res-" + hex.EncodeToString(sum[:16])
}

// ensureSlots materialises a day's inventory for one restaurant.
//
// The generated ids are derived from restaurant, date and time, so re-seeding is
// a no-op rather than a duplicate: the store creates what is missing and leaves
// existing rows alone, which is what stops a second read of the same day from
// resetting the capacity a live hold is holding.
func (s *Service) ensureSlots(ctx context.Context, restaurantID int64, date string, now time.Time) error {
	slots := make([]reservation.Slot, 0, len(serviceTimes))
	for _, at := range serviceTimes {
		slots = append(slots, reservation.Slot{
			SlotID:        SlotID(restaurantID, date, at),
			RestaurantID:  restaurantID,
			SlotDate:      date,
			SlotTime:      at,
			Capacity:      defaultCapacity,
			PolicyVersion: s.cfg.PolicyVersion,
			CreatedAt:     now,
		})
	}
	return s.deps.Reservations.EnsureSlots(ctx, slots)
}

// SlotID is the deterministic identity of one bookable time.
//
// It is derived from what the slot is — restaurant, date, time — rather than
// generated, because the id is what the model quotes back and what a retry
// re-quotes. A random id would make a retry after a restart name a slot that no
// longer exists.
func SlotID(restaurantID int64, date, at string) string {
	return fmt.Sprintf("r%d-%s-%s", restaurantID, date, strings.ReplaceAll(at, ":", ""))
}

// ServiceTimes returns the template's times, for a caller that wants to describe
// the service without asking the store.
func ServiceTimes() []string {
	out := make([]string, len(serviceTimes))
	copy(out, serviceTimes)
	return out
}

func (s *Service) now() time.Time { return s.cfg.Clock().UTC() }
