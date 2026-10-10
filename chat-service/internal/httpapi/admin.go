package httpapi

import (
	"context"
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"

	"github.com/zed1995/platepilot/chat-service/internal/httpapi/middleware"
	"github.com/zed1995/platepilot/chat-service/internal/httperr"
	domainadmin "github.com/zed1995/platepilot/shared/domain/admin"
	"github.com/zed1995/platepilot/shared/domain/errs"
)

// AdminService is the read-only administration application layer this
// transport talks to.
//
// It is an interface rather than a concrete service so handlers are testable
// without a database; every method is a read.
type AdminService interface {
	Overview(ctx context.Context) (domainadmin.Overview, error)
	Restaurants(
		ctx context.Context, q domainadmin.RestaurantQuery,
	) (domainadmin.RestaurantPage, error)
	RestaurantDetail(
		ctx context.Context, restaurantID int64,
	) (domainadmin.RestaurantDetail, error)
	Reviews(
		ctx context.Context, q domainadmin.ReviewQuery,
	) (domainadmin.ReviewPage, error)
	Summaries(
		ctx context.Context, restaurantID int64,
	) ([]domainadmin.ReviewSummary, error)
	Documents(
		ctx context.Context, q domainadmin.DocumentQuery,
	) (domainadmin.DocumentPage, error)
	DocumentDetail(
		ctx context.Context, documentID int64, includeVectorPreview bool,
	) (domainadmin.DocumentDetail, error)
	DocumentSourceReviews(
		ctx context.Context, documentID int64,
	) ([]domainadmin.ReviewListItem, error)
	DocumentsByRestaurant(
		ctx context.Context, restaurantID int64,
	) ([]domainadmin.DocumentSummary, error)
	Batches(
		ctx context.Context, q domainadmin.BatchQuery,
	) (domainadmin.BatchPage, error)
	BatchDetail(
		ctx context.Context, batchID int64,
	) (domainadmin.BatchDetail, error)
	Boundaries(ctx context.Context) ([]domainadmin.Boundary, error)

	// Inventory and ResetInventory serve the mock reservation inventory. The
	// reset is the console's one deliberate write; both are no-ops against a
	// deployment without the reservation feature.
	Inventory(
		ctx context.Context, restaurantID int64, date string,
	) (domainadmin.InventoryView, error)
	ResetInventory(
		ctx context.Context, restaurantID int64, date string,
	) (domainadmin.InventoryResetResult, error)
}

// registerAdminRoutes mounts the whole administration surface behind the
// local-host guard. The debug posts reuse the client-contract handlers so the
// console observes the exact serving path.
func registerAdminRoutes(
	h *server.Hertz, admin AdminService, search SearchService, evidence EvidenceService,
) {
	g := h.Group("/admin/v1", middleware.LoopbackOnly())

	g.GET("/overview", OverviewHandler(admin))
	g.GET("/restaurants", RestaurantsAdminHandler(admin))
	g.GET("/restaurants/:id", RestaurantDetailAdminHandler(admin))
	g.GET("/restaurants/:id/reviews", RestaurantReviewsHandler(admin))
	g.GET("/restaurants/:id/summaries", RestaurantSummariesHandler(admin))
	g.GET("/restaurants/:id/documents", RestaurantDocumentsHandler(admin))
	g.GET("/documents", DocumentsAdminHandler(admin))
	g.GET("/documents/:id", DocumentDetailAdminHandler(admin))
	g.GET("/documents/:id/source-reviews", DocumentSourceReviewsHandler(admin))
	g.GET("/batches", BatchesAdminHandler(admin))
	g.GET("/batches/:id", BatchDetailAdminHandler(admin))
	g.GET("/boundaries", BoundariesHandler(admin))
	g.GET("/restaurants/:id/inventory", InventoryHandler(admin))
	g.POST("/restaurants/:id/inventory/reset", ResetInventoryHandler(admin))

	if search != nil {
		g.POST("/debug/search", SearchHandler(search))
	}
	if evidence != nil {
		g.POST("/debug/evidence", EvidenceHandler(evidence))
	}
}

// ---------------------------------------------------------------------------
// Query parameter helpers
// ---------------------------------------------------------------------------

// queryValue reads one query parameter as text. A missing parameter is "".
func queryValue(c *app.RequestContext, key string) string {
	return string(c.QueryArgs().Peek(key))
}

