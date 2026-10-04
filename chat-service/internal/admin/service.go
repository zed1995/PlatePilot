// Package admin is the administration console's read-only application layer.
//
// It turns the store's raw rows into view payloads: it clamps page sizes,
// decodes and emits opaque keyset cursors, and attaches the serving
// environment description. It holds no state and depends on neither the
// transport framework nor the database driver.
package admin

import (
	"context"
	"time"

	"github.com/zed1995/platepilot/shared/domain/errs"
	"github.com/zed1995/platepilot/shared/domain/reservation"
	"github.com/zed1995/platepilot/shared/store"

	domainadmin "github.com/zed1995/platepilot/shared/domain/admin"
)

// Defaults used when the configuration leaves a value unset.
const (
	defaultPageSize      = 25
	defaultMaxPageSize   = 100
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

	// Reservations backs the mock inventory view and reset. It is optional:
	// when nil the inventory endpoints answer with a clear error instead of
	// pretending the feature exists.
	Reservations store.ReservationRepository
}

// Service is the administration application service. Everything except the
// mock inventory is read-only; the inventory reset exists because the
// reservation feature is a demo whose whole point is being replayable, and
// its reset route stays behind the same loopback guard as the rest of the
// console. AdminStore itself remains strictly read-only — the reset lives on
// the reservation repository, which is demo state, not pipeline data.
type Service struct {
	store store.AdminStore
	// reservations is optional: nil when the deployment runs without the
	// reservation feature, and the inventory endpoints report it as such.
	reservations store.ReservationRepository
	cfg          Config
}

// NewService builds an application service on the store.
func NewService(store store.AdminStore, cfg Config) *Service {
	if cfg.DefaultPageSize <= 0 {
		cfg.DefaultPageSize = defaultPageSize
	}
	if cfg.MaxPageSize <= 0 {
		cfg.MaxPageSize = defaultMaxPageSize
	}
	if cfg.MaxRejections <= 0 {
		cfg.MaxRejections = defaultMaxRejections
	}
	return &Service{store: store, reservations: cfg.Reservations, cfg: cfg}
}

// Overview returns the dashboard payload with the environment description
// attached.
func (s *Service) Overview(ctx context.Context) (domainadmin.Overview, error) {
	overview, err := s.store.Overview(ctx)
	if err != nil {
		return overview, err
	}
	overview.Environment = domainadmin.Environment{
		EmbeddingModel:      s.cfg.EmbeddingModel,
		EmbeddingDimensions: s.cfg.EmbeddingDimensions,
	}
	return overview, nil
}

// Restaurants returns one keyset page of restaurants.
func (s *Service) Restaurants(
	ctx context.Context, q domainadmin.RestaurantQuery,
) (domainadmin.RestaurantPage, error) {
	if q.Cursor != "" {
		afterID, err := decodeID(q.Cursor)
		if err != nil {
			return domainadmin.RestaurantPage{}, err
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
			return domainadmin.RestaurantPage{}, err
		}
		page.NextCursor = cursor
	}
	return page, nil
}

// RestaurantDetail returns one restaurant's full row.
func (s *Service) RestaurantDetail(
	ctx context.Context, restaurantID int64,
) (domainadmin.RestaurantDetail, error) {
	return s.store.RestaurantDetail(ctx, restaurantID)
}

// Reviews returns one keyset page of one restaurant's reviews.
func (s *Service) Reviews(
	ctx context.Context, q domainadmin.ReviewQuery,
) (domainadmin.ReviewPage, error) {
	if q.Cursor != "" {
		afterTime, afterID, err := decodeTimed(q.Cursor)
		if err != nil {
			return domainadmin.ReviewPage{}, err
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
			return domainadmin.ReviewPage{}, err
		}
		page.NextCursor = cursor
	}
	return page, nil
}

// Summaries returns one restaurant's per-topic review rollups.
func (s *Service) Summaries(
	ctx context.Context, restaurantID int64,
) ([]domainadmin.ReviewSummary, error) {
	return s.store.Summaries(ctx, restaurantID)
}

// Documents returns one keyset page of documents.
func (s *Service) Documents(
	ctx context.Context, q domainadmin.DocumentQuery,
) (domainadmin.DocumentPage, error) {
	if q.Cursor != "" {
		afterID, err := decodeID(q.Cursor)
		if err != nil {
			return domainadmin.DocumentPage{}, err
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
			return domainadmin.DocumentPage{}, err
		}
		page.NextCursor = cursor
	}
	return page, nil
}

