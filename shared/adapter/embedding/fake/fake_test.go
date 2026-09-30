package fake

import (
	"context"
	"testing"

	"github.com/zed/platepilot/shared/domain/errs"
)

func TestIsDeterministic(t *testing.T) {
	p := New(64)
	ctx := context.Background()

	first, err := p.EmbedDocuments(ctx, []string{"pizza", "sushi"})
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	second, err := p.EmbedDocuments(ctx, []string{"pizza", "sushi"})
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	for i := range first {
		for j := range first[i] {
			if first[i][j] != second[i][j] {
				t.Fatalf("vector %d component %d differs between runs", i, j)
			}
		}
	}
}

func TestDifferentTextsDiverge(t *testing.T) {
	p := New(64)
	got, err := p.EmbedDocuments(context.Background(), []string{"pizza", "pizza "})
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	identical := true
	for i := range got[0] {
		if got[0][i] != got[1][i] {
			identical = false
			break
		}
	}
	if identical {
		t.Error("two different inputs produced identical vectors")
	}
}

func TestOutputPassesTheQualityGate(t *testing.T) {
	p := New(256)
	got, err := p.EmbedDocuments(context.Background(), []string{"a", "a longer piece of text"})
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	for i, vec := range got {
		if len(vec) != 256 {
			t.Fatalf("vector %d has %d dims, want 256", i, len(vec))
		}
		if err := Normalize(vec); err != nil {
			t.Errorf("vector %d would be rejected by the quality gate: %v", i, err)
		}
	}
}

// An empty document is a real edge case the pipeline must survive, and the fake
// reproduces it faithfully: all zeroes, which is exactly what the quality gate
// is there to catch.
func TestEmptyTextYieldsAZeroVector(t *testing.T) {
	p := New(16)
	got, err := p.EmbedDocuments(context.Background(), []string{""})
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	if err := Normalize(got[0]); errs.CodeOf(err) != errs.CodeEmbeddingZeroVector {
		t.Errorf("code = %q, want %q", errs.CodeOf(err), errs.CodeEmbeddingZeroVector)
	}
}

func TestEmptyBatchIsNotAnError(t *testing.T) {
	p := New(8)
	got, err := p.EmbedDocuments(context.Background(), nil)
	if err != nil {
		t.Fatalf("EmbedDocuments(nil): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d vectors, want 0", len(got))
	}
}

func TestEmbedQueryMatchesTheDocumentPath(t *testing.T) {
	p := New(64)
	ctx := context.Background()
	query, err := p.EmbedQuery(ctx, "quiet italian")
	if err != nil {
		t.Fatalf("EmbedQuery: %v", err)
	}
	docs, err := p.EmbedDocuments(ctx, []string{"quiet italian"})
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	for i := range query {
		if query[i] != docs[0][i] {
			t.Fatalf("query and document vectors diverge at %d", i)
		}
	}
}

func TestFailingProviderPropagates(t *testing.T) {
	sentinel := errs.New(errs.CodeProviderUnavailable, "down")
	p := Failing(sentinel)
	if _, err := p.EmbedDocuments(context.Background(), []string{"a"}); err == nil {
		t.Fatal("want error, got nil")
	}
}

func TestRespectsContextCancellation(t *testing.T) {
	p := New(8)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.EmbedDocuments(ctx, []string{"a"}); err == nil {
		t.Fatal("want error, got nil")
	}
}

func TestNewClampsDimensions(t *testing.T) {
	for _, in := range []int{0, -1} {
		if got := New(in).Dimensions(); got != DefaultDimensions {
			t.Errorf("New(%d).Dimensions() = %d, want %d", in, got, DefaultDimensions)
		}
	}
}
