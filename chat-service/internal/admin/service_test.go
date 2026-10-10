package admin_test

import (
	"context"
	"testing"
	"time"

	"github.com/zed1995/platepilot/chat-service/internal/admin"
	domainadmin "github.com/zed1995/platepilot/shared/domain/admin"
	"github.com/zed1995/platepilot/shared/domain/reservation"
	"github.com/zed1995/platepilot/shared/store/memory"
)

// fakeStore is an in-memory AdminStore for the application layer tests.
type fakeStore struct {
	overview       domainadmin.Overview
	restaurantPage domainadmin.RestaurantPage
	reviewPage     domainadmin.ReviewPage
	documentPage   domainadmin.DocumentPage
	batchPage      domainadmin.BatchPage
	batchDetail    domainadmin.BatchDetail

	restaurantQueries []domainadmin.RestaurantQuery
	reviewQueries     []domainadmin.ReviewQuery
	documentQueries   []domainadmin.DocumentQuery
	batchQueries      []domainadmin.BatchQuery

	lastDetailID      int64
	lastIncludeVector bool
	lastMaxRejections int

	sourceReviews []domainadmin.ReviewListItem
}

func (f *fakeStore) Overview(context.Context) (domainadmin.Overview, error) {
	return f.overview, nil
}

func (f *fakeStore) Restaurants(
	_ context.Context, q domainadmin.RestaurantQuery,
) (domainadmin.RestaurantPage, error) {
	f.restaurantQueries = append(f.restaurantQueries, q)
	return f.restaurantPage, nil
}

func (f *fakeStore) RestaurantDetail(
	_ context.Context, restaurantID int64,
) (domainadmin.RestaurantDetail, error) {
	f.lastDetailID = restaurantID
	return domainadmin.RestaurantDetail{}, nil
}

func (f *fakeStore) Reviews(
	_ context.Context, q domainadmin.ReviewQuery,
) (domainadmin.ReviewPage, error) {
	f.reviewQueries = append(f.reviewQueries, q)
	return f.reviewPage, nil
}

func (f *fakeStore) Summaries(
	_ context.Context, restaurantID int64,
) ([]domainadmin.ReviewSummary, error) {
	return nil, nil
}

func (f *fakeStore) Documents(
	_ context.Context, q domainadmin.DocumentQuery,
) (domainadmin.DocumentPage, error) {
	f.documentQueries = append(f.documentQueries, q)
	return f.documentPage, nil
}

func (f *fakeStore) DocumentDetail(
	_ context.Context, documentID int64, includeVectorPreview bool,
) (domainadmin.DocumentDetail, error) {
	f.lastDetailID = documentID
	f.lastIncludeVector = includeVectorPreview
	return domainadmin.DocumentDetail{}, nil
}

func (f *fakeStore) DocumentsByRestaurant(
	_ context.Context, restaurantID int64,
) ([]domainadmin.DocumentSummary, error) {
	f.lastDetailID = restaurantID
	return nil, nil
}

func (f *fakeStore) DocumentSourceReviews(
	_ context.Context, documentID int64,
) ([]domainadmin.ReviewListItem, error) {
	f.lastDetailID = documentID
	return f.sourceReviews, nil
}

func (f *fakeStore) Batches(
	_ context.Context, q domainadmin.BatchQuery,
) (domainadmin.BatchPage, error) {
	f.batchQueries = append(f.batchQueries, q)
	return f.batchPage, nil
}

func (f *fakeStore) BatchDetail(
	_ context.Context, batchID int64, maxRejections int,
) (domainadmin.BatchDetail, error) {
	f.lastDetailID = batchID
	f.lastMaxRejections = maxRejections
	return f.batchDetail, nil
}

func (f *fakeStore) Boundaries(context.Context) ([]domainadmin.Boundary, error) {
	return nil, nil
}

func newService(store *fakeStore) *admin.Service {
	return admin.NewService(store, admin.Config{
		DefaultPageSize:     25,
		MaxPageSize:         100,
		MaxRejections:       200,
		EmbeddingModel:      "nomic-embed-text",
		EmbeddingDimensions: 768,
	})
}

