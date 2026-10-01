package memory_test

import (
	"testing"

	"github.com/zed/platepilot/shared/store/contract"
	"github.com/zed/platepilot/shared/store/memory"
)

// TestMemoryStoresSatisfyContract proves the in-memory write stores honour the
// same behaviour contract the Postgres adapter must satisfy.
func TestMemoryStoresSatisfyContract(t *testing.T) {
	contract.Run(t, func(t *testing.T) contract.Stores {
		return contract.Stores{
			Restaurants: memory.NewRestaurantStore(),
			Reviews:     memory.NewReviewStore(),
			Pipeline:    memory.NewPipelineStore(),
			Knowledge:   memory.NewKnowledgeStore(),
		}
	})
}