// queryIntValue reads a non-negative integer. A missing parameter is zero so
// the application layer can apply its default.
func queryIntValue(c *app.RequestContext, key string) (int, error) {
	raw := queryValue(c, key)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, errs.Newf(errs.CodeInvalidArgument,
			"%s must be a non-negative integer, got %q", key, raw)
	}
	return n, nil
}

// queryInt64Value reads a non-negative 64-bit integer. A missing parameter is
// zero so the application layer can apply its default.
func queryInt64Value(c *app.RequestContext, key string) (int64, error) {
	raw := queryValue(c, key)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, errs.Newf(errs.CodeInvalidArgument,
			"%s must be a non-negative integer, got %q", key, raw)
	}
	return n, nil
}

// queryBoolPtrValue reads a tri-state boolean: missing means no filter, a
// present true/false carries the third state.
func queryBoolPtrValue(c *app.RequestContext, key string) (*bool, error) {
	raw := queryValue(c, key)
	switch raw {
	case "":
		return nil, nil
	case "true", "1":
		value := true
		return &value, nil
	case "false", "0":
		value := false
		return &value, nil
	default:
		return nil, errs.Newf(errs.CodeInvalidArgument,
			"%s must be true or false, got %q", key, raw)
	}
}

// queryBoolValue reads a plain boolean defaulting to false.
func queryBoolValue(c *app.RequestContext, key string) (bool, error) {
	ptr, err := queryBoolPtrValue(c, key)
	if err != nil || ptr == nil {
		return false, err
	}
	return *ptr, nil
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// OverviewHandler answers GET /admin/v1/overview.
func OverviewHandler(svc AdminService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		overview, err := svc.Overview(ctx)
		if err != nil {
			httperr.Write(ctx, c, err)
			return
		}
		c.JSON(200, overview)
	}
}

// RestaurantsAdminHandler answers GET /admin/v1/restaurants.
func RestaurantsAdminHandler(svc AdminService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		limit, err := queryIntValue(c, "limit")
		if err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		active, err := queryBoolPtrValue(c, "active")
		if err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}

		page, err := svc.Restaurants(ctx, domainadmin.RestaurantQuery{
			Cursor:  queryValue(c, "cursor"),
			Limit:   limit,
			Borough: queryValue(c, "borough"),
			Cuisine: queryValue(c, "cuisine"),
			Active:  active,
			Q:       queryValue(c, "q"),
		})
		if err != nil {
			httperr.Write(ctx, c, err)
			return
		}
		if page.Items == nil {
			page.Items = []domainadmin.RestaurantListItem{}
		}
		c.JSON(200, page)
	}
}

// RestaurantDetailAdminHandler answers GET /admin/v1/restaurants/:id.
func RestaurantDetailAdminHandler(svc AdminService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		restaurantID, err := pathID(c, "id")
		if err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		detail, err := svc.RestaurantDetail(ctx, restaurantID)
		if err != nil {
			httperr.Write(ctx, c, err)
			return
		}
		c.JSON(200, detail)
	}
}

// RestaurantReviewsHandler answers GET /admin/v1/restaurants/:id/reviews.
func RestaurantReviewsHandler(svc AdminService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		restaurantID, err := pathID(c, "id")
		if err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		limit, err := queryIntValue(c, "limit")
		if err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}

		page, err := svc.Reviews(ctx, domainadmin.ReviewQuery{
			Cursor:       queryValue(c, "cursor"),
			Limit:        limit,
			RestaurantID: restaurantID,
		})
		if err != nil {
			httperr.Write(ctx, c, err)
			return
		}
		if page.Items == nil {
			page.Items = []domainadmin.ReviewListItem{}
		}
		c.JSON(200, page)
	}
}

// RestaurantSummariesHandler answers GET /admin/v1/restaurants/:id/summaries.
func RestaurantSummariesHandler(svc AdminService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		restaurantID, err := pathID(c, "id")
		if err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		summaries, err := svc.Summaries(ctx, restaurantID)
		if err != nil {
			httperr.Write(ctx, c, err)
			return
		}
		// An empty rollup set is an empty array, not null.
		if summaries == nil {
			summaries = []domainadmin.ReviewSummary{}
		}
		c.JSON(200, summaries)
	}
}

// RestaurantDocumentsHandler answers GET /admin/v1/restaurants/:id/documents.
func RestaurantDocumentsHandler(svc AdminService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		restaurantID, err := pathID(c, "id")
		if err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		documents, err := svc.DocumentsByRestaurant(ctx, restaurantID)
		if err != nil {
			httperr.Write(ctx, c, err)
			return
		}
		if documents == nil {
			documents = []domainadmin.DocumentSummary{}
		}
		c.JSON(200, documents)
	}
}

