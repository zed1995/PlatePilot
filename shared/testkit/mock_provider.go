// Package testkit provides deterministic mocks, fixtures, and in-memory
// repositories for tests across the codebase.
package testkit

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"io"
	"sync"

	chatport "github.com/zed1995/platepilot/shared/chat"
	"github.com/zed1995/platepilot/shared/domain/chat"
	"github.com/zed1995/platepilot/shared/domain/search"
	"github.com/zed1995/platepilot/shared/domain/tool"
	"github.com/zed1995/platepilot/shared/embedding"
	"github.com/zed1995/platepilot/shared/rerank"
)

// Compile-time guarantees that the mocks satisfy the ports.
var (
	_ chatport.ChatProvider             = (*MockChatProvider)(nil)
	_ chatport.ToolCallingProvider      = (*MockChatProvider)(nil)
	_ chatport.StructuredOutputProvider = (*MockChatProvider)(nil)
	_ embedding.EmbeddingProvider       = (*MockEmbeddingProvider)(nil)
	_ rerank.RerankProvider             = (*MockRerankProvider)(nil)
)

// MockChatProvider is a scriptable chatport.ChatProvider plus tool-calling and
// structured-output ports. Recorded calls make assertions easy.
type MockChatProvider struct {
	mu sync.Mutex

	Response           chat.ChatResponse
	ToolResponse       chat.ToolCallResponse
	StructuredResponse chat.StructuredResponse
	Chunks             []chat.ChatChunk
	Err                error

	Tools         bool
	ParallelTools bool
	JSONSchema    bool

	CompleteCalls   []chat.ChatRequest
	StreamCalls     []chat.ChatRequest
	ToolCalls       []chat.ChatRequest
	StructuredCalls []chat.StructuredRequest
}

// Complete returns the scripted response.
func (m *MockChatProvider) Complete(_ context.Context, req chat.ChatRequest) (chat.ChatResponse, error) {
	m.mu.Lock()
	m.CompleteCalls = append(m.CompleteCalls, req)
	err := m.Err
	resp := m.Response
	m.mu.Unlock()
	if err != nil {
		return chat.ChatResponse{}, err
	}
	return resp, nil
}

// Stream returns a stream over the scripted chunks.
func (m *MockChatProvider) Stream(_ context.Context, req chat.ChatRequest) (chatport.ChatStream, error) {
	m.mu.Lock()
	m.StreamCalls = append(m.StreamCalls, req)
	err := m.Err
	chunks := append([]chat.ChatChunk(nil), m.Chunks...)
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &sliceStream{chunks: chunks}, nil
}

// SupportsTools reports the scripted capability.
func (m *MockChatProvider) SupportsTools() bool { return m.Tools }

// SupportsParallelTools reports the scripted capability.
func (m *MockChatProvider) SupportsParallelTools() bool { return m.ParallelTools }

// ChatWithTools returns the scripted tool-call response.
func (m *MockChatProvider) ChatWithTools(_ context.Context, req chat.ChatRequest, _ []tool.ToolSpec) (chat.ToolCallResponse, error) {
	m.mu.Lock()
	m.ToolCalls = append(m.ToolCalls, req)
	err := m.Err
	resp := m.ToolResponse
	m.mu.Unlock()
	if err != nil {
		return chat.ToolCallResponse{}, err
	}
	return resp, nil
}

// SupportsJSONSchema reports the scripted capability.
func (m *MockChatProvider) SupportsJSONSchema() bool { return m.JSONSchema }

// CompleteStructured returns the scripted structured response.
func (m *MockChatProvider) CompleteStructured(_ context.Context, req chat.StructuredRequest, _ json.RawMessage) (chat.StructuredResponse, error) {
	m.mu.Lock()
	m.StructuredCalls = append(m.StructuredCalls, req)
	err := m.Err
	resp := m.StructuredResponse
	m.mu.Unlock()
	if err != nil {
		return chat.StructuredResponse{}, err
	}
	return resp, nil
}

// sliceStream is an in-memory chatport.ChatStream.
type sliceStream struct {
	mu     sync.Mutex
	chunks []chat.ChatChunk
	pos    int
	closed bool
}

func (s *sliceStream) Recv() (chat.ChatChunk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.pos >= len(s.chunks) {
		return chat.ChatChunk{}, io.EOF
	}
	chunk := s.chunks[s.pos]
	s.pos++
	return chunk, nil
}

func (s *sliceStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

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
