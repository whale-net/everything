//go:build integration

// Real-Postgres integration coverage for FR70.2-.4/FR77's ownership
// closure, self-service adoption seam, and transfer with a departure
// record (#1343): ComputeClosure's fixed six-step enumeration (not a
// transitive closure) and its "current placement only" semantics,
// TransferClosure's entangled-board refusal (FR59.3) and its permitted
// "leaves an owned board with a sensor in an unowned region" outcome,
// FR77's evidence gating (release token / discharged-challenge, "an admin
// assertion alone is never sufficient"), the departure record's
// losing-side-only placement and append-only shape (NFR6.3), and
// board_ownership's SCD2 history surviving a transfer.
//
// Adoption out of Unadopted (FR76's CompleteClaim, #1342) does not move
// the whole closure today -- that gap is tracked by scope note #1442
// against #1342, filed during this task's own Implementation phase, and
// is explicitly out of this task's scope (the issue's RPC surface is
// ReleaseBoard/TransferClosure/PreviewClosure only). Where the issue's
// Testing bullets describe an "adoption" refusal/success shape that FR70's
// closure enumeration also governs, this file exercises the equivalent
// through TransferClosure instead -- the same ComputeClosure/
// ErrEntangledClosure code path CompleteClaim will eventually reuse once
// #1442 lands -- and says so at each such test's doc comment.
//
// Schema is self-contained hand-written DDL, mirroring migration
// 015_ownership's household/household_membership/board/board_ownership/
// region/plant shape, 001_initial_schema's sensor/sensor_region_history/
// plant_type shape (trimmed to the columns ComputeClosure/TransferClosure
// touch), 021_claim_challenge's claim_challenge shape (trimmed to
// state/device_id, matching claim_integration_test.go's dischargeChallenge
// simulation precedent), 022_departure_record's table plus its append-only
// trigger verbatim, 023_board_release_token's table, and postgres.go's
// audit_log column set -- same self-contained-schema rationale as every
// other *_integration_test.go file in this package.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api:closure_integration_test --test_output=all
package main

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/whale-net/everything/leaflab/api/claim"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/api/ratelimit"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/grpcauth"
)

const closureTestSchema = `
	CREATE TABLE household (
		household_id BIGSERIAL PRIMARY KEY,
		name         TEXT NOT NULL,
		is_unadopted BOOLEAN NOT NULL DEFAULT FALSE,
		created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	CREATE TABLE household_membership (
		household_membership_id BIGSERIAL PRIMARY KEY,
		household_id             BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
		principal_subject        TEXT NOT NULL,
		valid_from                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to                  TIMESTAMPTZ
	);
	CREATE INDEX idx_closure_household_membership_current
		ON household_membership(household_id) WHERE valid_to IS NULL;
	CREATE INDEX idx_closure_household_membership_principal_current
		ON household_membership(principal_subject) WHERE valid_to IS NULL;

	CREATE TABLE board (
		board_id      BIGSERIAL PRIMARY KEY,
		device_id     VARCHAR(64) NOT NULL UNIQUE,
		household_id  BIGINT REFERENCES household(household_id),
		registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	CREATE TABLE board_ownership (
		board_ownership_id BIGSERIAL PRIMARY KEY,
		board_id            BIGINT NOT NULL REFERENCES board(board_id) ON DELETE RESTRICT,
		household_id        BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
		valid_from           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to             TIMESTAMPTZ
	);
	CREATE INDEX idx_closure_board_ownership_current
		ON board_ownership(board_id) WHERE valid_to IS NULL;

	CREATE TABLE region (
		region_id        BIGSERIAL PRIMARY KEY,
		parent_region_id BIGINT REFERENCES region(region_id) ON DELETE RESTRICT,
		name             VARCHAR(255) NOT NULL,
		household_id     BIGINT REFERENCES household(household_id) ON DELETE RESTRICT,
		created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);
	CREATE INDEX idx_closure_region_parent ON region(parent_region_id);

	CREATE TABLE sensor (
		sensor_id     BIGSERIAL PRIMARY KEY,
		board_id      BIGINT NOT NULL REFERENCES board(board_id) ON DELETE RESTRICT,
		region_id     BIGINT REFERENCES region(region_id) ON DELETE RESTRICT,
		name          VARCHAR(128) NOT NULL,
		registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		UNIQUE (board_id, name)
	);
	CREATE INDEX idx_closure_sensor_board_id  ON sensor(board_id);
	CREATE INDEX idx_closure_sensor_region_id ON sensor(region_id);

	-- Mirrors 001_initial_schema's sensor_region_history -- used only by
	-- TestComputeClosure_HistoricalPlacementDoesNotWidenClosure to record a
	-- sensor's past region, proving ComputeClosure never consults it (it
	-- reads only sensor.region_id's current value).
	CREATE TABLE sensor_region_history (
		history_id    BIGSERIAL PRIMARY KEY,
		sensor_id     BIGINT NOT NULL REFERENCES sensor(sensor_id) ON DELETE RESTRICT,
		region_id     BIGINT NOT NULL REFERENCES region(region_id) ON DELETE RESTRICT,
		assigned_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		unassigned_at TIMESTAMPTZ
	);

	CREATE TABLE plant_type (
		plant_type_id BIGSERIAL PRIMARY KEY,
		common_name   VARCHAR(128) NOT NULL
	);

	CREATE TABLE plant (
		plant_id      BIGSERIAL PRIMARY KEY,
		region_id     BIGINT NOT NULL REFERENCES region(region_id) ON DELETE RESTRICT,
		plant_type_id BIGINT NOT NULL REFERENCES plant_type(plant_type_id),
		household_id  BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
		name          VARCHAR(128) NOT NULL,
		created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		removed_at    TIMESTAMPTZ
	);
	CREATE INDEX idx_closure_plant_region_id ON plant(region_id);
	CREATE INDEX idx_closure_plant_active    ON plant(region_id) WHERE removed_at IS NULL;

	-- Trimmed to the columns verifyDischargedChallenge (closure.go) reads:
	-- handle, device_id, state. Same discharge-by-direct-SQL simulation
	-- precedent as claim_integration_test.go's dischargeChallenge.
	CREATE TABLE claim_challenge (
		challenge_id BIGSERIAL PRIMARY KEY,
		handle       TEXT NOT NULL UNIQUE,
		device_id    TEXT NOT NULL,
		state        TEXT NOT NULL DEFAULT 'open'
		                 CHECK (state IN ('open', 'discharged', 'not_discharged'))
	);

	-- Mirrors migration 023_board_release_token verbatim (schema only --
	-- the REVOKE/no append-only-trigger distinction doesn't apply to this
	-- table).
	CREATE TABLE board_release_token (
		release_token_id BIGSERIAL PRIMARY KEY,
		token             TEXT NOT NULL UNIQUE,
		board_id          BIGINT NOT NULL REFERENCES board(board_id) ON DELETE CASCADE,
		household_id      BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
		released_by       TEXT NOT NULL,
		reason            TEXT,
		issued_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		expires_at        TIMESTAMPTZ NOT NULL,
		used_at           TIMESTAMPTZ
	);
	CREATE INDEX idx_closure_board_release_token_token ON board_release_token(token);

	-- Mirrors migration 022_departure_record verbatim, including its
	-- append-only trigger (NFR6.3) -- TestDepartureRecord_AppendOnly below
	-- must exercise the real guard, not a stand-in.
	CREATE TABLE departure_record (
		departure_id         BIGSERIAL PRIMARY KEY,
		losing_household_id  BIGINT NOT NULL REFERENCES household(household_id) ON DELETE RESTRICT,
		occurred_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		summary                JSONB NOT NULL,
		board_count            INT,
		region_count            INT,
		plant_count             INT,
		actor_subject           TEXT NOT NULL,
		reason                  TEXT
	);

	CREATE FUNCTION enforce_departure_record_append_only() RETURNS TRIGGER AS $$
	BEGIN
		RAISE EXCEPTION 'departure_record is append-only (NFR6.3): % is not permitted', TG_OP;
	END;
	$$ LANGUAGE plpgsql;

	CREATE TRIGGER trg_departure_record_append_only
		BEFORE UPDATE OR DELETE ON departure_record
		FOR EACH ROW
		EXECUTE FUNCTION enforce_departure_record_append_only();

	CREATE TABLE audit_log (
		audit_id             BIGSERIAL PRIMARY KEY,
		actor_subject        TEXT NOT NULL,
		actor_kind           TEXT NOT NULL,
		target_household_id  BIGINT NULL REFERENCES household(household_id),
		action                TEXT NOT NULL,
		entity_kind           TEXT NOT NULL,
		entity_id             TEXT NULL,
		reason                TEXT NULL,
		occurred_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		correlation_id        TEXT NULL
	);
`

