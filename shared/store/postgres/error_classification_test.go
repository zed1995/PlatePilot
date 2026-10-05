package postgres

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/zed1995/platepilot/shared/domain/errs"
)

// TestOperationErrorClassifiesSchemaDrift pins the mapping that decides whether
// a schema mismatch reads as an outage.
//
// This is the failure it was written for. The database was two migrations
// behind, so a tool-call insert named a column that did not exist yet. The
// audit path logged and swallowed the write, and the read path answered
// 502 provider_unavailable — "the database is down" for a database that was up
// and answering every other query. The two symptoms looked unrelated precisely
// because the code hid the one thing they had in common, so the mapping is
// asserted here rather than left to whichever SQLSTATE happens to be seen next.
func TestOperationErrorClassifiesSchemaDrift(t *testing.T) {
	cases := []struct {
		sqlstate string
		what     string
	}{
		{"42P01", "undefined_table"},
		{"42703", "undefined_column"},
		{"42883", "undefined_function"},
		{"42704", "undefined_object"},
		{"3F000", "invalid_schema_name"},
		{"23502", "not_null_violation"},
	}
	for _, tc := range cases {
		t.Run(tc.what, func(t *testing.T) {
			err := operationError("postgres: test", &pgconn.PgError{Code: tc.sqlstate, Message: "synthetic"})
			if got := errs.CodeOf(err); got != errs.CodeInvalidArgument {
				t.Errorf("SQLSTATE %s (%s) maps to %q, want %q; a schema mismatch "+
					"reported as %q sends an operator to look for an outage that is not there",
					tc.sqlstate, tc.what, got, errs.CodeInvalidArgument, got)
			}
			if status := errs.HTTPStatusOf(err); status != 400 {
				t.Errorf("SQLSTATE %s (%s) maps to HTTP %d, want 400", tc.sqlstate, tc.what, status)
			}
			// The hint is the whole value of the code: the caller has to be
			// able to tell that the fix is a migration, not a retry.
			if !strings.Contains(err.Error(), "run migrate") &&
				!strings.Contains(err.Error(), "roll the schema back") {
				t.Errorf("SQLSTATE %s (%s) message %q names neither `run migrate` nor a "+
					"schema rollback; the caller cannot act on it", tc.sqlstate, tc.what, err.Error())
			}
		})
	}
}

// TestOperationErrorNamesTheMissingColumnOnNotNull covers the one case where the
// SQLSTATE alone does not identify the problem: which column was not supplied is
// the entire diagnosis, and pgErr.ColumnName is the only place it lives.
func TestOperationErrorNamesTheMissingColumnOnNotNull(t *testing.T) {
	err := operationError("postgres: record tool call", &pgconn.PgError{
		Code:       "23502",
		ColumnName: "seq",
		Message:    `null value in column "seq" violates not-null constraint`,
	})
	if !strings.Contains(err.Error(), "seq") {
		t.Errorf("message %q does not name the column; the SQLSTATE says only that "+
			"some column was null", err.Error())
	}
}

// TestOperationErrorKeepsGenuineOutagesAsProviderUnavailable guards the other
// direction. Widening the drift family is the obvious way to fix the case above
// and the obvious way to break this one: a real outage turned into a 400 tells
// the caller to fix a request that was never wrong.
func TestOperationErrorKeepsGenuineOutagesAsProviderUnavailable(t *testing.T) {
	cases := []struct {
		sqlstate string
		what     string
		want     errs.Code
	}{
		{"08006", "connection_failure", errs.CodeProviderUnavailable},
		{"53300", "too_many_connections", errs.CodeProviderUnavailable},
		{"57014", "query_canceled", errs.CodeProviderTimeout},
		{"XX000", "unclassified_internal_error", errs.CodeProviderUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.what, func(t *testing.T) {
			err := operationError("postgres: test", &pgconn.PgError{Code: tc.sqlstate, Message: "synthetic"})
			if got := errs.CodeOf(err); got != tc.want {
				t.Errorf("SQLSTATE %s (%s) maps to %q, want %q", tc.sqlstate, tc.what, got, tc.want)
			}
		})
	}
}

// TestOperationErrorKeepsTheCauseUnderTheDomainError is what makes the log
// useful at all: the envelope hides the cause from clients, so if the wrap
// dropped it the operator would see a code and no detail.
func TestOperationErrorKeepsTheCauseUnderTheDomainError(t *testing.T) {
	cause := &pgconn.PgError{Code: "42703", Message: `column "seq" does not exist`}
	err := operationError("postgres: record tool call", cause)

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("the pg error is not reachable through the domain error: %v", err)
	}
	if pgErr.Message != cause.Message {
		t.Errorf("cause message is %q, want %q", pgErr.Message, cause.Message)
	}
	if !errors.Is(err, errs.ErrInvalidArgument) {
		t.Error("errors.Is(err, errs.ErrInvalidArgument) is false; the sentinel no longer matches the code")
	}
}
