package embedding

import "context"

// EmbeddingProvider turns text into vectors. It is deliberately separate from
// ChatProvider: the chat model and the embedding model are chosen and deployed
// independently.
type EmbeddingProvider interface {
	// ModelID identifies the embedding model, e.g. "qwen3-embedding:0.6b".
	ModelID() string
	// Dimensions is the fixed vector length, e.g. 1024 for qwen3-embedding.
	Dimensions() int
	EmbedDocuments(ctx context.Context, docs []string) ([][]float32, error)
	EmbedQuery(ctx context.Context, query string) ([]float32, error)
}
