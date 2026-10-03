package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"strconv"
	"strings"

	"github.com/zed1995/platepilot/shared/domain/errs"
)

// zeroEpsilon is the magnitude below which a component counts as zero.
//
// It exists so that a vector of denormal-scale values is not mistaken for a
// zero vector: float32 arithmetic can leave components at 1e-20 rather than
// exactly 0, and an exact test would let those through.
const zeroEpsilon = 1e-12

// Check validates one vector before it is written.
//
// It is deliberately pure - no database, no model - so every rejection rule is
// unit-testable on its own, which is the only way to cover them exhaustively
// without arranging a pipeline run per case.
//
// The order matters: empty and dimension are structural and cheap, and checking
// them first means a wrong-width vector is never scanned 1024 times looking for
// a NaN that is not the real problem.
func Check(v []float32, wantDim int) error {
	if len(v) == 0 {
		return errs.New(errs.CodeEmbeddingEmpty, "embedding is empty")
	}
	if wantDim > 0 && len(v) != wantDim {
		return errs.Newf(errs.CodeEmbeddingDimensionMismatch,
			"embedding has %d dimensions, want %d", len(v), wantDim)
	}
	for i, x := range v {
		switch {
		case math.IsNaN(float64(x)):
			return errs.Newf(errs.CodeEmbeddingNaN, "embedding component %d is NaN", i)
		case math.IsInf(float64(x), 0):
			return errs.Newf(errs.CodeEmbeddingInf, "embedding component %d is Inf", i)
		}
	}
	// The zero check is last because it is the only one that has to look at
	// every component, and an all-zero vector is a statement about the whole
	// vector rather than one position in it.
	if isZero(v) {
		return errs.New(errs.CodeEmbeddingZeroVector, "embedding is a zero vector")
	}
	return nil
}

// isZero reports whether every component is below the epsilon.
//
// A zero vector is the failure mode worth guarding hardest. Cosine distance
// divides by the norm, so pgvector cannot order a zero vector against anything
// and simply omits the row from the result: the document is not degraded, it is
// silently unreachable, with no error anywhere to explain why.
func isZero(v []float32) bool {
	for _, x := range v {
		if math.Abs(float64(x)) > zeroEpsilon {
			return false
		}
	}
	return true
}

// Fingerprint identifies a vector for exact-duplicate detection.
//
// It is an exact digest, not an approximation. Near-duplicate detection was
// considered and rejected: two chunks from the same restaurant that genuinely
// read alike are legitimate, and treating them as duplicates would delete real
// evidence. Exact equality is the only condition where a repeat vector is
// certainly a provider or cache fault rather than similar text.
func Fingerprint(v []float32) string {
	var b strings.Builder
	b.Grow(len(v) * 12)
	for _, x := range v {
		// 'g' with -1 gives the shortest representation that round-trips, so
		// equal float32 values always produce equal text.
		b.WriteString(strconv.FormatFloat(float64(x), 'g', -1, 32))
		b.WriteByte(',')
	}
	return hashFingerprint(b.String())
}

// hashFingerprint digests the rendered vector text.
func hashFingerprint(rendered string) string {
	sum := sha256.Sum256([]byte(rendered))
	return "sha256:" + hex.EncodeToString(sum[:])
}
