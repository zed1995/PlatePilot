package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	domainchat "github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/conversation"
	"github.com/zed1995/platepilot/shared/domain/errs"
	domainres "github.com/zed1995/platepilot/shared/domain/reservation"
	"github.com/zed1995/platepilot/shared/domain/search"
	domaintool "github.com/zed1995/platepilot/shared/domain/tool"
	"github.com/zed1995/platepilot/shared/store"
	"github.com/zed1995/platepilot/shared/testkit"

	"github.com/zed1995/platepilot/chat-service/internal/agent"
	"github.com/zed1995/platepilot/chat-service/internal/agent/slots"
	"github.com/zed1995/platepilot/chat-service/internal/agent/toolreg"
	"github.com/zed1995/platepilot/chat-service/internal/agent/tools"
	"github.com/zed1995/platepilot/chat-service/internal/hitl"
	"github.com/zed1995/platepilot/chat-service/internal/reservation"
)

const (
	reservationDate = "2026-10-10"
	reservationSlot = "r7-2026-10-10-1900"
)

var reservationNow = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

// countingReservations is the in-memory reservation store behind a write
// counter. The counter is the assertion the whole gate exists to make possible:
// "the model asked to book and nothing was booked" is only checkable if the
// store can say it was never asked to write.
type countingReservations struct {
	*testkit.ReservationRepository

	mu     sync.Mutex
	holds  int
	saves  int
	writes []string
}

func newCountingReservations() *countingReservations {
	return &countingReservations{ReservationRepository: testkit.NewReservationRepository()}
}

func (c *countingReservations) HoldSlot(ctx context.Context, slotID string, partySize int) (domainres.Slot, error) {
	c.mu.Lock()
	c.holds++
	c.writes = append(c.writes, "hold:"+slotID)
	c.mu.Unlock()
	return c.ReservationRepository.HoldSlot(ctx, slotID, partySize)
}

func (c *countingReservations) SaveReservation(ctx context.Context, res domainres.Reservation) error {
	c.mu.Lock()
	c.saves++
	c.writes = append(c.writes, "save:"+res.ReservationID+":"+string(res.Status))
	c.mu.Unlock()
	return c.ReservationRepository.SaveReservation(ctx, res)
}

// bookingWrites is every write that would leave a booking behind. Slot
// materialisation is deliberately not included: seeding the mock inventory is a
// read path that happens to insert rows, and counting it would make "no writes"
// unassertable rather than stricter.
func (c *countingReservations) bookingWrites() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.writes))
	copy(out, c.writes)
	return out
}

var _ store.ReservationRepository = (*countingReservations)(nil)

// reservationRegistry wires the two reservation tools over a counting store.
func reservationRegistry(t *testing.T, repo *countingReservations) *toolreg.Registry {
	t.Helper()
	svc := reservationService(t, repo)
	registry := toolreg.New(0)
	register(t, registry, tools.GetAvailabilityEntry(svc))
	register(t, registry, tools.RequestReservationEntry(svc))
	return registry
}

func reservationService(t *testing.T, repo store.ReservationRepository) *reservation.Service {
	t.Helper()
	svc, err := reservation.NewService(reservation.Config{
		Clock: func() time.Time { return reservationNow },
	}, reservation.Deps{
		Reservations: repo,
		// A name source is what makes the summary readable. Without one the
		// summary falls back to the database id, and the user would be asked to
		// approve a booking at "restaurant_id=7".
		Restaurants: fixedRestaurantNames{},
	})
	if err != nil {
		t.Fatalf("reservation.NewService: %v", err)
	}
	return svc
}

// fixedRestaurantNames answers the one question a summary asks.
type fixedRestaurantNames struct{}

func (fixedRestaurantNames) GetByID(_ context.Context, restaurantID int64) (search.RestaurantDetail, error) {
	if restaurantID != 7 {
		return search.RestaurantDetail{}, errs.Newf(errs.CodeNotFound,
			"restaurant %d not found", restaurantID)
	}
	return search.RestaurantDetail{RestaurantID: 7, Name: "Joe's Pizza", Borough: "manhattan"}, nil
}

