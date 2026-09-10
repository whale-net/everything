//go:build integration

// Real-Postgres coverage for the M3 region lifecycle repository methods and
// server handlers (#2312: FR1-FR3, FR5, NFR2) -- the parts of the region
// lifecycle that cannot be verified against the in-memory fake in
// server_test.go: the initial open region_parent_history row written at
// creation, the SCD2 close-and-open plus mirror-column sync inside one
// transaction, the recursive-CTE cycle walk, that a rename is a pure
// current-value write (no history churn, no reading reattribution), and the
// owner-plus-admin-bypass write fence enforced against real SQL.
//
// Same harness as repository_integration_test.go: schema setup runs the real
// migrations via newLeafLabTestPool (testdb_integration_test.go), so these
// tests exercise the actual schema rather than a hand-maintained copy of it.
// Run explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api:api_integration_test --test_output=all
package main

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/grpcauth"
)

// -- fixtures -----------------------------------------------------------------

// newRegionLifecycleTestRepo starts a real, throwaway Postgres with the real
// migrations applied (see newLeafLabTestPool) and returns a Repository plus
// the raw pool for fixture setup.
func newRegionLifecycleTestRepo(t *testing.T) (*Repository, *pgxpool.Pool) {
	t.Helper()
	pool := newLeafLabTestPool(t)
	return NewRepository(pool), pool
}

// regionLCSeedUser inserts a leaflab_user row (leaflab-api never creates one
// -- LB1, so tests must) and returns its leaflab_user_id.
func regionLCSeedUser(t *testing.T, pool *pgxpool.Pool, oidcSub string) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO leaflab_user (oidc_sub, display_name) VALUES ($1, $2) RETURNING leaflab_user_id`,
		oidcSub, oidcSub).Scan(&id)
	require.NoError(t, err, "seed leaflab_user %s", oidcSub)
	return id
}

// regionLCSeedAdminGrant opens an 'admin' leaflab_user_role grant -- the
// shape HasRole's real query reads (role = 'admin' AND valid_to IS NULL).
func regionLCSeedAdminGrant(t *testing.T, pool *pgxpool.Pool, userID int64) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO leaflab_user_role (leaflab_user_id, role) VALUES ($1, 'admin')`, userID)
	require.NoError(t, err, "seed admin grant for user %d", userID)
}

// regionParentHistoryRow is one region_parent_history row, compared whole so
// "untouched" assertions catch any churn at all (new rows, re-closed rows,
// shifted intervals), not just changes to the columns a query happened to
// select.
type regionParentHistoryRow struct {
	HistoryID      int64
	ParentRegionID *int64
	ValidFrom      time.Time
	ValidTo        *time.Time
}

// regionLCParentHistoryRows returns all region_parent_history rows for a
// region, ordered by history_id (insertion order -- the SCD2 timeline).
func regionLCParentHistoryRows(t *testing.T, pool *pgxpool.Pool, regionID int64) []regionParentHistoryRow {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT history_id, parent_region_id, valid_from, valid_to
		FROM region_parent_history
		WHERE region_id = $1
		ORDER BY history_id`, regionID)
	require.NoError(t, err, "query region_parent_history for region %d", regionID)
	defer rows.Close()

	var result []regionParentHistoryRow
	for rows.Next() {
		var r regionParentHistoryRow
		require.NoError(t, rows.Scan(&r.HistoryID, &r.ParentRegionID, &r.ValidFrom, &r.ValidTo))
		result = append(result, r)
	}
	require.NoError(t, rows.Err(), "iterate region_parent_history rows")
	return result
}

// regionLCRegionRow reads a region's current-value row (name, mirror parent
// column, owner) for direct DB assertions.
func regionLCRegionRow(t *testing.T, pool *pgxpool.Pool, regionID int64) (name string, parentID, ownerID *int64) {
	t.Helper()
	err := pool.QueryRow(context.Background(), `
		SELECT name, parent_region_id, owner_leaflab_user_id
		FROM region WHERE region_id = $1`, regionID).Scan(&name, &parentID, &ownerID)
	require.NoError(t, err, "read region row %d", regionID)
	return name, parentID, ownerID
}

// regionLCOpenParentRowCount counts a region's open region_parent_history
// rows -- the "exactly one open row at all times" invariant (FR1).
func regionLCOpenParentRowCount(t *testing.T, pool *pgxpool.Pool, regionID int64) int {
	t.Helper()
	var count int
	err := pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM region_parent_history
		WHERE region_id = $1 AND valid_to IS NULL`, regionID).Scan(&count)
	require.NoError(t, err, "count open parent rows for region %d", regionID)
	return count
}

