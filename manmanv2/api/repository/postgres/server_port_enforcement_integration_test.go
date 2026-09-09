//go:build integration

// Real-Postgres coverage for FR12's enforcement half (task #2097, plan
// #2080): at session start, a FRESH host-port allocation outside the
// server's configured allowed ranges must refuse the assignment on the
// same synchronous failure path a port conflict already takes -- instead
// of starting a "healthy" session whose port is silently unreachable.
// Where no ranges exist for a protocol, assignment stays unconstrained
// (exactly the pre-038 behavior, SB-1.2); existing allocations stay valid
// until their session ends, so a range change takes effect only on the
// next fresh allocation (OQ5).
//
// Schema here is hand-written, self-contained DDL mirroring exactly the
// pieces the code path touches: 001's servers table (FK target), the
// sessions table (FK target), server_ports in its post-014 shape
// (session-scoped allocation), and 038_server_allowed_port_ranges.up.sql
// verbatim -- per dbtest's README ("Options.Schema should be
// self-contained DDL").
package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/dbtest"
	manman "github.com/whale-net/everything/manmanv2/models"
)

// portEnforcementSchema mirrors 001's servers table (scoped to the
// columns the FKs need), a minimal sessions table (FK target), 014's
// server_ports shape (session_id instead of sgc_id), and
// 038_server_allowed_port_ranges.up.sql verbatim.
const portEnforcementSchema = `
	CREATE TABLE servers (
		server_id BIGSERIAL PRIMARY KEY,
		name VARCHAR(255) NOT NULL UNIQUE
	);

	CREATE TABLE sessions (
		session_id BIGSERIAL PRIMARY KEY
	);

	CREATE TABLE server_ports (
		server_id BIGINT NOT NULL REFERENCES servers(server_id) ON DELETE CASCADE,
		port INTEGER NOT NULL,
		protocol VARCHAR(10) NOT NULL CHECK (protocol IN ('TCP', 'UDP')),
		session_id BIGINT REFERENCES sessions(session_id) ON DELETE CASCADE,
		allocated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (server_id, port, protocol)
	);

	CREATE TABLE server_allowed_port_ranges (
		range_id BIGSERIAL PRIMARY KEY,
		server_id BIGINT NOT NULL REFERENCES servers(server_id) ON DELETE CASCADE,
		start_port INTEGER NOT NULL,
		end_port INTEGER NOT NULL,
		protocol VARCHAR(16) NOT NULL DEFAULT 'TCP',
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		CONSTRAINT server_allowed_port_ranges_port_order CHECK (start_port >= 1 AND end_port >= start_port),
		CONSTRAINT server_allowed_port_ranges_protocol CHECK (protocol IN ('TCP', 'UDP'))
	);

	CREATE INDEX idx_server_allowed_port_ranges_server
		ON server_allowed_port_ranges(server_id);
`

func newPortEnforcementHarness(t *testing.T) (*pgxpool.Pool, *ServerPortRepository, *ServerPortRangeRepository) {
	t.Helper()
	pool := dbtest.NewPostgres(context.Background(), t, dbtest.Options{Schema: portEnforcementSchema}).Pool
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `INSERT INTO servers (server_id, name) VALUES (7, 'srv-7')`); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sessions (session_id) VALUES (101), (102), (103)`); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}
	return pool, NewServerPortRepository(pool), NewServerPortRangeRepository(pool)
}

func seedRanges(t *testing.T, rr *ServerPortRangeRepository, ranges []*manman.ServerAllowedPortRange) {
	t.Helper()
	if _, err := rr.Replace(context.Background(), 7, ranges); err != nil {
		t.Fatalf("seed ranges: %v", err)
	}
}

func countAllocated(t *testing.T, pool *pgxpool.Pool, port int32, protocol string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM server_ports WHERE server_id = 7 AND port = $1 AND protocol = $2`,
		port, protocol).Scan(&n); err != nil {
		t.Fatalf("count allocated: %v", err)
	}
	return n
}

