package rerank

import (
	"context"

	"github.com/zed1995/platepilot/shared/domain/search"
)

// RerankProvider optionally reorders candidates. The system must work without
// one: when no RerankProvider is configured, fusion order is used as-is.
type RerankProvider interface {
	ModelID() string
	Rerank(ctx context.Context, query string, candidates []search.RestaurantCandidate) ([]search.RestaurantCandidate, error)
}