// regionLCSeedSensor seeds a board, sensor_type, and sensor placed in the
// given region (sensor.region_id mirror + one open sensor_region_history
// row), returning the sensor's ID -- the attribution fixtures FR2's
// "rename leaves readings untouched" test snapshots.
func regionLCSeedSensor(t *testing.T, pool *pgxpool.Pool, regionID int64) int64 {
	t.Helper()
	ctx := context.Background()

	var boardID int64
	err := pool.QueryRow(ctx,
		`INSERT INTO board (device_id) VALUES ('region-lc-board') RETURNING board_id`).Scan(&boardID)
	require.NoError(t, err, "seed board")

	// Upsert, not a plain INSERT: migration 001 seeds real sensor_type rows,
	// so a literal name could collide with the seed data.
	var sensorTypeID int64
	err = pool.QueryRow(ctx, `
		INSERT INTO sensor_type (name, default_unit) VALUES ('region-lc-type', 'unit')
		ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
		RETURNING sensor_type_id`).Scan(&sensorTypeID)
	require.NoError(t, err, "seed sensor_type")

	var sensorID int64
	err = pool.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, region_id, name, unit)
		VALUES ($1, $2, $3, 'region-lc-sensor', 'unit')
		RETURNING sensor_id`, boardID, sensorTypeID, regionID).Scan(&sensorID)
	require.NoError(t, err, "seed sensor")

	// Open placement row matching the mirror column, like the real placement
	// write path would leave behind.
	_, err = pool.Exec(ctx,
		`INSERT INTO sensor_region_history (sensor_id, region_id) VALUES ($1, $2)`, sensorID, regionID)
	require.NoError(t, err, "seed sensor_region_history")
	return sensorID
}

// regionLCReadingRow is one sensor_reading row. RegionID is the attribution
// snapshot column FR2 must not rewrite; rows are compared whole after a
// rename so any reattribution or churn shows up as a difference.
type regionLCReadingRow struct {
	ReadingID  int64
	RegionID   *int64
	Value      float64
	Valid      bool
	UptimeS    int
	RecordedAt time.Time
}

// regionLCInsertReadings bulk-inserts n readings for sensorID, each
// snapshotting regionID.
func regionLCInsertReadings(t *testing.T, pool *pgxpool.Pool, sensorID, regionID int64, n int) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO sensor_reading (sensor_id, region_id, value, valid, uptime_s, recorded_at)
		SELECT $1, $2, i, TRUE, 1, $3::timestamptz + (i * interval '1 second')
		FROM generate_series(0, $4::int - 1) AS s(i)`,
		sensorID, regionID, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), n)
	require.NoError(t, err, "insert %d readings", n)
}

// regionLCReadingRows snapshots a sensor's readings.
func regionLCReadingRows(t *testing.T, pool *pgxpool.Pool, sensorID int64) []regionLCReadingRow {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT reading_id, region_id, value, valid, uptime_s, recorded_at
		FROM sensor_reading WHERE sensor_id = $1
		ORDER BY reading_id, recorded_at`, sensorID)
	require.NoError(t, err, "query readings for sensor %d", sensorID)
	defer rows.Close()

	var result []regionLCReadingRow
	for rows.Next() {
		var r regionLCReadingRow
		require.NoError(t, rows.Scan(&r.ReadingID, &r.RegionID, &r.Value, &r.Valid, &r.UptimeS, &r.RecordedAt))
		result = append(result, r)
	}
	require.NoError(t, rows.Err(), "iterate readings")
	return result
}

// regionLCSensorPlacementRow is one sensor_region_history row.
type regionLCSensorPlacementRow struct {
	HistoryID int64
	SensorID  int64
	RegionID  int64
	ValidFrom time.Time
	ValidTo   *time.Time
}

// regionLCSensorPlacementRows snapshots a sensor's placement history.
func regionLCSensorPlacementRows(t *testing.T, pool *pgxpool.Pool, sensorID int64) []regionLCSensorPlacementRow {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT history_id, sensor_id, region_id, valid_from, valid_to
		FROM sensor_region_history WHERE sensor_id = $1
		ORDER BY history_id`, sensorID)
	require.NoError(t, err, "query sensor_region_history for sensor %d", sensorID)
	defer rows.Close()

	var result []regionLCSensorPlacementRow
	for rows.Next() {
		var r regionLCSensorPlacementRow
		require.NoError(t, rows.Scan(&r.HistoryID, &r.SensorID, &r.RegionID, &r.ValidFrom, &r.ValidTo))
		result = append(result, r)
	}
	require.NoError(t, rows.Err(), "iterate sensor_region_history rows")
	return result
}

