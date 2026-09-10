//go:build integration

// Real-Postgres coverage for the M3 region tree view (#2316: FR6
// GetRegionTree with drill-down and sensor counts) -- the parts the fake
// store in server_test.go cannot verify: the recursive CTE's actual bounding
// (unbounded nesting depth, drill-down subtree scoping), the open
// sensor_region_history aggregation over real SQL (closed/historical
// placement rows must not count), and sibling ordering applied to rows the
// database returns in arbitrary (insertion/ID) order.
//
// Same harness as region_lifecycle_integration_test.go: schema setup runs the
// real migrations via newLeafLabTestPool (testdb_integration_test.go), so
// these tests exercise the actual schema rather than a hand-maintained copy
// of it, and handlers are invoked directly on a real Repository-backed
// server. Run explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api:api_integration_test --test_output=all
package main

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// -- fixtures -----------------------------------------------------------------

// newRegionTreeTestServer starts a real, throwaway Postgres with the real
// migrations applied (see newLeafLabTestPool) and returns a server backed by
// a real Repository plus the raw pool for fixture setup.
func newRegionTreeTestServer(t *testing.T) (*LeafLabAPIServer, *pgxpool.Pool) {
	t.Helper()
	pool := newLeafLabTestPool(t)
	return NewLeafLabAPIServer(NewRepository(pool), nil, slog.Default()), pool
}

// regionTreeSeedBoard seeds one board with a unique device_id (the column is
// UNIQUE, so every call needs a fresh value) and returns its board_id.
func regionTreeSeedBoard(t *testing.T, pool *pgxpool.Pool, deviceID string) int64 {
	t.Helper()
	var boardID int64
	err := pool.QueryRow(context.Background(),
		`INSERT INTO board (device_id) VALUES ($1) RETURNING board_id`, deviceID).
		Scan(&boardID)
	require.NoError(t, err, "seed board %s", deviceID)
	return boardID
}

// regionTreeSeedPlacedSensor seeds a board + sensor currently placed in the
// given region: the sensor.region_id mirror column AND one open
// sensor_region_history row -- exactly what a real placement write leaves
// behind. Returns the sensor's ID. The board device_id and sensor name are
// derived from a monotonically increasing counter (the schema only enforces
// UNIQUE(board_id, name) on sensor, but distinct names keep fixtures legible).
func regionTreeSeedPlacedSensor(t *testing.T, pool *pgxpool.Pool, regionID int64) int64 {
	t.Helper()
	ctx := context.Background()

	regionTreeFixtureN++
	board := regionTreeSeedBoard(t, pool, fmt.Sprintf("region-tree-board-%d", regionTreeFixtureN))

	// Upsert, not a plain INSERT: migration 001 seeds real sensor_type rows,
	// so a literal name could collide with the seed data.
	var sensorTypeID int64
	err := pool.QueryRow(ctx, `
		INSERT INTO sensor_type (name, default_unit) VALUES ('region-tree-type', 'unit')
		ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
		RETURNING sensor_type_id`).Scan(&sensorTypeID)
	require.NoError(t, err, "seed sensor_type")

	var sensorID int64
	err = pool.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, region_id, name, unit)
		VALUES ($1, $2, $3, $4, 'unit')
		RETURNING sensor_id`, board, sensorTypeID, regionID,
		fmt.Sprintf("region-tree-sensor-%d", regionTreeFixtureN)).Scan(&sensorID)
	require.NoError(t, err, "seed sensor in region %d", regionID)

	_, err = pool.Exec(ctx,
		`INSERT INTO sensor_region_history (sensor_id, region_id) VALUES ($1, $2)`,
		sensorID, regionID)
	require.NoError(t, err, "seed open sensor_region_history in region %d", regionID)
	return sensorID
}

// regionTreeMoveSensor replays a real placement change against a seeded
// sensor: closes the sensor's open sensor_region_history row(s) (SCD2 write
// path), then -- when toRegionID is non-nil -- opens a new row in the target
// region and moves the sensor.region_id mirror column with it. A nil target
// leaves the sensor unplaced (its current state is the absence of an open
// row). Returns nothing; the shape of the resulting history is what the
// current-placement assertions are about.
func regionTreeMoveSensor(t *testing.T, pool *pgxpool.Pool, sensorID int64, toRegionID *int64) {
	t.Helper()
	ctx := context.Background()
	_, err := pool.Exec(ctx,
		`UPDATE sensor_region_history SET valid_to = NOW()
		 WHERE sensor_id = $1 AND valid_to IS NULL`, sensorID)
	require.NoError(t, err, "close open placement rows for sensor %d", sensorID)
	if toRegionID != nil {
		_, err = pool.Exec(ctx,
			`INSERT INTO sensor_region_history (sensor_id, region_id) VALUES ($1, $2)`,
			sensorID, *toRegionID)
		require.NoError(t, err, "open placement row in region %d", *toRegionID)
		_, err = pool.Exec(ctx,
			`UPDATE sensor SET region_id = $2 WHERE sensor_id = $1`, sensorID, *toRegionID)
		require.NoError(t, err, "move sensor %d mirror column", sensorID)
	} else {
		_, err = pool.Exec(ctx,
			`UPDATE sensor SET region_id = NULL WHERE sensor_id = $1`, sensorID)
		require.NoError(t, err, "unplace sensor %d (mirror column)", sensorID)
	}
}

// regionTreeSensorHistoryRowCount returns how many sensor_region_history rows
// a sensor has in total -- used to prove fixtures actually wrote the closed
// (historical) rows the counts must ignore.
func regionTreeSensorHistoryRowCount(t *testing.T, pool *pgxpool.Pool, sensorID int64) int {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM sensor_region_history WHERE sensor_id = $1`, sensorID).Scan(&n)
	require.NoError(t, err, "count placement history rows for sensor %d", sensorID)
	return n
}

