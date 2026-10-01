package postgres

import (
	"strings"
	"testing"

	"github.com/zed/platepilot/shared/domain/evidence"
	"github.com/zed/platepilot/shared/store"
)

func TestVectorSearchStatementIsMinimalWhenUnfiltered(t *testing.T) {
	statement, args := buildVectorSearch(store.VectorSearchRequest{
		Scope: evidence.ScopeRestaurant,
		Query: []float32{1, 0, 0},
		TopK:  10,
	})
	// scope + query vector
	assertPlaceholdersContiguous(t, statement, len(args))
	if len(args) != 2 {
		t.Fatalf("want scope and vector only, got %d args", len(args))
	}
	if !strings.Contains(statement, "ORDER BY embedding <=> ") {
		t.Fatalf("the statement must order by vector distance:\n%s", statement)
	}
}

func TestVectorSearchNumbersEveryFilterItAppends(t *testing.T) {
	cases := []struct {
		name string
		req  store.VectorSearchRequest
		args int
		want []string
	}{
		{
			name: "borough",
			req: store.VectorSearchRequest{
				Scope: evidence.ScopeRestaurant, Query: []float32{1}, TopK: 5, Borough: "manhattan",
			},
			args: 3,
			want: []string{"borough = $"},
		},
		{
			name: "restaurants",
			req: store.VectorSearchRequest{
				Scope: evidence.ScopeEvidence, Query: []float32{1}, TopK: 5,
				RestaurantIDs: []int64{1, 2},
			},
			args: 3,
			want: []string{"restaurant_id = ANY($"},
		},
		{
			name: "doc types",
			req: store.VectorSearchRequest{
				Scope: evidence.ScopeRestaurant, Query: []float32{1}, TopK: 5,
				DocTypes: []evidence.DocType{evidence.DocTypeRestaurantProfile},
			},
			args: 3,
			want: []string{"doc_type = ANY($"},
		},
		{
			// A topic costs two placeholders: the doc type guard and the topic
			// itself. Forgetting the guard would let a topic match a document that
			// merely happens to carry the same metadata key.
			name: "topic",
			req: store.VectorSearchRequest{
				Scope: evidence.ScopeEvidence, Query: []float32{1}, TopK: 5,
				RestaurantIDs: []int64{1}, Topic: "wait",
			},
			args: 5,
			want: []string{"metadata->>'topic' = $"},
		},
		{
			name: "everything at once",
			req: store.VectorSearchRequest{
				Scope: evidence.ScopeEvidence, Query: []float32{1}, TopK: 5,
				Borough: "brooklyn", RestaurantIDs: []int64{1, 2},
				DocTypes: []evidence.DocType{evidence.DocTypeRestaurantReviewSummary},
				Topic:    "food",
			},
			args: 7,
			want: []string{"borough = $", "restaurant_id = ANY($", "doc_type = ANY($"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			statement, args := buildVectorSearch(tc.req)
			assertPlaceholdersContiguous(t, statement, len(args))
			if len(args) != tc.args {
				t.Fatalf("want %d args, got %d:\n%s", tc.args, len(args), statement)
			}
			for _, want := range tc.want {
				if !strings.Contains(statement, want) {
					t.Fatalf("statement is missing %q:\n%s", want, statement)
				}
			}
		})
	}
}

// An empty restaurant list means "no restriction". Rendering it as
// `= ANY('{}')` would match nothing and return an empty result that looks
// exactly like a restaurant with no documents.
func TestVectorSearchOmitsAnEmptyRestaurantFilter(t *testing.T) {
	statement, args := buildVectorSearch(store.VectorSearchRequest{
		Scope: evidence.ScopeRestaurant, Query: []float32{1}, TopK: 5,
		RestaurantIDs: []int64{},
	})
	if strings.Contains(statement, "restaurant_id = ANY") {
		t.Fatalf("an empty slice must not become a predicate:\n%s", statement)
	}
	assertPlaceholdersContiguous(t, statement, len(args))
}