// regionLCClaimsCtx returns a context carrying grpcauth.Claims for subject,
// the same context shape the real OIDC interceptor would have set up before
// a handler runs (see claimsCtx in server_test.go -- a separate copy here
// because that file lives in the untagged unit target, not this one).
func regionLCClaimsCtx(subject string, roles ...string) context.Context {
	return grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: subject, Roles: roles})
}

// -- Repository.CreateRegion: FR1 (initial open history row, owned) -----------

func TestRegionLifecycle_CreateRegion_InitialOpenHistoryRow(t *testing.T) {
	ctx := context.Background()
	repo, pool := newRegionLifecycleTestRepo(t)
	ownerID := regionLCSeedUser(t, pool, "region-owner")
	parentID := migration016SeedRegion(t, pool, "parent", 0)

	t.Run("nested region gets one open row naming its parent, owned by the creating user", func(t *testing.T) {
		regionID, err := repo.CreateRegion(ctx, "child", &parentID, ownerID)
		require.NoError(t, err)

		name, parent, owner := regionLCRegionRow(t, pool, regionID)
		assert.Equal(t, "child", name)
		require.NotNil(t, parent)
		assert.Equal(t, parentID, *parent)
		require.NotNil(t, owner, "FR1: a created region is never ownerless")
		assert.Equal(t, ownerID, *owner, "FR1: the creating user is the region's owner from creation")

		rows := regionLCParentHistoryRows(t, pool, regionID)
		require.Len(t, rows, 1, "exactly one history row total, from creation")
		require.NotNil(t, rows[0].ParentRegionID)
		assert.Equal(t, parentID, *rows[0].ParentRegionID)
		assert.Nil(t, rows[0].ValidTo, "the initial row must be open")
		assert.Equal(t, 1, regionLCOpenParentRowCount(t, pool, regionID))
	})

	t.Run("top-level region gets one open row recording a NULL parent", func(t *testing.T) {
		regionID, err := repo.CreateRegion(ctx, "top", nil, ownerID)
		require.NoError(t, err)

		_, parent, _ := regionLCRegionRow(t, pool, regionID)
		assert.Nil(t, parent, "top-level region's mirror parent is NULL")

		rows := regionLCParentHistoryRows(t, pool, regionID)
		require.Len(t, rows, 1)
		assert.Nil(t, rows[0].ParentRegionID,
			"top-level must be recorded as a NULL parent on the open row, not an absent row")
		assert.Nil(t, rows[0].ValidTo)
	})
}

// -- Repository.ReparentRegion: FR3 (SCD2 close-and-open + mirror, one tx) ----

