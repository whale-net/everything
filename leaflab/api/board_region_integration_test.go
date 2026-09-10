//go:build integration

// Real-Postgres integration coverage for SetBoardRegion (#2315, FR10 board
// recorded region, bookkeeping only) -- the issue's Testing criteria: set/
// change/clear produce correct board_region_history rows (SCD2 close-and-open
// per AGENTS.md section SCD2) with the board.region_id mirror kept in sync;
// sensor placement (sensor.region_id / sensor_region_history) and reading
// attribution (sensor_reading.region_id) are provably untouched; a non-owner
// is rejected with no write at all; NFR2's admin bypass lets an admin write
// on an owner's behalf; and the response carries the board's sensors with
// their current regions -- the read-only FR11 nudge snapshot.
//
// These tests drive the real server handler (LeafLabAPIServer.SetBoardRegion)
// against the real Repository on a database built from the real migrations
// (newLeafLabTestPool), so authz, handler logic, and the SQL write path are
// all exercised end-to-end. Same pattern as repository_integration_test.go --
// see that file's doc comment and //libs/go/dbtest's README for how to run:
//
//	bazel test //leaflab/api:api_integration_test --test_output=all
package main

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/grpcauth"
)

// boardRegionNopPublisher satisfies configPublisher for the test server;
// SetBoardRegion never publishes, so a no-op is exactly right.
type boardRegionNopPublisher struct{}

func (boardRegionNopPublisher) Publish(context.Context, string, string, interface{}) error {
	return nil
}

// boardRegionCallerCtx returns a ctx carrying grpcauth claims for the given
// oidc_sub -- the same identity shape the auth interceptor injects in
// production, so callerUserID resolves it via the real leaflab_user table.
func boardRegionCallerCtx(sub string) context.Context {
	return grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: sub})
}

// newBoardRegionTestServer provisions a real Postgres with the real
// migrations applied and returns the server handler wired to a real
// Repository on it, plus the pool for fixture seeding and direct SQL
// assertions.
func newBoardRegionTestServer(t *testing.T) (*LeafLabAPIServer, *pgxpool.Pool) {
	t.Helper()
	repo, pool := newTestRepository(t)
	return NewLeafLabAPIServer(repo, boardRegionNopPublisher{}, slog.Default()), pool
}

// boardRegionSeedOwnedBoard seeds a leaflab_user, a board owned by them, and
// returns both ids -- SetBoardRegion's precondition is an owned board (an
// unowned one is PermissionDenied even for an admin, NFR2).
func boardRegionSeedOwnedBoard(t *testing.T, pool *pgxpool.Pool, userSub, deviceID string) (int64, int64) {
	t.Helper()
	userID := seedLeafLabUser(t, pool, userSub)
	boardID := seedBoard(t, pool, deviceID)
	openBoardOwnerHistory(t, pool, boardID, userID)
	return userID, boardID
}

// boardRegionSeedPlacedSensor seeds a sensor whose physical placement is
// regionID: the sensor.region_id mirror plus an open sensor_region_history
// row (the same close-and-open shape the placement write path uses, seeded
// directly since placement writes are not this issue's code under test).
func boardRegionSeedPlacedSensor(t *testing.T, pool *pgxpool.Pool, boardID, regionID int64, name string) int64 {
	t.Helper()
	typeID := seedSensorType(t, pool, "temperature")
	sensorID := seedSensor(t, pool, boardID, typeID, name)
	if _, err := pool.Exec(context.Background(),
		`UPDATE sensor SET region_id = $1 WHERE sensor_id = $2`, regionID, sensorID); err != nil {
		t.Fatalf("set sensor.region_id for sensor %d: %v", sensorID, err)
	}
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO sensor_region_history (sensor_id, region_id) VALUES ($1, $2)`, sensorID, regionID); err != nil {
		t.Fatalf("open sensor_region_history for sensor %d in region %d: %v", sensorID, regionID, err)
	}
	return sensorID
}

// boardRegionSeedClosedPlacement adds a closed (valid_to set) historical
// sensor_region_history row -- FR10's "provably unchanged" test must catch
// any attempt to rewrite or drop placement *history*, not just the open row.
func boardRegionSeedClosedPlacement(t *testing.T, pool *pgxpool.Pool, sensorID, regionID int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO sensor_region_history (sensor_id, region_id, valid_from, valid_to)
		VALUES ($1, $2, NOW() - INTERVAL '2 hours', NOW() - INTERVAL '1 hour')`,
		sensorID, regionID); err != nil {
		t.Fatalf("seed closed sensor_region_history for sensor %d: %v", sensorID, err)
	}
}

