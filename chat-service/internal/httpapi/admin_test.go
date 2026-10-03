package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"

	domainadmin "github.com/zed1995/platepilot/shared/domain/admin"
)

// fakeAdmin is a test AdminService.
type fakeAdmin struct {
	overview    domainadmin.Overview
	restPage    domainadmin.RestaurantPage
	restDetail  domainadmin.RestaurantDetail
	reviewPage  domainadmin.ReviewPage
	summaries   []domainadmin.ReviewSummary
	docPage     domainadmin.DocumentPage
	docDetail   domainadmin.DocumentDetail
	restDocs    []domainadmin.DocumentSummary
	batchPage   domainadmin.BatchPage
	batchDetail domainadmin.BatchDetail
	boundaries  []domainadmin.Boundary

	restQ     domainadmin.RestaurantQuery
	reviewQ   domainadmin.ReviewQuery
	docQ      domainadmin.DocumentQuery
	batchQ    domainadmin.BatchQuery
	detailIDs []int64
	preview   bool
}

func (f *fakeAdmin) Overview(context.Context) (domainadmin.Overview, error) {
	return f.overview, nil
}

func (f *fakeAdmin) Restaurants(
	_ context.Context, q domainadmin.RestaurantQuery,
) (domainadmin.RestaurantPage, error) {
	f.restQ = q
	return f.restPage, nil
}

func (f *fakeAdmin) RestaurantDetail(
	_ context.Context, id int64,
) (domainadmin.RestaurantDetail, error) {
	f.detailIDs = append(f.detailIDs, id)
	return f.restDetail, nil
}

func (f *fakeAdmin) Reviews(
	_ context.Context, q domainadmin.ReviewQuery,
) (domainadmin.ReviewPage, error) {
	f.reviewQ = q
	return f.reviewPage, nil
}

func (f *fakeAdmin) Summaries(
	_ context.Context, id int64,
) ([]domainadmin.ReviewSummary, error) {
	f.detailIDs = append(f.detailIDs, id)
	return f.summaries, nil
}

func (f *fakeAdmin) Documents(
	_ context.Context, q domainadmin.DocumentQuery,
) (domainadmin.DocumentPage, error) {
	f.docQ = q
	return f.docPage, nil
}

func (f *fakeAdmin) DocumentDetail(
	_ context.Context, id int64, includePreview bool,
) (domainadmin.DocumentDetail, error) {
	f.detailIDs = append(f.detailIDs, id)
	f.preview = includePreview
	return f.docDetail, nil
}

func (f *fakeAdmin) DocumentsByRestaurant(
	_ context.Context, id int64,
) ([]domainadmin.DocumentSummary, error) {
	f.detailIDs = append(f.detailIDs, id)
	return f.restDocs, nil
}

func (f *fakeAdmin) Batches(
	_ context.Context, q domainadmin.BatchQuery,
) (domainadmin.BatchPage, error) {
	f.batchQ = q
	return f.batchPage, nil
}

func (f *fakeAdmin) BatchDetail(
	_ context.Context, id int64,
) (domainadmin.BatchDetail, error) {
	f.detailIDs = append(f.detailIDs, id)
	return f.batchDetail, nil
}

func (f *fakeAdmin) Boundaries(context.Context) ([]domainadmin.Boundary, error) {
	return f.boundaries, nil
}

// adminHandlersEngine mounts every admin handler without the guard, so the
// handler logic can be exercised by the in-process test client.
func adminHandlersEngine(f *fakeAdmin) *server.Hertz {
	h := server.Default(server.WithHostPorts(":0"))
	h.GET("/overview", OverviewHandler(f))
	h.GET("/restaurants", RestaurantsAdminHandler(f))
	h.GET("/restaurants/:id", RestaurantDetailAdminHandler(f))
	h.GET("/restaurants/:id/reviews", RestaurantReviewsHandler(f))
	h.GET("/restaurants/:id/summaries", RestaurantSummariesHandler(f))
	h.GET("/restaurants/:id/documents", RestaurantDocumentsHandler(f))
	h.GET("/documents", DocumentsAdminHandler(f))
	h.GET("/documents/:id", DocumentDetailAdminHandler(f))
	h.GET("/batches", BatchesAdminHandler(f))
	h.GET("/batches/:id", BatchDetailAdminHandler(f))
	h.GET("/boundaries", BoundariesHandler(f))
	return h
}

