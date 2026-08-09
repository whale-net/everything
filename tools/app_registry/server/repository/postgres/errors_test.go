package postgres

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// TestTranslatePgError_MapsEachSQLSTATE is the direct, mechanism-level test
// for the central translation point Fix 1 requires: every write path in this
// package must route Postgres integrity-constraint violations through
// translatePgError rather than returning the raw pgx error, so a caller's
// retry logic (see AR-5's AllocateVersion) can tell "someone else took this,
// retry" apart from "the server is broken."
//
// This uses errors.As against a synthetic *pgconn.PgError deliberately —
// asserting the code matches on the wrapped SQLSTATE, never on message text,
// proves the implementation isn't secretly string-matching.
func TestTranslatePgError_MapsEachSQLSTATE(t *testing.T) {
	cases := []struct {
		name    string
		code    string
		wantErr error
	}{
		{"unique_violation", sqlStateUniqueViolation, repository.ErrAlreadyExists},
		{"check_violation", sqlStateCheckViolation, repository.ErrInvalidArgument},
		{"foreign_key_violation", sqlStateForeignKeyViolation, repository.ErrFailedPrecondition},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pgErr := &pgconn.PgError{
				Code:           tc.code,
				Message:        "duplicate key value violates unique constraint \"artifact_version_idx\"",
				ConstraintName: "artifact_version_idx",
			}
			// Wrap the way pgx actually returns it, to prove errors.As sees
			// through wrapping rather than requiring an exact type match.
			wrapped := fmt.Errorf("exec failed: %w", pgErr)

			domainErr, ok := translatePgError(wrapped, "artifact demo-svc v1.2.3 already recorded")
			if !ok {
				t.Fatalf("expected translatePgError to recognize SQLSTATE %s", tc.code)
			}
			if !errors.Is(domainErr, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, domainErr)
			}
			// The index name and SQLSTATE must never reach the message.
			got := domainErr.Error()
			for _, leak := range []string{"artifact_version_idx", tc.code, "SQLSTATE"} {
				if strings.Contains(got, leak) {
					t.Fatalf("domain error leaks database internals (%q): %q", leak, got)
				}
			}
		})
	}
}

// TestTranslatePgError_PassesThroughNonIntegrityErrors proves a non-pgError
// (or a pgError with an unmapped code) is left for the caller's generic
// Internal-error wrap, rather than being silently swallowed or misclassified.
func TestTranslatePgError_PassesThroughNonIntegrityErrors(t *testing.T) {
	if _, ok := translatePgError(errors.New("connection reset"), "irrelevant"); ok {
		t.Fatal("expected a non-pgError to be left untranslated")
	}

	other := &pgconn.PgError{Code: "40001"} // serialization_failure — not one of ours
	if _, ok := translatePgError(other, "irrelevant"); ok {
		t.Fatal("expected an unmapped SQLSTATE to be left untranslated")
	}
}