// The vector is bound once and referenced twice. Two copies of a 1024-dimension
// literal would be roughly 20 KB of protocol traffic per recall.
func TestVectorSearchBindsTheVectorOnce(t *testing.T) {
	statement, _ := buildVectorSearch(store.VectorSearchRequest{
		Scope: evidence.ScopeRestaurant, Query: []float32{1, 2, 3}, TopK: 5,
	})
	if got := strings.Count(statement, "embedding <=>"); got != 2 {
		t.Fatalf("the vector operator appears %d times, want 2 (projection and order):\n%s",
			got, statement)
	}
	// One placeholder must therefore serve both occurrences.
	vectorParams := 0
	for _, match := range placeholderPattern.FindAllStringSubmatch(statement, -1) {
		if match[1] == "2" {
			vectorParams++
		}
	}
	if vectorParams != 2 {
		t.Fatalf("both vector references must share one placeholder, found %d", vectorParams)
	}
}

// The limit is interpolated, not bound, because a bound LIMIT makes PostgreSQL
// infer the type from context. It must still be a literal this file wrote.
func TestVectorSearchInterpolatesOnlyItsOwnLimit(t *testing.T) {
	statement, _ := buildVectorSearch(store.VectorSearchRequest{
		Scope: evidence.ScopeRestaurant, Query: []float32{1}, TopK: 17,
	})
	if !strings.Contains(statement, "LIMIT 17") {
		t.Fatalf("the limit must appear as an integer literal:\n%s", statement)
	}
	if strings.Contains(statement, "LIMIT $") {
		t.Fatalf("a bound limit forces type inference:\n%s", statement)
	}
}

func TestValidateVectorRequestRequiresAnExplicitScope(t *testing.T) {
	err := validateVectorRequest(store.VectorSearchRequest{
		Query: []float32{1}, TopK: 5,
	})
	if err == nil {
		t.Fatal("a recall without a scope could mix profiles with evidence")
	}
	if !strings.Contains(err.Error(), "scope") {
		t.Fatalf("the error should name what is missing: %v", err)
	}
}

// The cross-restaurant leak is the failure this whole layer exists to prevent,
// so the guard is asserted at the store, not only at the service.
func TestValidateVectorRequestRefusesAnUnscopedEvidenceRecall(t *testing.T) {
	err := validateVectorRequest(store.VectorSearchRequest{
		Scope: evidence.ScopeEvidence, Query: []float32{1}, TopK: 5,
	})
	if err == nil {
		t.Fatal("evidence recall without restaurants must be refused")
	}
	if !strings.Contains(err.Error(), "restaurants") {
		t.Fatalf("the error should say what is missing: %v", err)
	}
}

// A restaurant-scope recall legitimately has no restaurant filter: its whole
// job is to find the restaurants in the first place.
func TestValidateVectorRequestAllowsUnscopedRestaurantRecall(t *testing.T) {
	if err := validateVectorRequest(store.VectorSearchRequest{
		Scope: evidence.ScopeRestaurant, Query: []float32{1}, TopK: 5,
	}); err != nil {
		t.Fatalf("a restaurant recall needs no restaurant filter: %v", err)
	}
}

func TestValidateVectorRequestRejectsEmptyInput(t *testing.T) {
	cases := map[string]store.VectorSearchRequest{
		"no vector": {Scope: evidence.ScopeRestaurant, TopK: 5},
		"no top_k":  {Scope: evidence.ScopeRestaurant, Query: []float32{1}},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validateVectorRequest(req); err == nil {
				t.Fatalf("%s must be rejected", name)
			}
		})
	}
}

// A distance above 2 means the vectors are not normalised; the similarity must
// still land in [0,1] rather than going negative and ranking below a
// non-matching candidate.
func TestSimilarityClampsToTheUnitRange(t *testing.T) {
	cases := map[float64]float64{
		0:    1,
		0.25: 0.75,
		1:    0,
		2:    0,
		2.4:  0,
		-0.5: 1,
	}
	for distance, want := range cases {
		if got := similarityOf(distance); got != want {
			t.Errorf("similarityOf(%v) = %v, want %v", distance, got, want)
		}
	}
}

// The borough has to reach the statement as a column test. Naming it inside a
// metadata expression would stop the planner from recognising the partial index
// and silently restore the sequential scan.
func TestBoroughReachesTheStatementAsAColumnPredicate(t *testing.T) {
	statement, _ := buildVectorSearch(store.VectorSearchRequest{
		Scope: evidence.ScopeRestaurant, Query: []float32{1}, TopK: 5, Borough: "manhattan",
	})
	if strings.Contains(statement, "metadata->>'borough'") {
		t.Fatalf("a metadata borough predicate cannot select the index:\n%s", statement)
	}
	if !strings.Contains(statement, "borough = $") {
		t.Fatalf("the borough column predicate is missing:\n%s", statement)
	}
}