// ---- the gate -------------------------------------------------------------

// TestAWriteToolIsParkedWithoutWriting is the milestone's central claim, and it
// asserts all three halves of it at once: the tool did not run, the thread says
// what it is waiting for, and the client was told what to ask.
//
// Asserting only the event would pass on an implementation that parked the call
// *and* executed it; asserting only the store would pass on one that wrote
// nothing and told nobody. The three are one property.
func TestAWriteToolIsParkedWithoutWriting(t *testing.T) {
	ctx := context.Background()
	repo := newCountingReservations()
	conversations := testkit.NewConversationRepository()
	registry := reservationRegistry(t, repo)

	// The model reads the inventory, then asks for the table. That order is what
	// the tool descriptions tell it to follow, and it is also what makes the
	// approval summary possible: the summary names the time, and the time comes
	// from the slot the availability call materialised.
	provider := &scriptedProvider{
		supportTools: true,
		toolResps: []domainchat.ToolCallResponse{
			toolCallResponse("c1", tools.GetAvailabilityToolName,
				`{"restaurant_id":7,"date":"`+reservationDate+`","party_size":2}`),
			toolCallResponse("c2", tools.RequestReservationToolName,
				`{"restaurant_id":7,"slot_id":"`+reservationSlot+`","party_size":2}`),
		},
	}
	runner := conversationRunner(t, provider, registry, conversations)

	result, stream := runner.Run(ctx, agent.TurnInput{
		ThreadID:  "thread-1",
		UserID:    "user-1",
		UserInput: "帮我订 10 月 10 号晚上 7 点两个人的位子",
	})
	events := drain(stream)

	if writes := repo.bookingWrites(); len(writes) != 0 {
		t.Fatalf("a parked write reached the store: %v", writes)
	}

	// No tool.start/tool.finish pair for the parked call. The tool did not run,
	// and an audit trail that reported it did would describe work nobody did.
	for _, ev := range events {
		if ev.Tool == tools.RequestReservationToolName &&
			(ev.Type == agent.EventToolStart || ev.Type == agent.EventToolFinish) {
			t.Fatalf("the parked call reported %q", ev.Type)
		}
	}

	asks := confirmationAsks(events)
	if len(asks) != 1 {
		t.Fatalf("confirmation.required events = %d, want exactly 1", len(asks))
	}
	ask := asks[0]
	if ask.State != string(conversation.StateAwaitingConfirmation) {
		t.Fatalf("event state = %q, want %q", ask.State, conversation.StateAwaitingConfirmation)
	}
	if ask.PendingAction != tools.RequestReservationToolName {
		t.Fatalf("event pending action = %q", ask.PendingAction)
	}
	if ask.ConfirmationSummary == "" {
		t.Fatal("the ask carries no summary: the user would be asked to approve nothing")
	}
	for _, want := range []string{"Joe's Pizza", "19:00", "2 人"} {
		if !strings.Contains(ask.ConfirmationSummary, want) {
			t.Fatalf("summary %q does not mention %q", ask.ConfirmationSummary, want)
		}
	}

	// The answer text is the same sentence, so a client that renders only the
	// stream and one that reads the thread show the user the same request.
	if result.Answer == nil || !strings.Contains(result.Answer.Text, ask.ConfirmationSummary) {
		t.Fatalf("the answer does not quote the summary: %+v", result.Answer)
	}

	checkpoint, err := conversations.LoadCheckpoint(ctx, "thread-1")
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if checkpoint.State != conversation.StateAwaitingConfirmation {
		t.Fatalf("checkpoint state = %q, want %q", checkpoint.State, conversation.StateAwaitingConfirmation)
	}
	if checkpoint.PendingAction != tools.RequestReservationToolName {
		t.Fatalf("checkpoint pending action = %q", checkpoint.PendingAction)
	}
	if checkpoint.PendingToolCallID == "" {
		t.Fatal("the parked call has no server-minted request id: a retry could not be recognised")
	}
	if checkpoint.PendingToolCallID == "c2" {
		t.Fatal("the parked call reused the model's call id as its request id")
	}
	var approved map[string]any
	if err := json.Unmarshal(checkpoint.PendingArguments, &approved); err != nil {
		t.Fatalf("the approved arguments were not stored verbatim: %v", err)
	}
	if approved["slot_id"] != reservationSlot {
		t.Fatalf("stored arguments = %v, want the model's own slot", approved)
	}
}

