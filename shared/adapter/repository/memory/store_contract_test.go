package memory_test

import (
	"testing"

	"github.com/zed/platepilot/shared/adapter/repository/contract"
	"github.com/zed/platepilot/shared/adapter/repository/memory"
)

// TestMemoryStoresSatisfyContract proves the in-memory write stores honour the
// same behaviour contract the Mongo adapter must satisfy.
func TestMemoryStoresSatisfyContract(t *testing.T) {
	contract.Run(t, func(t *testing.T) contract.Stores {
		return contract.Stores{
			Restaurants: memory.NewRestaurantStore(),
			Reviews:     memory.NewReviewStore(),
			Pipeline:    memory.NewPipelineStore(),
		}
	})
}