// DocumentDetail returns one document. The vector preview is included only
// when requested.
func (s *Service) DocumentDetail(
	ctx context.Context, documentID int64, includeVectorPreview bool,
) (domainadmin.DocumentDetail, error) {
	return s.store.DocumentDetail(ctx, documentID, includeVectorPreview)
}

// DocumentsByRestaurant returns one restaurant's documents, active first.
func (s *Service) DocumentsByRestaurant(
	ctx context.Context, restaurantID int64,
) ([]domainadmin.DocumentSummary, error) {
	return s.store.DocumentsByRestaurant(ctx, restaurantID)
}

// Batches returns one keyset page of audit records.
func (s *Service) Batches(
	ctx context.Context, q domainadmin.BatchQuery,
) (domainadmin.BatchPage, error) {
	if q.Cursor != "" {
		afterTime, afterID, err := decodeTimed(q.Cursor)
		if err != nil {
			return domainadmin.BatchPage{}, err
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
			return domainadmin.BatchPage{}, err
		}
		page.NextCursor = cursor
	}
	return page, nil
}

// BatchDetail returns one audit record with its rejection breakdown, capped by
// the configured maximum.
func (s *Service) BatchDetail(
	ctx context.Context, batchID int64,
) (domainadmin.BatchDetail, error) {
	return s.store.BatchDetail(ctx, batchID, s.cfg.MaxRejections)
}

// Boundaries returns the administrative areas without geometry.
func (s *Service) Boundaries(ctx context.Context) ([]domainadmin.Boundary, error) {
	return s.store.Boundaries(ctx)
}

// Inventory renders one restaurant's bookable mock inventory: its slots for
// the scope and the reservations currently spending those seats. An empty
// date spans every date the store holds.
func (s *Service) Inventory(
	ctx context.Context, restaurantID int64, date string,
) (domainadmin.InventoryView, error) {
	view := domainadmin.InventoryView{
		RestaurantID: restaurantID,
		Date:         date,
		Slots:        []reservation.Slot{},
		Reservations: []reservation.Reservation{},
	}
	if s.reservations == nil {
		return view, errs.New(errs.CodeProviderUnavailable,
			"the reservation feature is not assembled in this deployment")
	}
	if err := validateInventoryDate(date); err != nil {
		return view, err
	}
	slots, err := s.reservations.ListSlots(ctx, restaurantID, date)
	if err != nil {
		return view, err
	}
	bookings, err := s.reservations.ListReservations(ctx, restaurantID, date)
	if err != nil {
		return view, err
	}
	if slots == nil {
		slots = []reservation.Slot{}
	}
	if bookings == nil {
		bookings = []reservation.Reservation{}
	}
	view.Slots = slots
	view.Reservations = bookings
	return view, nil
}

// ResetInventory clears the mock inventory so a demo can be replayed from the
// top: booked counts return to zero and the reservations that spent them are
// deleted. It is the console's one deliberate write, scoped to demo state.
func (s *Service) ResetInventory(
	ctx context.Context, restaurantID int64, date string,
) (domainadmin.InventoryResetResult, error) {
	result := domainadmin.InventoryResetResult{RestaurantID: restaurantID, Date: date}
	if s.reservations == nil {
		return result, errs.New(errs.CodeProviderUnavailable,
			"the reservation feature is not assembled in this deployment")
	}
	if err := validateInventoryDate(date); err != nil {
		return result, err
	}
	slotsReset, removed, err := s.reservations.ResetInventory(ctx, restaurantID, date)
	if err != nil {
		return result, err
	}
	result.SlotsReset = slotsReset
	result.ReservationsRemoved = removed
	return result, nil
}

// validateInventoryDate accepts either no scope at all or one ISO date. The
// store would silently return nothing for a malformed date, and a reset with
// a typo'd date reporting "0 slots reset" would read as success.
func validateInventoryDate(date string) error {
	if date == "" {
		return nil
	}
	if len(date) != 10 || date[4] != '-' || date[7] != '-' {
		return errs.Newf(errs.CodeInvalidArgument,
			"date must be an ISO date (YYYY-MM-DD) or empty, got %q", date)
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return errs.Newf(errs.CodeInvalidArgument,
			"date must be an ISO date (YYYY-MM-DD) or empty, got %q", date)
	}
	return nil
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
