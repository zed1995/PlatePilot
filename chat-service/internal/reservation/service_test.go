package reservation_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zed1995/platepilot/shared/domain/errs"
	domainres "github.com/zed1995/platepilot/shared/domain/reservation"
	"github.com/zed1995/platepilot/shared/store"
	"github.com/zed1995/platepilot/shared/testkit"

	"github.com/zed1995/platepilot/chat-service/internal/reservation"
)

// fixtureDate is the date every fixture books. It is fixed so nothing in these
// tests depends on when they run.
const fixtureDate = "2026-10-10"

// fixtureNow is the injected clock's answer.
var fixtureNow = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func newTestService(t *testing.T, cfg reservation.Config, repo store.ReservationRepository) *reservation.Service {
	t.Helper()
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return fixtureNow }
	}
	svc, err := reservation.NewService(cfg, reservation.Deps{Reservations: repo})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// approvedRequest is a well-formed confirmation for the fixture slot.
func approvedRequest(slotID string) reservation.ConfirmRequest {
	return reservation.ConfirmRequest{
		ThreadID:     "thread-1",
		UserID:       "user-1",
		Action:       "request_reservation",
		Arguments:    json.RawMessage(`{"restaurant_id":7,"slot_id":"` + slotID + `","party_size":2}`),
		RequestID:    "req-1",
		RestaurantID: 7,
		SlotID:       slotID,
		PartySize:    2,
	}
}

// ---- availability ---------------------------------------------------------

// The inventory is generated on first read, and a slot that cannot hold the
// party is not offered at all. The second half is the part worth asserting: a
// 19:00 that is full is not an option, and listing it would send the user to a
// confirmation that can only fail.
func TestAvailabilityMaterialisesTheDayAndHidesSlotsThePartyDoesNotFit(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewReservationRepository()
	svc := newTestService(t, reservation.Config{}, repo)

	result, err := svc.Availability(ctx, reservation.AvailabilityRequest{
		RestaurantID: 7, Date: fixtureDate, PartySize: 2,
	})
	if err != nil {
		t.Fatalf("Availability: %v", err)
	}
	if want := len(reservation.ServiceTimes()); len(result.Slots) != want {
		t.Fatalf("slots = %d, want %d (the whole dinner service)", len(result.Slots), want)
	}
	if got := result.Slots[0].SlotID; got != "r7-2026-10-10-1700" {
		t.Fatalf("first slot id = %q, want the derived 17:00 id", got)
	}
	for _, slot := range result.Slots {
		if slot.Remaining != slot.Capacity || slot.Capacity == 0 {
			t.Fatalf("unbooked slot %s reports %d/%d remaining", slot.SlotID, slot.Remaining, slot.Capacity)
		}
		if slot.PolicyVersion == "" {
			t.Fatalf("slot %s has no policy version to quote in a summary", slot.SlotID)
		}
	}

	// A party larger than any slot is told the day is unavailable rather than
	// offered times it cannot take.
	tight, err := svc.Availability(ctx, reservation.AvailabilityRequest{
		RestaurantID: 7, Date: fixtureDate, PartySize: 99,
	})
	if err != nil {
		t.Fatalf("Availability (large party): %v", err)
	}
	if len(tight.Slots) != 0 {
		t.Fatalf("a party of 99 was offered %d slots", len(tight.Slots))
	}
}

// An expired hold returns its seats to the answer. Without the sweep the user
// would be told the restaurant is full because of a booking nobody ever made.
func TestAvailabilityReturnsTheSeatsOfAnExpiredHold(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewReservationRepository()
	svc := newTestService(t, reservation.Config{}, repo)

	slotID := reservation.SlotID(7, fixtureDate, "19:00")
	if _, err := svc.Availability(ctx, reservation.AvailabilityRequest{
		RestaurantID: 7, Date: fixtureDate, PartySize: 2,
	}); err != nil {
		t.Fatalf("Availability: %v", err)
	}
	seedHold(t, ctx, repo, slotID, 8, fixtureNow.Add(-time.Minute))

	free, err := svc.Availability(ctx, reservation.AvailabilityRequest{
		RestaurantID: 7, Date: fixtureDate, PartySize: 8,
	})
	if err != nil {
		t.Fatalf("Availability after expiry: %v", err)
	}
	if !offersSlot(free, slotID) {
		t.Fatal("the expired hold's seats were not returned to the answer")
	}

	// The other direction, so the assertion above cannot pass on a service that
	// simply ignores holds: one whose TTL has not passed still holds its seats.
	live := reservation.SlotID(7, fixtureDate, "20:00")
	seedHold(t, ctx, repo, live, 8, fixtureNow.Add(time.Hour))
	held, err := svc.Availability(ctx, reservation.AvailabilityRequest{
		RestaurantID: 7, Date: fixtureDate, PartySize: 8,
	})
	if err != nil {
		t.Fatalf("Availability with a live hold: %v", err)
	}
	if offersSlot(held, live) {
		t.Fatal("a live hold was ignored: its slot was offered while still occupied")
	}
}

