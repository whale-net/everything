//go:build integration

// Real-Postgres integration coverage for FR10/FR12 activation's admin
// standing lane and elevation lifecycle (migration 029_admin_elevation):
// the SQL-level half this package's unit tests (server_admin_test.go)
// can't reach with a fake repository --
//
//   - RenewElevation only ever extends an *already open, unexpired* row
//     (WHERE ... ended_at IS NULL AND expires_at > NOW()); it never opens a
//     fresh one, and refuses (ErrNoActiveElevation) when none is open.
//   - EndElevation and ActiveElevation apply the identical open-row
//     predicate.
//   - An elevation opened against one household never satisfies a lookup
//     against a different household (FR10.3).
//   - A row whose expires_at has already passed is treated as "no active
//     elevation" by ActiveElevation, RenewElevation and EndElevation alike
//     -- the "a request at 60:01 is refused" acceptance criterion, proven
//     by manipulating expires_at directly rather than waiting a literal
//     hour in a test.
//   - AdminBoardHealthByPerson/AdminBoardHealthByPartialDeviceID return
//     FR79's health projection for the correct board(s), excluding retired
//     and unclaimed boards, exactly as ListBoards' own retirement guard
//     does for the non-admin path.
//
// Schema is self-contained hand-written DDL, deliberately not shared with
// dbtest_helpers_integration_test.go's testSchema (no household concept)
// or authz_scope_integration_test.go's authzTestSchema (no admin_elevation
// table, no household.name, no sensor table) -- same rationale as those
// files' own doc comments. See //libs/go/dbtest/README.md for how to run
// integration tests like this one; same tag set as this package's other
// integration go_test targets.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api:admin_elevation_integration_test --test_output=all
package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/api/audit"
	"github.com/whale-net/everything/leaflab/api/authz"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/grpcauth"
)

const adminElevationTestSchema = `
	CREATE TABLE household (
		household_id BIGSERIAL PRIMARY KEY,
		name         TEXT NOT NULL DEFAULT ''
	);

	CREATE TABLE household_membership (
		membership_id     BIGSERIAL PRIMARY KEY,
		household_id      BIGINT NOT NULL REFERENCES household(household_id),
		principal_subject TEXT NOT NULL,
		valid_from        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to          TIMESTAMPTZ
	);

	CREATE TABLE board (
		board_id      BIGSERIAL PRIMARY KEY,
		device_id     VARCHAR(64) NOT NULL UNIQUE,
		household_id  BIGINT REFERENCES household(household_id),
		registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		retired_at    TIMESTAMPTZ
	);

	CREATE TABLE device_config (
		config_id        BIGSERIAL   PRIMARY KEY,
		board_id         BIGINT      NOT NULL REFERENCES board(board_id) ON DELETE RESTRICT,
		version          BIGINT      NOT NULL,
		config_json      JSONB       NOT NULL,
		accepted         BOOLEAN     NOT NULL DEFAULT FALSE,
		pushed_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		acked_at         TIMESTAMPTZ,
		rejection_reason TEXT,
		UNIQUE (board_id, version)
	);

	CREATE TABLE sensor (
		sensor_id BIGSERIAL PRIMARY KEY,
		board_id  BIGINT NOT NULL REFERENCES board(board_id) ON DELETE CASCADE
	);

	CREATE TABLE admin_elevation (
		elevation_id         BIGSERIAL PRIMARY KEY,
		admin_subject        TEXT NOT NULL,
		target_household_id  BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
		reason                TEXT NOT NULL,
		started_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		expires_at            TIMESTAMPTZ NOT NULL,
		ended_at              TIMESTAMPTZ NULL
	);

	CREATE TABLE audit_log (
		audit_id             BIGSERIAL PRIMARY KEY,
		actor_subject        TEXT NOT NULL,
		actor_kind           TEXT NOT NULL,
		target_household_id  BIGINT NULL,
		action                TEXT NOT NULL,
		entity_kind           TEXT NOT NULL,
		entity_id             TEXT NULL,
		reason                TEXT NULL,
		occurred_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		correlation_id        TEXT NULL
	);
`

