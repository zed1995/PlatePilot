// Package fake provides a deterministic EmbeddingProvider for tests and for
// running the pipeline without a local model.
//
// The vectors are a pure function of the input text, so the same corpus always
// produces the same vectors. That property is what lets the pipeline tests
// assert on retrieval outcomes at all: with a random provider, a passing test
// would only mean the run happened to be lucky.
package fake

import (
	"context"
	"math"

	"github.com/zed/platepilot/shared/domain/errs"
)

// DefaultDimensions matches the production model width so a test that
// accidentally mixes the fake with the real adapter still fails loudly on
// dimension rather than silently.
const DefaultDimensions = 1024

// Provider is a deterministic embedding.EmbeddingProvider.
type Provider struct {
	dimensions int
	// failWith, when set, is returned by every call. It lets a test exercise
	// the worker's provider-failure path without a server.
	failWith error
}

// New returns a fake provider producing vectors of the given width. A
// non-positive width falls back to DefaultDimensions.
func New(dimensions int) *Provider {
	if dimensions <= 0 {
		dimensions = DefaultDimensions
	}
	return &Provider{dimensions: dimensions}
}

// Failing returns a provider whose every call fails with err, so callers can
// test retry and abort behaviour.
func Failing(err error) *Provider {
	return &Provider{dimensions: DefaultDimensions, failWith: err}
}

// compile-time proof that the fake satisfies the port.
var _ interface {
	ModelID() string
	Dimensions() int
	EmbedDocuments(ctx context.Context, docs []string) ([][]float32, error)
	EmbedQuery(ctx context.Context, query string) ([]float32, error)
} = (*Provider)(nil)

// ModelID returns a stable fake model id.
func (p *Provider) ModelID() string { return "fake-embedding-v1" }

// Dimensions returns the configured vector width.
func (p *Provider) Dimensions() int { return p.dimensions }

// EmbedDocuments returns one deterministic vector per input, in order.
func (p *Provider) EmbedDocuments(ctx context.Context, docs []string) ([][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(errs.CodeProviderTimeout, "fake: cancelled", err)
	}
	if p.failWith != nil {
		return nil, p.failWith
	}
	if len(docs) == 0 {
		return [][]float32{}, nil
	}
	out := make([][]float32, len(docs))
	for i, doc := range docs {
		out[i] = vector(doc, p.dimensions)
	}
	return out, nil
}

// EmbedQuery returns the same vector EmbedDocuments would produce for query.
func (p *Provider) EmbedQuery(ctx context.Context, query string) ([]float32, error) {
	vecs, err := p.EmbedDocuments(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

// vector maps text to a vector of the given width.
//
// The value is sin(h), scaled into [-1, 1] so it stays inside float32 range.
// h mixes the byte content with the width so that two texts differing only in
// length still land far apart, and identical text always lands identically.
//
// A non-empty input never yields a zero vector: every component is a sine, and
// an all-zero result would look like the corrupted vector M2-08 exists to
// reject, which would make the fake useless for testing that path. An empty
// input deliberately does yield zeroes, because that is a real edge case the
// pipeline must handle.
func vector(text string, width int) []float32 {
	out := make([]float32, width)
	if text == "" {
		return out
	}
	var h uint32 = 2166136261 // FNV-1a offset basis
	for i := 0; i < width; i++ {
		h ^= uint32(text[i%len(text)])
		h *= 16777619
		out[i] = float32(math.Sin(float64(h%10000)) / 2)
	}
	return out
}

// Normalize reports whether a vector is finite and non-zero, mirroring the
// checks the pipeline applies. Tests use it to assert the fake's output would
// pass the quality gate.
func Normalize(v []float32) error {
	if len(v) == 0 {
		return errs.New(errs.CodeEmbeddingEmpty, "empty vector")
	}
	nonZero := false
	for _, x := range v {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return errs.Newf(errs.CodeEmbeddingNaN, "non-finite component %v", x)
		}
		if math.Abs(float64(x)) > 1e-12 {
			nonZero = true
		}
	}
	if !nonZero {
		return errs.New(errs.CodeEmbeddingZeroVector, "zero vector")
	}
	return nil
}
