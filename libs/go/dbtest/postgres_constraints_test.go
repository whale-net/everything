//go:build integration

// This file only builds under the "integration" build tag so that `bazel
// test //...` (which builds and runs the whole tree, including on
// Docker-less machines) never even compiles it, let alone runs it. See the
// integration_test go_test target's gotags in BUILD.bazel and the package
// README for how to run it.
package dbtest_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/whale-net/everything/libs/go/dbtest"
)

// schema is deliberately self-contained (no dependency on App Registry,
// which lives on an unmerged branch). It mirrors the two guarantees App
// Registry actually needs from Postgres:
//
//  1. a plain UNIQUE index rejecting a duplicate (owner, kind, version)
//  2. a PARTIAL unique index that only enforces uniqueness among rows
//     where valid_to IS NULL (SCD2 "current row" convention, see AGENTS.md)
const schema = `
CREATE TABLE app_version (
	id      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	owner   TEXT NOT NULL,
	kind    TEXT NOT NULL,
	version TEXT NOT NULL,
	UNIQUE (owner, kind, version)
);

CREATE TABLE app_current (
	id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
	app_id     TEXT NOT NULL,
	valid_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	valid_to   TIMESTAMPTZ
);

CREATE UNIQUE INDEX app_current_one_row_per_app
	ON app_current (app_id)
	WHERE valid_to IS NULL;
`

// isUniqueViolation reports whether err is Postgres SQLSTATE 23505
// (unique_violation) -- see https://www.postgresql.org/docs/current/errcodes-appendix.html.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

func TestUniqueConstraint_RejectsDuplicateOwnerKindVersion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: schema})

	const insert = `INSERT INTO app_version (owner, kind, version) VALUES ($1, $2, $3)`

	if _, err := db.Pool.Exec(ctx, insert, "whale-net", "server", "1.0.0"); err != nil {
		t.Fatalf("first insert should succeed: %v", err)
	}

	_, err := db.Pool.Exec(ctx, insert, "whale-net", "server", "1.0.0")
	if err == nil {
		t.Fatal("duplicate (owner, kind, version) insert succeeded; the UNIQUE constraint did not fire")
	}
	if !isUniqueViolation(err) {
		t.Fatalf("expected a unique_violation (23505), got: %v", err)
	}
	t.Logf("got expected unique_violation: %v", err)

	// A different version for the same (owner, kind) is a distinct key and
	// must be allowed -- proves the constraint isn't overly broad.
	if _, err := db.Pool.Exec(ctx, insert, "whale-net", "server", "1.0.1"); err != nil {
		t.Fatalf("insert with a different version should succeed: %v", err)
	}
}

func TestPartialUniqueIndex_OnlyGuardsCurrentRow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: schema})

	const insertCurrent = `INSERT INTO app_current (app_id, valid_to) VALUES ($1, NULL)`
	const insertHistorical = `INSERT INTO app_current (app_id, valid_to) VALUES ($1, NOW())`

	// Two historical (closed) rows for the same app_id are fine: valid_to
	// IS NOT NULL puts them outside the partial index.
	if _, err := db.Pool.Exec(ctx, insertHistorical, "app-1"); err != nil {
		t.Fatalf("first historical row should succeed: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, insertHistorical, "app-1"); err != nil {
		t.Fatalf("second historical row should succeed (partial index excludes closed rows): %v", err)
	}

	// The first "current" (valid_to IS NULL) row for app-1 is fine.
	if _, err := db.Pool.Exec(ctx, insertCurrent, "app-1"); err != nil {
		t.Fatalf("first current row should succeed: %v", err)
	}

	// A second "current" row for the same app_id must be rejected -- this
	// is exactly the guarantee a NULLable-column, non-partial index (or an
	// in-memory fake) cannot express or verify.
	_, err := db.Pool.Exec(ctx, insertCurrent, "app-1")
	if err == nil {
		t.Fatal("second current (valid_to IS NULL) row for the same app_id succeeded; the partial unique index did not fire")
	}
	if !isUniqueViolation(err) {
		t.Fatalf("expected a unique_violation (23505), got: %v", err)
	}
	t.Logf("got expected unique_violation: %v", err)

	// A current row for a *different* app_id must still be allowed.
	if _, err := db.Pool.Exec(ctx, insertCurrent, "app-2"); err != nil {
		t.Fatalf("current row for a different app_id should succeed: %v", err)
	}
}