// newAdminElevationTestRepository starts a real Postgres container, applies
// adminElevationTestSchema, and returns a *Repository plus the raw pool for
// fixture setup / assertions.
func newAdminElevationTestRepository(t *testing.T) (*Repository, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminElevationTestSchema})
	return NewRepository(db.Pool), db.Pool
}

func insertHousehold(t *testing.T, pool *pgxpool.Pool, name string) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO household (name) VALUES ($1) RETURNING household_id`, name).Scan(&id)
	if err != nil {
		t.Fatalf("insert household %s: %v", name, err)
	}
	return id
}

// TestElevationLifecycle_OpenRenewEnd_PerHouseholdIsolation is the
// end-to-end elevation lifecycle over real SQL: opening an elevation makes
// it active for exactly its target household and no other (FR10.3);
// renewal extends the same row (never inserting a second one) and updates
// its expiry and reason; ending it closes the row so it is no longer
// active, and a second renewal/end attempt after that is refused.
func TestElevationLifecycle_OpenRenewEnd_PerHouseholdIsolation(t *testing.T) {
	repo, pool := newAdminElevationTestRepository(t)
	ctx := context.Background()

	householdA := insertHousehold(t, pool, "household-a")
	householdB := insertHousehold(t, pool, "household-b")

	firstExpiry := time.Now().Add(60 * time.Minute).Truncate(time.Millisecond)
	if err := repo.OpenElevation(ctx, "admin1", householdA, "investigating a report", firstExpiry, testAuditEntry()); err != nil {
		t.Fatalf("OpenElevation: %v", err)
	}

	if n := countRows(t, pool, "admin_elevation"); n != 1 {
		t.Fatalf("admin_elevation has %d rows after OpenElevation, want 1", n)
	}

	// Active against A, not against B (FR10.3): an elevation against
	// household A does not open household B.
	gotExpiry, err := repo.ActiveElevation(ctx, "admin1", householdA)
	if err != nil {
		t.Fatalf("ActiveElevation(householdA): %v", err)
	}
	if !gotExpiry.Equal(firstExpiry) {
		t.Errorf("ActiveElevation(householdA) expiry = %v, want %v", gotExpiry, firstExpiry)
	}
	if _, err := repo.ActiveElevation(ctx, "admin1", householdB); !errors.Is(err, ErrNoActiveElevation) {
		t.Errorf("ActiveElevation(householdB) = %v, want ErrNoActiveElevation -- elevation against A must not open B", err)
	}

	// Renewal extends the same row -- never opens a second one -- and its
	// new reason/expiry take effect.
	secondExpiry := time.Now().Add(120 * time.Minute).Truncate(time.Millisecond)
	if err := repo.RenewElevation(ctx, "admin1", householdA, "still investigating", secondExpiry, testAuditEntry()); err != nil {
		t.Fatalf("RenewElevation: %v", err)
	}
	if n := countRows(t, pool, "admin_elevation"); n != 1 {
		t.Fatalf("admin_elevation has %d rows after RenewElevation, want 1 (renewal must never open a new elevation)", n)
	}
	gotExpiry, err = repo.ActiveElevation(ctx, "admin1", householdA)
	if err != nil {
		t.Fatalf("ActiveElevation after renewal: %v", err)
	}
	if !gotExpiry.Equal(secondExpiry) {
		t.Errorf("ActiveElevation after renewal expiry = %v, want the renewed expiry %v", gotExpiry, secondExpiry)
	}
	var storedReason string
	if err := pool.QueryRow(ctx, `SELECT reason FROM admin_elevation WHERE admin_subject = 'admin1' AND target_household_id = $1`, householdA).Scan(&storedReason); err != nil {
		t.Fatalf("read reason after renewal: %v", err)
	}
	if storedReason != "still investigating" {
		t.Errorf("stored reason after renewal = %q, want the restated reason", storedReason)
	}

	// Ending closes the row -- no longer active, and a second End/Renew
	// attempt is refused rather than silently succeeding.
	if err := repo.EndElevation(ctx, "admin1", householdA, testAuditEntry()); err != nil {
		t.Fatalf("EndElevation: %v", err)
	}
	if _, err := repo.ActiveElevation(ctx, "admin1", householdA); !errors.Is(err, ErrNoActiveElevation) {
		t.Errorf("ActiveElevation after EndElevation = %v, want ErrNoActiveElevation", err)
	}
	if err := repo.EndElevation(ctx, "admin1", householdA, testAuditEntry()); !errors.Is(err, ErrNoActiveElevation) {
		t.Errorf("second EndElevation = %v, want ErrNoActiveElevation (already ended)", err)
	}
	if err := repo.RenewElevation(ctx, "admin1", householdA, "reopen attempt", time.Now().Add(time.Hour), testAuditEntry()); !errors.Is(err, ErrNoActiveElevation) {
		t.Errorf("RenewElevation after end = %v, want ErrNoActiveElevation -- renewal must never resurrect an ended elevation", err)
	}
	if n := countRows(t, pool, "admin_elevation"); n != 1 {
		t.Errorf("admin_elevation has %d rows after a failed renew-after-end, want still 1", n)
	}
}

// TestRenewElevation_RefusesWhenNeverOpened proves renewing an elevation
// that was never opened at all is refused, and writes no row.
func TestRenewElevation_RefusesWhenNeverOpened(t *testing.T) {
	repo, pool := newAdminElevationTestRepository(t)
	ctx := context.Background()
	household := insertHousehold(t, pool, "household-solo")

	err := repo.RenewElevation(ctx, "admin1", household, "reason", time.Now().Add(time.Hour), testAuditEntry())
	if !errors.Is(err, ErrNoActiveElevation) {
		t.Errorf("RenewElevation with nothing ever opened = %v, want ErrNoActiveElevation", err)
	}
	if n := countRows(t, pool, "admin_elevation"); n != 0 {
		t.Errorf("admin_elevation has %d rows, want 0 -- renewal must never open a new elevation", n)
	}
}

// TestExpiredElevation_TreatedAsNoneOpen proves the "a request at 60:01 is
// refused" acceptance criterion at the SQL level: a row whose expires_at
// has already passed is invisible to ActiveElevation, RenewElevation and
// EndElevation alike, even though ended_at is still NULL -- expiry alone,
// with no intervention, is what closes an elevation's reach.
func TestExpiredElevation_TreatedAsNoneOpen(t *testing.T) {
	repo, pool := newAdminElevationTestRepository(t)
	ctx := context.Background()
	household := insertHousehold(t, pool, "household-expired")

	pastExpiry := time.Now().Add(-1 * time.Minute)
	var elevationID int64
	err := pool.QueryRow(ctx, `
		INSERT INTO admin_elevation (admin_subject, target_household_id, reason, expires_at)
		VALUES ($1, $2, $3, $4)
		RETURNING elevation_id
	`, "admin1", household, "already expired", pastExpiry).Scan(&elevationID)
	if err != nil {
		t.Fatalf("test setup: insert expired elevation: %v", err)
	}

	if _, err := repo.ActiveElevation(ctx, "admin1", household); !errors.Is(err, ErrNoActiveElevation) {
		t.Errorf("ActiveElevation on an expired-but-not-ended row = %v, want ErrNoActiveElevation", err)
	}
	if err := repo.RenewElevation(ctx, "admin1", household, "trying to renew after expiry", time.Now().Add(time.Hour), testAuditEntry()); !errors.Is(err, ErrNoActiveElevation) {
		t.Errorf("RenewElevation on an expired row = %v, want ErrNoActiveElevation", err)
	}
	if err := repo.EndElevation(ctx, "admin1", household, testAuditEntry()); !errors.Is(err, ErrNoActiveElevation) {
		t.Errorf("EndElevation on an expired row = %v, want ErrNoActiveElevation", err)
	}
}

// TestAdminBoardHealth_ByPersonAndByPartialDeviceID_ProjectsFR79Fields
// proves both standing-lane resolution queries return FR79's health
// projection for the correct board(s) and exclude a retired board and an
// unclaimed (no household) board -- mirroring ListBoards' own retirement
// guard, applied here to the admin resolution path.
func TestAdminBoardHealth_ByPersonAndByPartialDeviceID_ProjectsFR79Fields(t *testing.T) {
	repo, pool := newAdminElevationTestRepository(t)
	ctx := context.Background()

	household := insertHousehold(t, pool, "The Smiths")
	if _, err := pool.Exec(ctx, `INSERT INTO household_membership (household_id, principal_subject) VALUES ($1, $2)`, household, "alice@example.com"); err != nil {
		t.Fatalf("test setup: insert membership: %v", err)
	}

	var boardID int64
	if err := pool.QueryRow(ctx, `INSERT INTO board (device_id, household_id) VALUES ($1, $2) RETURNING board_id`, "device-abc123", household).Scan(&boardID); err != nil {
		t.Fatalf("test setup: insert board: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO device_config (board_id, version, config_json, accepted) VALUES ($1, 1, '{}', TRUE)`, boardID); err != nil {
		t.Fatalf("test setup: insert accepted config: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO device_config (board_id, version, config_json, accepted) VALUES ($1, 2, '{}', FALSE)`, boardID); err != nil {
		t.Fatalf("test setup: insert outstanding config: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sensor (board_id) VALUES ($1), ($1)`, boardID); err != nil {
		t.Fatalf("test setup: insert sensors: %v", err)
	}

	// A retired sibling board in the same household -- must not appear.
	var retiredBoardID int64
	if err := pool.QueryRow(ctx, `INSERT INTO board (device_id, household_id, retired_at) VALUES ($1, $2, NOW()) RETURNING board_id`, "device-abc999-retired", household).Scan(&retiredBoardID); err != nil {
		t.Fatalf("test setup: insert retired board: %v", err)
	}

	// An unclaimed board (no household) matching the same partial id --
	// must not appear (the standing lane resolves *to* a household).
	if _, err := pool.Exec(ctx, `INSERT INTO board (device_id) VALUES ($1)`, "device-abc000-unclaimed"); err != nil {
		t.Fatalf("test setup: insert unclaimed board: %v", err)
	}

	rowsByPerson, err := repo.AdminBoardHealthByPerson(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("AdminBoardHealthByPerson: %v", err)
	}
	if len(rowsByPerson) != 1 {
		t.Fatalf("AdminBoardHealthByPerson returned %d rows, want 1 (retired and unclaimed boards excluded)", len(rowsByPerson))
	}
	got := rowsByPerson[0]
	if got.DeviceID != "device-abc123" {
		t.Errorf("DeviceID = %q, want %q", got.DeviceID, "device-abc123")
	}
	if got.HouseholdID != household {
		t.Errorf("HouseholdID = %d, want %d", got.HouseholdID, household)
	}
	if got.HouseholdName != "The Smiths" {
		t.Errorf("HouseholdName = %q, want %q", got.HouseholdName, "The Smiths")
	}
	if got.ActiveVersion != 1 {
		t.Errorf("ActiveVersion = %d, want 1 (highest accepted version)", got.ActiveVersion)
	}
	if !got.OutstandingPush {
		t.Error("OutstandingPush = false, want true (version 2 was pushed but not accepted)")
	}
	if got.SensorCount != 2 {
		t.Errorf("SensorCount = %d, want 2", got.SensorCount)
	}

	rowsByPartial, err := repo.AdminBoardHealthByPartialDeviceID(ctx, "abc123")
	if err != nil {
		t.Fatalf("AdminBoardHealthByPartialDeviceID: %v", err)
	}
	if len(rowsByPartial) != 1 || rowsByPartial[0].DeviceID != "device-abc123" {
		t.Fatalf("AdminBoardHealthByPartialDeviceID returned %+v, want exactly device-abc123", rowsByPartial)
	}

	// A broader partial match ("abc") would hit all three device_ids by
	// substring, but only the non-retired, claimed board must come back.
	rowsByBroaderPartial, err := repo.AdminBoardHealthByPartialDeviceID(ctx, "abc")
	if err != nil {
		t.Fatalf("AdminBoardHealthByPartialDeviceID (broader): %v", err)
	}
	if len(rowsByBroaderPartial) != 1 || rowsByBroaderPartial[0].DeviceID != "device-abc123" {
		t.Fatalf("AdminBoardHealthByPartialDeviceID(\"abc\") returned %+v, want exactly device-abc123 (retired and unclaimed excluded)", rowsByBroaderPartial)
	}
}

// TestRecordAuditEntry_WritesStandingLaneAuditRow proves
// RecordAuditEntry -- the path ResolveToHousehold's handler uses directly
// against the pool, with no accompanying write to piggyback a transaction
// on -- actually persists a row with the fields FR8.1/FR10.4 require.
func TestRecordAuditEntry_WritesStandingLaneAuditRow(t *testing.T) {
	repo, pool := newAdminElevationTestRepository(t)
	ctx := context.Background()

	queryTerm := "person_identifier=alice@example.com"
	entry := audit.Entry{
		ActorSubject: "root",
		ActorKind:    audit.ActorKindHuman,
		Action:       "ResolveToHousehold",
		EntityKind:   "admin_resolution",
		EntityID:     &queryTerm,
	}
	if err := repo.RecordAuditEntry(ctx, entry); err != nil {
		t.Fatalf("RecordAuditEntry: %v", err)
	}

	if n := countRows(t, pool, "audit_log"); n != 1 {
		t.Fatalf("audit_log has %d rows, want 1", n)
	}
	var actorSubject, action, entityKind, entityID string
	if err := pool.QueryRow(ctx, `SELECT actor_subject, action, entity_kind, entity_id FROM audit_log LIMIT 1`).Scan(&actorSubject, &action, &entityKind, &entityID); err != nil {
		t.Fatalf("read audit_log row: %v", err)
	}
	if actorSubject != "root" || action != "ResolveToHousehold" || entityKind != "admin_resolution" || entityID != queryTerm {
		t.Errorf("audit_log row = (%q, %q, %q, %q), want (%q, %q, %q, %q)", actorSubject, action, entityKind, entityID, "root", "ResolveToHousehold", "admin_resolution", queryTerm)
	}
}

// -- FR10.3: GetDeviceConfig's ElevatedScope wiring, end to end --------------
//
// server.go's authorizeBoardAccess/elevatedBoardScope is unit-tested
// against fakeRepo/fakeAuthz in server_test.go/server_admin_test.go; the
// test below proves the same behavior against a real Repository and a real
// authz.PGResolver (the combination authz_scope_integration_test.go's
// newAuthzTestServer uses for the household path), extended with this
// file's admin_elevation table -- the one thing newAuthzTestServer's
// narrower schema doesn't have.

// newAdminElevationTestServer starts a real Postgres container with
// adminElevationTestSchema applied and returns a LeafLabAPIServer backed by
// a real Repository and a real authz.PGResolver.
func newAdminElevationTestServer(t *testing.T) (*LeafLabAPIServer, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: adminElevationTestSchema})
	repo := NewRepository(db.Pool)
	resolver := authz.NewPGResolver(db.Pool)
	return NewLeafLabAPIServer(repo, resolver, nil, nil, nil, nil, discardLogger()), db.Pool
}