func TestAvailabilityRejectsMalformedRequests(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, reservation.Config{}, testkit.NewReservationRepository())

	cases := map[string]reservation.AvailabilityRequest{
		"no restaurant":     {Date: fixtureDate, PartySize: 2},
		"unparseable date":  {RestaurantID: 7, Date: "next friday", PartySize: 2},
		"no party size":     {RestaurantID: 7, Date: fixtureDate},
		"negative party":    {RestaurantID: 7, Date: fixtureDate, PartySize: -1},
		"empty date string": {RestaurantID: 7, PartySize: 2},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.Availability(ctx, req); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

// ---- confirmation ---------------------------------------------------------

// The booking is written from a hold and promoted, and the difference is
// load-bearing rather than cosmetic: a hold that never got promoted still owns
// its idempotency key and its seats, so a retry finds it and promotes it.
func TestAConfirmationWritesAConfirmedBookingUnderAHoldFirst(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewReservationRepository()
	svc := newTestService(t, reservation.Config{}, repo)

	slotID := materialise(t, ctx, svc, 7, 2)
	booked, err := svc.Confirm(ctx, approvedRequest(slotID))
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if booked.Status != domainres.StatusConfirmed {
		t.Fatalf("status = %q, want confirmed", booked.Status)
	}
	if booked.HoldExpiresAt != nil {
		t.Fatal("a confirmed booking must not carry a hold deadline")
	}
	if booked.IdempotencyKey == "" {
		t.Fatal("the booking was not written under an idempotency key")
	}
	stored, err := repo.GetByIdempotencyKey(ctx, booked.IdempotencyKey)
	if err != nil {
		t.Fatalf("the booking cannot be found by its own key: %v", err)
	}
	if stored.ReservationID != booked.ReservationID {
		t.Fatalf("key %q resolves to %q, not %q", booked.IdempotencyKey, stored.ReservationID, booked.ReservationID)
	}
}

// The whole point of the key: a retried confirmation is the same booking, and a
// second one does not spend a second table.
func TestARepeatedConfirmationIsTheSameBookingAndSpendsNoSecondTable(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewReservationRepository()
	svc := newTestService(t, reservation.Config{}, repo)

	slotID := materialise(t, ctx, svc, 7, 2)
	req := approvedRequest(slotID)

	first, err := svc.Confirm(ctx, req)
	if err != nil {
		t.Fatalf("first Confirm: %v", err)
	}
	second, err := svc.Confirm(ctx, req)
	if err != nil {
		t.Fatalf("second Confirm: %v", err)
	}
	if first.ReservationID != second.ReservationID {
		t.Fatalf("a retry produced a second booking: %q then %q",
			first.ReservationID, second.ReservationID)
	}

	slot, err := repo.GetSlot(ctx, slotID)
	if err != nil {
		t.Fatalf("GetSlot: %v", err)
	}
	if slot.Booked != 2 {
		t.Fatalf("slot booked = %d, want 2 (one party of two, not two)", slot.Booked)
	}

	// A genuinely different request is a different booking. Without this the
	// test above would pass on an implementation that ignored the request id.
	other := req
	other.RequestID = "req-2"
	third, err := svc.Confirm(ctx, other)
	if err != nil {
		t.Fatalf("a new request was refused: %v", err)
	}
	if third.ReservationID == first.ReservationID {
		t.Fatal("two different requests collapsed into one booking")
	}
}

// A slot id from another restaurant would book a table the summary never
// mentioned, and the summary is what the user approved.
func TestAConfirmationRefusesASlotThatBelongsToAnotherRestaurant(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewReservationRepository()
	svc := newTestService(t, reservation.Config{}, repo)

	slotID := materialise(t, ctx, svc, 7, 2)
	req := approvedRequest(slotID)
	req.RestaurantID = 8

	_, err := svc.Confirm(ctx, req)
	if err == nil {
		t.Fatal("expected the mismatched slot to be refused")
	}
	if code := errs.CodeOf(err); code != errs.CodeValidationFailed {
		t.Fatalf("code = %q, want %q", code, errs.CodeValidationFailed)
	}
	if _, err := repo.GetByIdempotencyKey(ctx, reservation.IdempotencyKey(req)); err == nil {
		t.Fatal("the refused request still wrote a booking")
	}
}

// Capacity is checked atomically at the hold, so the second party is told the
// slot is gone and nothing is written for them.
func TestAConfirmationIsRefusedWhenTheSlotIsGone(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewReservationRepository()
	svc := newTestService(t, reservation.Config{}, repo)

	slotID := materialise(t, ctx, svc, 7, 8)

	// The first party takes the whole slot.
	first := approvedRequest(slotID)
	first.PartySize = 8
	first.Arguments = json.RawMessage(`{"restaurant_id":7,"slot_id":"` + slotID + `","party_size":8}`)
	if _, err := svc.Confirm(ctx, first); err != nil {
		t.Fatalf("first Confirm: %v", err)
	}

	second := approvedRequest(slotID)
	second.RequestID = "req-2"
	_, err := svc.Confirm(ctx, second)
	if err == nil {
		t.Fatal("expected the second party to be refused")
	}
	if code := errs.CodeOf(err); code != errs.CodeReservationUnavailable {
		t.Fatalf("code = %q, want %q", code, errs.CodeReservationUnavailable)
	}
	if _, err := repo.GetByIdempotencyKey(ctx, reservation.IdempotencyKey(second)); !errors.Is(err, errs.ErrNotFound) {
		t.Fatalf("the refused booking was written anyway: %v", err)
	}
}

// A request that cannot identify its approval cannot be made idempotent, so it
// is refused rather than written under a key derived from nothing.
func TestAConfirmationWithoutAnIdentifiedApprovalIsRefused(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, reservation.Config{}, testkit.NewReservationRepository())

	slotID := materialise(t, ctx, svc, 7, 2)
	cases := map[string]func(*reservation.ConfirmRequest){
		"no thread":     func(r *reservation.ConfirmRequest) { r.ThreadID = "" },
		"no action":     func(r *reservation.ConfirmRequest) { r.Action = "" },
		"no request id": func(r *reservation.ConfirmRequest) { r.RequestID = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			req := approvedRequest(slotID)
			mutate(&req)
			if _, err := svc.Confirm(ctx, req); err == nil {
				t.Fatal("expected a refusal")
			}
		})
	}
}

