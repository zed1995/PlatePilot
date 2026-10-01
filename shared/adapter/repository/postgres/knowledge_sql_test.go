package postgres

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/zed/platepilot/shared/domain/evidence"
	"github.com/zed/platepilot/shared/port"
)

// placeholderPattern finds every $n the statement references.
var placeholderPattern = regexp.MustCompile(`\$(\d+)`)

// assertPlaceholdersContiguous is the assertion that keeps optional filters
// honest, shared by every statement builder in this package.
//
// It checks both directions. A placeholder the statement never references makes
// PostgreSQL fail with "could not determine data type of parameter $2", and a
// bound parameter the statement never mentions is the same error from the other
// side. Neither names the filter that got skipped, so both have to be asserted
// rather than inferred from a passing query.
func assertPlaceholdersContiguous(t *testing.T, statement string, bound int) {
	t.Helper()
	seen := map[int]bool{}
	for _, match := range placeholderPattern.FindAllStringSubmatch(statement, -1) {
		n, err := strconv.Atoi(match[1])
		if err != nil {
			t.Fatalf("bad placeholder %q", match[0])
		}
		if n < 1 || n > bound {
			t.Errorf("statement references $%d but only %d parameters are bound:\n%s",
				n, bound, statement)
		}
		seen[n] = true
	}
	for i := 1; i <= bound; i++ {
		if !seen[i] {
			t.Errorf("parameter $%d is bound but never referenced:\n%s", i, statement)
		}
	}
}

// TestVectorSearchStatementHasNoDanglingParameters is the assertion that keeps
// the optional filters honest.
//
// PostgreSQL rejects a statement whose bound parameters are not all referenced:
// it tries to infer a type for the unreferenced one and fails with "could not
// determine data type of parameter $n". That error names a number rather than
// the clause that should have used it, so it is slow to diagnose — which is
// exactly why it is worth a test that needs no database.
func TestVectorSearchStatementHasNoDanglingParameters(t *testing.T) {
	query := make([]float32, 4)
	cases := []struct {
		name   string
		filter port.VectorFilter
		want   int
	}{
		{"scope only", port.VectorFilter{}, 2},
		{"with borough", port.VectorFilter{Borough: "manhattan"}, 3},
		{"with restaurant", port.VectorFilter{RestaurantID: 7}, 3},
		{"with both", port.VectorFilter{Borough: "queens", RestaurantID: 7}, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			statement, args := vectorSearchStatement(evidence.ScopeEvidence, query, 10, tc.filter)
			if len(args) != tc.want {
				t.Errorf("bound %d parameters, want %d", len(args), tc.want)
			}
			assertPlaceholdersContiguous(t, statement, len(args))
		})
	}
}

// The scope and the vector must be the first two parameters in every variant,
// because the projection and the ORDER BY both reference the vector and the
// scope is the one clause that is never optional.
func TestVectorSearchStatementBindsScopeFirstAndVectorOnce(t *testing.T) {
	query := []float32{1, 0, 0, 0}
	statement, args := vectorSearchStatement(evidence.ScopeRestaurant, query, 5, port.VectorFilter{})

	if args[0] != string(evidence.ScopeRestaurant) {
		t.Errorf("first parameter = %v, want the scope", args[0])
	}
	if args[1] != vectorLiteral(query) {
		t.Errorf("second parameter = %v, want the query vector", args[1])
	}
	// The vector must appear exactly twice: once for the distance column and
	// once for the ordering. A third occurrence would mean the same value is
	// being sent again instead of referenced.
	if got := strings.Count(statement, "$2"); got != 2 {
		t.Errorf("query vector referenced %d times, want 2 (distance and order):\n%s", got, statement)
	}
}

// A caller-supplied value must never reach the SQL text. The top_k is the only
// interpolated value, and it is an int the caller range-checks.
func TestVectorSearchStatementInterpolatesOnlyTopK(t *testing.T) {
	statement, _ := vectorSearchStatement(evidence.ScopeEvidence,
		[]float32{1, 0}, 42, port.VectorFilter{Borough: "manhattan"})

	if !strings.Contains(statement, "LIMIT 42") {
		t.Errorf("top_k was not interpolated:\n%s", statement)
	}
	if strings.Contains(statement, "manhattan") {
		t.Errorf("the borough reached the SQL text instead of a bound parameter:\n%s", statement)
	}
	if strings.Contains(statement, "evidence") {
		t.Errorf("the scope reached the SQL text instead of a bound parameter:\n%s", statement)
	}
}

// The inactive filter has to be in the statement, not implied. Every partial
// index is WHERE is_active, so a missing predicate would both return
// superseded versions and change which index the planner can use.
func TestVectorSearchStatementFiltersInactiveAndUnembedded(t *testing.T) {
	statement, _ := vectorSearchStatement(evidence.ScopeEvidence, []float32{1}, 5, port.VectorFilter{})
	if !strings.Contains(statement, "is_active") {
		t.Errorf("statement does not filter on is_active:\n%s", statement)
	}
	if !strings.Contains(statement, "embedding IS NOT NULL") {
		t.Errorf("statement does not exclude unembedded rows:\n%s", statement)
	}
	// The cosine operator must match the vector_cosine_ops opclass the indexes
	// were built with; a different operator would silently stop using them.
	if !strings.Contains(statement, "<=>") {
		t.Errorf("statement does not order by cosine distance:\n%s", statement)
	}
}