func TestOverviewAttachesEnvironment(t *testing.T) {
	store := &fakeStore{}
	svc := newService(store)

	out, err := svc.Overview(context.Background())
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if out.Environment.EmbeddingModel != "nomic-embed-text" {
		t.Errorf("embedding model got %q", out.Environment.EmbeddingModel)
	}
	if out.Environment.EmbeddingDimensions != 768 {
		t.Errorf("dimensions got %d", out.Environment.EmbeddingDimensions)
	}
}

func TestRestaurantPageSizeDefaultsAndClamps(t *testing.T) {
	cases := []struct {
		name    string
		request int
		want    int
	}{
		{"zero uses default", 0, 25},
		{"negative uses default", -5, 25},
		{"inside range passes through", 40, 40},
		{"above max clamps", 999, 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{}
			svc := newService(store)

			_, err := svc.Restaurants(context.Background(), domainadmin.RestaurantQuery{
				Limit: tc.request,
			})
			if err != nil {
				t.Fatalf("Restaurants: %v", err)
			}
			if got := store.restaurantQueries[0].Limit; got != tc.want {
				t.Errorf("limit got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRestaurantCursorDecodedToAfterID(t *testing.T) {
	store := &fakeStore{}
	svc := newService(store)

	cursor := mustEncodeID(t, 500)
	_, err := svc.Restaurants(context.Background(), domainadmin.RestaurantQuery{
		Cursor: cursor,
	})
	if err != nil {
		t.Fatalf("Restaurants: %v", err)
	}
	got := store.restaurantQueries[0]
	if got.AfterID != 500 {
		t.Errorf("after id got %d, want 500", got.AfterID)
	}
}

func TestRestaurantInvalidCursorRejected(t *testing.T) {
	store := &fakeStore{}
	svc := newService(store)

	_, err := svc.Restaurants(context.Background(), domainadmin.RestaurantQuery{
		Cursor: "!!!bad!!!",
	})
	if err == nil {
		t.Fatal("expected error for a bad cursor")
	}
	if len(store.restaurantQueries) != 0 {
		t.Error("store must not be called with a bad cursor")
	}
}

func TestEmitsCursorOnlyWhenMore(t *testing.T) {
	store := &fakeStore{
		restaurantPage: domainadmin.RestaurantPage{
			Items: []domainadmin.RestaurantListItem{
				{RestaurantID: 10},
			},
			HasMore: true,
		},
	}
	svc := newService(store)

	page, err := svc.Restaurants(context.Background(), domainadmin.RestaurantQuery{})
	if err != nil {
		t.Fatalf("Restaurants: %v", err)
	}
	if page.NextCursor == "" {
		t.Fatal("expected a next cursor when HasMore is true")
	}
	if mustDecodeID(t, page.NextCursor) != 10 {
		t.Errorf("cursor must encode the last row id 10")
	}

	store.restaurantPage.HasMore = false
	page, err = svc.Restaurants(context.Background(), domainadmin.RestaurantQuery{})
	if err != nil {
		t.Fatalf("Restaurants: %v", err)
	}
	if page.NextCursor != "" {
		t.Errorf("last page must not carry a cursor, got %q", page.NextCursor)
	}
}

func TestReviewsCursorRoundTrip(t *testing.T) {
	at := time.Date(2022, 3, 4, 8, 0, 0, 0, time.UTC)
	store := &fakeStore{
		reviewPage: domainadmin.ReviewPage{
			Items: []domainadmin.ReviewListItem{
				{ReviewID: 77, ReviewedAt: at},
			},
			HasMore: true,
		},
	}
	svc := newService(store)

	page, err := svc.Reviews(context.Background(), domainadmin.ReviewQuery{})
	if err != nil {
		t.Fatalf("Reviews: %v", err)
	}
	if page.NextCursor == "" {
		t.Fatal("expected a next cursor")
	}
	gotAt, gotID := mustDecodeTimed(t, page.NextCursor)
	if !gotAt.Equal(at) || gotID != 77 {
		t.Errorf("cursor got (%v, %d), want (%v, 77)", gotAt, gotID, at)
	}

	// Walking back must re-establish the store position.
	_, err = svc.Reviews(context.Background(), domainadmin.ReviewQuery{
		Cursor: page.NextCursor,
	})
	if err != nil {
		t.Fatalf("Reviews with cursor: %v", err)
	}
	received := store.reviewQueries[len(store.reviewQueries)-1]
	if !received.AfterReviewedAt.Equal(at) || received.AfterID != 77 {
		t.Errorf("store position got (%v, %d)", received.AfterReviewedAt, received.AfterID)
	}
}

func TestBatchesCursorRoundTrip(t *testing.T) {
	at := time.Date(2023, 1, 2, 3, 4, 5, 0, time.UTC)
	store := &fakeStore{
		batchPage: domainadmin.BatchPage{
			Items:   []domainadmin.BatchListItem{{BatchID: 9, StartedAt: at}},
			HasMore: true,
		},
	}
	svc := newService(store)

	page, err := svc.Batches(context.Background(), domainadmin.BatchQuery{})
	if err != nil {
		t.Fatalf("Batches: %v", err)
	}
	gotAt, gotID := mustDecodeTimed(t, page.NextCursor)
	if !gotAt.Equal(at) || gotID != 9 {
		t.Errorf("cursor got (%v, %d)", gotAt, gotID)
	}
}

func TestDocumentsCursorRoundTrip(t *testing.T) {
	store := &fakeStore{
		documentPage: domainadmin.DocumentPage{
			Items:   []domainadmin.DocumentListItem{{DocumentID: 33}},
			HasMore: true,
		},
	}
	svc := newService(store)

	page, err := svc.Documents(context.Background(), domainadmin.DocumentQuery{})
	if err != nil {
		t.Fatalf("Documents: %v", err)
	}
	if mustDecodeID(t, page.NextCursor) != 33 {
		t.Errorf("cursor must encode document id 33")
	}
}

func TestBatchDetailPassesConfiguredCap(t *testing.T) {
	store := &fakeStore{}
	svc := newService(store)

	if _, err := svc.BatchDetail(context.Background(), 12); err != nil {
		t.Fatalf("BatchDetail: %v", err)
	}
	if store.lastDetailID != 12 {
		t.Errorf("detail id got %d, want 12", store.lastDetailID)
	}
	if store.lastMaxRejections != 200 {
		t.Errorf("rejections cap got %d, want 200", store.lastMaxRejections)
	}
}

func TestDocumentDetailForwardsVectorFlag(t *testing.T) {
	store := &fakeStore{}
	svc := newService(store)

	_, _ = svc.DocumentDetail(context.Background(), 5, true)
	if !store.lastIncludeVector {
		t.Error("vector preview flag was not forwarded")
	}
}

// The service is a pass-through for source reviews, but the document id must
// reach the store and the recorded items must come back unchanged.
func TestDocumentSourceReviewsPassesThrough(t *testing.T) {
	store := &fakeStore{sourceReviews: []domainadmin.ReviewListItem{
		{ReviewID: 101, RestaurantID: 10, Rating: 5, Text: "amazing broth"},
	}}
	svc := newService(store)

	got, err := svc.DocumentSourceReviews(context.Background(), 7)
	if err != nil {
		t.Fatalf("DocumentSourceReviews: %v", err)
	}
	if store.lastDetailID != 7 {
		t.Errorf("document id = %d, want 7", store.lastDetailID)
	}
	if len(got) != 1 || got[0].ReviewID != 101 {
		t.Errorf("reviews = %+v, want the stored item", got)
	}
}

// mustEncodeID builds an opaque id cursor using the package internals via the
// exported encode path is not available in _test package; craft one with the
// same wire shape.
func mustEncodeID(t *testing.T, id int64) string {
	t.Helper()
	// encodeID lives in the internal package; the service emits cursors of
	// this shape when HasMore is set.
	store := &fakeStore{
		restaurantPage: domainadmin.RestaurantPage{
			Items:   []domainadmin.RestaurantListItem{{RestaurantID: id}},
			HasMore: true,
		},
	}
	page, err := newService(store).Restaurants(context.Background(), domainadmin.RestaurantQuery{})
	if err != nil {
		t.Fatalf("build cursor: %v", err)
	}
	return page.NextCursor
}

func mustDecodeID(t *testing.T, cursor string) int64 {
	t.Helper()
	store := &fakeStore{
		restaurantPage: domainadmin.RestaurantPage{},
	}
	_, err := newService(store).Restaurants(context.Background(), domainadmin.RestaurantQuery{
		Cursor: cursor,
	})
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	return store.restaurantQueries[0].AfterID
}

func mustDecodeTimed(t *testing.T, cursor string) (time.Time, int64) {
	t.Helper()
	store := &fakeStore{}
	_, err := newService(store).Reviews(context.Background(), domainadmin.ReviewQuery{
		Cursor: cursor,
	})
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	q := store.reviewQueries[0]
	return q.AfterReviewedAt, q.AfterID
}

func TestInventoryWithoutReservationsIsUnassembled(t *testing.T) {
	svc := newService(&fakeStore{})

	_, err := svc.Inventory(context.Background(), 42, "")
	if err == nil {
		t.Fatal("Inventory without the reservation feature must fail")
	}
	_, err = svc.ResetInventory(context.Background(), 42, "")
	if err == nil {
		t.Fatal("ResetInventory without the reservation feature must fail")
	}
}

func TestInventoryReadsSlotsAndBookings(t *testing.T) {
	mem := memory.NewReservationRepository()
	if err := mem.EnsureSlots(context.Background(), []reservation.Slot{
		{SlotID: "s1", RestaurantID: 42, SlotDate: "2026-10-04", SlotTime: "19:00", Capacity: 4, Booked: 2},
	}); err != nil {
		t.Fatalf("EnsureSlots: %v", err)
	}
	if err := mem.SaveReservation(context.Background(), reservation.Reservation{
		ReservationID: "r1", RestaurantID: 42, SlotID: "s1", PartySize: 2,
		Status: reservation.StatusConfirmed, IdempotencyKey: "key-r1",
	}); err != nil {
		t.Fatalf("SaveReservation: %v", err)
	}
	svc := admin.NewService(&fakeStore{}, admin.Config{Reservations: mem})

	view, err := svc.Inventory(context.Background(), 42, "2026-10-04")
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	if len(view.Slots) != 1 || view.Slots[0].Booked != 2 {
		t.Errorf("slots got %+v", view.Slots)
	}
	if len(view.Reservations) != 1 || view.Reservations[0].ReservationID != "r1" {
		t.Errorf("reservations got %+v", view.Reservations)
	}

	// The reset hands back every booked seat and clears the reservations.
	result, err := svc.ResetInventory(context.Background(), 42, "2026-10-04")
	if err != nil {
		t.Fatalf("ResetInventory: %v", err)
	}
	if result.SlotsReset != 1 || result.ReservationsRemoved != 1 {
		t.Errorf("reset got %+v", result)
	}
	view, err = svc.Inventory(context.Background(), 42, "2026-10-04")
	if err != nil {
		t.Fatalf("Inventory after reset: %v", err)
	}
	if view.Slots[0].Booked != 0 || len(view.Reservations) != 0 {
		t.Errorf("inventory after reset got %+v / %+v", view.Slots, view.Reservations)
	}
}

func TestInventoryRejectsMalformedDate(t *testing.T) {
	mem := memory.NewReservationRepository()
	svc := admin.NewService(&fakeStore{}, admin.Config{Reservations: mem})

	if _, err := svc.Inventory(context.Background(), 42, "10/04/2026"); err == nil {
		t.Error("Inventory must reject a non-ISO date")
	}
	if _, err := svc.ResetInventory(context.Background(), 42, "2026-13-40"); err == nil {
		t.Error("ResetInventory must reject an impossible date")
	}
}
