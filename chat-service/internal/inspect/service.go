// Package inspect is the administration console's read-only application layer.
//
// It turns the store's raw rows into view payloads: it clamps page sizes,
// decodes and emits opaque keyset cursors, and attaches the serving
// environment description. It holds no state and depends on neither the
// transport framework nor the database driver.
package inspect

import (
	"context"

	domaininspect "github.com/zed/platepilot/shared/domain/inspect"
	"github.com/zed/platepilot/shared/port"
)

// Defaults used when the configuration leaves a value unset.
const (
	defaultPageSize     = 25
	defaultMaxPageSize  = 100
	defaultMaxRejections = 200
)

// Config configures the application service.
type Config struct {
	DefaultPageSize int
	MaxPageSize     int
	MaxRejections   int
	// EmbeddingModel and EmbeddingDimensions describe the serving process.
	// They are reported on the dashboard but do not come from stored data.
	EmbeddingModel      string
	EmbeddingDimensions int
}

// Service is the read-only administration application service.
type Service struct {
	store port.InspectStore
	cfg   Config
}

// NewService builds an application service on the store.
func NewService(store port.InspectStore, cfg Config) *Service {
	if cfg.DefaultPageSize <= 0 {
		cfg.DefaultPageSize = defaultPageSize
	}
	if cfg.MaxPageSize <= 0 {
		cfg.MaxPageSize = defaultMaxPageSize
	}
	if cfg.MaxRejections <= 0 {
		cfg.MaxRejections = defaultMaxRejections
	}
	return &Service{store: store, cfg: cfg}
}

// Overview returns the dashboard payload with the environment description
// attached.
func (s *Service) Overview(ctx context.Context) (domaininspect.Overview, error) {
	overview, err := s.store.Overview(ctx)
	if err != nil {
		return overview, err
	}
	overview.Environment = domaininspect.Environment{
		EmbeddingModel:      s.cfg.EmbeddingModel,
		EmbeddingDimensions: s.cfg.EmbeddingDimensions,
	}
	return overview, nil
}

// Restaurants returns one keyset page of restaurants.
func (s *Service) Restaurants(
	ctx context.Context, q domaininspect.RestaurantQuery,
) (domaininspect.RestaurantPage, error) {
	if q.Cursor != "" {
		afterID, err := decodeID(q.Cursor)
		if err != nil {
			return domaininspect.RestaurantPage{}, err
		}
		q.AfterID = afterID
	}
	q.Limit = s.pageSize(q.Limit)

	page, err := s.store.Restaurants(ctx, q)
	if err != nil {
		return page, err
	}
	if page.HasMore && len(page.Items) > 0 {
		last := page.Items[len(page.Items)-1]
		cursor, err := encodeID(last.RestaurantID)
		if err != nil {
			return domaininspect.RestaurantPage{}, err
		}
		page.NextCursor = cursor
	}
	return page, nil
}

// RestaurantDetail returns one restaurant's full row.
func (s *Service) RestaurantDetail(
	ctx context.Context, restaurantID int64,
) (domaininspect.RestaurantDetail, error) {
	return s.store.RestaurantDetail(ctx, restaurantID)
}

// Reviews returns one keyset page of one restaurant's reviews.
func (s *Service) Reviews(
	ctx context.Context, q domaininspect.ReviewQuery,
) (domaininspect.ReviewPage, error) {
	if q.Cursor != "" {
		afterTime, afterID, err := decodeTimed(q.Cursor)
		if err != nil {
			return domaininspect.ReviewPage{}, err
		}
		q.AfterReviewedAt = afterTime
		q.AfterID = afterID
	}
	q.Limit = s.pageSize(q.Limit)

	page, err := s.store.Reviews(ctx, q)
	if err != nil {
		return page, err
	}
	if page.HasMore && len(page.Items) > 0 {
		last := page.Items[len(page.Items)-1]
		cursor, err := encodeTimed(last.ReviewedAt, last.ReviewID)
		if err != nil {
			return domaininspect.ReviewPage{}, err
		}
		page.NextCursor = cursor
	}
	return page, nil
}

// Summaries returns one restaurant's per-topic review rollups.
func (s *Service) Summaries(
	ctx context.Context, restaurantID int64,
) ([]domaininspect.ReviewSummary, error) {
	return s.store.Summaries(ctx, restaurantID)
}

// Documents returns one keyset page of documents.
func (s *Service) Documents(
	ctx context.Context, q domaininspect.DocumentQuery,
) (domaininspect.DocumentPage, error) {
	if q.Cursor != "" {
		afterID, err := decodeID(q.Cursor)
		if err != nil {
			return domaininspect.DocumentPage{}, err
		}
		q.AfterID = afterID
	}
	q.Limit = s.pageSize(q.Limit)

	page, err := s.store.Documents(ctx, q)
	if err != nil {
		return page, err
	}
	if page.HasMore && len(page.Items) > 0 {
		last := page.Items[len(page.Items)-1]
		cursor, err := encodeID(last.DocumentID)
		if err != nil {
			return domaininspect.DocumentPage{}, err
		}
		page.NextCursor = cursor
	}
	return page, nil
}

// DocumentDetail returns one document. The vector preview is included only
// when requested.
func (s *Service) DocumentDetail(
	ctx context.Context, documentID int64, includeVectorPreview bool,
) (domaininspect.DocumentDetail, error) {
	return s.store.DocumentDetail(ctx, documentID, includeVectorPreview)
}

// DocumentsByRestaurant returns one restaurant's documents, active first.
func (s *Service) DocumentsByRestaurant(
	ctx context.Context, restaurantID int64,
) ([]domaininspect.DocumentSummary, error) {
	return s.store.DocumentsByRestaurant(ctx, restaurantID)
}

// Batches returns one keyset page of audit records.
func (s *Service) Batches(
	ctx context.Context, q domaininspect.BatchQuery,
) (domaininspect.BatchPage, error) {
	if q.Cursor != "" {
		afterTime, afterID, err := decodeTimed(q.Cursor)
		if err != nil {
			return domaininspect.BatchPage{}, err
		}
		q.AfterStartedAt = afterTime
		q.AfterID = afterID
	}
	q.Limit = s.pageSize(q.Limit)

	page, err := s.store.Batches(ctx, q)
	if err != nil {
		return page, err
	}
	if page.HasMore && len(page.Items) > 0 {
		last := page.Items[len(page.Items)-1]
		cursor, err := encodeTimed(last.StartedAt, last.BatchID)
		if err != nil {
			return domaininspect.BatchPage{}, err
		}
		page.NextCursor = cursor
	}
	return page, nil
}

// BatchDetail returns one audit record with its rejection breakdown, capped by
// the configured maximum.
func (s *Service) BatchDetail(
	ctx context.Context, batchID int64,
) (domaininspect.BatchDetail, error) {
	return s.store.BatchDetail(ctx, batchID, s.cfg.MaxRejections)
}

// Boundaries returns the administrative areas without geometry.
func (s *Service) Boundaries(ctx context.Context) ([]domaininspect.Boundary, error) {
	return s.store.Boundaries(ctx)
}

// pageSize resolves the requested limit against the configured defaults. A
// limit above the maximum is clamped rather than rejected, matching the
// top-k handling.
func (s *Service) pageSize(requested int) int {
	switch {
	case requested <= 0:
		return s.cfg.DefaultPageSize
	case requested > s.cfg.MaxPageSize:
		return s.cfg.MaxPageSize
	default:
		return requested
	}
}