func TestAdminOverviewHandler(t *testing.T) {
	fake := &fakeAdmin{
		overview: domainadmin.Overview{
			Tables: domainadmin.TableCounts{RestaurantsTotal: 3000},
		},
	}
	w := ut.PerformRequest(adminHandlersEngine(fake).Engine, http.MethodGet, "/overview", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", w.Code, w.Body.String())
	}
	var out domainadmin.Overview
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Tables.RestaurantsTotal != 3000 {
		t.Errorf("total got %d", out.Tables.RestaurantsTotal)
	}
}

func TestAdminRestaurantsForwardsQuery(t *testing.T) {
	fake := &fakeAdmin{
		restPage: domainadmin.RestaurantPage{
			Items: []domainadmin.RestaurantListItem{{RestaurantID: 7}},
		},
	}
	url := "/restaurants?cursor=abc&limit=50&borough=manhattan&cuisine=pizza&active=true&q=joe"
	w := ut.PerformRequest(adminHandlersEngine(fake).Engine, http.MethodGet, url, nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", w.Code, w.Body.String())
	}
	q := fake.restQ
	if q.Cursor != "abc" || q.Limit != 50 || q.Borough != "manhattan" ||
		q.Cuisine != "pizza" || q.Q != "joe" {
		t.Errorf("query not forwarded: %+v", q)
	}
	if q.Active == nil || !*q.Active {
		t.Error("active must be the pointer to true")
	}
}

func TestAdminRestaurantsActiveDefaultsToUnfiltered(t *testing.T) {
	fake := &fakeAdmin{}
	w := ut.PerformRequest(adminHandlersEngine(fake).Engine, http.MethodGet, "/restaurants?active=false", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if fake.restQ.Active == nil || *fake.restQ.Active {
		t.Error("active must be the pointer to false, distinguishing it from unset")
	}
}

func TestAdminRestaurantsRejectsBadParams(t *testing.T) {
	h := adminHandlersEngine(&fakeAdmin{})

	cases := map[string]string{
		"bad limit":  "/restaurants?limit=many",
		"bad active": "/restaurants?active=maybe",
	}
	for name, url := range cases {
		t.Run(name, func(t *testing.T) {
			w := ut.PerformRequest(h.Engine, http.MethodGet, url, nil)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", w.Code)
			}
			if decodeError(t, w.Body.Bytes()).Error.Code != "invalid_argument" {
				t.Errorf("want invalid_argument error code")
			}
		})
	}
}

func TestAdminRestaurantDetailPathID(t *testing.T) {
	fake := &fakeAdmin{}
	h := adminHandlersEngine(fake)

	w := ut.PerformRequest(h.Engine, http.MethodGet, "/restaurants/42", nil)
	if w.Code != http.StatusOK || len(fake.detailIDs) != 1 || fake.detailIDs[0] != 42 {
		t.Fatalf("status = %d, forwarded ids = %v", w.Code, fake.detailIDs)
	}

	w = ut.PerformRequest(h.Engine, http.MethodGet, "/restaurants/0", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a non-positive id", w.Code)
	}
}

func TestAdminReviewsForwardsQuery(t *testing.T) {
	fake := &fakeAdmin{}
	w := ut.PerformRequest(adminHandlersEngine(fake).Engine, http.MethodGet,
		"/restaurants/9/reviews?cursor=c&limit=30", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", w.Code, w.Body.String())
	}
	if fake.reviewQ.RestaurantID != 9 || fake.reviewQ.Cursor != "c" ||
		fake.reviewQ.Limit != 30 {
		t.Errorf("query not forwarded: %+v", fake.reviewQ)
	}
}