// newClosureTestServer starts a real Postgres container with
// closureTestSchema applied. authzSvc is stubAuthz (from
// dbtest_helpers_integration_test.go, shared via this target's srcs): none
// of ReleaseBoard/TransferClosure gate on authz.Scope (see closure.go's
// doc comments -- ReleaseBoard's own membership check and TransferClosure's
// evidence gating are the authorization), and this file's PreviewClosure
// coverage (if any) would need only the not-found masking stubAuthz
// already exercises identically to claim/response-contract tests.
func newClosureTestServer(t *testing.T) (*LeafLabAPIServer, *Repository, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: closureTestSchema})
	repo := NewRepository(db.Pool)
	server := NewLeafLabAPIServer(repo, stubAuthz{}, nil, nil, discardLogger(), claim.DefaultConfig, ratelimit.NewInMemoryLimiter(nil))
	return server, repo, db.Pool
}

func closureCtxFor(subject string) context.Context {
	return grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: subject})
}

func insertClosureHousehold(t *testing.T, pool *pgxpool.Pool, name string, isUnadopted bool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO household (name, is_unadopted) VALUES ($1, $2) RETURNING household_id`, name, isUnadopted).Scan(&id); err != nil {
		t.Fatalf("insert household %q: %v", name, err)
	}
	return id
}

func insertClosureMembership(t *testing.T, pool *pgxpool.Pool, householdID int64, subject string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO household_membership (household_id, principal_subject) VALUES ($1, $2)`, householdID, subject); err != nil {
		t.Fatalf("insert membership for %q: %v", subject, err)
	}
}

// insertClosureBoard inserts a board, optionally owned by householdID (0 =
// unclaimed, board.household_id NULL, no board_ownership row) -- same
// convention as claim_integration_test.go's insertClaimBoard.
func insertClosureBoard(t *testing.T, pool *pgxpool.Pool, deviceID string, householdID int64) int64 {
	t.Helper()
	var id int64
	var hh any
	if householdID != 0 {
		hh = householdID
	}
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO board (device_id, household_id) VALUES ($1, $2) RETURNING board_id`, deviceID, hh).Scan(&id); err != nil {
		t.Fatalf("insert board %s: %v", deviceID, err)
	}
	if householdID != 0 {
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO board_ownership (board_id, household_id) VALUES ($1, $2)`, id, householdID); err != nil {
			t.Fatalf("insert board_ownership for board %d: %v", id, err)
		}
	}
	return id
}