// regionTreeNames lists the names of a sibling group in response order.
func regionTreeNames(nodes []*pb.RegionTreeNode) []string {
	names := make([]string, len(nodes))
	for i, n := range nodes {
		names[i] = n.Name
	}
	return names
}

// regionTreeFind returns the node named name from nodes, failing the test if
// absent.
func regionTreeFind(t *testing.T, nodes []*pb.RegionTreeNode, name string) *pb.RegionTreeNode {
	t.Helper()
	for _, n := range nodes {
		if n.Name == name {
			return n
		}
	}
	t.Fatalf("no region node named %q (siblings: %v)", name, regionTreeNames(nodes))
	return nil
}

// -- full-forest view: counts, sibling ordering, empty regions ----------------

// regionTreeFixtureN is the counter behind the unique board/sensor names
// regionTreeSeedPlacedSensor generates. Tests run sequentially (no
// t.Parallel), so a plain counter is safe.
var regionTreeFixtureN int

// TestGetRegionTree_Integration_FullForest_MultiLevelCountsAndOrdering: a
// three-level forest with sensors at several levels verifies the only-count
// (region only) vs inclusive-count (region + descendants) split per node,
// alphabetical sibling ordering against rows the DB returns in ID order
// (deliberately not alphabetical), and empty regions reporting 0/0.
func TestGetRegionTree_Integration_FullForest_MultiLevelCountsAndOrdering(t *testing.T) {
	ctx := context.Background()
	srv, pool := newRegionTreeTestServer(t)

	// Insert in non-alphabetical order so response order can only come from
	// the handler's sort, never from row order.
	avRoot := migration016SeedRegion(t, pool, "av-root", 0)
	dmBranch := migration016SeedRegion(t, pool, "dm-branch", avRoot)
	_ = migration016SeedRegion(t, pool, "ez-empty", 0)
	bzBranch := migration016SeedRegion(t, pool, "bz-branch", avRoot)
	caLeaf := migration016SeedRegion(t, pool, "ca-leaf", bzBranch)
	_ = migration016SeedRegion(t, pool, "cm-leaf", bzBranch) // empty region
	dzChild := migration016SeedRegion(t, pool, "dz-child", dmBranch)

	regionTreeSeedPlacedSensor(t, pool, avRoot)
	regionTreeSeedPlacedSensor(t, pool, avRoot)
	regionTreeSeedPlacedSensor(t, pool, bzBranch)
	regionTreeSeedPlacedSensor(t, pool, caLeaf)
	regionTreeSeedPlacedSensor(t, pool, caLeaf)
	regionTreeSeedPlacedSensor(t, pool, dzChild)
	regionTreeSeedPlacedSensor(t, pool, dzChild)
	regionTreeSeedPlacedSensor(t, pool, dzChild)
	regionTreeSeedPlacedSensor(t, pool, dzChild)

	resp, err := srv.GetRegionTree(ctx, &pb.GetRegionTreeRequest{RootRegionId: 0})
	require.NoError(t, err)

	// Top-level siblings alphabetical, not insertion order.
	require.Len(t, resp.Regions, 2, "two top-level regions in the forest")
	assert.Equal(t, []string{"av-root", "ez-empty"}, regionTreeNames(resp.Regions))

	root := regionTreeFind(t, resp.Regions, "av-root")
	assert.Equal(t, avRoot, root.RegionId, "node identity is the seeded region's ID")
	assert.Equal(t, int64(2), root.SensorCount, "only-count: sensors placed in av-root itself")
	assert.Equal(t, int64(9), root.InclusiveSensorCount,
		"inclusive: av-root(2) + bz-branch(1) + ca-leaf(2) + cm-leaf(0) + dm-branch(0) + dz-child(4)")

	// Siblings under av-root: alphabetical (bz before dm) despite dm-branch
	// being created (and thus ID-ordered) first.
	require.Len(t, root.Children, 2)
	assert.Equal(t, []string{"bz-branch", "dm-branch"}, regionTreeNames(root.Children))

	bz := regionTreeFind(t, root.Children, "bz-branch")
	assert.Equal(t, int64(1), bz.SensorCount)
	assert.Equal(t, int64(3), bz.InclusiveSensorCount, "inclusive: bz-branch(1) + ca-leaf(2) + cm-leaf(0)")

	// Children under bz-branch: alphabetical, and a leaf's inclusive count
	// equals its own direct count.
	require.Len(t, bz.Children, 2)
	assert.Equal(t, []string{"ca-leaf", "cm-leaf"}, regionTreeNames(bz.Children))
	ca := regionTreeFind(t, bz.Children, "ca-leaf")
	assert.Equal(t, int64(2), ca.SensorCount)
	assert.Equal(t, int64(2), ca.InclusiveSensorCount,
		"a leaf's inclusive count equals its own only-count")

	// Empty region: no sensors of its own and none below -- 0/0.
	cm := regionTreeFind(t, bz.Children, "cm-leaf")
	assert.Zero(t, cm.SensorCount, "empty region's only-count is 0")
	assert.Zero(t, cm.InclusiveSensorCount, "empty region's inclusive-count is 0")

	dm := regionTreeFind(t, root.Children, "dm-branch")
	assert.Equal(t, int64(0), dm.SensorCount, "dm-branch holds no sensor itself")
	assert.Equal(t, int64(4), dm.InclusiveSensorCount, "inclusive folds descendant dz-child(4) in")

	dz := regionTreeFind(t, dm.Children, "dz-child")
	assert.Equal(t, int64(4), dz.SensorCount)
	assert.Equal(t, int64(4), dz.InclusiveSensorCount)

	// A completely empty top-level region: 0/0 and no children.
	ez := regionTreeFind(t, resp.Regions, "ez-empty")
	assert.Zero(t, ez.SensorCount)
	assert.Zero(t, ez.InclusiveSensorCount)
	assert.Empty(t, ez.Children)
}

