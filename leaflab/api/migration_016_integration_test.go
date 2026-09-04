//go:build integration

// This file only builds under the "integration" build tag, same as
// repository_integration_test.go in this package -- see that file's doc
// comment for why (Docker-less machines, `bazel test //...` never even
// compiling it).
//
// Schema here is hand-written, self-contained DDL mirroring exactly the
// pieces of migration 016 (leaflab/migrate/migrations/016_m2_ownership_rename.up.sql)
// under test: board.name, leaflab_user_role's SCD2 shape (including the
// partial unique index on the open interval), the seeded-admin INSERT, and
// the two sensor corrective-push columns. Per repository_integration_test.go's
// own precedent and dbtest's README ("Options.Schema should be
// self-contained DDL -- do not depend on another package's migrations"),
// this does not import leaflab/migrate's real migrations -- its embed.FS
// lives in package main there and isn't importable anyway.
package main

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/dbtest"
)

// migration016Schema mirrors 016_m2_ownership_rename.up.sql: board.name,
// leaflab_user (013_ownership.up.sql's shape, needed as leaflab_user_role's
// FK target), leaflab_user_role itself, and sensor's two new
// corrective-push columns. sensor_type/sensor are scoped down to only the
// columns migration 016 touches or that a sensor row requires NOT NULL.
const migration016Schema = `
	CREATE TABLE board (
		board_id      BIGSERIAL PRIMARY KEY,
		device_id     VARCHAR(64) NOT NULL UNIQUE,
		name          TEXT,
		registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	CREATE TABLE leaflab_user (
		leaflab_user_id     BIGSERIAL   PRIMARY KEY,
		oidc_sub            TEXT        NOT NULL UNIQUE,
		preferred_username  TEXT,
		email               TEXT,
		display_name        TEXT,
		created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_seen_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	CREATE TABLE leaflab_user_role (
		leaflab_user_role_id BIGSERIAL   PRIMARY KEY,
		leaflab_user_id      BIGINT      NOT NULL REFERENCES leaflab_user(leaflab_user_id) ON DELETE CASCADE,
		role                 TEXT        NOT NULL,
		valid_from           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to             TIMESTAMPTZ
	);

	CREATE INDEX idx_leaflab_user_role_user_id ON leaflab_user_role(leaflab_user_id);
	CREATE UNIQUE INDEX idx_leaflab_user_role_current
		ON leaflab_user_role(leaflab_user_id, role) WHERE valid_to IS NULL;

	CREATE TABLE sensor_type (
		sensor_type_id BIGSERIAL PRIMARY KEY,
		name           VARCHAR(64) NOT NULL UNIQUE,
		default_unit   VARCHAR(16) NOT NULL
	);

	CREATE TABLE sensor (
		sensor_id                           BIGSERIAL PRIMARY KEY,
		board_id                            BIGINT NOT NULL REFERENCES board(board_id) ON DELETE RESTRICT,
		sensor_type_id                      BIGINT NOT NULL REFERENCES sensor_type(sensor_type_id),
		name                                VARCHAR(128) NOT NULL,
		unit                                VARCHAR(16) NOT NULL,
		registered_at                       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_seen_at                        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		corrective_push_attempts            INT NOT NULL DEFAULT 0,
		corrective_push_outstanding_version BIGINT,
		UNIQUE (board_id, name)
	);
`

// seedAdminInsert is copied verbatim from migration 016's seeded-first-admin
// step, so these tests exercise the actual statement the migration runs,
// not a paraphrase of it.
const seedAdminInsert = `
	INSERT INTO leaflab_user_role (leaflab_user_id, role)
	SELECT leaflab_user_id, 'admin'
	FROM leaflab_user
	ORDER BY leaflab_user_id
	LIMIT 1
	ON CONFLICT DO NOTHING
`

func newMigration016TestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := dbtest.NewPostgres(context.Background(), t, dbtest.Options{Schema: migration016Schema})
	return db.Pool
}

func migration016SeedBoard(t *testing.T, pool *pgxpool.Pool, deviceID string) int64 {
	t.Helper()
	var boardID int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO board (device_id) VALUES ($1) RETURNING board_id`, deviceID).Scan(&boardID); err != nil {
		t.Fatalf("seed board %s: %v", deviceID, err)
	}
	return boardID
}

func migration016SeedUser(t *testing.T, pool *pgxpool.Pool, oidcSub string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO leaflab_user (oidc_sub) VALUES ($1) RETURNING leaflab_user_id`, oidcSub).Scan(&id); err != nil {
		t.Fatalf("seed leaflab_user %s: %v", oidcSub, err)
	}
	return id
}