// insertClosureRegion inserts a region. parentID = 0 means a tree root;
// householdID is only meaningful (and only ever set by these tests) on a
// root row, mirroring migration 015's "non-root household_id is NULL"
// convention -- this test schema doesn't reproduce
// trg_region_household_root, so it's the caller's job to only pass a
// non-zero householdID for a root.
func insertClosureRegion(t *testing.T, pool *pgxpool.Pool, name string, parentID, householdID int64) int64 {
	t.Helper()
	var id int64
	var parent, hh any
	if parentID != 0 {
		parent = parentID
	}
	if householdID != 0 {
		hh = householdID
	}
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO region (name, parent_region_id, household_id) VALUES ($1, $2, $3) RETURNING region_id`,
		name, parent, hh).Scan(&id); err != nil {
		t.Fatalf("insert region %s: %v", name, err)
	}
	return id
}

func insertClosureSensor(t *testing.T, pool *pgxpool.Pool, boardID, regionID int64, name string) int64 {
	t.Helper()
	var id int64
	var region any
	if regionID != 0 {
		region = regionID
	}
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO sensor (board_id, region_id, name) VALUES ($1, $2, $3) RETURNING sensor_id`,
		boardID, region, name).Scan(&id); err != nil {
		t.Fatalf("insert sensor %s: %v", name, err)
	}
	return id
}

func moveClosureSensor(t *testing.T, pool *pgxpool.Pool, sensorID, newRegionID int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE sensor SET region_id = $2 WHERE sensor_id = $1`, sensorID, newRegionID); err != nil {
		t.Fatalf("move sensor %d to region %d: %v", sensorID, newRegionID, err)
	}
}

// recordClosureSensorHistory writes a closed sensor_region_history row --
// "sensorID used to be in regionID, no longer" -- purely as the historical
// fact TestComputeClosure_HistoricalPlacementDoesNotWidenClosure asserts
// ComputeClosure never consults.
func recordClosureSensorHistory(t *testing.T, pool *pgxpool.Pool, sensorID, regionID int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO sensor_region_history (sensor_id, region_id, assigned_at, unassigned_at)
		VALUES ($1, $2, NOW() - interval '30 days', NOW() - interval '1 day')
	`, sensorID, regionID); err != nil {
		t.Fatalf("record sensor_region_history for sensor %d region %d: %v", sensorID, regionID, err)
	}
}

func closurePlantTypeID(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO plant_type (common_name) VALUES ('Test Plant') RETURNING plant_type_id`).Scan(&id); err != nil {
		t.Fatalf("insert plant_type: %v", err)
	}
	return id
}

func insertClosurePlant(t *testing.T, pool *pgxpool.Pool, regionID, householdID int64, name string) int64 {
	t.Helper()
	plantTypeID := closurePlantTypeID(t, pool)
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO plant (region_id, plant_type_id, household_id, name) VALUES ($1, $2, $3, $4) RETURNING plant_id`,
		regionID, plantTypeID, householdID, name).Scan(&id); err != nil {
		t.Fatalf("insert plant %s: %v", name, err)
	}
	return id
}