// The second half of the same guarantee: even reached directly, the writing tool
// refuses to act without an approval on its context. The model cannot talk its
// way past this, because there is nothing it could pass that would satisfy it.
func TestTheWriteToolRefusesToRunWithoutAnApproval(t *testing.T) {
	ctx := context.Background()
	repo := newCountingReservations()
	registry := reservationRegistry(t, repo)

	// Materialise the slots so the refusal is about the missing approval and not
	// about a slot that does not exist.
	svc := reservationService(t, repo)
	if _, err := svc.Availability(ctx, reservation.AvailabilityRequest{
		RestaurantID: 7, Date: reservationDate, PartySize: 2,
	}); err != nil {
		t.Fatalf("Availability: %v", err)
	}
	before := len(repo.bookingWrites())

	result := registry.Invoke(ctx, domaintool.ToolCall{
		ID:   "c1",
		Name: tools.RequestReservationToolName,
		Arguments: json.RawMessage(
			`{"restaurant_id":7,"slot_id":"` + reservationSlot + `","party_size":2}`),
	})

	if result.Status != domaintool.ToolStatusError {
		t.Fatalf("status = %q, want an error", result.Status)
	}
	if result.Error == nil || errs.CodeOf(result.Error) != errs.CodeAgentNoPendingAction {
		t.Fatalf("error = %v, want %q", result.Error, errs.CodeAgentNoPendingAction)
	}
	if after := len(repo.bookingWrites()); after != before {
		t.Fatalf("the refused call still wrote: %v", repo.bookingWrites()[before:])
	}
}

// A parked call that cannot be described is not parked at all. Asking a user to
// approve "some call with these arguments" teaches them to approve without
// reading, which is the failure the gate exists to prevent.
func TestAWriteWithNoDescribableSummaryIsNotParked(t *testing.T) {
	ctx := context.Background()
	repo := newCountingReservations()
	conversations := testkit.NewConversationRepository()
	registry := reservationRegistry(t, repo)

	// The slot id names no slot this restaurant ever materialised, so the
	// summary cannot be rendered.
	provider := &scriptedProvider{
		supportTools: true,
		toolResps: []domainchat.ToolCallResponse{
			toolCallResponse("c1", tools.RequestReservationToolName,
				`{"restaurant_id":7,"slot_id":"r7-2026-10-10-0300","party_size":2}`),
			assistantText("抱歉，那个时间订不了，请换一个时段。"),
		},
	}
	runner := conversationRunner(t, provider, registry, conversations)

	_, stream := runner.Run(ctx, agent.TurnInput{
		ThreadID:  "thread-1",
		UserID:    "user-1",
		UserInput: "订凌晨三点的位子",
	})
	events := drain(stream)

	if asks := confirmationAsks(events); len(asks) != 0 {
		t.Fatalf("an undescribable call was parked anyway: %+v", asks)
	}
	if writes := repo.bookingWrites(); len(writes) != 0 {
		t.Fatalf("an undescribable call reached the store: %v", writes)
	}
	checkpoint, err := conversations.LoadCheckpoint(ctx, "thread-1")
	if err == nil && checkpoint.PendingAction != "" {
		t.Fatalf("checkpoint holds a pending action %q", checkpoint.PendingAction)
	}
}

