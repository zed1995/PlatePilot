package store

import (
	"context"

	"github.com/zed/platepilot/shared/domain/admin"
)

// AdminStore is the read-only query surface of the administration console.
//
// It deliberately offers no write methods: the chat service stays a pure read
// side, and the data pipeline remains the only writer. Each method answers one
// page block rather than being a generic table query, so a change in the stored
// columns fails to compile instead of hiding in a runtime string.
type AdminStore interface {
	// Overview returns the dashboard counters and recent records.
	Overview(ctx context.Context) (admin.Overview, error)

	// Restaurants lists restaurants with a keyset cursor, newest id first.
	Restaurants(ctx context.Context, q admin.RestaurantQuery) (admin.RestaurantPage, error)
	// RestaurantDetail returns one restaurant's full row.
	RestaurantDetail(ctx context.Context, restaurantID int64) (admin.RestaurantDetail, error)
	// Reviews lists one restaurant's reviews, newest first.
	Reviews(ctx context.Context, q admin.ReviewQuery) (admin.ReviewPage, error)
	// Summaries lists one restaurant's per-topic review rollups.
	Summaries(ctx context.Context, restaurantID int64) ([]admin.ReviewSummary, error)

	// Documents lists documents across restaurants, newest id first.
	Documents(ctx context.Context, q admin.DocumentQuery) (admin.DocumentPage, error)
	// DocumentDetail returns one document including vector metadata. When
	// includeVectorPreview is set, the first components of the stored vector
	// are returned as well.
	DocumentDetail(ctx context.Context, documentID int64, includeVectorPreview bool) (admin.DocumentDetail, error)
	// DocumentsByRestaurant lists one restaurant's documents, active first.
	DocumentsByRestaurant(ctx context.Context, restaurantID int64) ([]admin.DocumentSummary, error)

	// Batches lists audit records, newest start time first.
	Batches(ctx context.Context, q admin.BatchQuery) (admin.BatchPage, error)
	// BatchDetail returns one record with its rejection breakdown. At most
	// maxRejections rejection items are returned.
	BatchDetail(ctx context.Context, batchID int64, maxRejections int) (admin.BatchDetail, error)

	// Boundaries lists administrative areas without their geometry.
	Boundaries(ctx context.Context) ([]admin.Boundary, error)
}