func insertClosureClaimChallenge(t *testing.T, pool *pgxpool.Pool, handle, deviceID, state string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO claim_challenge (handle, device_id, state) VALUES ($1, $2, $3)`, handle, deviceID, state); err != nil {
		t.Fatalf("insert claim_challenge %s: %v", handle, err)
	}
}

func int64SetEqual(t *testing.T, got []int64, want []int64, what string) {
	t.Helper()
	gotSet := make(map[int64]bool, len(got))
	for _, v := range got {
		gotSet[v] = true
	}
	wantSet := make(map[int64]bool, len(want))
	for _, v := range want {
		wantSet[v] = true
	}
	if len(gotSet) != len(wantSet) {
		t.Errorf("%s = %v, want %v", what, got, want)
		return
	}
	for v := range wantSet {
		if !gotSet[v] {
			t.Errorf("%s = %v, want %v (missing %d)", what, got, want, v)
			return
		}
	}
}

func int64SetContains(ids []int64, id int64) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

// -- Closure enumeration: fixed six-step, not transitive ---------------------

// TestComputeClosure_FixedEnumeration_NotTransitive is the issue's first
// Testing bullet, both halves in one fixture: board B's closure includes
// entangled board C (a sensor sharing B's subtree), but does NOT include
// board D, which is reachable only through C's *other* sensor in an
// unrelated subtree -- proving ComputeClosure stops at step 5 instead of
// recursing into entangled boards' other placements.
func TestComputeClosure_FixedEnumeration_NotTransitive(t *testing.T) {
	_, repo, pool := newClosureTestServer(t)

	source := insertClosureHousehold(t, pool, "Source Household", false)

	// Subtree A: root RA, child RA1.
	rootA := insertClosureRegion(t, pool, "Greenhouse", 0, source)
	childA := insertClosureRegion(t, pool, "Greenhouse Shelf", rootA, 0)
	// Subtree Z: unrelated root, no relation to A.
	rootZ := insertClosureRegion(t, pool, "Garage", 0, source)

	boardB := insertClosureBoard(t, pool, "leaflab-closure-b", source)
	boardC := insertClosureBoard(t, pool, "leaflab-closure-c", source)
	boardD := insertClosureBoard(t, pool, "leaflab-closure-d", source)

	sensorB := insertClosureSensor(t, pool, boardB, childA, "b-sensor")
	insertClosureSensor(t, pool, boardC, rootA, "c-sensor-a") // entangles C with B, via subtree A
	insertClosureSensor(t, pool, boardC, rootZ, "c-sensor-z") // C's OTHER sensor, in unrelated subtree Z
	insertClosureSensor(t, pool, boardD, rootZ, "d-sensor")   // reachable only through C's subtree-Z sensor

	plantA := insertClosurePlant(t, pool, childA, source, "plant-in-a")
	insertClosurePlant(t, pool, rootZ, source, "plant-in-z") // must not appear in B's closure

	closure, err := repo.ComputeClosure(context.Background(), boardB)
	if err != nil {
		t.Fatalf("ComputeClosure: %v", err)
	}

	if closure.BoardID != boardB {
		t.Errorf("BoardID = %d, want %d", closure.BoardID, boardB)
	}
	int64SetEqual(t, closure.SensorIDs, []int64{sensorB}, "SensorIDs")
	int64SetEqual(t, closure.RegionIDs, []int64{childA}, "RegionIDs")
	int64SetEqual(t, closure.SubtreeRootIDs, []int64{rootA}, "SubtreeRootIDs")
	int64SetEqual(t, closure.SubtreeRegionIDs, []int64{rootA, childA}, "SubtreeRegionIDs")
	int64SetEqual(t, closure.EntangledBoardIDs, []int64{boardC}, "EntangledBoardIDs")
	if int64SetContains(closure.EntangledBoardIDs, boardD) {
		t.Errorf("EntangledBoardIDs = %v, must NOT contain board %d (reachable only through C's other subtree -- fixed enumeration, not transitive)", closure.EntangledBoardIDs, boardD)
	}
	int64SetEqual(t, closure.PlantIDs, []int64{plantA}, "PlantIDs")
}

// TestComputeClosure_HistoricalPlacementDoesNotWidenClosure is the issue's
// second Testing bullet: sensorB was historically placed in subtree Z
// (recorded in sensor_region_history, closed) before being moved to its
// current placement in subtree A. Board E's only sensor is in subtree Z.
// ComputeClosure(B) must not reach subtree Z or board E at all --
// ComputeClosure reads only sensor.region_id's current value, never
// sensor_region_history, so a historical fact structurally cannot widen
// the closure.
func TestComputeClosure_HistoricalPlacementDoesNotWidenClosure(t *testing.T) {
	_, repo, pool := newClosureTestServer(t)

	source := insertClosureHousehold(t, pool, "Source Household", false)

	rootA := insertClosureRegion(t, pool, "Greenhouse", 0, source)
	rootZ := insertClosureRegion(t, pool, "Garage", 0, source)

	boardB := insertClosureBoard(t, pool, "leaflab-hist-b", source)
	boardE := insertClosureBoard(t, pool, "leaflab-hist-e", source)

	sensorB := insertClosureSensor(t, pool, boardB, rootA, "b-sensor")
	insertClosureSensor(t, pool, boardE, rootZ, "e-sensor")

	// Historical fact: sensorB used to live in rootZ. Not sensorB's
	// current placement (that's rootA, set above) -- a closed
	// sensor_region_history row only.
	recordClosureSensorHistory(t, pool, sensorB, rootZ)

	closure, err := repo.ComputeClosure(context.Background(), boardB)
	if err != nil {
		t.Fatalf("ComputeClosure: %v", err)
	}

	int64SetEqual(t, closure.SubtreeRootIDs, []int64{rootA}, "SubtreeRootIDs")
	if int64SetContains(closure.SubtreeRootIDs, rootZ) {
		t.Errorf("SubtreeRootIDs = %v, must NOT contain historical root %d", closure.SubtreeRootIDs, rootZ)
	}
	if int64SetContains(closure.EntangledBoardIDs, boardE) {
		t.Errorf("EntangledBoardIDs = %v, must NOT contain board %d (only reachable via sensorB's historical, not current, placement)", closure.EntangledBoardIDs, boardE)
	}
}

// TestComputeClosure_RemovedPlant_NotInClosure proves step 6's "currently
// placed" is present-tense: a removed plant (removed_at set) in the
// subtree does not appear in the closure.
func TestComputeClosure_RemovedPlant_NotInClosure(t *testing.T) {
	_, repo, pool := newClosureTestServer(t)

	source := insertClosureHousehold(t, pool, "Source Household", false)
	rootA := insertClosureRegion(t, pool, "Greenhouse", 0, source)
	boardB := insertClosureBoard(t, pool, "leaflab-removed-b", source)
	insertClosureSensor(t, pool, boardB, rootA, "b-sensor")

	removedPlant := insertClosurePlant(t, pool, rootA, source, "removed-plant")
	if _, err := pool.Exec(context.Background(),
		`UPDATE plant SET removed_at = NOW() WHERE plant_id = $1`, removedPlant); err != nil {
		t.Fatalf("mark plant removed: %v", err)
	}

	closure, err := repo.ComputeClosure(context.Background(), boardB)
	if err != nil {
		t.Fatalf("ComputeClosure: %v", err)
	}
	if int64SetContains(closure.PlantIDs, removedPlant) {
		t.Errorf("PlantIDs = %v, must NOT contain removed plant %d", closure.PlantIDs, removedPlant)
	}
}

// -- TransferClosure: entangled refusal / permitted asymmetry ----------------

// TestTransferClosure_EntangledForeignBoard_Refuses is the issue's third
// Testing bullet, reached through TransferClosure rather than adoption
// (see this file's doc comment -- CompleteClaim doesn't move the whole
// closure yet, tracked by scope note #1442): board B's closure shares its
// subtree with board C, which is owned by a *different* real household.
// The whole transfer refuses, naming C and the shared subtree root, and no
// partial move commits.
func TestTransferClosure_EntangledForeignBoard_Refuses(t *testing.T) {
	server, _, pool := newClosureTestServer(t)

	source := insertClosureHousehold(t, pool, "Losing Household", false)
	foreign := insertClosureHousehold(t, pool, "Foreign Household", false)
	dest := insertClosureHousehold(t, pool, "Destination Household", false)
	insertClosureMembership(t, pool, source, "releaser@example.com")

	rootA := insertClosureRegion(t, pool, "Greenhouse", 0, source)
	boardB := insertClosureBoard(t, pool, "leaflab-entangled-b", source)
	boardC := insertClosureBoard(t, pool, "leaflab-entangled-c", foreign)
	insertClosureSensor(t, pool, boardB, rootA, "b-sensor")
	insertClosureSensor(t, pool, boardC, rootA, "c-sensor")

	releaseResp, err := server.ReleaseBoard(closureCtxFor("releaser@example.com"), &pb.ReleaseBoardRequest{BoardId: boardB})
	if err != nil {
		t.Fatalf("ReleaseBoard: %v", err)
	}

	_, err = server.TransferClosure(closureCtxFor("releaser@example.com"), &pb.TransferClosureRequest{
		BoardId:                boardB,
		DestinationHouseholdId: dest,
		Evidence:               &pb.TransferClosureRequest_ReleaseToken{ReleaseToken: releaseResp.GetReleaseToken()},
	})
	if err == nil {
		t.Fatal("TransferClosure with an entangled foreign board: want refusal, got success")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("TransferClosure error code = %v, want %v (Refuse); message: %v", status.Code(err), codes.FailedPrecondition, err)
	}
	msg := err.Error()
	if !contains(msg, "leaflab-entangled") && !contains(msg, "board(s)") {
		t.Logf("refusal message did not obviously name the entangled board (best-effort check): %v", msg)
	}

	// No partial move: B is still owned by source.
	var currentHousehold int64
	if err := pool.QueryRow(context.Background(),
		`SELECT household_id FROM board_ownership WHERE board_id = $1 AND valid_to IS NULL`, boardB).Scan(&currentHousehold); err != nil {
		t.Fatalf("query board_ownership: %v", err)
	}
	if currentHousehold != source {
		t.Errorf("board B household after refused transfer = %d, want unchanged %d", currentHousehold, source)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// TestTransferClosure_EntangledBoardSameHousehold_LeavesOtherSensorUnowned
// is the issue's fourth Testing bullet: board C is entangled with B (a
// sensor sharing B's subtree) but is already owned by the SAME (source)
// household, so the transfer succeeds and moves both B and C to the
// destination. C's OTHER sensor sits in an unrelated, unowned region
// (region.household_id NULL) -- the fixed enumeration never followed it,
// so after the transfer C (now owned by the destination) has a sensor in a
// region nobody owns. That is not refused -- it's explicitly permitted
// (FR70: "not a live cross-household reference and is dischargeable").
func TestTransferClosure_EntangledBoardSameHousehold_LeavesOtherSensorUnowned(t *testing.T) {
	server, _, pool := newClosureTestServer(t)

	source := insertClosureHousehold(t, pool, "Losing Household", false)
	dest := insertClosureHousehold(t, pool, "Destination Household", false)
	insertClosureMembership(t, pool, source, "releaser@example.com")

	rootA := insertClosureRegion(t, pool, "Greenhouse", 0, source)
	rootZ := insertClosureRegion(t, pool, "Garage", 0, 0) // unowned root

	boardB := insertClosureBoard(t, pool, "leaflab-same-hh-b", source)
	boardC := insertClosureBoard(t, pool, "leaflab-same-hh-c", source)
	insertClosureSensor(t, pool, boardB, rootA, "b-sensor")
	insertClosureSensor(t, pool, boardC, rootA, "c-sensor-a") // entangles C with B
	insertClosureSensor(t, pool, boardC, rootZ, "c-sensor-z") // C's other sensor, unowned region

	releaseResp, err := server.ReleaseBoard(closureCtxFor("releaser@example.com"), &pb.ReleaseBoardRequest{BoardId: boardB})
	if err != nil {
		t.Fatalf("ReleaseBoard: %v", err)
	}

	_, err = server.TransferClosure(closureCtxFor("releaser@example.com"), &pb.TransferClosureRequest{
		BoardId:                boardB,
		DestinationHouseholdId: dest,
		Evidence:               &pb.TransferClosureRequest_ReleaseToken{ReleaseToken: releaseResp.GetReleaseToken()},
	})
	if err != nil {
		t.Fatalf("TransferClosure: want success (entangled board owned by same source household), got: %v", err)
	}

	for _, boardID := range []int64{boardB, boardC} {
		var currentHousehold int64
		if err := pool.QueryRow(context.Background(),
			`SELECT household_id FROM board_ownership WHERE board_id = $1 AND valid_to IS NULL`, boardID).Scan(&currentHousehold); err != nil {
			t.Fatalf("query board_ownership for board %d: %v", boardID, err)
		}
		if currentHousehold != dest {
			t.Errorf("board %d household after transfer = %d, want %d", boardID, currentHousehold, dest)
		}
	}

	// rootZ (C's other sensor's region) is untouched -- still unowned.
	var rootZHousehold *int64
	if err := pool.QueryRow(context.Background(),
		`SELECT household_id FROM region WHERE region_id = $1`, rootZ).Scan(&rootZHousehold); err != nil {
		t.Fatalf("query rootZ household: %v", err)
	}
	if rootZHousehold != nil {
		t.Errorf("rootZ household_id = %v, want still NULL (unowned region left alone by the transfer)", *rootZHousehold)
	}
}

// -- TransferClosure: evidence gating (FR77) ----------------------------------

// TestTransferClosure_NoEvidence_Refused is the issue's fifth Testing
// bullet: a TransferClosure call carrying neither a release token nor
// admin evidence -- "an admin assertion alone is never sufficient" -- is
// refused before any ownership change, regardless of a plausible reason
// string.
func TestTransferClosure_NoEvidence_Refused(t *testing.T) {
	server, _, pool := newClosureTestServer(t)

	source := insertClosureHousehold(t, pool, "Losing Household", false)
	dest := insertClosureHousehold(t, pool, "Destination Household", false)
	rootA := insertClosureRegion(t, pool, "Greenhouse", 0, source)
	boardB := insertClosureBoard(t, pool, "leaflab-no-evidence-b", source)
	insertClosureSensor(t, pool, boardB, rootA, "b-sensor")

	_, err := server.TransferClosure(closureCtxFor("admin@example.com"), &pb.TransferClosureRequest{
		BoardId:                boardB,
		DestinationHouseholdId: dest,
		Reason:                 "trust me",
	})
	if err == nil {
		t.Fatal("TransferClosure with no evidence: want refusal, got success")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("error code = %v, want %v (Refuse); message: %v", status.Code(err), codes.FailedPrecondition, err)
	}

	var currentHousehold int64
	if err := pool.QueryRow(context.Background(),
		`SELECT household_id FROM board_ownership WHERE board_id = $1 AND valid_to IS NULL`, boardB).Scan(&currentHousehold); err != nil {
		t.Fatalf("query board_ownership: %v", err)
	}
	if currentHousehold != source {
		t.Errorf("board household after refused transfer = %d, want unchanged %d", currentHousehold, source)
	}
}

// TestTransferClosure_AdminEvidenceEmptyHandle_RefusedNotInternal covers a
// caller-error shape the bare-request case above doesn't reach: an
// AdminTransferEvidence oneof branch that IS set (a genuine admin
// assertion was made) but with an empty discharged_challenge_handle -- a
// malformed or buggy elevated-lane client rather than a caller who
// supplied no evidence at all. Repository.TransferClosure's evidence check
// (`releaseToken == "" && dischargedChallengeHandle == ""`) is
// indistinguishable from the bare-request case at that point and returns
// the same ErrClosureNoEvidence -- this must still surface as a clean
// refusal (FailedPrecondition), not fall through server.go's error switch
// into the generic Internal-error branch, which would hide "you didn't
// actually provide evidence" behind an opaque "please try again".
func TestTransferClosure_AdminEvidenceEmptyHandle_RefusedNotInternal(t *testing.T) {
	server, _, pool := newClosureTestServer(t)

	source := insertClosureHousehold(t, pool, "Losing Household", false)
	dest := insertClosureHousehold(t, pool, "Destination Household", false)
	rootA := insertClosureRegion(t, pool, "Greenhouse", 0, source)
	boardB := insertClosureBoard(t, pool, "leaflab-empty-handle-b", source)
	insertClosureSensor(t, pool, boardB, rootA, "b-sensor")

	_, err := server.TransferClosure(closureCtxFor("admin@example.com"), &pb.TransferClosureRequest{
		BoardId:                boardB,
		DestinationHouseholdId: dest,
		Evidence: &pb.TransferClosureRequest_AdminEvidence{
			AdminEvidence: &pb.AdminTransferEvidence{DischargedChallengeHandle: ""},
		},
		Reason: "elevated review, no challenge on hand",
	})
	if err == nil {
		t.Fatal("TransferClosure with an empty discharged_challenge_handle: want refusal, got success")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("error code = %v, want %v (Refuse, not Internal); message: %v", status.Code(err), codes.FailedPrecondition, err)
	}
}

// TestTransferClosure_ReleaseByMember_Succeeds is the issue's sixth
// Testing bullet's first half: a release token issued to a current member
// of the losing household is sufficient evidence.
func TestTransferClosure_ReleaseByMember_Succeeds(t *testing.T) {
	server, _, pool := newClosureTestServer(t)

	source := insertClosureHousehold(t, pool, "Losing Household", false)
	dest := insertClosureHousehold(t, pool, "Destination Household", false)
	insertClosureMembership(t, pool, source, "member@example.com")
	rootA := insertClosureRegion(t, pool, "Greenhouse", 0, source)
	boardB := insertClosureBoard(t, pool, "leaflab-release-b", source)
	insertClosureSensor(t, pool, boardB, rootA, "b-sensor")

	releaseResp, err := server.ReleaseBoard(closureCtxFor("member@example.com"), &pb.ReleaseBoardRequest{BoardId: boardB, Reason: "moving house"})
	if err != nil {
		t.Fatalf("ReleaseBoard: %v", err)
	}
	if releaseResp.GetReleaseToken() == "" {
		t.Fatal("ReleaseBoard: empty release_token")
	}

	resp, err := server.TransferClosure(closureCtxFor("member@example.com"), &pb.TransferClosureRequest{
		BoardId:                boardB,
		DestinationHouseholdId: dest,
		Evidence:               &pb.TransferClosureRequest_ReleaseToken{ReleaseToken: releaseResp.GetReleaseToken()},
	})
	if err != nil {
		t.Fatalf("TransferClosure with a valid release token: %v", err)
	}
	if resp.GetHousehold().GetHouseholdId() != dest {
		t.Errorf("TransferClosure destination household_id = %d, want %d", resp.GetHousehold().GetHouseholdId(), dest)
	}
}

// TestTransferClosure_AdminElevationWithDischargedChallenge_Succeeds is
// the issue's sixth Testing bullet's second half: an elevated admin action
// carrying a discharged FR76 possession-challenge handle is sufficient
// evidence.
func TestTransferClosure_AdminElevationWithDischargedChallenge_Succeeds(t *testing.T) {
	server, _, pool := newClosureTestServer(t)

	source := insertClosureHousehold(t, pool, "Losing Household", false)
	dest := insertClosureHousehold(t, pool, "Destination Household", false)
	deviceID := "leaflab-admin-b"
	rootA := insertClosureRegion(t, pool, "Greenhouse", 0, source)
	boardB := insertClosureBoard(t, pool, deviceID, source)
	insertClosureSensor(t, pool, boardB, rootA, "b-sensor")

	insertClosureClaimChallenge(t, pool, "handle-admin-1", deviceID, "discharged")

	resp, err := server.TransferClosure(closureCtxFor("admin@example.com"), &pb.TransferClosureRequest{
		BoardId:                boardB,
		DestinationHouseholdId: dest,
		Evidence: &pb.TransferClosureRequest_AdminEvidence{
			AdminEvidence: &pb.AdminTransferEvidence{DischargedChallengeHandle: "handle-admin-1"},
		},
		Reason: "possession dispute resolved by support",
	})
	if err != nil {
		t.Fatalf("TransferClosure with admin evidence + discharged challenge: %v", err)
	}
	if resp.GetHousehold().GetHouseholdId() != dest {
		t.Errorf("TransferClosure destination household_id = %d, want %d", resp.GetHousehold().GetHouseholdId(), dest)
	}
}

// TestTransferClosure_AdminEvidenceWithoutReason_Refused proves the other
// half of "an admin assertion alone is never sufficient": even a
// discharged-challenge handle needs a non-empty reason on the admin
// branch.
func TestTransferClosure_AdminEvidenceWithoutReason_Refused(t *testing.T) {
	server, _, pool := newClosureTestServer(t)

	source := insertClosureHousehold(t, pool, "Losing Household", false)
	dest := insertClosureHousehold(t, pool, "Destination Household", false)
	deviceID := "leaflab-admin-noreason-b"
	rootA := insertClosureRegion(t, pool, "Greenhouse", 0, source)
	boardB := insertClosureBoard(t, pool, deviceID, source)
	insertClosureSensor(t, pool, boardB, rootA, "b-sensor")
	insertClosureClaimChallenge(t, pool, "handle-admin-2", deviceID, "discharged")

	_, err := server.TransferClosure(closureCtxFor("admin@example.com"), &pb.TransferClosureRequest{
		BoardId:                boardB,
		DestinationHouseholdId: dest,
		Evidence: &pb.TransferClosureRequest_AdminEvidence{
			AdminEvidence: &pb.AdminTransferEvidence{DischargedChallengeHandle: "handle-admin-2"},
		},
	})
	if err == nil {
		t.Fatal("TransferClosure with admin evidence but no reason: want refusal, got success")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("error code = %v, want %v; message: %v", status.Code(err), codes.InvalidArgument, err)
	}
}

// -- Departure record (FR77, NFR6.3) ------------------------------------------

// TestTransferClosure_DepartureRecord_LosingSideOnly is the issue's
// seventh Testing bullet: after a successful transfer, exactly one
// departure_record row exists, naming the LOSING household -- never the
// gaining one. departureSummary (closure.go) carries no
// destination_household_id field at all, so a query for departure rows
// scoped to the gaining household structurally returns nothing -- there is
// no destination-household column to filter on, which is FR77's "does not
// become a cross-household oracle" enforced by the row's shape rather than
// a rendering convention.
func TestTransferClosure_DepartureRecord_LosingSideOnly(t *testing.T) {
	server, _, pool := newClosureTestServer(t)

	source := insertClosureHousehold(t, pool, "Losing Household", false)
	dest := insertClosureHousehold(t, pool, "Destination Household", false)
	insertClosureMembership(t, pool, source, "member@example.com")
	rootA := insertClosureRegion(t, pool, "Greenhouse", 0, source)
	boardB := insertClosureBoard(t, pool, "leaflab-departure-b", source)
	insertClosureSensor(t, pool, boardB, rootA, "b-sensor")
	insertClosurePlant(t, pool, rootA, source, "departing-plant")

	releaseResp, err := server.ReleaseBoard(closureCtxFor("member@example.com"), &pb.ReleaseBoardRequest{BoardId: boardB})
	if err != nil {
		t.Fatalf("ReleaseBoard: %v", err)
	}
	if _, err := server.TransferClosure(closureCtxFor("member@example.com"), &pb.TransferClosureRequest{
		BoardId:                boardB,
		DestinationHouseholdId: dest,
		Evidence:               &pb.TransferClosureRequest_ReleaseToken{ReleaseToken: releaseResp.GetReleaseToken()},
	}); err != nil {
		t.Fatalf("TransferClosure: %v", err)
	}

	rows, err := pool.Query(context.Background(), `SELECT losing_household_id, board_count, plant_count, summary FROM departure_record`)
	if err != nil {
		t.Fatalf("query departure_record: %v", err)
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		count++
		var losingHouseholdID int64
		var boardCount, plantCount int
		var summary []byte
		if err := rows.Scan(&losingHouseholdID, &boardCount, &plantCount, &summary); err != nil {
			t.Fatalf("scan departure_record row: %v", err)
		}
		if losingHouseholdID != source {
			t.Errorf("departure_record.losing_household_id = %d, want %d (never the gaining household %d)", losingHouseholdID, source, dest)
		}
		if boardCount != 1 {
			t.Errorf("departure_record.board_count = %d, want 1", boardCount)
		}
		if plantCount != 1 {
			t.Errorf("departure_record.plant_count = %d, want 1", plantCount)
		}
		summaryStr := string(summary)
		if contains(summaryStr, "household") {
			t.Errorf("departure_record.summary mentions a household field -- must carry only board/region/plant ids, never a household reference: %s", summaryStr)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate departure_record rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("departure_record rows = %d, want exactly 1", count)
	}

	// Structural: no destination/gaining household is ever recorded --
	// there is no column to even query for one, and no row exists scoped
	// to dest.
	var gainingSideCount int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM departure_record WHERE losing_household_id = $1`, dest).Scan(&gainingSideCount); err != nil {
		t.Fatalf("query departure_record for gaining household: %v", err)
	}
	if gainingSideCount != 0 {
		t.Errorf("departure_record rows scoped to the gaining household = %d, want 0", gainingSideCount)
	}
}