func TestAdminEmptyListsSerializeAsArrays(t *testing.T) {
	fake := &fakeAdmin{} // every slice is nil
	h := adminHandlersEngine(fake)

	paths := []string{
		"/restaurants/1/summaries",
		"/restaurants/1/documents",
		"/boundaries",
	}
	for _, path := range paths {
		w := ut.PerformRequest(h.Engine, http.MethodGet, path, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, w.Code)
		}
		if w.Body.String() != "[]" {
			t.Errorf("%s body = %q, want []", path, w.Body.String())
		}
	}
}

func TestAdminEmptyPagesSerializeItemsAsArrays(t *testing.T) {
	fake := &fakeAdmin{}
	h := adminHandlersEngine(fake)

	cases := []string{
		"/restaurants",
		"/restaurants/1/reviews",
		"/documents",
		"/batches",
	}
	for _, path := range cases {
		w := ut.PerformRequest(h.Engine, http.MethodGet, path, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, w.Code)
		}
		var body struct {
			Items json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s decode: %v", path, err)
		}
		if string(body.Items) != "[]" {
			t.Errorf("%s items = %q, want []", path, body.Items)
		}
	}
}

func TestAdminDocumentsForwardsFilters(t *testing.T) {
	fake := &fakeAdmin{}
	url := "/documents?cursor=x&limit=80&restaurant_id=12&scope=restaurant" +
		"&doc_type=profile&is_active=false&has_embedding=true"
	w := ut.PerformRequest(adminHandlersEngine(fake).Engine, http.MethodGet, url, nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (body %s)", w.Code, w.Body.String())
	}
	q := fake.docQ
	if q.Cursor != "x" || q.Limit != 80 || q.RestaurantID != 12 ||
		q.Scope != "restaurant" || q.DocType != "profile" {
		t.Errorf("filters not forwarded: %+v", q)
	}
	if q.IsActive == nil || *q.IsActive {
		t.Error("is_active must be pointer to false")
	}
	if q.HasEmbedding == nil || !*q.HasEmbedding {
		t.Error("has_embedding must be pointer to true")
	}
}

func TestAdminDocumentsRejectsBadRestaurantID(t *testing.T) {
	w := ut.PerformRequest(adminHandlersEngine(&fakeAdmin{}).Engine, http.MethodGet,
		"/documents?restaurant_id=abc", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestAdminDocumentDetailVectorPreviewFlag(t *testing.T) {
	fake := &fakeAdmin{}
	h := adminHandlersEngine(fake)

	w := ut.PerformRequest(h.Engine, http.MethodGet, "/documents/5", nil)
	if w.Code != http.StatusOK || fake.preview {
		t.Fatalf("status = %d, preview default must be false", w.Code)
	}
	w = ut.PerformRequest(h.Engine, http.MethodGet, "/documents/5?vector_preview=true", nil)
	if w.Code != http.StatusOK || !fake.preview {
		t.Fatalf("status = %d, preview must be true", w.Code)
	}
}

func TestAdminBatchesForwardsFilters(t *testing.T) {
	fake := &fakeAdmin{}
	w := ut.PerformRequest(adminHandlersEngine(fake).Engine, http.MethodGet,
		"/batches?cursor=c&limit=60&stage=m2", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if fake.batchQ.Cursor != "c" || fake.batchQ.Limit != 60 ||
		fake.batchQ.Stage != "m2" {
		t.Errorf("filters not forwarded: %+v", fake.batchQ)
	}
}

func TestAdminRoutesAreNotRegisteredWhenDisabled(t *testing.T) {
	h := testRouter()

	w := ut.PerformRequest(h.Engine, http.MethodGet, "/admin/v1/overview", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 while the console is disabled", w.Code)
	}
}

func TestAdminRoutesAreGuardedWhenEnabled(t *testing.T) {
	h := NewRouter(Config{
		Addr:         ":0",
		Version:      "test",
		Logger:       quietLogger(),
		Admin:        &fakeAdmin{},
		AdminEnabled: true,
	})

	// The test client's peer address is the unspecified address, so the
	// guard refuses the request even though the route exists.
	w := ut.PerformRequest(h.Engine, http.MethodGet, "/admin/v1/overview", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a non-loopback peer", w.Code)
	}
	if decodeError(t, w.Body.Bytes()).Error.Code != "unauthorized" {
		t.Errorf("want unauthorized error code")
	}
}