func TestRegionLifecycle_ReparentRegion_ClosesOldOpensNew_UpdatesMirror(t *testing.T) {
	ctx := context.Background()
	repo, pool := newRegionLifecycleTestRepo(t)
	ownerID := regionLCSeedUser(t, pool, "region-owner")

	root := migration016SeedRegion(t, pool, "lc-root", 0)
	branchA := migration016SeedRegion(t, pool, "lc-branch-a", root)
	branchB := migration016SeedRegion(t, pool, "lc-branch-b", root)
	moved, err := repo.CreateRegion(ctx, "lc-moved", &branchA, ownerID)
	require.NoError(t, err)
	// A descendant under the moved region: its own row must survive the move
	// untouched (FR3 -- the subtree relocates by construction).
	descendant := migration016SeedRegion(t, pool, "lc-descendant", moved)
	descendantRowsBefore := regionLCParentHistoryRows(t, pool, descendant)

	// First re-parent: moved moves from branch-a to branch-b.
	require.NoError(t, repo.ReparentRegion(ctx, moved, &branchB))

	rows := regionLCParentHistoryRows(t, pool, moved)
	require.Len(t, rows, 2, "one re-parent = exactly one closed row + one open row")
	require.NotNil(t, rows[0].ParentRegionID)
	assert.Equal(t, branchA, *rows[0].ParentRegionID, "the closed row records the old parent")
	assert.NotNil(t, rows[0].ValidTo, "the original row must be closed after a re-parent")
	assert.Nil(t, rows[1].ValidTo, "the new row must be open")
	require.NotNil(t, rows[1].ParentRegionID)
	assert.Equal(t, branchB, *rows[1].ParentRegionID, "the open row records the new parent")

	// Same-transaction SCD2 semantics (AGENTS.md section SCD2): the close and
	// the open share the transaction's NOW(), so the open row's valid_from
	// equals the closed row's valid_to and the value-at-time-T query
	// (valid_from <= T AND (valid_to IS NULL OR valid_to > T)) always sees
	// exactly one row.
	assert.Equal(t, *rows[0].ValidTo, rows[1].ValidFrom,
		"open row's valid_from must equal the closed row's valid_to (same-transaction close-and-open)")

	_, parent, _ := regionLCRegionRow(t, pool, moved)
	require.NotNil(t, parent)
	assert.Equal(t, branchB, *parent,
		"region.parent_region_id mirror must be updated in the same transaction")

	// Descendants untouched: the descendant's history is exactly the one row
	// it had before the move, still open, still naming its immediate parent.
	descendantRows := regionLCParentHistoryRows(t, pool, descendant)
	assert.Equal(t, descendantRowsBefore, descendantRows,
		"FR3: a re-parent touches no descendant row")

	// Second re-parent, back to top-level (a recorded NULL): a new open row,
	// the branch-b row closed, the mirror back to NULL.
	require.NoError(t, repo.ReparentRegion(ctx, moved, nil))

	rows = regionLCParentHistoryRows(t, pool, moved)
	require.Len(t, rows, 3)
	assert.Nil(t, rows[2].ParentRegionID, "top-level re-parent records a NULL parent")
	assert.Nil(t, rows[2].ValidTo, "the newest row must be open")
	assert.True(t, rows[2].ValidFrom.After(rows[1].ValidFrom),
		"the second re-parent's interval starts strictly after the previous one (separate transactions)")

	_, parent, _ = regionLCRegionRow(t, pool, moved)
	assert.Nil(t, parent, "mirror after top-level re-parent is NULL")
	assert.Equal(t, 1, regionLCOpenParentRowCount(t, pool, moved),
		"exactly one open row at all times, after every re-parent")
}

// -- Repository.ReparentCreatesCycle: FR5 (ancestor walk) ---------------------

func TestRegionLifecycle_ReparentCreatesCycle_SelfDescendantDeepChain(t *testing.T) {
	ctx := context.Background()
	repo, pool := newRegionLifecycleTestRepo(t)

	// Deep chain: root -> l2 -> l3 -> l4 -> l5 (root is the top, l5 the
	// deepest leaf), plus an unrelated two-node tree other -> o2.
	root := migration016SeedRegion(t, pool, "cycle-root", 0)
	l2 := migration016SeedRegion(t, pool, "cycle-l2", root)
	l3 := migration016SeedRegion(t, pool, "cycle-l3", l2)
	l4 := migration016SeedRegion(t, pool, "cycle-l4", l3)
	l5 := migration016SeedRegion(t, pool, "cycle-l5", l4)
	other := migration016SeedRegion(t, pool, "cycle-other", 0)
	o2 := migration016SeedRegion(t, pool, "cycle-o2", other)
	tests := []struct {
		name      string
		regionID  int64
		newParent int64
		wantCycle bool
	}{
		{"self", l2, l2, true},
		{"direct child", root, l2, true},
		{"deep chain end", root, l5, true},
		{"mid-chain region under its own deep descendant", l3, l5, true},
		{"child under its own root is not a cycle", l2, root, false},
		{"unrelated branch", root, o2, false},
		{"second tree's child under first tree's root", other, root, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := repo.ReparentCreatesCycle(ctx, tt.regionID, tt.newParent)
			require.NoError(t, err)
			assert.Equal(t, tt.wantCycle, got,
				"ReparentCreatesCycle(region %d under %d)", tt.regionID, tt.newParent)
		})
	}
}