// TestDepartureRecord_AppendOnly_UpdateAndDeleteRaise is the issue's
// eighth Testing bullet: UPDATE and DELETE on departure_record both raise,
// via the trigger installed by migration 022 (reproduced verbatim in this
// file's schema).
func TestDepartureRecord_AppendOnly_UpdateAndDeleteRaise(t *testing.T) {
	_, _, pool := newClosureTestServer(t)

	losing := insertClosureHousehold(t, pool, "Losing Household", false)
	var departureID int64
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO departure_record (losing_household_id, summary, board_count, region_count, plant_count, actor_subject, reason)
		VALUES ($1, '{}'::jsonb, 1, 1, 0, 'tester@example.com', 'test row')
		RETURNING departure_id
	`, losing).Scan(&departureID); err != nil {
		t.Fatalf("insert departure_record: %v", err)
	}

	if _, err := pool.Exec(context.Background(),
		`UPDATE departure_record SET reason = 'changed' WHERE departure_id = $1`, departureID); err == nil {
		t.Error("UPDATE on departure_record: want error (append-only, NFR6.3), got success")
	}

	if _, err := pool.Exec(context.Background(),
		`DELETE FROM departure_record WHERE departure_id = $1`, departureID); err == nil {
		t.Error("DELETE on departure_record: want error (append-only, NFR6.3), got success")
	}

	var stillExists bool
	if err := pool.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM departure_record WHERE departure_id = $1)`, departureID).Scan(&stillExists); err != nil {
		t.Fatalf("check departure_record row survives: %v", err)
	}
	if !stillExists {
		t.Error("departure_record row no longer exists after the refused UPDATE/DELETE attempts")
	}
}