// isUniqueViolation reports whether err is Postgres SQLSTATE 23505
// (unique_violation), mirroring //libs/go/dbtest:postgres_constraints_test's
// helper of the same name.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

// TestBoardName_NullableAndNoUniquenessAcrossBoards proves board.name (FR3)
// is a plain nullable column with no uniqueness enforcement: two boards can
// share the same non-empty name, and a board can be left with no name at
// all.
func TestBoardName_NullableAndNoUniquenessAcrossBoards(t *testing.T) {
	pool := newMigration016TestDB(t)
	ctx := context.Background()

	unnamedBoardID := migration016SeedBoard(t, pool, "board-unnamed")
	var name *string
	if err := pool.QueryRow(ctx, `SELECT name FROM board WHERE board_id = $1`, unnamedBoardID).Scan(&name); err != nil {
		t.Fatalf("select name for unnamed board: %v", err)
	}
	if name != nil {
		t.Fatalf("expected NULL name for a board with no name set, got %q", *name)
	}

	boardAID := migration016SeedBoard(t, pool, "board-a")
	boardBID := migration016SeedBoard(t, pool, "board-b")

	if _, err := pool.Exec(ctx, `UPDATE board SET name = $1 WHERE board_id = $2`, "greenhouse", boardAID); err != nil {
		t.Fatalf("set name on board-a: %v", err)
	}
	// Same name on a second, distinct board must be accepted -- no UNIQUE
	// constraint on board.name.
	if _, err := pool.Exec(ctx, `UPDATE board SET name = $1 WHERE board_id = $2`, "greenhouse", boardBID); err != nil {
		t.Fatalf("expected duplicate board name across two boards to be accepted (no uniqueness constraint), got: %v", err)
	}

	var nameA, nameB string
	if err := pool.QueryRow(ctx, `SELECT name FROM board WHERE board_id = $1`, boardAID).Scan(&nameA); err != nil {
		t.Fatalf("select name for board-a: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT name FROM board WHERE board_id = $1`, boardBID).Scan(&nameB); err != nil {
		t.Fatalf("select name for board-b: %v", err)
	}
	if nameA != "greenhouse" || nameB != "greenhouse" {
		t.Fatalf("expected both boards to hold the shared name %q, got nameA=%q nameB=%q", "greenhouse", nameA, nameB)
	}
}

// TestLeaflabUserRole_ConcurrentOpenGrantsRejectedThenAllowedAfterRevoke
// proves FR10's SCD2 shape: idx_leaflab_user_role_current rejects a second
// concurrently-open grant of the same role to the same user, but closing
// the first (valid_to = NOW()) then allows a fresh one to be opened.
func TestLeaflabUserRole_ConcurrentOpenGrantsRejectedThenAllowedAfterRevoke(t *testing.T) {
	pool := newMigration016TestDB(t)
	ctx := context.Background()

	userID := migration016SeedUser(t, pool, "sub-1")

	if _, err := pool.Exec(ctx,
		`INSERT INTO leaflab_user_role (leaflab_user_id, role) VALUES ($1, 'admin')`, userID); err != nil {
		t.Fatalf("first admin grant should succeed: %v", err)
	}

	_, err := pool.Exec(ctx,
		`INSERT INTO leaflab_user_role (leaflab_user_id, role) VALUES ($1, 'admin')`, userID)
	if err == nil {
		t.Fatal("second concurrently-open admin grant for the same user succeeded; idx_leaflab_user_role_current did not fire")
	}
	if !isUniqueViolation(err) {
		t.Fatalf("expected a unique_violation (23505), got: %v", err)
	}
	t.Logf("got expected unique_violation: %v", err)

	// Revoke: close the open grant.
	if _, err := pool.Exec(ctx,
		`UPDATE leaflab_user_role SET valid_to = NOW() WHERE leaflab_user_id = $1 AND role = 'admin' AND valid_to IS NULL`, userID); err != nil {
		t.Fatalf("close current admin grant: %v", err)
	}

	// Re-grant after revocation must be allowed.
	if _, err := pool.Exec(ctx,
		`INSERT INTO leaflab_user_role (leaflab_user_id, role) VALUES ($1, 'admin')`, userID); err != nil {
		t.Fatalf("re-grant after revocation should succeed: %v", err)
	}

	// The revoked row must still exist (SCD2: revocation preserves history,
	// it does not delete the fact that the role was once granted), and
	// exactly one row must now be open.
	var totalRows, openRows int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM leaflab_user_role WHERE leaflab_user_id = $1`, userID).Scan(&totalRows); err != nil {
		t.Fatalf("count total rows: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM leaflab_user_role WHERE leaflab_user_id = $1 AND valid_to IS NULL`, userID).Scan(&openRows); err != nil {
		t.Fatalf("count open rows: %v", err)
	}
	if totalRows != 2 {
		t.Fatalf("expected the revoked grant to be preserved alongside the new one (2 total rows), got %d", totalRows)
	}
	if openRows != 1 {
		t.Fatalf("expected exactly one open admin grant after revoke-then-regrant, got %d", openRows)
	}
}