// DocumentsAdminHandler answers GET /admin/v1/documents.
func DocumentsAdminHandler(svc AdminService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		limit, err := queryIntValue(c, "limit")
		if err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		restaurantID, err := queryInt64Value(c, "restaurant_id")
		if err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		isActive, err := queryBoolPtrValue(c, "is_active")
		if err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		hasEmbedding, err := queryBoolPtrValue(c, "has_embedding")
		if err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}

		page, err := svc.Documents(ctx, domainadmin.DocumentQuery{
			Cursor:       queryValue(c, "cursor"),
			Limit:        limit,
			RestaurantID: restaurantID,
			Scope:        queryValue(c, "scope"),
			DocType:      queryValue(c, "doc_type"),
			IsActive:     isActive,
			HasEmbedding: hasEmbedding,
		})
		if err != nil {
			httperr.Write(ctx, c, err)
			return
		}
		if page.Items == nil {
			page.Items = []domainadmin.DocumentListItem{}
		}
		c.JSON(200, page)
	}
}

// DocumentDetailAdminHandler answers GET /admin/v1/documents/:id.
func DocumentDetailAdminHandler(svc AdminService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		documentID, err := pathID(c, "id")
		if err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		includePreview, err := queryBoolValue(c, "vector_preview")
		if err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		detail, err := svc.DocumentDetail(ctx, documentID, includePreview)
		if err != nil {
			httperr.Write(ctx, c, err)
			return
		}
		c.JSON(200, detail)
	}
}

// DocumentSourceReviewsHandler answers GET
// /admin/v1/documents/:id/source-reviews.
func DocumentSourceReviewsHandler(svc AdminService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		documentID, err := pathID(c, "id")
		if err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		reviews, err := svc.DocumentSourceReviews(ctx, documentID)
		if err != nil {
			httperr.Write(ctx, c, err)
			return
		}
		if reviews == nil {
			reviews = []domainadmin.ReviewListItem{}
		}
		c.JSON(200, reviews)
	}
}

// BatchesAdminHandler answers GET /admin/v1/batches.
func BatchesAdminHandler(svc AdminService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		limit, err := queryIntValue(c, "limit")
		if err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		page, err := svc.Batches(ctx, domainadmin.BatchQuery{
			Cursor: queryValue(c, "cursor"),
			Limit:  limit,
			Stage:  queryValue(c, "stage"),
		})
		if err != nil {
			httperr.Write(ctx, c, err)
			return
		}
		if page.Items == nil {
			page.Items = []domainadmin.BatchListItem{}
		}
		c.JSON(200, page)
	}
}

// BatchDetailAdminHandler answers GET /admin/v1/batches/:id.
func BatchDetailAdminHandler(svc AdminService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		batchID, err := pathID(c, "id")
		if err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		detail, err := svc.BatchDetail(ctx, batchID)
		if err != nil {
			httperr.Write(ctx, c, err)
			return
		}
		c.JSON(200, detail)
	}
}

// BoundariesHandler answers GET /admin/v1/boundaries.
func BoundariesHandler(svc AdminService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		boundaries, err := svc.Boundaries(ctx)
		if err != nil {
			httperr.Write(ctx, c, err)
			return
		}
		if boundaries == nil {
			boundaries = []domainadmin.Boundary{}
		}
		c.JSON(200, boundaries)
	}
}

// InventoryHandler answers GET /admin/v1/restaurants/:id/inventory?date=.
// The date query parameter is optional: absent it spans every date.
func InventoryHandler(svc AdminService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		restaurantID, err := pathID(c, "id")
		if err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		view, err := svc.Inventory(ctx, restaurantID, queryValue(c, "date"))
		if err != nil {
			httperr.Write(ctx, c, err)
			return
		}
		c.JSON(200, view)
	}
}

// ResetInventoryHandler answers POST /admin/v1/restaurants/:id/inventory/reset?date=.
// It is the console's one write: the mock inventory returns to its pristine
// state so a demo can be replayed. Same route guard as every read here.
func ResetInventoryHandler(svc AdminService) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		restaurantID, err := pathID(c, "id")
		if err != nil {
			WriteAndAbort(ctx, c, err)
			return
		}
		result, err := svc.ResetInventory(ctx, restaurantID, queryValue(c, "date"))
		if err != nil {
			httperr.Write(ctx, c, err)
			return
		}
		c.JSON(200, result)
	}
}