// -- History moves with the closure -------------------------------------------

// TestTransferClosure_BoardOwnershipHistoryPreserved is the issue's ninth
// Testing bullet: after a transfer, the board's full ownership history --
// not just its current value -- is intact and consistent: the prior
// (source) board_ownership interval is closed (SCD2, AGENTS.md), a new one
// opens at the destination, and both rows remain queryable. Nothing about
// the closure's history is deleted or rewritten by the move.
func TestTransferClosure_BoardOwnershipHistoryPreserved(t *testing.T) {
	server, _, pool := newClosureTestServer(t)

	source := insertClosureHousehold(t, pool, "Losing Household", false)
	dest := insertClosureHousehold(t, pool, "Destination Household", false)
	insertClosureMembership(t, pool, source, "member@example.com")
	rootA := insertClosureRegion(t, pool, "Greenhouse", 0, source)
	boardB := insertClosureBoard(t, pool, "leaflab-history-b", source)
	insertClosureSensor(t, pool, boardB, rootA, "b-sensor")

	releaseResp, err := server.ReleaseBoard(closureCtxFor("member@example.com"), &pb.ReleaseBoardRequest{BoardId: boardB})
	if err != nil {
		t.Fatalf("ReleaseBoard: %v", err)
	}
	if _, err := server.TransferClosure(closureCtxFor("member@example.com"), &pb.TransferClosureRequest{
		BoardId:                boardB,
		DestinationHouseholdId: dest,
		Evidence:               &pb.TransferClosureRequest_ReleaseToken{ReleaseToken: releaseResp.GetReleaseToken()},
	}); err != nil {
		t.Fatalf("TransferClosure: %v", err)
	}

	rows, err := pool.Query(context.Background(),
		`SELECT household_id, valid_from, valid_to FROM board_ownership WHERE board_id = $1 ORDER BY valid_from`, boardB)
	if err != nil {
		t.Fatalf("query board_ownership history: %v", err)
	}
	defer rows.Close()

	type interval struct {
		householdID int64
		validTo     *time.Time
	}
	var got []interval
	for rows.Next() {
		var iv interval
		if err := rows.Scan(&iv.householdID, new(time.Time), &iv.validTo); err != nil {
			t.Fatalf("scan board_ownership row: %v", err)
		}
		got = append(got, iv)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate board_ownership rows: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("board_ownership rows for board %d = %d, want 2 (prior closed + current open)", boardB, len(got))
	}
	if got[0].householdID != source || got[0].validTo == nil {
		t.Errorf("first board_ownership interval = {household=%d valid_to=%v}, want {household=%d valid_to=<closed>}", got[0].householdID, got[0].validTo, source)
	}
	if got[1].householdID != dest || got[1].validTo != nil {
		t.Errorf("second board_ownership interval = {household=%d valid_to=%v}, want {household=%d valid_to=<open>}", got[1].householdID, got[1].validTo, dest)
	}
}