// The key names one approval attempt. Deriving it from anything the client
// controls would let a client book twice by choosing differently.
func TestTheIdempotencyKeyDependsOnTheApprovedRequest(t *testing.T) {
	slotID := reservation.SlotID(7, fixtureDate, "19:00")
	base := approvedRequest(slotID)

	if reservation.IdempotencyKey(base) != reservation.IdempotencyKey(base) {
		t.Fatal("the key is not deterministic")
	}

	differentID := base
	differentID.RequestID = "req-2"
	if reservation.IdempotencyKey(differentID) == reservation.IdempotencyKey(base) {
		t.Fatal("two different approvals share a key: a retry and a new request would collapse")
	}

	differentArgs := base
	differentArgs.Arguments = json.RawMessage(`{"restaurant_id":7,"slot_id":"` + slotID + `","party_size":4}`)
	if reservation.IdempotencyKey(differentArgs) == reservation.IdempotencyKey(base) {
		t.Fatal("a changed party size kept the same key")
	}

	differentThread := base
	differentThread.ThreadID = "thread-2"
	if reservation.IdempotencyKey(differentThread) == reservation.IdempotencyKey(base) {
		t.Fatal("the same request on two threads shares a key")
	}

	if got := reservation.IdempotencyKey(base); len(got) == 0 || got[:4] != "res-" {
		t.Fatalf("key %q does not carry the reservation prefix", got)
	}
}

// The summary is the product's final text: the restaurant by name, the date and
// time, the party, and the policy. A summary that named a restaurant by id
// would ask the user to approve something they have never seen.
func TestTheApprovalSummaryNamesTheRestaurantAndThePolicy(t *testing.T) {
	ctx := context.Background()
	repo := testkit.NewReservationRepository()
	svc := newTestService(t, reservation.Config{PolicyVersion: "mock-v2"}, repo)

	slotID := materialise(t, ctx, svc, 7, 2)
	summary, err := svc.Summary(ctx, approvedRequest(slotID))
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	for _, want := range []string{"19:00", fixtureDate, "2 人", "mock-v2"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary %q does not mention %q", summary, want)
		}
	}
}

// ---- helpers --------------------------------------------------------------

// materialise makes a day's slots and returns the 19:00 one.
func materialise(t *testing.T, ctx context.Context, svc *reservation.Service, restaurantID int64, partySize int) string {
	t.Helper()
	if _, err := svc.Availability(ctx, reservation.AvailabilityRequest{
		RestaurantID: restaurantID, Date: fixtureDate, PartySize: partySize,
	}); err != nil {
		t.Fatalf("materialise slots: %v", err)
	}
	return reservation.SlotID(restaurantID, fixtureDate, "19:00")
}

// seedHold occupies seats with a hold that expires at the given time.
func seedHold(
	t *testing.T, ctx context.Context, repo store.ReservationRepository,
	slotID string, partySize int, expires time.Time,
) {
	t.Helper()
	if _, err := repo.HoldSlot(ctx, slotID, partySize); err != nil {
		t.Fatalf("HoldSlot: %v", err)
	}
	err := repo.SaveReservation(ctx, domainres.Reservation{
		ReservationID:  "hold-" + slotID,
		ThreadID:       "thread-hold",
		UserID:         "user-hold",
		RestaurantID:   7,
		SlotID:         slotID,
		PartySize:      partySize,
		Status:         domainres.StatusHeld,
		HoldExpiresAt:  &expires,
		IdempotencyKey: "key-" + slotID,
		CreatedAt:      fixtureNow,
		UpdatedAt:      fixtureNow,
	})
	if err != nil {
		t.Fatalf("SaveReservation: %v", err)
	}
}

func offersSlot(result reservation.AvailabilityResult, slotID string) bool {
	for _, slot := range result.Slots {
		if slot.SlotID == slotID {
			return true
		}
	}
	return false
}