// -- Repository.RenameRegion: FR2 (pure current-value write) ------------------

func TestRegionLifecycle_RenameRegion_LeavesReadingsUntouched(t *testing.T) {
	ctx := context.Background()
	repo, pool := newRegionLifecycleTestRepo(t)
	ownerID := regionLCSeedUser(t, pool, "region-owner")

	regionID, err := repo.CreateRegion(ctx, "garden", nil, ownerID)
	require.NoError(t, err)
	sensorID := regionLCSeedSensor(t, pool, regionID)
	regionLCInsertReadings(t, pool, sensorID, regionID, 3)

	// Whole-row snapshots: anything the rename touches beyond region.name
	// must show up as a difference here.
	readingsBefore := regionLCReadingRows(t, pool, sensorID)
	placementBefore := regionLCSensorPlacementRows(t, pool, sensorID)
	parentHistoryBefore := regionLCParentHistoryRows(t, pool, regionID)
	nameBefore, parentBefore, ownerBefore := regionLCRegionRow(t, pool, regionID)

	require.NoError(t, repo.RenameRegion(ctx, regionID, "market garden"))

	nameAfter, parentAfter, ownerAfter := regionLCRegionRow(t, pool, regionID)
	assert.Equal(t, "market garden", nameAfter)
	assert.NotEqual(t, nameBefore, nameAfter)
	assert.True(t, sameNullableInt64(parentAfter, parentBefore) && sameNullableInt64(ownerAfter, ownerBefore),
		"FR2: a rename changes the name and nothing else on the region row")

	readingsAfter := regionLCReadingRows(t, pool, sensorID)
	assert.Equal(t, readingsBefore, readingsAfter,
		"FR2: a rename must leave every sensor_reading row untouched (past attribution is never re-written)")
	assert.Equal(t, placementBefore, regionLCSensorPlacementRows(t, pool, sensorID),
		"FR2: a rename must not churn sensor_region_history")
	assert.Equal(t, parentHistoryBefore, regionLCParentHistoryRows(t, pool, regionID),
		"FR2: names are current-value with no history table -- a rename must not churn region_parent_history")
}

