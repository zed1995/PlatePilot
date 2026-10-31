package testkit

import (
	"context"
	"testing"

	"github.com/zed1995/platepilot/shared/domain/search"
)

func TestMockEmbeddingProviderIsDeterministicAndSized(t *testing.T) {
	provider := &MockEmbeddingProvider{Dim: 16}
	first, err := provider.EmbedQuery(context.Background(), "pizza")
	if err != nil {
		t.Fatalf("embed query: %v", err)
	}
	if len(first) != 16 {
		t.Fatalf("dimension = %d, want 16", len(first))
	}
	if provider.Dimensions() != 16 {
		t.Fatalf("Dimensions() = %d", provider.Dimensions())
	}
	second, _ := provider.EmbedQuery(context.Background(), "pizza")
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("vectors must be deterministic at index %d", i)
		}
	}
	if len(provider.QueryCalls) != 2 {
		t.Fatalf("query calls = %d", len(provider.QueryCalls))
	}
}

func TestMockEmbeddingProviderDefaultDimension(t *testing.T) {
	provider := &MockEmbeddingProvider{}
	if provider.Dimensions() != 8 {
		t.Fatalf("default dimension = %d, want 8", provider.Dimensions())
	}
	if provider.ModelID() != "mock-embedding" {
		t.Fatalf("default model = %q", provider.ModelID())
	}
}

func TestMockRerankProviderReverses(t *testing.T) {
	provider := &MockRerankProvider{}
	in := []search.RestaurantCandidate{{RestaurantID: 1}, {RestaurantID: 2}, {RestaurantID: 3}}
	out, err := provider.Rerank(context.Background(), "quiet", in)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if out[0].RestaurantID != 3 || out[2].RestaurantID != 1 {
		t.Fatalf("unexpected order: %+v", out)
	}
	if provider.Calls != 1 {
		t.Fatalf("calls = %d", provider.Calls)
	}
}
