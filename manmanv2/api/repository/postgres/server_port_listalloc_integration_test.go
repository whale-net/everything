//go:build integration

// This file only builds under the "integration" build tag, same as this
// package's pending_restart/action/server_port_range integration precedent.
//
// It exists because mock-based CI cannot catch the bug class task #2142
// (plan #2080) shipped with: GetPortAllocation, ListAllocatedPorts, and
// ListPortsBySessionID kept SELECTing server_ports.sgc_id after migration
// 014_port_allocation_per_session dropped that column, so every call failed
// at runtime with SQLSTATE 42703 (undefined column) -- invisible to the
// mock-based unit suite, which never talks to Postgres. A real-schema run
// fails these tests on the pre-fix code and passes after it.
//
// Schema here is hand-written, self-contained DDL mirroring exactly the
// pieces of manmanv2/migrate/migrations that server_ports reads depend on:
// 001_initial_schema.up.sql's servers/sessions tables (scoped to the columns
// the FKs need), 008_server_ports.up.sql's server_ports table (PK + CHECKs),
// and 014_port_allocation_per_session.up.sql's session-scoped reshape
// (sgc_id dropped, session_id added, partial index) -- per dbtest's README
// ("Options.Schema should be self-contained DDL").
package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/dbtest"
)

// serverPortAllocSchema mirrors the post-014 shape of server_ports plus the
// FK targets its columns reference (servers from 001, sessions from 001
// scoped to its PK -- the only column 014's FK needs).
const serverPortAllocSchema = `
	CREATE TABLE servers (
		server_id BIGSERIAL PRIMARY KEY,
		name VARCHAR(255) NOT NULL UNIQUE
	);

	CREATE TABLE sessions (
		session_id BIGSERIAL PRIMARY KEY
	);

	CREATE TABLE server_ports (
		server_id BIGINT NOT NULL REFERENCES servers(server_id) ON DELETE CASCADE,
		port INTEGER NOT NULL CHECK (port >= 1 AND port <= 65535),
		protocol VARCHAR(10) NOT NULL CHECK (protocol IN ('TCP', 'UDP')),
		allocated_at TIMESTAMP NOT NULL DEFAULT NOW(),
		session_id BIGINT REFERENCES sessions(session_id) ON DELETE CASCADE,
		CONSTRAINT pk_server_ports PRIMARY KEY (server_id, port, protocol)
	);

	CREATE INDEX idx_server_ports_session_id
		ON server_ports(session_id) WHERE session_id IS NOT NULL;
`

func newPortAllocHarness(t *testing.T) (*pgxpool.Pool, *ServerPortRepository) {
	t.Helper()
	db := dbtest.NewPostgres(context.Background(), t, dbtest.Options{Schema: serverPortAllocSchema})
	pool := db.Pool

	if _, err := pool.Exec(context.Background(), `INSERT INTO servers (server_id, name) VALUES (5, 'srv-5')`); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	return pool, NewServerPortRepository(pool)
}

// TestListAllocatedPorts_ListsSessionAllocations is the red test for task
// #2142: against the pre-fix SELECT (which still named the dropped sgc_id
// column) this fails with SQLSTATE 42703 on the real migrated schema, which
// is exactly how the live RPC 500'd. Green after the fix: allocations made
// by a session start (server-scoped AllocatePort with a session id) come
// back with their session attribution intact.
func TestListAllocatedPorts_ListsSessionAllocations(t *testing.T) {
	pool, repo := newPortAllocHarness(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `INSERT INTO sessions (session_id) VALUES (101), (102)`); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}
	if err := repo.AllocatePort(ctx, 5, 7710, "TCP", 101); err != nil {
		t.Fatalf("AllocatePort TCP 7710: %v", err)
	}
	if err := repo.AllocatePort(ctx, 5, 7710, "UDP", 101); err != nil {
		t.Fatalf("AllocatePort UDP 7710: %v", err)
	}
	if err := repo.AllocatePort(ctx, 5, 7742, "TCP", 102); err != nil {
		t.Fatalf("AllocatePort TCP 7742: %v", err)
	}

	ports, err := repo.ListAllocatedPorts(ctx, 5)
	if err != nil {
		t.Fatalf("ListAllocatedPorts: %v", err)
	}
	if len(ports) != 3 {
		t.Fatalf("ListAllocatedPorts returned %d rows, want 3: %+v", len(ports), ports)
	}

	// ORDER BY port, protocol: 7710/TCP, 7710/UDP, 7742/TCP.
	want := []struct {
		port      int
		protocol  string
		sessionID int64
	}{
		{7710, "TCP", 101},
		{7710, "UDP", 101},
		{7742, "TCP", 102},
	}
	for i, w := range want {
		got := ports[i]
		if got.Port != w.port || got.Protocol != w.protocol {
			t.Errorf("row %d = %d/%s, want %d/%s", i, got.Port, got.Protocol, w.port, w.protocol)
		}
		if got.ServerID != 5 {
			t.Errorf("row %d server_id = %d, want 5", i, got.ServerID)
		}
		if got.SessionID == nil || *got.SessionID != w.sessionID {
			t.Errorf("row %d (%d/%s) session_id = %v, want %d", i, w.port, w.protocol, got.SessionID, w.sessionID)
		}
		if got.AllocatedAt.IsZero() {
			t.Errorf("row %d (%d/%s) has zero allocated_at", i, w.port, w.protocol)
		}
	}
}

