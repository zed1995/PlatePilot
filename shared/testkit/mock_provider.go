// Package testkit provides deterministic mocks, fixtures, and in-memory
// repositories for tests across the codebase.
package testkit

import (
	"context"
	"hash/fnv"
	"sync"

	"github.com/zed1995/platepilot/shared/domain/search"
	"github.com/zed1995/platepilot/shared/embedding"
	"github.com/zed1995/platepilot/shared/rerank"
)

// Compile-time guarantees that the mocks satisfy the ports.
var (
	_ embedding.EmbeddingProvider = (*MockEmbeddingProvider)(nil)
	_ rerank.RerankProvider       = (*MockRerankProvider)(nil)
)

// MockEmbeddingProvider returns deterministic, dimension-stable vectors.
type MockEmbeddingProvider struct {
	mu sync.Mutex

	Model         string
	Dim           int
	Err           error
	QueryCalls    []string
	DocumentCalls [][]string
}

// ModelID returns the configured model, defaulting to "mock-embedding".
func (m *MockEmbeddingProvider) ModelID() string {
	if m.Model == "" {
		return "mock-embedding"
	}
	return m.Model
}

// Dimensions returns the configured dimension, defaulting to 8.
func (m *MockEmbeddingProvider) Dimensions() int {
	if m.Dim <= 0 {
		return 8
	}
	return m.Dim
}

// EmbedDocuments returns one deterministic vector per document.
func (m *MockEmbeddingProvider) EmbedDocuments(_ context.Context, docs []string) ([][]float32, error) {
	m.mu.Lock()
	m.DocumentCalls = append(m.DocumentCalls, append([]string(nil), docs...))
	err := m.Err
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	out := make([][]float32, len(docs))
	for i, doc := range docs {
		out[i] = vectorFor(doc, m.Dimensions())
	}
	return out, nil
}

// EmbedQuery returns a deterministic vector for query.
func (m *MockEmbeddingProvider) EmbedQuery(_ context.Context, query string) ([]float32, error) {
	m.mu.Lock()
	m.QueryCalls = append(m.QueryCalls, query)
	err := m.Err
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return vectorFor(query, m.Dimensions()), nil
}

// MockRerankProvider reverses candidate order to make reranking observable.
type MockRerankProvider struct {
	mu sync.Mutex

	Model string
	Err   error
	Calls int
}

// ModelID returns the configured model, defaulting to "mock-rerank".
func (m *MockRerankProvider) ModelID() string {
	if m.Model == "" {
		return "mock-rerank"
	}
	return m.Model
}

// Rerank reverses the candidates.
func (m *MockRerankProvider) Rerank(_ context.Context, _ string, candidates []search.RestaurantCandidate) ([]search.RestaurantCandidate, error) {
	m.mu.Lock()
	m.Calls++
	err := m.Err
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	out := make([]search.RestaurantCandidate, len(candidates))
	for i, candidate := range candidates {
		out[len(candidates)-1-i] = candidate
	}
	return out, nil
}

// vectorFor derives a stable unit-ish vector from text so tests are repeatable.
func vectorFor(text string, dim int) []float32 {
	if dim <= 0 {
		dim = 8
	}
	out := make([]float32, dim)
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(text))
	seed := hasher.Sum64()
	for i := range out {
		seed = seed*6364136223846793005 + 1442695040888963407
		out[i] = float32(int64(seed>>33)%1000) / 1000
	}
	return out
}
