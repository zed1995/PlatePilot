package inspect_test

import (
	"context"
	"testing"
	"time"

	"github.com/zed/platepilot/chat-service/internal/inspect"
	domaininspect "github.com/zed/platepilot/shared/domain/inspect"
)

// fakeStore is an in-memory InspectStore for the application layer tests.
type fakeStore struct {
	overview       domaininspect.Overview
	restaurantPage domaininspect.RestaurantPage
	reviewPage     domaininspect.ReviewPage
	documentPage   domaininspect.DocumentPage
	batchPage      domaininspect.BatchPage
	batchDetail    domaininspect.BatchDetail

	restaurantQueries []domaininspect.RestaurantQuery
	reviewQueries     []domaininspect.ReviewQuery
	documentQueries   []domaininspect.DocumentQuery
	batchQueries      []domaininspect.BatchQuery

	lastDetailID      int64
	lastIncludeVector bool
	lastMaxRejections  int
}

func (f *fakeStore) Overview(context.Context) (domaininspect.Overview, error) {
	return f.overview, nil
}

func (f *fakeStore) Restaurants(
	_ context.Context, q domaininspect.RestaurantQuery,
) (domaininspect.RestaurantPage, error) {
	f.restaurantQueries = append(f.restaurantQueries, q)
	return f.restaurantPage, nil
}

func (f *fakeStore) RestaurantDetail(
	_ context.Context, restaurantID int64,
) (domaininspect.RestaurantDetail, error) {
	f.lastDetailID = restaurantID
	return domaininspect.RestaurantDetail{}, nil
}

func (f *fakeStore) Reviews(
	_ context.Context, q domaininspect.ReviewQuery,
) (domaininspect.ReviewPage, error) {
	f.reviewQueries = append(f.reviewQueries, q)
	return f.reviewPage, nil
}

func (f *fakeStore) Summaries(
	_ context.Context, restaurantID int64,
) ([]domaininspect.ReviewSummary, error) {
	return nil, nil
}

func (f *fakeStore) Documents(
	_ context.Context, q domaininspect.DocumentQuery,
) (domaininspect.DocumentPage, error) {
	f.documentQueries = append(f.documentQueries, q)
	return f.documentPage, nil
}

func (f *fakeStore) DocumentDetail(
	_ context.Context, documentID int64, includeVectorPreview bool,
) (domaininspect.DocumentDetail, error) {
	f.lastDetailID = documentID
	f.lastIncludeVector = includeVectorPreview
	return domaininspect.DocumentDetail{}, nil
}

func (f *fakeStore) DocumentsByRestaurant(
	_ context.Context, restaurantID int64,
) ([]domaininspect.DocumentSummary, error) {
	f.lastDetailID = restaurantID
	return nil, nil
}

func (f *fakeStore) Batches(
	_ context.Context, q domaininspect.BatchQuery,
) (domaininspect.BatchPage, error) {
	f.batchQueries = append(f.batchQueries, q)
	return f.batchPage, nil
}

func (f *fakeStore) BatchDetail(
	_ context.Context, batchID int64, maxRejections int,
) (domaininspect.BatchDetail, error) {
	f.lastDetailID = batchID
	f.lastMaxRejections = maxRejections
	return f.batchDetail, nil
}

func (f *fakeStore) Boundaries(context.Context) ([]domaininspect.Boundary, error) {
	return nil, nil
}

func newService(store *fakeStore) *inspect.Service {
	return inspect.NewService(store, inspect.Config{
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

			_, err := svc.Restaurants(context.Background(), domaininspect.RestaurantQuery{
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
	_, err := svc.Restaurants(context.Background(), domaininspect.RestaurantQuery{
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

	_, err := svc.Restaurants(context.Background(), domaininspect.RestaurantQuery{
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
		restaurantPage: domaininspect.RestaurantPage{
			Items: []domaininspect.RestaurantListItem{
				{RestaurantID: 10},
			},
			HasMore: true,
		},
	}
	svc := newService(store)

	page, err := svc.Restaurants(context.Background(), domaininspect.RestaurantQuery{})
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
	page, err = svc.Restaurants(context.Background(), domaininspect.RestaurantQuery{})
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
		reviewPage: domaininspect.ReviewPage{
			Items: []domaininspect.ReviewListItem{
				{ReviewID: 77, ReviewedAt: at},
			},
			HasMore: true,
		},
	}
	svc := newService(store)

	page, err := svc.Reviews(context.Background(), domaininspect.ReviewQuery{})
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
	_, err = svc.Reviews(context.Background(), domaininspect.ReviewQuery{
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
		batchPage: domaininspect.BatchPage{
			Items:   []domaininspect.BatchListItem{{BatchID: 9, StartedAt: at}},
			HasMore: true,
		},
	}
	svc := newService(store)

	page, err := svc.Batches(context.Background(), domaininspect.BatchQuery{})
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
		documentPage: domaininspect.DocumentPage{
			Items:   []domaininspect.DocumentListItem{{DocumentID: 33}},
			HasMore: true,
		},
	}
	svc := newService(store)

	page, err := svc.Documents(context.Background(), domaininspect.DocumentQuery{})
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

// mustEncodeID builds an opaque id cursor using the package internals via the
// exported encode path is not available in _test package; craft one with the
// same wire shape.
func mustEncodeID(t *testing.T, id int64) string {
	t.Helper()
	// encodeID lives in the internal package; the service emits cursors of
	// this shape when HasMore is set.
	store := &fakeStore{
		restaurantPage: domaininspect.RestaurantPage{
			Items:   []domaininspect.RestaurantListItem{{RestaurantID: id}},
			HasMore: true,
		},
	}
	page, err := newService(store).Restaurants(context.Background(), domaininspect.RestaurantQuery{})
	if err != nil {
		t.Fatalf("build cursor: %v", err)
	}
	return page.NextCursor
}

func mustDecodeID(t *testing.T, cursor string) int64 {
	t.Helper()
	store := &fakeStore{
		restaurantPage: domaininspect.RestaurantPage{},
	}
	_, err := newService(store).Restaurants(context.Background(), domaininspect.RestaurantQuery{
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
	_, err := newService(store).Reviews(context.Background(), domaininspect.ReviewQuery{
		Cursor: cursor,
	})
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	q := store.reviewQueries[0]
	return q.AfterReviewedAt, q.AfterID
}