// -- deep chain: unbounded nesting (FR3/FR6) ----------------------------------

// TestGetRegionTree_Integration_DeepChain_SixLevels: a single chain six
// regions deep (5+ required) with sensors at the top, middle, and bottom.
// The recursive CTE must descend every level (unbounded nesting depth), each
// node's only-count must stay local, and the inclusive fold must accumulate
// the entire remaining chain at every level.
func TestGetRegionTree_Integration_DeepChain_SixLevels(t *testing.T) {
	ctx := context.Background()
	srv, pool := newRegionTreeTestServer(t)

	// Six levels: chain-l0 -> l1 -> l2 -> l3 -> l4 -> l5.
	ids := make([]int64, 6)
	ids[0] = migration016SeedRegion(t, pool, "chain-l0", 0)
	for i := 1; i < len(ids); i++ {
		ids[i] = migration016SeedRegion(t, pool, fmt.Sprintf("chain-l%d", i), ids[i-1])
	}

	regionTreeSeedPlacedSensor(t, pool, ids[0]) // top: 1
	regionTreeSeedPlacedSensor(t, pool, ids[2]) // middle: 2
	regionTreeSeedPlacedSensor(t, pool, ids[2])
	regionTreeSeedPlacedSensor(t, pool, ids[5]) // bottom: 3
	regionTreeSeedPlacedSensor(t, pool, ids[5])
	regionTreeSeedPlacedSensor(t, pool, ids[5])

	resp, err := srv.GetRegionTree(ctx, &pb.GetRegionTreeRequest{RootRegionId: 0})
	require.NoError(t, err)
	require.Len(t, resp.Regions, 1, "the chain's top-level region is the only forest root")

	// Walk down the chain and check both counts at every level.
	wantOnly := []int64{1, 0, 2, 0, 0, 3}
	// inclusive[i] = sum of wantOnly[i..5] -- each level folds only what is
	// below it, so the deepest level's inclusive count is its own only-count.
	wantInclusive := []int64{6, 5, 5, 3, 3, 3}
	node := resp.Regions[0]
	for i := 0; i < len(ids); i++ {
		require.Equal(t, ids[i], node.RegionId, "chain node at level %d keeps its region ID", i)
		assert.Equal(t, wantOnly[i], node.SensorCount, "only-count at chain level %d", i)
		assert.Equal(t, wantInclusive[i], node.InclusiveSensorCount,
			"inclusive at chain level %d folds every deeper level's sensors", i)
		if i < len(ids)-1 {
			require.Len(t, node.Children, 1, "exactly one child continues the chain at level %d", i)
			assert.Equal(t, fmt.Sprintf("chain-l%d", i+1), node.Children[0].Name)
			node = node.Children[0]
		} else {
			assert.Empty(t, node.Children, "the deepest level has no children")
		}
	}

	// Drill into the middle of the deep chain: the subtree from chain-l2 down
	// must be returned with counts folded only over the remaining depth.
	resp, err = srv.GetRegionTree(ctx, &pb.GetRegionTreeRequest{RootRegionId: ids[2]})
	require.NoError(t, err)
	require.Len(t, resp.Regions, 1, "drill-down returns the anchor as the single top node")
	mid := resp.Regions[0]
	assert.Equal(t, "chain-l2", mid.Name)
	assert.Equal(t, int64(2), mid.SensorCount)
	assert.Equal(t, int64(5), mid.InclusiveSensorCount,
		"drill-down inclusive: chain-l2(2) + l3(0) + l4(0) + l5(3) -- nothing above the anchor")
}