// TestMigration016Seed_ExistingUsersGetExactlyOneOpenAdminGrantOnEarliestUser
// exercises migration 016's seeded-first-admin INSERT against a
// leaflab_user table that already has rows, proving FR10's requirement that
// after migration at least one user holds admin, with the selection rule
// (earliest leaflab_user_id) the migration documents.
func TestMigration016Seed_ExistingUsersGetExactlyOneOpenAdminGrantOnEarliestUser(t *testing.T) {
	pool := newMigration016TestDB(t)
	ctx := context.Background()

	earliestID := migration016SeedUser(t, pool, "sub-earliest")
	migration016SeedUser(t, pool, "sub-second")
	migration016SeedUser(t, pool, "sub-third")

	if _, err := pool.Exec(ctx, seedAdminInsert); err != nil {
		t.Fatalf("seed admin insert against a non-empty leaflab_user table should succeed: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM leaflab_user_role`).Scan(&count); err != nil {
		t.Fatalf("count grants: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one admin grant to be seeded, got %d", count)
	}

	var grantedUserID int64
	var role string
	var validTo *string
	if err := pool.QueryRow(ctx,
		`SELECT leaflab_user_id, role, valid_to::text FROM leaflab_user_role`).Scan(&grantedUserID, &role, &validTo); err != nil {
		t.Fatalf("select the seeded grant: %v", err)
	}
	if grantedUserID != earliestID {
		t.Fatalf("expected the seeded admin grant on the earliest leaflab_user_id (%d), got %d", earliestID, grantedUserID)
	}
	if role != "admin" {
		t.Fatalf("expected role 'admin', got %q", role)
	}
	if validTo != nil {
		t.Fatalf("expected the seeded grant to be open (valid_to IS NULL), got valid_to=%q", *validTo)
	}
}

// TestMigration016Seed_NoUsersProducesNoGrantsWithoutError exercises the
// same seeded-first-admin INSERT against an empty leaflab_user table, per
// FR10's requirement that this be a no-op, not an error, on zero users (the
// zero-users case is bootstrapped separately at first sign-in).
func TestMigration016Seed_NoUsersProducesNoGrantsWithoutError(t *testing.T) {
	pool := newMigration016TestDB(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, seedAdminInsert); err != nil {
		t.Fatalf("seed admin insert against an empty leaflab_user table should be a no-op, not an error: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM leaflab_user_role`).Scan(&count); err != nil {
		t.Fatalf("count grants: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected zero grants when leaflab_user is empty, got %d", count)
	}
}

// TestSensorCorrectivePushColumns_DefaultValues proves NFR4's counter and
// concurrent-guard columns default correctly for a newly inserted sensor:
// corrective_push_attempts starts at 0 (no attempts made yet) and
// corrective_push_outstanding_version starts NULL (no corrective push
// outstanding).
func TestSensorCorrectivePushColumns_DefaultValues(t *testing.T) {
	pool := newMigration016TestDB(t)
	ctx := context.Background()

	boardID := migration016SeedBoard(t, pool, "board-sensor-defaults")
	var sensorTypeID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO sensor_type (name, default_unit) VALUES ('temperature', 'C') RETURNING sensor_type_id`).Scan(&sensorTypeID); err != nil {
		t.Fatalf("seed sensor_type: %v", err)
	}

	var sensorID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, name, unit)
		VALUES ($1, $2, 'temp', 'C')
		RETURNING sensor_id`, boardID, sensorTypeID).Scan(&sensorID); err != nil {
		t.Fatalf("seed sensor: %v", err)
	}

	var attempts int
	var outstandingVersion *int64
	if err := pool.QueryRow(ctx,
		`SELECT corrective_push_attempts, corrective_push_outstanding_version FROM sensor WHERE sensor_id = $1`, sensorID,
	).Scan(&attempts, &outstandingVersion); err != nil {
		t.Fatalf("select corrective-push columns: %v", err)
	}
	if attempts != 0 {
		t.Fatalf("expected corrective_push_attempts to default to 0, got %d", attempts)
	}
	if outstandingVersion != nil {
		t.Fatalf("expected corrective_push_outstanding_version to default to NULL, got %d", *outstandingVersion)
	}
}
