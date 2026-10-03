package app

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/zed1995/platepilot/chat-service/internal/agent/tools"
	"github.com/zed1995/platepilot/chat-service/internal/config"
	"github.com/zed1995/platepilot/chat-service/internal/httpapi"
	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/testkit"
)

// reservationConfig is the base configuration with the mock capability switched
// on, so a test can flip one field at a time.
func reservationConfig() config.Config {
	cfg := assemblyConfig()
	cfg.Reservation = config.ReservationConfig{
		Enabled:       true,
		HoldTTL:       10 * time.Minute,
		PolicyVersion: "mock-v1",
	}
	return cfg
}

func reservationDeps(chatProvider *testkit.MockChatProvider) Deps {
	return Deps{
		Chat:          chatProvider,
		ToolCalling:   chatProvider,
		Structured:    chatProvider,
		Restaurants:   testkit.NewRestaurantRepository(),
		Knowledge:     testkit.NewKnowledgeRepository(),
		Conversations: testkit.NewConversationRepository(),
		Memories:      testkit.NewMemoryRepository(),
		Reservations:  testkit.NewReservationRepository(),
	}
}

func newAssembly(t *testing.T, cfg config.Config, deps Deps) *App {
	t.Helper()
	application, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), deps, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return application
}

// The switch has to remove the tools from the model's view, not leave them
// registered and failing. A deployment without the capability presents a model
// that cannot even ask for a table, so it never promises one it cannot book.
func TestReservationToolsAreAbsentWhenTheCapabilityIsOff(t *testing.T) {
	application := newAssembly(t, assemblyConfig(), reservationDeps(&testkit.MockChatProvider{Tools: true}))

	for _, name := range []string{tools.GetAvailabilityToolName, tools.RequestReservationToolName} {
		if application.registry.RequiresConfirmation(name) {
			t.Fatalf("%s is gated in a deployment with no reservation capability", name)
		}
	}
	if offered(application, tools.RequestReservationToolName) {
		t.Fatal("request_reservation was offered to the model with the capability switched off")
	}
}

func TestReservationToolsArePresentAndGatedWhenTheCapabilityIsOn(t *testing.T) {
	application := newAssembly(t, reservationConfig(), reservationDeps(&testkit.MockChatProvider{Tools: true}))

	if !offered(application, tools.GetAvailabilityToolName) {
		t.Fatal("get_availability is missing from the tool list")
	}
	if !offered(application, tools.RequestReservationToolName) {
		t.Fatal("request_reservation is missing from the tool list")
	}
	// The read is not gated and the write is: parking the lookup would turn a
	// lookup into a question, and letting the write through would be the whole
	// failure the gate exists to prevent.
	if application.registry.RequiresConfirmation(tools.GetAvailabilityToolName) {
		t.Fatal("get_availability is parked for approval")
	}
	if !application.registry.RequiresConfirmation(tools.RequestReservationToolName) {
		t.Fatal("request_reservation is not gated")
	}
}

// The switch is an operator assertion about a capability that needs somewhere to
// write inventory. Honouring it without a store would produce a booking path
// that fails at the first request instead of at startup.
func TestEnablingReservationsWithoutAStoreIsAStartupFailure(t *testing.T) {
	deps := reservationDeps(&testkit.MockChatProvider{Tools: true})
	deps.Reservations = nil

	if _, err := New(reservationConfig(), slog.New(slog.NewTextHandler(io.Discard, nil)), deps, "test"); err == nil {
		t.Fatal("expected the missing reservation store to fail assembly")
	}
}

// Without a conversation store there is no thread to park a request on, so the
// capability degrades to availability-only rather than offering a booking
// nobody could approve.
func TestReservationsWithoutAConversationStoreDegradeToAvailabilityOnly(t *testing.T) {
	deps := reservationDeps(&testkit.MockChatProvider{Tools: true})
	deps.Conversations = nil

	application := newAssembly(t, reservationConfig(), deps)
	if !offered(application, tools.GetAvailabilityToolName) {
		t.Fatal("get_availability should still be offered")
	}
	// The tool is no longer reachable on a thread, and the decision endpoint
	// says so instead of reporting a thread with nothing pending.
	svc := newChatService(chatServiceDeps{Runner: application.agent})
	if _, err := svc.ConfirmAction(context.Background(), httpapi.ConfirmInput{
		ThreadID: "thread-1", Decision: httpapi.DecisionConfirm,
	}); err == nil {
		t.Fatal("expected the decision endpoint to report the capability as unavailable")
	}
}

// With the capability off entirely the decision endpoint reports it as
// unavailable. It must not look like a thread with nothing pending: the two are
// different answers and a client acts on them differently.
func TestTheDecisionEndpointReportsADisabledCapability(t *testing.T) {
	svc := newChatService(chatServiceDeps{})

	_, err := svc.ConfirmAction(context.Background(), httpapi.ConfirmInput{
		ThreadID: "thread-1", Decision: httpapi.DecisionConfirm,
	})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if code := errs.CodeOf(err); code != errs.CodeInvalidArgument {
		t.Fatalf("code = %q, want %q", code, errs.CodeInvalidArgument)
	}
	if code := errs.CodeOf(err); code == errs.CodeAgentNoPendingAction {
		t.Fatal("a disabled capability reported itself as a thread with nothing pending")
	}
}

func offered(application *App, name string) bool {
	for _, spec := range application.registry.Specs() {
		if spec.Name == name {
			return true
		}
	}
	return false
}