// boardRegionSeedReading inserts a sensor_reading attributed to regionID --
// readings snapshot sensor.region_id at write time, which is exactly what
// FR10 forbids SetBoardRegion from rewriting. regionID may be nil (an
// unplaced sensor's reading carries no region).
func boardRegionSeedReading(t *testing.T, pool *pgxpool.Pool, sensorID int64, regionID *int64, valid bool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO sensor_reading (sensor_id, region_id, value, valid, uptime_s, recorded_at)
		VALUES ($1, $2, 1.0, $3, 1, NOW())`, sensorID, regionID, valid); err != nil {
		t.Fatalf("seed reading for sensor %d: %v", sensorID, err)
	}
}

// boardRegionGrantAdmin opens an 'admin' leaflab_user_role row -- the NFR2
// grant HasRole reads.
func boardRegionGrantAdmin(t *testing.T, pool *pgxpool.Pool, userID int64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO leaflab_user_role (leaflab_user_id, role) VALUES ($1, 'admin')`, userID); err != nil {
		t.Fatalf("grant admin to user %d: %v", userID, err)
	}
}

// boardRegionHistoryRow is one board_region_history row read back for
// assertions -- validTo is nil for the currently open row.
type boardRegionHistoryRow struct {
	historyID int64
	boardID   int64
	regionID  int64
	validFrom time.Time
	validTo   *time.Time
}

