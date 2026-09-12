//go:build integration

// This file only builds under the "integration" build tag, same as this
// package's server_port_range_integration_test.go precedent. It exercises
// ServerRepository's drain-state surface (#2360, manmanv2 M6, C29
// groundwork): SetDrainState round-trips all three states, Get/List surface
// the new columns, and ListByDrainState filters correctly.
//
// Schema here is hand-written, self-contained DDL mirroring the servers
// columns serverSelectColumns actually selects -- per dbtest's README
// ("Options.Schema should be self-contained DDL") -- rather than the full
// embedded migration history.
package postgres

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/dbtest"
	manman "github.com/whale-net/everything/manmanv2/models"
)

const serverDrainSchema = `
	CREATE TABLE servers (
		server_id BIGSERIAL PRIMARY KEY,
		name VARCHAR(255) NOT NULL UNIQUE,
		status VARCHAR(50) NOT NULL DEFAULT 'offline',
		last_seen TIMESTAMP,
		is_default BOOLEAN NOT NULL DEFAULT FALSE,
		host_public_address VARCHAR(255),
		drain_state TEXT NOT NULL DEFAULT 'schedulable',
		drain_requested_at TIMESTAMPTZ NULL,
		CONSTRAINT servers_drain_state_check
			CHECK (drain_state IN ('schedulable', 'draining', 'drained'))
	);
`

func newServerDrainHarness(t *testing.T) (*pgxpool.Pool, *ServerRepository) {
	t.Helper()
	db := dbtest.NewPostgres(context.Background(), t, dbtest.Options{Schema: serverDrainSchema})
	return db.Pool, NewServerRepository(db.Pool)
}