// FR12 enforcement, red case today: with ranges configured, a fresh
// allocation outside them must be refused legibly -- not start a
// "healthy" session that is silently unreachable.
func TestAllocateMultiplePorts_OutsideConfiguredRanges_Refused(t *testing.T) {
	pool, pr, rr := newPortEnforcementHarness(t)
	seedRanges(t, rr, []*manman.ServerAllowedPortRange{
		{ServerID: 7, StartPort: 25565, EndPort: 25570, Protocol: "TCP"},
	})
	ctx := context.Background()

	err := pr.AllocateMultiplePorts(ctx, 7, []*manman.PortBinding{
		{ContainerPort: 25565, HostPort: 30000, Protocol: "TCP"},
	}, 101)
	if err == nil {
		t.Fatal("fresh allocation outside the configured ranges succeeded; want refusal")
	}
	if !IsPortOutOfRangeError(err) {
		t.Fatalf("error = %T (%v), want *PortOutOfRangeError (legible, same path kind as PortConflictError)", err, err)
	}
	oor := err.(*PortOutOfRangeError)
	if oor.Port != 30000 || oor.Protocol != "TCP" || oor.ServerID != 7 {
		t.Errorf("error details = %d/%s on server %d, want 30000/TCP on server 7", oor.Port, oor.Protocol, oor.ServerID)
	}
	if countAllocated(t, pool, 30000, "TCP") != 0 {
		t.Error("refused allocation left server_ports rows behind")
	}
}