// adminElevationTestCtx returns a context carrying grpcauth.Claims for
// subject with the leaflab-admin realm role -- eligible to call Elevate,
// exactly as adminCtx (server_admin_test.go) does for the unit tests.
func adminElevationTestCtx(subject string) context.Context {
	return grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: subject, Roles: []string{RoleAdmin}})
}

// insertBoardWithAcceptedConfig inserts a board owned by householdID with
// one accepted device_config row, so a successful GetDeviceConfig has
// something real to find.
func insertBoardWithAcceptedConfig(t *testing.T, pool *pgxpool.Pool, deviceID string, householdID int64) int64 {
	t.Helper()
	ctx := context.Background()
	var boardID int64
	if err := pool.QueryRow(ctx, `INSERT INTO board (device_id, household_id) VALUES ($1, $2) RETURNING board_id`, deviceID, householdID).Scan(&boardID); err != nil {
		t.Fatalf("test setup: insert board: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO device_config (board_id, version, config_json, accepted) VALUES ($1, 1, '{}', TRUE)`, boardID); err != nil {
		t.Fatalf("test setup: insert accepted config: %v", err)
	}
	return boardID
}

// insertElevation inserts an admin_elevation row directly (bypassing
// Elevate's own request validation, which server_admin_test.go already
// covers) so a test can set up "already elevated" state without an extra
// RPC round trip.
func insertElevation(t *testing.T, pool *pgxpool.Pool, adminSubject string, targetHouseholdID int64, reason string, expiresAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO admin_elevation (admin_subject, target_household_id, reason, expires_at)
		VALUES ($1, $2, $3, $4)
	`, adminSubject, targetHouseholdID, reason, expiresAt); err != nil {
		t.Fatalf("test setup: insert elevation: %v", err)
	}
}

// TestGetDeviceConfig_ElevatedAdmin_FR10_3 is the Implementation section's
// core mandate ("every non-standing admin read resolves its scope through
// ElevatedScope") proven against GetDeviceConfig, the one config-payload
// read this codebase has today: an admin-eligible caller with no elevation
// is refused exactly like a nonexistent device (NFR2); the same caller
// after elevating against the board's own household succeeds; and an
// elevation against a *different* household still refuses (FR10.3 -- an
// elevation against household A does not open household B).
func TestGetDeviceConfig_ElevatedAdmin_FR10_3(t *testing.T) {
	server, pool := newAdminElevationTestServer(t)

	householdA := insertHousehold(t, pool, "household-a")
	householdB := insertHousehold(t, pool, "household-b")
	insertBoardWithAcceptedConfig(t, pool, "device-elevated-a", householdA)

	callerCtx := adminElevationTestCtx("root")

	t.Run("no elevation refused", func(t *testing.T) {
		_, err := server.GetDeviceConfig(callerCtx, &pb.GetDeviceConfigRequest{DeviceId: "device-elevated-a"})
		if err == nil {
			t.Fatal("GetDeviceConfig with no elevation returned nil error, want a refusal")
		}
	})

	insertElevation(t, pool, "root", householdB, "wrong household", time.Now().Add(time.Hour))
	t.Run("elevated against a different household still refused", func(t *testing.T) {
		_, err := server.GetDeviceConfig(callerCtx, &pb.GetDeviceConfigRequest{DeviceId: "device-elevated-a"})
		if err == nil {
			t.Fatal("GetDeviceConfig elevated against a different household returned nil error, want a refusal (FR10.3)")
		}
	})

	insertElevation(t, pool, "root", householdA, "investigating a report", time.Now().Add(time.Hour))
	t.Run("elevated against the right household succeeds", func(t *testing.T) {
		resp, err := server.GetDeviceConfig(callerCtx, &pb.GetDeviceConfigRequest{DeviceId: "device-elevated-a"})
		if err != nil {
			t.Fatalf("GetDeviceConfig elevated against the right household returned an error, want success: %v", err)
		}
		if !resp.Found {
			t.Error("Found = false, want true -- an elevated admin must reach the accepted config")
		}
	})
}