// boardRegionHistoryRows returns every board_region_history row for boardID,
// ordered oldest first, so tests assert both the count and the sequence.
func boardRegionHistoryRows(t *testing.T, pool *pgxpool.Pool, boardID int64) []boardRegionHistoryRow {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT history_id, board_id, region_id, valid_from, valid_to
		 FROM board_region_history WHERE board_id = $1 ORDER BY history_id`, boardID)
	if err != nil {
		t.Fatalf("query board_region_history for board %d: %v", boardID, err)
	}
	defer rows.Close()

	var out []boardRegionHistoryRow
	for rows.Next() {
		var r boardRegionHistoryRow
		if err := rows.Scan(&r.historyID, &r.boardID, &r.regionID, &r.validFrom, &r.validTo); err != nil {
			t.Fatalf("scan board_region_history row: %v", err)
		}
		out = append(out, r)
	}
	require.NoError(t, rows.Err(), "iterate board_region_history rows")
	return out
}

// boardRegionOpenRows filters to the currently open row(s).
func boardRegionOpenRows(t *testing.T, pool *pgxpool.Pool, boardID int64) []boardRegionHistoryRow {
	t.Helper()
	var open []boardRegionHistoryRow
	for _, r := range boardRegionHistoryRows(t, pool, boardID) {
		if r.validTo == nil {
			open = append(open, r)
		}
	}
	return open
}

// boardRegionMirrorRegionID reads the board.region_id mirror column back.
func boardRegionMirrorRegionID(t *testing.T, pool *pgxpool.Pool, boardID int64) *int64 {
	t.Helper()
	var regionID *int64
	if err := pool.QueryRow(context.Background(),
		`SELECT region_id FROM board WHERE board_id = $1`, boardID).Scan(&regionID); err != nil {
		t.Fatalf("read board.region_id mirror for board %d: %v", boardID, err)
	}
	return regionID
}

// boardRegionFindSensor locates one response sensor entry by id.
func boardRegionFindSensor(t *testing.T, sensors []*pb.BoardSensorRegion, sensorID int64) *pb.BoardSensorRegion {
	t.Helper()
	for _, s := range sensors {
		if s.SensorId == sensorID {
			return s
		}
	}
	t.Fatalf("SetBoardRegionResponse.sensors has no sensor %d (got %d entries)", sensorID, len(sensors))
	return nil
}

// -- FR10 snapshots ------------------------------------------------------------
// These snapshots capture the *complete* state FR10 forbids SetBoardRegion
// from touching: sensor_region_history rows (open AND closed), the
// sensor.region_id mirror, and sensor_reading attribution rows. Tests re-read
// them after the write and require.Equal-compare against the pre-write
// snapshot.

type boardRegionPlacementRow struct {
	historyID int64
	sensorID  int64
	regionID  int64
	validFrom time.Time
	validTo   *time.Time
}

type boardRegionSensorMirror struct {
	sensorID int64
	regionID *int64
}

type boardRegionReadingRow struct {
	readingID  int64
	sensorID   int64
	regionID   *int64
	value      float64
	valid      bool
	recordedAt time.Time
}

type boardRegionSensorState struct {
	placements    []boardRegionPlacementRow
	sensorMirrors []boardRegionSensorMirror
	readings      []boardRegionReadingRow
}

// boardRegionSnapshotSensorState captures placement history, the placement
// mirror, and reading attribution for every sensor on the board, in stable
// orders (history_id / sensor_id / reading_id) so require.Equal compares
// element-for-element.
func boardRegionSnapshotSensorState(t *testing.T, pool *pgxpool.Pool, boardID int64) boardRegionSensorState {
	t.Helper()
	ctx := context.Background()

	placementRows, err := pool.Query(ctx, `
		SELECT srh.history_id, srh.sensor_id, srh.region_id, srh.valid_from, srh.valid_to
		FROM sensor_region_history srh
		JOIN sensor s ON s.sensor_id = srh.sensor_id
		WHERE s.board_id = $1
		ORDER BY srh.history_id`, boardID)
	require.NoError(t, err, "query sensor_region_history snapshot")
	defer placementRows.Close()

	var state boardRegionSensorState
	for placementRows.Next() {
		var r boardRegionPlacementRow
		if err := placementRows.Scan(&r.historyID, &r.sensorID, &r.regionID, &r.validFrom, &r.validTo); err != nil {
			t.Fatalf("scan sensor_region_history snapshot row: %v", err)
		}
		state.placements = append(state.placements, r)
	}
	require.NoError(t, placementRows.Err(), "iterate sensor_region_history snapshot")

	mirrorRows, err := pool.Query(ctx, `
		SELECT sensor_id, region_id FROM sensor WHERE board_id = $1 ORDER BY sensor_id`, boardID)
	require.NoError(t, err, "query sensor.region_id mirror snapshot")
	defer mirrorRows.Close()
	for mirrorRows.Next() {
		var m boardRegionSensorMirror
		if err := mirrorRows.Scan(&m.sensorID, &m.regionID); err != nil {
			t.Fatalf("scan sensor.region_id mirror row: %v", err)
		}
		state.sensorMirrors = append(state.sensorMirrors, m)
	}
	require.NoError(t, mirrorRows.Err(), "iterate sensor.region_id mirror snapshot")

	readingRows, err := pool.Query(ctx, `
		SELECT reading_id, sensor_id, region_id, value, valid, recorded_at
		FROM sensor_reading
		WHERE sensor_id IN (SELECT sensor_id FROM sensor WHERE board_id = $1)
		ORDER BY reading_id`, boardID)
	require.NoError(t, err, "query sensor_reading attribution snapshot")
	defer readingRows.Close()
	for readingRows.Next() {
		var r boardRegionReadingRow
		if err := readingRows.Scan(&r.readingID, &r.sensorID, &r.regionID, &r.value, &r.valid, &r.recordedAt); err != nil {
			t.Fatalf("scan sensor_reading snapshot row: %v", err)
		}
		state.readings = append(state.readings, r)
	}
	require.NoError(t, readingRows.Err(), "iterate sensor_reading snapshot")

	return state
}

// -- Testing criterion 1+3: set writes exactly one open history row, syncs
// the mirror, and the response carries the FR11 nudge snapshot. ---------------

// TestSetBoardRegion_Set_OpensOneRowSyncsMirrorCarriesNudge proves the set
// path end-to-end: exactly one open board_region_history row with the
// recorded region, the board.region_id mirror matching it, and a response
// that carries the board's sensors with their current regions (placed sensor
// resolves id+name; unplaced sensor carries no region) -- everything FR11's
// UI nudge needs, read-only.
func TestSetBoardRegion_Set_OpensOneRowSyncsMirrorCarriesNudge(t *testing.T) {
	srv, pool := newBoardRegionTestServer(t)
	_, boardID := boardRegionSeedOwnedBoard(t, pool, "owner-br-set", "board-br-set")

	regionA := migration016SeedRegion(t, pool, "shelf-a", 0)

	placed := boardRegionSeedPlacedSensor(t, pool, boardID, regionA, "soil-probe")
	unplaced := seedSensor(t, pool, boardID, seedSensorType(t, pool, "temperature"), "air-probe")

	resp, err := srv.SetBoardRegion(boardRegionCallerCtx("owner-br-set"),
		&pb.SetBoardRegionRequest{BoardId: boardID, RegionId: &regionA})
	require.NoError(t, err, "owner sets recorded region")

	// Response: post-write state + FR11 nudge snapshot.
	require.Equal(t, boardID, resp.BoardId)
	require.NotNil(t, resp.RegionId, "response must carry the recorded region")
	require.Equal(t, regionA, *resp.RegionId)
	require.Equal(t, "shelf-a", resp.RegionName)
	require.Len(t, resp.Sensors, 2)

	placedEntry := boardRegionFindSensor(t, resp.Sensors, placed)
	require.Equal(t, "soil-probe", placedEntry.SensorName)
	require.NotNil(t, placedEntry.RegionId, "placed sensor's nudge entry carries its current region")
	require.Equal(t, regionA, *placedEntry.RegionId)
	require.Equal(t, "shelf-a", placedEntry.RegionName)

	unplacedEntry := boardRegionFindSensor(t, resp.Sensors, unplaced)
	require.Equal(t, "air-probe", unplacedEntry.SensorName)
	require.Nil(t, unplacedEntry.RegionId, "unplaced sensor carries no region in the nudge snapshot")
	require.Empty(t, unplacedEntry.RegionName)

	// History: exactly one row, open, for the recorded region.
	rows := boardRegionHistoryRows(t, pool, boardID)
	require.Len(t, rows, 1, "set on an unrecorded board writes exactly one history row")
	require.Equal(t, regionA, rows[0].regionID)
	require.Nil(t, rows[0].validTo, "the row must be open")

	// Mirror column matches the history row.
	mirror := boardRegionMirrorRegionID(t, pool, boardID)
	require.NotNil(t, mirror, "board.region_id mirror must be set after recording a region")
	require.Equal(t, regionA, *mirror)
}

// -- Testing criterion 1: change closes the old row and opens exactly one
// new one, with non-overlapping SCD2 intervals. --------------------------------

// TestSetBoardRegion_Change_ClosesOldRowOpensNew_NoOverlap proves the
// close-and-open: after changing the recorded region there are exactly two
// history rows -- the old one closed (valid_to set), the new one open for the
// new region -- the closed interval does not overlap the open one, and the
// mirror column follows the new region.
func TestSetBoardRegion_Change_ClosesOldRowOpensNew_NoOverlap(t *testing.T) {
	srv, pool := newBoardRegionTestServer(t)
	_, boardID := boardRegionSeedOwnedBoard(t, pool, "owner-br-change", "board-br-change")

	regionA := migration016SeedRegion(t, pool, "greenhouse", 0)
	regionB := migration016SeedRegion(t, pool, "annex", 0)

	_, err := srv.SetBoardRegion(boardRegionCallerCtx("owner-br-change"),
		&pb.SetBoardRegionRequest{BoardId: boardID, RegionId: &regionA})
	require.NoError(t, err, "initial set")

	resp, err := srv.SetBoardRegion(boardRegionCallerCtx("owner-br-change"),
		&pb.SetBoardRegionRequest{BoardId: boardID, RegionId: &regionB})
	require.NoError(t, err, "change recorded region")
	require.NotNil(t, resp.RegionId)
	require.Equal(t, regionB, *resp.RegionId)
	require.Equal(t, "annex", resp.RegionName)

	rows := boardRegionHistoryRows(t, pool, boardID)
	require.Len(t, rows, 2, "change writes a close-and-open pair: exactly two rows total")
	require.Equal(t, regionA, rows[0].regionID, "oldest row records the first region")
	require.NotNil(t, rows[0].validTo, "the first region's row must be closed after the change")
	require.Equal(t, regionB, rows[1].regionID, "newest row records the second region")
	require.Nil(t, rows[1].validTo, "the new region's row must be open")

	// SCD2 intervals must not overlap (same NOW() inside the one tx, per the
	// close-and-open pattern in AGENTS.md section SCD2).
	require.False(t, rows[0].validTo.After(rows[1].validFrom),
		"closed row's valid_to (%v) must not come after the open row's valid_from (%v)",
		rows[0].validTo, rows[1].validFrom)

	mirror := boardRegionMirrorRegionID(t, pool, boardID)
	require.NotNil(t, mirror)
	require.Equal(t, regionB, *mirror, "mirror column must track the newly recorded region")
}

// -- Testing criterion 1: clear closes without re-opening, mirror NULLs. -------

// TestSetBoardRegion_Clear_ClosesRowWithoutReopening proves clearing the
// recorded region closes the open history row and opens none (a board's
// un-recorded state is the absence of an open row -- migration 017), NULLs
// the mirror, and still returns the FR11 nudge snapshot read-only.
func TestSetBoardRegion_Clear_ClosesRowWithoutReopening(t *testing.T) {
	srv, pool := newBoardRegionTestServer(t)
	_, boardID := boardRegionSeedOwnedBoard(t, pool, "owner-br-clear", "board-br-clear")

	regionA := migration016SeedRegion(t, pool, "nursery", 0)
	boardRegionSeedPlacedSensor(t, pool, boardID, regionA, "soil-clear")

	_, err := srv.SetBoardRegion(boardRegionCallerCtx("owner-br-clear"),
		&pb.SetBoardRegionRequest{BoardId: boardID, RegionId: &regionA})
	require.NoError(t, err, "initial set")

	resp, err := srv.SetBoardRegion(boardRegionCallerCtx("owner-br-clear"),
		&pb.SetBoardRegionRequest{BoardId: boardID}) // RegionId unset = clear
	require.NoError(t, err, "clear recorded region")
	require.Nil(t, resp.RegionId, "cleared board's response carries no region")
	require.Empty(t, resp.RegionName)
	require.Len(t, resp.Sensors, 1, "nudge snapshot is still read-only populated after a clear")

	rows := boardRegionHistoryRows(t, pool, boardID)
	require.Len(t, rows, 1, "clear closes the existing row and must not open another")
	require.NotNil(t, rows[0].validTo, "the row must be closed")
	require.Empty(t, boardRegionOpenRows(t, pool, boardID), "no open board_region_history row after clear")
	require.Nil(t, boardRegionMirrorRegionID(t, pool, boardID),
		"board.region_id mirror must be NULL after clearing -- un-recorded state is NULL mirror + no open row")
}

// -- Testing criterion 2 (FR10): sensor placement and reading attribution are
// provably unchanged. -----------------------------------------------------------

// TestSetBoardRegion_NeverTouchesSensorPlacementOrReadingAttribution is FR10's
// core guarantee, proven at the data level: a full snapshot of placement
// history (open AND closed sensor_region_history rows), the sensor.region_id
// mirror, and every sensor_reading attribution row is byte-identical before
// and after a set, a change, and a clear.
func TestSetBoardRegion_NeverTouchesSensorPlacementOrReadingAttribution(t *testing.T) {
	srv, pool := newBoardRegionTestServer(t)
	_, boardID := boardRegionSeedOwnedBoard(t, pool, "owner-br-fr10", "board-br-fr10")

	placedIn := migration016SeedRegion(t, pool, "seed-bench", 0)
	otherA := migration016SeedRegion(t, pool, "propagation", 0)
	otherB := migration016SeedRegion(t, pool, "drying-room", 0)

	// A sensor with placement *history*: closed past placement in placedIn's
	// sibling region, open placement, and the mirror column in sync.
	sensorID := boardRegionSeedPlacedSensor(t, pool, boardID, placedIn, "soil-fr10")
	boardRegionSeedClosedPlacement(t, pool, sensorID, otherA)

	// An unplaced sensor on the same board -- the write must not fabricate a
	// placement for it either.
	unplacedID := seedSensor(t, pool, boardID, seedSensorType(t, pool, "humidity"), "air-fr10")

	// Readings attributed to regions: the placed sensor's readings snapshot
	// its placement region, the unplaced sensor's carries no region.
	boardRegionSeedReading(t, pool, sensorID, &placedIn, true)
	boardRegionSeedReading(t, pool, sensorID, &placedIn, false) // invalid readings keep attribution too
	boardRegionSeedReading(t, pool, unplacedID, nil, true)

	before := boardRegionSnapshotSensorState(t, pool, boardID)
	require.NotEmpty(t, before.placements, "fixture must seed placement history")
	require.Len(t, before.readings, 3, "fixture must seed attributed readings")

	// Set, then clear -- two writes, each checked against the original.
	_, err := srv.SetBoardRegion(boardRegionCallerCtx("owner-br-fr10"),
		&pb.SetBoardRegionRequest{BoardId: boardID, RegionId: &otherB})
	require.NoError(t, err, "set recorded region")
	require.Equal(t, before, boardRegionSnapshotSensorState(t, pool, boardID),
		"a set must not change placement history, placement mirrors, or reading attribution (FR10)")

	_, err = srv.SetBoardRegion(boardRegionCallerCtx("owner-br-fr10"),
		&pb.SetBoardRegionRequest{BoardId: boardID}) // clear
	require.NoError(t, err, "clear recorded region")
	require.Equal(t, before, boardRegionSnapshotSensorState(t, pool, boardID),
		"a clear must not change placement history, placement mirrors, or reading attribution (FR10)")
}

// -- Testing criterion 4: non-owner rejected with no write at all. -------------

// TestSetBoardRegion_NonOwnerRejected_NoWriteAtAll proves NFR2's per-owner
// write enforcement: a registered non-owner is PermissionDenied and nothing
// is written -- no board_region_history row, no mirror change, and sensor
// placement/reading state untouched.
func TestSetBoardRegion_NonOwnerRejected_NoWriteAtAll(t *testing.T) {
	srv, pool := newBoardRegionTestServer(t)
	_, boardID := boardRegionSeedOwnedBoard(t, pool, "owner-br-deny", "board-br-deny")
	seedLeafLabUser(t, pool, "intruder-br") // registered, owns nothing, no roles

	regionA := migration016SeedRegion(t, pool, "laboratory", 0)
	sensorID := boardRegionSeedPlacedSensor(t, pool, boardID, regionA, "soil-deny")
	boardRegionSeedReading(t, pool, sensorID, &regionA, true)
	before := boardRegionSnapshotSensorState(t, pool, boardID)

	_, err := srv.SetBoardRegion(boardRegionCallerCtx("intruder-br"),
		&pb.SetBoardRegionRequest{BoardId: boardID, RegionId: &regionA})
	require.Error(t, err, "a non-owner's set attempt must be rejected")
	require.Equal(t, codes.PermissionDenied, status.Code(err),
		"a non-owner's set attempt is codes.PermissionDenied (NFR2)")

	require.Empty(t, boardRegionHistoryRows(t, pool, boardID), "rejected attempt must write no history row")
	require.Nil(t, boardRegionMirrorRegionID(t, pool, boardID), "rejected attempt must not set the mirror")
	require.Equal(t, before, boardRegionSnapshotSensorState(t, pool, boardID),
		"rejected attempt must leave placement and attribution state untouched")
}

// -- NFR2's admin bypass: an admin acts on the owner's behalf. -----------------

// TestSetBoardRegion_AdminBypass_WritesOnOwnersBehalf proves the NFR2
// admin-bypass pattern the issue's authz rides on: an admin (open 'admin'
// grant, not the owner) may record the region of someone else's board, and
// the write is a normal board-region write.
func TestSetBoardRegion_AdminBypass_WritesOnOwnersBehalf(t *testing.T) {
	srv, pool := newBoardRegionTestServer(t)
	_, boardID := boardRegionSeedOwnedBoard(t, pool, "owner-br-admin", "board-br-admin")
	adminID := seedLeafLabUser(t, pool, "admin-br")
	boardRegionGrantAdmin(t, pool, adminID)

	regionA := migration016SeedRegion(t, pool, "atrium", 0)

	resp, err := srv.SetBoardRegion(boardRegionCallerCtx("admin-br"),
		&pb.SetBoardRegionRequest{BoardId: boardID, RegionId: &regionA})
	require.NoError(t, err, "an admin may write on the owner's behalf (NFR2 admin bypass)")
	require.NotNil(t, resp.RegionId)
	require.Equal(t, regionA, *resp.RegionId)

	open := boardRegionOpenRows(t, pool, boardID)
	require.Len(t, open, 1)
	require.Equal(t, regionA, open[0].regionID)
	require.Equal(t, regionA, *boardRegionMirrorRegionID(t, pool, boardID))
}