func TestAllocateMultiplePorts_InsideConfiguredRanges_Succeeds(t *testing.T) {
	pool, pr, rr := newPortEnforcementHarness(t)
	seedRanges(t, rr, []*manman.ServerAllowedPortRange{
		{ServerID: 7, StartPort: 25565, EndPort: 25570, Protocol: "TCP"},
	})
	ctx := context.Background()

	if err := pr.AllocateMultiplePorts(ctx, 7, []*manman.PortBinding{
		{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"},
	}, 101); err != nil {
		t.Fatalf("fresh allocation inside the configured ranges failed: %v", err)
	}
	if countAllocated(t, pool, 25565, "TCP") != 1 {
		t.Error("allocated row missing after in-range allocation")
	}
}

// SB-1.2: ranges only constrain where they exist -- a server with zero
// configured ranges is exactly today's unconstrained behavior.
func TestAllocateMultiplePorts_NoRangesConfigured_Unconstrained(t *testing.T) {
	pool, pr, _ := newPortEnforcementHarness(t)
	ctx := context.Background()

	if err := pr.AllocateMultiplePorts(ctx, 7, []*manman.PortBinding{
		{ContainerPort: 30000, HostPort: 30000, Protocol: "TCP"},
	}, 101); err != nil {
		t.Fatalf("allocation on a range-less server must stay unconstrained: %v", err)
	}
	if countAllocated(t, pool, 30000, "TCP") != 1 {
		t.Error("allocated row missing after unconstrained allocation")
	}
}

// Ranges are protocol-scoped: only the protocols that have configured
// ranges are constrained.
func TestAllocateMultiplePorts_RangesConstrainOnlyTheirProtocol(t *testing.T) {
	_, pr, rr := newPortEnforcementHarness(t)
	seedRanges(t, rr, []*manman.ServerAllowedPortRange{
		{ServerID: 7, StartPort: 25565, EndPort: 25570, Protocol: "TCP"},
	})
	ctx := context.Background()

	// UDP has no configured ranges on this server: unconstrained.
	if err := pr.AllocateMultiplePorts(ctx, 7, []*manman.PortBinding{
		{ContainerPort: 7777, HostPort: 7777, Protocol: "UDP"},
	}, 101); err != nil {
		t.Fatalf("UDP allocation must be unconstrained when no UDP ranges exist: %v", err)
	}

	// TCP is constrained: outside 25565-25570 refuses.
	err := pr.AllocateMultiplePorts(ctx, 7, []*manman.PortBinding{
		{ContainerPort: 25565, HostPort: 7778, Protocol: "TCP"},
	}, 102)
	if !IsPortOutOfRangeError(err) {
		t.Fatalf("TCP allocation outside the TCP ranges: err = %v, want *PortOutOfRangeError", err)
	}

	// TCP inside the range succeeds.
	if err := pr.AllocateMultiplePorts(ctx, 7, []*manman.PortBinding{
		{ContainerPort: 25565, HostPort: 25566, Protocol: "TCP"},
	}, 102); err != nil {
		t.Fatalf("TCP allocation inside the TCP ranges failed: %v", err)
	}
}

// OQ5: existing allocations stay valid until their session ends; a range
// change takes effect only at the NEXT fresh allocation.
func TestAllocateMultiplePorts_RangeChange_TakesEffectOnNextFreshAllocation(t *testing.T) {
	pool, pr, rr := newPortEnforcementHarness(t)
	seedRanges(t, rr, []*manman.ServerAllowedPortRange{
		{ServerID: 7, StartPort: 20000, EndPort: 20010, Protocol: "TCP"},
	})
	ctx := context.Background()

	// 20005 is in range under the original config.
	if err := pr.AllocateMultiplePorts(ctx, 7, []*manman.PortBinding{
		{ContainerPort: 20005, HostPort: 20005, Protocol: "TCP"},
	}, 101); err != nil {
		t.Fatalf("seed allocation: %v", err)
	}

	// The operator shrinks the allowed set; 20005 is now outside it.
	seedRanges(t, rr, []*manman.ServerAllowedPortRange{
		{ServerID: 7, StartPort: 21000, EndPort: 21010, Protocol: "TCP"},
	})

	// The existing allocation is untouched (session still running).
	if countAllocated(t, pool, 20005, "TCP") != 1 {
		t.Error("range change must not disturb an existing allocation (OQ5)")
	}

	// A fresh allocation at the now-out-of-range port is refused...
	if err := pr.AllocateMultiplePorts(ctx, 7, []*manman.PortBinding{
		{ContainerPort: 20005, HostPort: 20006, Protocol: "TCP"},
	}, 102); !IsPortOutOfRangeError(err) {
		t.Fatalf("fresh allocation after range change: err = %v, want *PortOutOfRangeError", err)
	}

	// ...and one in the new range succeeds.
	if err := pr.AllocateMultiplePorts(ctx, 7, []*manman.PortBinding{
		{ContainerPort: 21000, HostPort: 21005, Protocol: "TCP"},
	}, 103); err != nil {
		t.Fatalf("fresh allocation inside the new ranges failed: %v", err)
	}
}

// The out-of-range failure is indistinguishable in KIND from the existing
// port-conflict failure: both are plain synchronous errors returned from
// the same AllocateMultiplePorts call, detectable by their typed helpers.
func TestAllocateMultiplePorts_OutOfRangeMatchesConflictFailureKind(t *testing.T) {
	pool, pr, rr := newPortEnforcementHarness(t)
	seedRanges(t, rr, []*manman.ServerAllowedPortRange{
		{ServerID: 7, StartPort: 25565, EndPort: 25570, Protocol: "TCP"},
	})
	ctx := context.Background()

	// Conflict path (pre-existing behavior): 25565 is inside the ranges
	// but already allocated to session 101.
	if err := pr.AllocateMultiplePorts(ctx, 7, []*manman.PortBinding{
		{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"},
	}, 101); err != nil {
		t.Fatalf("seed allocation: %v", err)
	}
	conflictErr := pr.AllocateMultiplePorts(ctx, 7, []*manman.PortBinding{
		{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"},
	}, 102)
	if !IsPortConflictError(conflictErr) {
		t.Fatalf("conflict path broken: err = %v, want *PortConflictError", conflictErr)
	}

	// Out-of-range path: 30000 is fresh but outside the ranges.
	rangeErr := pr.AllocateMultiplePorts(ctx, 7, []*manman.PortBinding{
		{ContainerPort: 25565, HostPort: 30000, Protocol: "TCP"},
	}, 103)
	if !IsPortOutOfRangeError(rangeErr) {
		t.Fatalf("out-of-range path: err = %v, want *PortOutOfRangeError", rangeErr)
	}
	if IsPortConflictError(rangeErr) || IsPortOutOfRangeError(conflictErr) {
		t.Error("the two failure kinds must remain distinguishable")
	}
	if countAllocated(t, pool, 30000, "TCP") != 0 {
		t.Error("refused allocation left server_ports rows behind")
	}
}