func sameNullableInt64(a, b *int64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// -- Server handlers over the real repository: NFR2 write fence, FR1 at the
// -- API boundary -------------------------------------------------------------

// TestRegionLifecycle_ServerCreate_WritesOwnerAndInitialHistoryRow proves the
// create RPC end-to-end against real SQL: the calling user's leaflab_user_id
// lands in region.owner_leaflab_user_id (FR1/NFR2) and the region gets its
// open region_parent_history row in the same transaction, for both the
// nested and the top-level case.
func TestRegionLifecycle_ServerCreate_WritesOwnerAndInitialHistoryRow(t *testing.T) {
	repo, pool := newRegionLifecycleTestRepo(t)
	ownerID := regionLCSeedUser(t, pool, "region-owner")
	parentID := migration016SeedRegion(t, pool, "api-parent", 0)
	srv := NewLeafLabAPIServer(repo, nil, slog.Default())

	t.Run("nested under an existing parent", func(t *testing.T) {
		resp, err := srv.CreateRegion(regionLCClaimsCtx("region-owner"),
			&pb.CreateRegionRequest{Name: "api-made", ParentRegionId: parentID})
		require.NoError(t, err)

		name, parent, owner := regionLCRegionRow(t, pool, resp.RegionId)
		assert.Equal(t, "api-made", name)
		require.NotNil(t, parent)
		assert.Equal(t, parentID, *parent)
		require.NotNil(t, owner)
		assert.Equal(t, ownerID, *owner, "FR1: the calling user owns the created region")

		rows := regionLCParentHistoryRows(t, pool, resp.RegionId)
		require.Len(t, rows, 1)
		require.NotNil(t, rows[0].ParentRegionID)
		assert.Equal(t, parentID, *rows[0].ParentRegionID)
		assert.Nil(t, rows[0].ValidTo)
	})

	t.Run("top-level", func(t *testing.T) {
		resp, err := srv.CreateRegion(regionLCClaimsCtx("region-owner"),
			&pb.CreateRegionRequest{Name: "api-top"})
		require.NoError(t, err)

		rows := regionLCParentHistoryRows(t, pool, resp.RegionId)
		require.Len(t, rows, 1)
		assert.Nil(t, rows[0].ParentRegionID, "top-level recorded as NULL on the open row")
	})
}

// TestRegionLifecycle_Server_NonOwnerWriteRejected_OwnerAndAdminAllowed is
// NFR2's fence against the real authorization path (callerUserID ->
// GetLeafLabUserIDBySub -> authorizeRegionWrite -> HasRole, all real SQL):
// a non-owner's rename and re-parent are PermissionDenied and reach no write,
// the owner's succeed, and the admin bypass works through the real
// leaflab_user_role grant.
func TestRegionLifecycle_Server_NonOwnerWriteRejected_OwnerAndAdminAllowed(t *testing.T) {
	ctx := context.Background()
	repo, pool := newRegionLifecycleTestRepo(t)
	ownerID := regionLCSeedUser(t, pool, "region-owner")
	otherID := regionLCSeedUser(t, pool, "region-other")
	_ = otherID // the row must exist for callerUserID; the id itself is never needed
	adminID := regionLCSeedUser(t, pool, "region-admin")
	regionLCSeedAdminGrant(t, pool, adminID)

	ownedRegion, err := repo.CreateRegion(ctx, "owner-garden", nil, ownerID)
	require.NoError(t, err)
	otherRoot := migration016SeedRegion(t, pool, "other-root", 0)

	srv := NewLeafLabAPIServer(repo, nil, slog.Default())

	// Non-owner rename: PermissionDenied, no write reaches the region.
	_, err = srv.RenameRegion(regionLCClaimsCtx("region-other"),
		&pb.RenameRegionRequest{RegionId: ownedRegion, Name: "hijacked"})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	name, _, _ := regionLCRegionRow(t, pool, ownedRegion)
	assert.Equal(t, "owner-garden", name, "a denied rename must not change the name")

	// Non-owner re-parent: PermissionDenied, and neither the mirror column
	// nor the history moves.
	_, err = srv.ReparentRegion(regionLCClaimsCtx("region-other"),
		&pb.ReparentRegionRequest{RegionId: ownedRegion, ParentRegionId: otherRoot})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	_, parent, _ := regionLCRegionRow(t, pool, ownedRegion)
	assert.Nil(t, parent, "a denied re-parent must not move the mirror column")
	assert.Len(t, regionLCParentHistoryRows(t, pool, ownedRegion), 1,
		"a denied re-parent must not churn region_parent_history")

	// Owner rename succeeds and lands in the DB.
	_, err = srv.RenameRegion(regionLCClaimsCtx("region-owner"),
		&pb.RenameRegionRequest{RegionId: ownedRegion, Name: "renamed-by-owner"})
	require.NoError(t, err)
	name, _, _ = regionLCRegionRow(t, pool, ownedRegion)
	assert.Equal(t, "renamed-by-owner", name)

	// Owner re-parent succeeds: SCD2 close-and-open + mirror, end to end.
	_, err = srv.ReparentRegion(regionLCClaimsCtx("region-owner"),
		&pb.ReparentRegionRequest{RegionId: ownedRegion, ParentRegionId: otherRoot})
	require.NoError(t, err)
	_, parent, _ = regionLCRegionRow(t, pool, ownedRegion)
	require.NotNil(t, parent)
	assert.Equal(t, otherRoot, *parent)
	rows := regionLCParentHistoryRows(t, pool, ownedRegion)
	require.Len(t, rows, 2)
	assert.Nil(t, rows[1].ValidTo)
	require.NotNil(t, rows[1].ParentRegionID)
	assert.Equal(t, otherRoot, *rows[1].ParentRegionID)

	// Admin bypass through the real leaflab_user_role grant (NFR2): an admin
	// who does not own the region may rename it.
	_, err = srv.RenameRegion(regionLCClaimsCtx("region-admin", "admin"),
		&pb.RenameRegionRequest{RegionId: ownedRegion, Name: "renamed-by-admin"})
	require.NoError(t, err)
	name, _, _ = regionLCRegionRow(t, pool, ownedRegion)
	assert.Equal(t, "renamed-by-admin", name)
}