// A read-only availability lookup is not gated. Parking it would turn a lookup
// into a question the user has to answer, and the registry refuses that
// declaration outright.
func TestTheAvailabilityLookupIsNotGated(t *testing.T) {
	registry := reservationRegistry(t, newCountingReservations())
	if registry.RequiresConfirmation(tools.GetAvailabilityToolName) {
		t.Fatal("get_availability is parked for approval")
	}
	if !registry.RequiresConfirmation(tools.RequestReservationToolName) {
		t.Fatal("request_reservation is not gated")
	}
}

// ---- the flow after a confirmation ---------------------------------------

// The confirmation endpoint's effect, exercised through the real tool: the
// approved call writes exactly one booking, and a replay of the same request
// answers with it rather than making a second.
func TestAnApprovedCallWritesOneBooking(t *testing.T) {
	ctx := context.Background()
	repo := newCountingReservations()
	registry := reservationRegistry(t, repo)
	svc := reservationService(t, repo)

	if _, err := svc.Availability(ctx, reservation.AvailabilityRequest{
		RestaurantID: 7, Date: reservationDate, PartySize: 2,
	}); err != nil {
		t.Fatalf("Availability: %v", err)
	}

	call := domaintool.ToolCall{
		ID:   "req-1",
		Name: tools.RequestReservationToolName,
		Arguments: json.RawMessage(
			`{"restaurant_id":7,"slot_id":"` + reservationSlot + `","party_size":2}`),
	}
	// The approval is the server's half of the call: the identity the tool
	// derives its idempotency key from, never an argument the model supplied.
	approvedCtx := hitl.WithApproval(ctx, hitl.Approval{
		ThreadID:  "thread-1",
		UserID:    "user-1",
		RequestID: call.ID,
		Action:    call.Name,
		Arguments: call.Arguments,
	})

	first := registry.Invoke(approvedCtx, call)
	if first.Status != domaintool.ToolStatusOK {
		t.Fatalf("approved call failed: %v", first.Error)
	}
	second := registry.Invoke(approvedCtx, call)
	if second.Status != domaintool.ToolStatusOK {
		t.Fatalf("replayed call failed: %v", second.Error)
	}

	var firstBooking, secondBooking domainres.Reservation
	if err := json.Unmarshal(first.Data, &firstBooking); err != nil {
		t.Fatalf("decode first booking: %v", err)
	}
	if err := json.Unmarshal(second.Data, &secondBooking); err != nil {
		t.Fatalf("decode second booking: %v", err)
	}
	if firstBooking.ReservationID != secondBooking.ReservationID {
		t.Fatalf("the replay booked a second table: %q then %q",
			firstBooking.ReservationID, secondBooking.ReservationID)
	}
	if firstBooking.Status != domainres.StatusConfirmed {
		t.Fatalf("status = %q, want confirmed", firstBooking.Status)
	}
	if firstBooking.IdempotencyKey == "" {
		t.Fatal("the booking was written without an idempotency key")
	}

	// One row, one key, two saves: the hold and its promotion.
	if saves := repo.saves; saves != 2 {
		t.Fatalf("reservation saves = %d, want 2 (hold, then promotion)", saves)
	}
	slot, err := repo.GetSlot(ctx, reservationSlot)
	if err != nil {
		t.Fatalf("GetSlot: %v", err)
	}
	if slot.Booked != 2 {
		t.Fatalf("slot booked = %d, want 2: the replay spent the seats twice", slot.Booked)
	}
}

// ---- helpers --------------------------------------------------------------

func conversationRunner(
	t *testing.T, provider *scriptedProvider, registry *toolreg.Registry,
	conversations store.ConversationRepository,
) *agent.Runner {
	t.Helper()
	runner, err := agent.NewRunner(agent.Config{MaxToolRounds: 4}, agent.Deps{
		Chat:          provider,
		ToolCalling:   provider,
		Registry:      registry,
		Extractor:     slots.New(slots.Deps{}),
		Conversations: conversations,
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner
}

func confirmationAsks(events []agent.Event) []agent.Event {
	var out []agent.Event
	for _, ev := range events {
		if ev.Type == agent.EventConfirmationRequired {
			out = append(out, ev)
		}
	}
	return out
}