// TestListAllocatedPorts_SessionlessRow_HasNilSession proves a server_ports
// row without a session (session_id NULL -- the column is nullable, and the
// 014 partial index explicitly skips NULLs) still lists, with SessionID nil.
// The ListAllocatedPorts handler maps nil to session_id 0 on the wire.
func TestListAllocatedPorts_SessionlessRow_HasNilSession(t *testing.T) {
	pool, repo := newPortAllocHarness(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx,
		`INSERT INTO server_ports (server_id, port, protocol, session_id) VALUES (5, 25565, 'TCP', NULL)`); err != nil {
		t.Fatalf("seed session-less allocation: %v", err)
	}

	ports, err := repo.ListAllocatedPorts(ctx, 5)
	if err != nil {
		t.Fatalf("ListAllocatedPorts: %v", err)
	}
	if len(ports) != 1 {
		t.Fatalf("ListAllocatedPorts returned %d rows, want 1: %+v", len(ports), ports)
	}
	if ports[0].SessionID != nil {
		t.Errorf("session-less row session_id = %v, want nil", *ports[0].SessionID)
	}
	if ports[0].Port != 25565 || ports[0].Protocol != "TCP" {
		t.Errorf("session-less row = %d/%s, want 25565/TCP", ports[0].Port, ports[0].Protocol)
	}
}

// TestListPortsBySessionID_AndGetPortAllocation_ReadMigratedSchema covers
// the sibling reads that shared ListAllocatedPorts' broken column list: a
// session's own rows come back via ListPortsBySessionID, and a single
// allocation resolves via GetPortAllocation with its session attached.
func TestListPortsBySessionID_AndGetPortAllocation_ReadMigratedSchema(t *testing.T) {
	pool, repo := newPortAllocHarness(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `INSERT INTO sessions (session_id) VALUES (201)`); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := repo.AllocatePort(ctx, 5, 7700, "TCP", 201); err != nil {
		t.Fatalf("AllocatePort TCP 7700: %v", err)
	}
	if err := repo.AllocatePort(ctx, 5, 7701, "UDP", 201); err != nil {
		t.Fatalf("AllocatePort UDP 7701: %v", err)
	}

	bySession, err := repo.ListPortsBySessionID(ctx, 201)
	if err != nil {
		t.Fatalf("ListPortsBySessionID: %v", err)
	}
	if len(bySession) != 2 {
		t.Fatalf("ListPortsBySessionID returned %d rows, want 2: %+v", len(bySession), bySession)
	}
	for _, p := range bySession {
		if p.SessionID == nil || *p.SessionID != 201 {
			t.Errorf("row %d/%s session_id = %v, want 201", p.Port, p.Protocol, p.SessionID)
		}
	}

	alloc, err := repo.GetPortAllocation(ctx, 5, 7700, "TCP")
	if err != nil {
		t.Fatalf("GetPortAllocation: %v", err)
	}
	if alloc.SessionID == nil || *alloc.SessionID != 201 {
		t.Errorf("GetPortAllocation session_id = %v, want 201", alloc.SessionID)
	}

	// The same session's ports on a different server must not be missed:
	// ListPortsBySessionID orders across servers and returns them all.
	if _, err := pool.Exec(ctx, `INSERT INTO servers (server_id, name) VALUES (6, 'srv-6')`); err != nil {
		t.Fatalf("seed second server: %v", err)
	}
	if err := repo.AllocatePort(ctx, 6, 7700, "TCP", 201); err != nil {
		t.Fatalf("AllocatePort on server 6: %v", err)
	}
	bySession, err = repo.ListPortsBySessionID(ctx, 201)
	if err != nil {
		t.Fatalf("ListPortsBySessionID after cross-server alloc: %v", err)
	}
	if len(bySession) != 3 {
		t.Fatalf("ListPortsBySessionID returned %d rows, want 3 (both servers): %+v", len(bySession), bySession)
	}
}