// -- drill-down: subtree scoping ----------------------------------------------

// TestGetRegionTree_Integration_DrillDown_SubtreeScoping: drilling into one
// region returns ONLY that region's subtree -- the anchor as the single top
// node, no sibling trees, inclusive counts folded over the drilled subtree
// alone -- and an unknown root_region_id is codes.NotFound against real SQL.
func TestGetRegionTree_Integration_DrillDown_SubtreeScoping(t *testing.T) {
	ctx := context.Background()
	srv, pool := newRegionTreeTestServer(t)

	// Two disjoint trees.
	forestA := migration016SeedRegion(t, pool, "forest-a", 0)
	aMid := migration016SeedRegion(t, pool, "a-mid", forestA)
	aLeaf := migration016SeedRegion(t, pool, "a-leaf", aMid)
	forestB := migration016SeedRegion(t, pool, "forest-b", 0)
	bLeaf := migration016SeedRegion(t, pool, "b-leaf", forestB)

	regionTreeSeedPlacedSensor(t, pool, forestA) // 1
	regionTreeSeedPlacedSensor(t, pool, aMid)    // 2
	regionTreeSeedPlacedSensor(t, pool, aMid)
	regionTreeSeedPlacedSensor(t, pool, aLeaf) // 3
	regionTreeSeedPlacedSensor(t, pool, aLeaf)
	regionTreeSeedPlacedSensor(t, pool, aLeaf)
	regionTreeSeedPlacedSensor(t, pool, forestB)
	regionTreeSeedPlacedSensor(t, pool, forestB)
	regionTreeSeedPlacedSensor(t, pool, forestB)
	regionTreeSeedPlacedSensor(t, pool, forestB)
	regionTreeSeedPlacedSensor(t, pool, forestB)
	regionTreeSeedPlacedSensor(t, pool, forestB)
	regionTreeSeedPlacedSensor(t, pool, forestB)
	regionTreeSeedPlacedSensor(t, pool, forestB)
	regionTreeSeedPlacedSensor(t, pool, forestB)
	regionTreeSeedPlacedSensor(t, pool, bLeaf) // 1

	// Drill into a-mid: mid-tree subtree only.
	resp, err := srv.GetRegionTree(ctx, &pb.GetRegionTreeRequest{RootRegionId: aMid})
	require.NoError(t, err)
	require.Len(t, resp.Regions, 1, "the anchor is the response's single top element")
	mid := resp.Regions[0]
	assert.Equal(t, "a-mid", mid.Name)
	assert.Equal(t, aMid, mid.RegionId)
	assert.Equal(t, int64(2), mid.SensorCount, "only-count is the anchor's own direct count")
	assert.Equal(t, int64(5), mid.InclusiveSensorCount,
		"inclusive folds only the drilled subtree: a-mid(2) + a-leaf(3) -- forest-a's own sensor is outside")
	require.Len(t, mid.Children, 1)
	assert.Equal(t, "a-leaf", mid.Children[0].Name)
	assert.Equal(t, int64(3), mid.Children[0].SensorCount)
	assert.Equal(t, int64(3), mid.Children[0].InclusiveSensorCount)
	assert.Empty(t, mid.Children[0].Children)

	// Drill into the top of tree B: subtree only, tree A entirely absent.
	resp, err = srv.GetRegionTree(ctx, &pb.GetRegionTreeRequest{RootRegionId: forestB})
	require.NoError(t, err)
	require.Len(t, resp.Regions, 1)
	top := resp.Regions[0]
	assert.Equal(t, "forest-b", top.Name)
	assert.Equal(t, int64(9), top.SensorCount, "forest-b's own nine sensors")
	assert.Equal(t, int64(10), top.InclusiveSensorCount, "inclusive: forest-b(9) + b-leaf(1)")
	assert.Equal(t, []string{"b-leaf"}, regionTreeNames(top.Children))

	// Drill into a leaf: single node, nothing nested.
	resp, err = srv.GetRegionTree(ctx, &pb.GetRegionTreeRequest{RootRegionId: bLeaf})
	require.NoError(t, err)
	require.Len(t, resp.Regions, 1)
	leaf := resp.Regions[0]
	assert.Equal(t, "b-leaf", leaf.Name)
	assert.Equal(t, int64(1), leaf.SensorCount)
	assert.Equal(t, int64(1), leaf.InclusiveSensorCount)
	assert.Empty(t, leaf.Children)

	// Unknown root region: NotFound (real GetRegionIdentity + real SQL).
	_, err = srv.GetRegionTree(ctx, &pb.GetRegionTreeRequest{RootRegionId: 999999})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err),
		"an unknown drill-down root must be NotFound against the real database")
}

