package knowledge

import (
	"math"
	"strings"
	"testing"

	"github.com/zed1995/platepilot/shared/domain/errs"
)

func TestCheckAcceptsAValidVector(t *testing.T) {
	v := make([]float32, 8)
	for i := range v {
		v[i] = float32(i) + 1
	}
	if err := Check(v, 8); err != nil {
		t.Errorf("Check = %v, want nil", err)
	}
}

func TestCheckRejectsEachFailureMode(t *testing.T) {
	base := func(n int) []float32 {
		v := make([]float32, n)
		for i := range v {
			v[i] = float32(i) + 1
		}
		return v
	}

	cases := []struct {
		name  string
		build func() []float32
		dim   int
		want  errs.Code
	}{
		{"empty", func() []float32 { return nil }, 8, errs.CodeEmbeddingEmpty},
		{"empty non-nil", func() []float32 { return []float32{} }, 8, errs.CodeEmbeddingEmpty},
		{"too short", func() []float32 { return base(4) }, 8, errs.CodeEmbeddingDimensionMismatch},
		{"too long", func() []float32 { return base(16) }, 8, errs.CodeEmbeddingDimensionMismatch},
		{"nan", func() []float32 {
			v := base(8)
			v[3] = float32(math.NaN())
			return v
		}, 8, errs.CodeEmbeddingNaN},
		{"inf", func() []float32 {
			v := base(8)
			v[0] = float32(math.Inf(1))
			return v
		}, 8, errs.CodeEmbeddingInf},
		{"negative inf", func() []float32 {
			v := base(8)
			v[7] = float32(math.Inf(-1))
			return v
		}, 8, errs.CodeEmbeddingInf},
		{"all zero", func() []float32 { return make([]float32, 8) }, 8, errs.CodeEmbeddingZeroVector},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Check(tc.build(), tc.dim)
			if got := errs.CodeOf(err); got != tc.want {
				t.Errorf("code = %q, want %q (err=%v)", got, tc.want, err)
			}
		})
	}
}

// A vector that is only *nearly* zero is still zero for cosine purposes: the
// components are far below the norm the model would produce.
func TestCheckRejectsNearZeroVectors(t *testing.T) {
	v := make([]float32, 8)
	for i := range v {
		v[i] = 1e-20
	}
	if got := errs.CodeOf(Check(v, 8)); got != errs.CodeEmbeddingZeroVector {
		t.Errorf("code = %q, want %q", got, errs.CodeEmbeddingZeroVector)
	}
}

// A vector with one real component is not zero.
func TestCheckAcceptsASparseVector(t *testing.T) {
	v := make([]float32, 8)
	v[4] = 1
	if err := Check(v, 8); err != nil {
		t.Errorf("Check = %v, want nil", err)
	}
}

// The dimension check runs first: a wrong-width vector must not be reported as
// a NaN problem, which would send an operator looking at the wrong place.
func TestCheckReportsDimensionBeforeScanning(t *testing.T) {
	v := []float32{float32(math.NaN()), 2}
	if got := errs.CodeOf(Check(v, 8)); got != errs.CodeEmbeddingDimensionMismatch {
		t.Errorf("code = %q, want %q", got, errs.CodeEmbeddingDimensionMismatch)
	}
}

func TestCheckNamesTheOffendingComponent(t *testing.T) {
	v := make([]float32, 8)
	for i := range v {
		v[i] = 1
	}
	v[5] = float32(math.NaN())
	err := Check(v, 8)
	if !strings.Contains(err.Error(), "5") {
		t.Errorf("error should name the component: %v", err)
	}
}

func TestCheckDimensionErrorStatesBothWidths(t *testing.T) {
	err := Check([]float32{1, 2}, 1024)
	if !strings.Contains(err.Error(), "2") || !strings.Contains(err.Error(), "1024") {
		t.Errorf("dimension error should state both widths: %v", err)
	}
}

// wantDim of zero means "do not check", so a caller that has no configuration
// can still run the finiteness and zero rules.
func TestCheckSkipsTheDimensionRuleWhenUnset(t *testing.T) {
	if err := Check([]float32{1, 2, 3}, 0); err != nil {
		t.Errorf("Check = %v, want nil", err)
	}
}

func TestFingerprintIsStableAndDiscriminating(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{1, 2, 3}
	c := []float32{1, 2, 3.5}
	if Fingerprint(a) != Fingerprint(b) {
		t.Error("identical vectors fingerprinted differently")
	}
	if Fingerprint(a) == Fingerprint(c) {
		t.Error("distinct vectors collided")
	}
	// Two float32 values that differ only below float32 precision are the same
	// value, and an exact fingerprint is supposed to treat them as one.
	d := []float32{1, 2, 3 + 1e-10}
	if Fingerprint(a) != Fingerprint(d) {
		t.Error("values identical as float32 must fingerprint identically")
	}
	if Fingerprint([]float32{1, 2}) == Fingerprint([]float32{1, 2, 0}) {
		t.Error("a length difference must change the fingerprint")
	}
}

// Exact equality is the whole point: near-duplicate text is legitimate and must
// not be rejected as a duplicate vector.
func TestFingerprintSeparatesSimilarButDistinctVectors(t *testing.T) {
	base := make([]float32, 64)
	for i := range base {
		base[i] = float32(i%5) + 0.1
	}
	altered := make([]float32, 64)
	copy(altered, base)
	altered[7] += 0.2

	if Fingerprint(base) == Fingerprint(altered) {
		t.Error("distinct vectors must not share a fingerprint")
	}
}

func TestFingerprintHandlesEmptyAndZero(t *testing.T) {
	if Fingerprint(nil) == "" {
		t.Error("fingerprint of an empty vector must still be a value")
	}
	zeros := make([]float32, 8)
	if Fingerprint(zeros) != Fingerprint(make([]float32, 8)) {
		t.Error("two zero vectors should fingerprint identically")
	}
}