func seedDrainServer(t *testing.T, pool *pgxpool.Pool, name string) int64 {
	t.Helper()
	var serverID int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO servers (name) VALUES ($1) RETURNING server_id`, name,
	).Scan(&serverID); err != nil {
		t.Fatalf("seed server %q: %v", name, err)
	}
	return serverID
}

// TestServerRepository_SetDrainStateRoundTripsAllThreeStates proves
// SetDrainState transitions a server through schedulable -> draining ->
// drained -> schedulable, and that Get reflects both the state and the
// requested_at timestamp (present while draining/drained-via-drain-path,
// nil once cleared).
func TestServerRepository_SetDrainStateRoundTripsAllThreeStates(t *testing.T) {
	pool, repo := newServerDrainHarness(t)
	ctx := context.Background()

	serverID := seedDrainServer(t, pool, "srv-drain-roundtrip")

	// Fresh server defaults to schedulable with no requested_at.
	fetched, err := repo.Get(ctx, serverID)
	if err != nil {
		t.Fatalf("Get (initial): %v", err)
	}
	if fetched.DrainState != manman.ServerDrainStateSchedulable {
		t.Fatalf("initial DrainState = %q, want %q", fetched.DrainState, manman.ServerDrainStateSchedulable)
	}
	if fetched.DrainRequestedAt != nil {
		t.Fatalf("initial DrainRequestedAt = %v, want nil", *fetched.DrainRequestedAt)
	}

	// schedulable -> draining, with a timestamp.
	drainedAt := time.Now()
	if err := repo.SetDrainState(ctx, serverID, manman.ServerDrainStateDraining, &drainedAt); err != nil {
		t.Fatalf("SetDrainState(draining): %v", err)
	}
	fetched, err = repo.Get(ctx, serverID)
	if err != nil {
		t.Fatalf("Get (draining): %v", err)
	}
	if fetched.DrainState != manman.ServerDrainStateDraining {
		t.Fatalf("DrainState after drain = %q, want %q", fetched.DrainState, manman.ServerDrainStateDraining)
	}
	if fetched.DrainRequestedAt == nil {
		t.Fatal("DrainRequestedAt after drain = nil, want non-nil")
	}

	// draining -> drained.
	if err := repo.SetDrainState(ctx, serverID, manman.ServerDrainStateDrained, &drainedAt); err != nil {
		t.Fatalf("SetDrainState(drained): %v", err)
	}
	fetched, err = repo.Get(ctx, serverID)
	if err != nil {
		t.Fatalf("Get (drained): %v", err)
	}
	if fetched.DrainState != manman.ServerDrainStateDrained {
		t.Fatalf("DrainState after mark-drained = %q, want %q", fetched.DrainState, manman.ServerDrainStateDrained)
	}

	// drained -> schedulable, clearing the timestamp.
	if err := repo.SetDrainState(ctx, serverID, manman.ServerDrainStateSchedulable, nil); err != nil {
		t.Fatalf("SetDrainState(schedulable): %v", err)
	}
	fetched, err = repo.Get(ctx, serverID)
	if err != nil {
		t.Fatalf("Get (undrained): %v", err)
	}
	if fetched.DrainState != manman.ServerDrainStateSchedulable {
		t.Fatalf("DrainState after undrain = %q, want %q", fetched.DrainState, manman.ServerDrainStateSchedulable)
	}
	if fetched.DrainRequestedAt != nil {
		t.Fatalf("DrainRequestedAt after undrain = %v, want nil", *fetched.DrainRequestedAt)
	}
}

// TestServerRepository_ListSurfacesDrainFields proves List returns the
// drain_state/drain_requested_at columns for every row, not just Get.
func TestServerRepository_ListSurfacesDrainFields(t *testing.T) {
	pool, repo := newServerDrainHarness(t)
	ctx := context.Background()

	id1 := seedDrainServer(t, pool, "srv-list-1")
	id2 := seedDrainServer(t, pool, "srv-list-2")

	requestedAt := time.Now()
	if err := repo.SetDrainState(ctx, id2, manman.ServerDrainStateDraining, &requestedAt); err != nil {
		t.Fatalf("SetDrainState: %v", err)
	}

	listed, err := repo.List(ctx, 50, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("List returned %d rows, want 2", len(listed))
	}

	byID := make(map[int64]*manman.Server, len(listed))
	for _, s := range listed {
		byID[s.ServerID] = s
	}
	if byID[id1].DrainState != manman.ServerDrainStateSchedulable {
		t.Errorf("server %d DrainState = %q, want %q", id1, byID[id1].DrainState, manman.ServerDrainStateSchedulable)
	}
	if byID[id2].DrainState != manman.ServerDrainStateDraining {
		t.Errorf("server %d DrainState = %q, want %q", id2, byID[id2].DrainState, manman.ServerDrainStateDraining)
	}
	if byID[id2].DrainRequestedAt == nil {
		t.Errorf("server %d DrainRequestedAt = nil, want non-nil", id2)
	}
}

// TestServerRepository_ListByDrainStateFilters proves ListByDrainState
// returns exactly the servers currently in the requested state -- the
// query the dependent eviction task relies on.
func TestServerRepository_ListByDrainStateFilters(t *testing.T) {
	pool, repo := newServerDrainHarness(t)
	ctx := context.Background()

	schedulableID := seedDrainServer(t, pool, "srv-filter-schedulable")
	drainingID1 := seedDrainServer(t, pool, "srv-filter-draining-1")
	drainingID2 := seedDrainServer(t, pool, "srv-filter-draining-2")
	drainedID := seedDrainServer(t, pool, "srv-filter-drained")

	requestedAt := time.Now()
	for _, id := range []int64{drainingID1, drainingID2} {
		if err := repo.SetDrainState(ctx, id, manman.ServerDrainStateDraining, &requestedAt); err != nil {
			t.Fatalf("SetDrainState(draining, %d): %v", id, err)
		}
	}
	if err := repo.SetDrainState(ctx, drainedID, manman.ServerDrainStateDrained, &requestedAt); err != nil {
		t.Fatalf("SetDrainState(drained): %v", err)
	}

	draining, err := repo.ListByDrainState(ctx, manman.ServerDrainStateDraining)
	if err != nil {
		t.Fatalf("ListByDrainState(draining): %v", err)
	}
	gotDraining := serverIDs(draining)
	wantDraining := []int64{drainingID1, drainingID2}
	sort.Slice(gotDraining, func(i, j int) bool { return gotDraining[i] < gotDraining[j] })
	sort.Slice(wantDraining, func(i, j int) bool { return wantDraining[i] < wantDraining[j] })
	if !equalInt64Slices(gotDraining, wantDraining) {
		t.Errorf("ListByDrainState(draining) = %v, want %v", gotDraining, wantDraining)
	}

	schedulable, err := repo.ListByDrainState(ctx, manman.ServerDrainStateSchedulable)
	if err != nil {
		t.Fatalf("ListByDrainState(schedulable): %v", err)
	}
	if got := serverIDs(schedulable); len(got) != 1 || got[0] != schedulableID {
		t.Errorf("ListByDrainState(schedulable) = %v, want [%d]", got, schedulableID)
	}

	drained, err := repo.ListByDrainState(ctx, manman.ServerDrainStateDrained)
	if err != nil {
		t.Fatalf("ListByDrainState(drained): %v", err)
	}
	if got := serverIDs(drained); len(got) != 1 || got[0] != drainedID {
		t.Errorf("ListByDrainState(drained) = %v, want [%d]", got, drainedID)
	}
}

// TestServerRepository_SetDrainStateUnknownServerErrors proves SetDrainState
// on a nonexistent server_id returns an error rather than silently
// succeeding (the handler layer maps this to codes.NotFound).
func TestServerRepository_SetDrainStateUnknownServerErrors(t *testing.T) {
	_, repo := newServerDrainHarness(t)
	ctx := context.Background()

	if err := repo.SetDrainState(ctx, 999999, manman.ServerDrainStateDraining, nil); err == nil {
		t.Fatal("expected SetDrainState on an unknown server_id to error, it succeeded")
	}
}

func serverIDs(servers []*manman.Server) []int64 {
	ids := make([]int64, len(servers))
	for i, s := range servers {
		ids[i] = s.ServerID
	}
	return ids
}

func equalInt64Slices(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