// -- counts reflect CURRENT placement -----------------------------------------

// TestGetRegionTree_Integration_CountsReflectCurrentPlacement: counts come
// from OPEN sensor_region_history rows only. A sensor moved between regions
// counts in its new region (not the old one), an unplaced sensor counts
// nowhere, and the closed historical rows the fixtures deliberately leave
// behind are ignored.
func TestGetRegionTree_Integration_CountsReflectCurrentPlacement(t *testing.T) {
	ctx := context.Background()
	srv, pool := newRegionTreeTestServer(t)

	curRoot := migration016SeedRegion(t, pool, "cur-root", 0)
	curA := migration016SeedRegion(t, pool, "cur-a", curRoot)
	curB := migration016SeedRegion(t, pool, "cur-b", curRoot)

	// s1: placed in cur-a, then MOVED to cur-b -- one closed row (cur-a) plus
	// one open row (cur-b).
	moved := regionTreeSeedPlacedSensor(t, pool, curA)
	bID := curB
	regionTreeMoveSensor(t, pool, moved, &bID)
	require.Equal(t, 2, regionTreeSensorHistoryRowCount(t, pool, moved),
		"fixture sanity: the move left one closed row and one open row")

	// s2: placed in cur-a, then UNPLACED -- open row closed, nothing open.
	unplaced := regionTreeSeedPlacedSensor(t, pool, curA)
	regionTreeMoveSensor(t, pool, unplaced, nil)
	require.Equal(t, 1, regionTreeSensorHistoryRowCount(t, pool, unplaced),
		"fixture sanity: the unplaced sensor keeps its closed history row")

	// s3: placed in cur-b and never moved.
	_ = regionTreeSeedPlacedSensor(t, pool, curB)

	// s4: never placed at all -- no history rows at all.
	var neverPlaced int64
	err := pool.QueryRow(context.Background(), `
		INSERT INTO sensor (board_id, sensor_type_id, region_id, name, unit)
		VALUES ($1, (SELECT sensor_type_id FROM sensor_type WHERE name = 'region-tree-type'),
		        NULL, 'region-tree-never-placed', 'unit')
		RETURNING sensor_id`,
		regionTreeSeedBoard(t, pool, "region-tree-board-never-placed")).Scan(&neverPlaced)
	require.NoError(t, err, "seed a sensor with no placement history")
	_ = neverPlaced

	resp, err := srv.GetRegionTree(ctx, &pb.GetRegionTreeRequest{RootRegionId: 0})
	require.NoError(t, err)

	root := regionTreeFind(t, resp.Regions, "cur-root")
	assert.Equal(t, int64(0), root.SensorCount, "the root holds no sensor directly")
	assert.Equal(t, int64(2), root.InclusiveSensorCount,
		"inclusive: moved sensor (in cur-b) + never-moved sensor -- the moved and unplaced ones count nowhere")

	a := regionTreeFind(t, root.Children, "cur-a")
	assert.Equal(t, int64(0), a.SensorCount,
		"cur-a's moved and unplaced sensors must not count in their OLD region")
	assert.Zero(t, a.InclusiveSensorCount)

	b := regionTreeFind(t, root.Children, "cur-b")
	assert.Equal(t, int64(2), b.SensorCount,
		"cur-b holds the moved sensor and the never-moved one -- current placement only")
	assert.Equal(t, int64(2), b.InclusiveSensorCount)
}