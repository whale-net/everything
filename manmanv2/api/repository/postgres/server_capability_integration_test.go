//go:build integration

// Real-Postgres coverage for ServerCapabilityRepository, replaying the real
// shipped migrations via //manmanv2/migrate/schema rather than hand-written
// DDL.
//
// The behaviour worth knowing: Insert is NOT an upsert. It appends a new row
// stamped with time.Now() on every call, and Get returns the most recent one
// per server (ORDER BY recorded_at DESC LIMIT 1). So this is a small
// time-series of capability readings, not a single mutable record -- the
// table is expected to accumulate rows.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //manmanv2/api/repository/postgres:server_capability_integration_test --test_output=all
package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	manman "github.com/whale-net/everything/manmanv2/models"
)

type capabilityFixture struct {
	repo    *ServerCapabilityRepository
	pool    *pgxpool.Pool
	serverA int64
	serverB int64
}

func newCapabilityHarness(t *testing.T) capabilityFixture {
	t.Helper()
	pool := newMigratedPool(t)
	ctx := context.Background()

	// Two servers, so Get's server_id scoping can be told apart from a
	// missing filter.
	seedCapServer := func(name string) int64 {
		var id int64
		if err := pool.QueryRow(ctx,
			`INSERT INTO servers (name) VALUES ($1) RETURNING server_id`, name,
		).Scan(&id); err != nil {
			t.Fatalf("seed server %q: %v", name, err)
		}
		return id
	}

	return capabilityFixture{
		repo:    NewServerCapabilityRepository(pool),
		pool:    pool,
		serverA: seedCapServer("cap-server-a"),
		serverB: seedCapServer("cap-server-b"),
	}
}

func (f capabilityFixture) insert(t *testing.T, serverID int64, totalMem, availMem, cores, availMillis int32, docker string) *manman.ServerCapability {
	t.Helper()
	cap := &manman.ServerCapability{
		ServerID:               serverID,
		TotalMemoryMB:          totalMem,
		AvailableMemoryMB:      availMem,
		CPUCores:               cores,
		AvailableCPUMillicores: availMillis,
		DockerVersion:          docker,
	}
	if err := f.repo.Insert(context.Background(), cap); err != nil {
		t.Fatalf("Insert for server %d: %v", serverID, err)
	}
	if cap.CapabilityID == 0 {
		t.Error("Insert did not populate CapabilityID")
	}
	if cap.RecordedAt == nil {
		t.Error("Insert did not populate RecordedAt")
	}
	return cap
}

// TestServerCapabilityRepository_InsertGetRoundTrip proves Insert assigns a
// capability_id and a recorded_at, and Get returns every stored field.
func TestServerCapabilityRepository_InsertGetRoundTrip(t *testing.T) {
	f := newCapabilityHarness(t)
	ctx := context.Background()

	inserted := f.insert(t, f.serverA, 32768, 16384, 8, 4000, "24.0.7")

	got, err := f.repo.Get(ctx, f.serverA)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.CapabilityID != inserted.CapabilityID {
		t.Errorf("CapabilityID = %d, want %d", got.CapabilityID, inserted.CapabilityID)
	}
	if got.ServerID != f.serverA {
		t.Errorf("ServerID = %d, want %d", got.ServerID, f.serverA)
	}
	if got.TotalMemoryMB != 32768 {
		t.Errorf("TotalMemoryMB = %d, want 32768", got.TotalMemoryMB)
	}
	if got.AvailableMemoryMB != 16384 {
		t.Errorf("AvailableMemoryMB = %d, want 16384", got.AvailableMemoryMB)
	}
	if got.CPUCores != 8 {
		t.Errorf("CPUCores = %d, want 8", got.CPUCores)
	}
	if got.AvailableCPUMillicores != 4000 {
		t.Errorf("AvailableCPUMillicores = %d, want 4000", got.AvailableCPUMillicores)
	}
	if got.DockerVersion != "24.0.7" {
		t.Errorf("DockerVersion = %q, want %q", got.DockerVersion, "24.0.7")
	}
	if got.RecordedAt == nil {
		t.Fatal("RecordedAt = nil, want non-nil")
	}
}

// TestServerCapabilityRepository_GetUnknownServerErrors proves Get on a server
// with no readings errors (pgx.ErrNoRows) rather than returning a zero-valued
// capability -- a caller must be able to tell "never reported" from "reported
// all zeroes".
func TestServerCapabilityRepository_GetUnknownServerErrors(t *testing.T) {
	f := newCapabilityHarness(t)

	if _, err := f.repo.Get(context.Background(), 999999); err == nil {
		t.Fatal("Get for a server with no readings returned no error, want ErrNoRows")
	}
}

// TestServerCapabilityRepository_InsertAppendsRatherThanReplaces proves Insert
// is not an upsert: a second reading adds a row rather than replacing the
// first, and Get returns the newest.
func TestServerCapabilityRepository_InsertAppendsRatherThanReplaces(t *testing.T) {
	f := newCapabilityHarness(t)
	ctx := context.Background()

	first := f.insert(t, f.serverA, 32768, 16384, 8, 4000, "24.0.7")
	// Insert stamps recorded_at with time.Now(), so separate the two readings
	// enough that the DESC ordering is unambiguous rather than a tie.
	time.Sleep(10 * time.Millisecond)
	second := f.insert(t, f.serverA, 65536, 32768, 16, 8000, "25.0.1")

	// Both rows survive -- nothing was overwritten.
	var rows int
	if err := f.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM server_capabilities WHERE server_id = $1`, f.serverA,
	).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 2 {
		t.Errorf("server has %d capability rows, want 2 (Insert appends, it does not upsert)", rows)
	}

	// Get returns the most recent, per ORDER BY recorded_at DESC LIMIT 1.
	got, err := f.repo.Get(ctx, f.serverA)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.CapabilityID != second.CapabilityID {
		t.Errorf("Get returned capability %d, want %d (the newest reading)", got.CapabilityID, second.CapabilityID)
	}
	if got.TotalMemoryMB != 65536 {
		t.Errorf("TotalMemoryMB = %d, want 65536 (the newer reading)", got.TotalMemoryMB)
	}
	if got.DockerVersion != "25.0.1" {
		t.Errorf("DockerVersion = %q, want %q", got.DockerVersion, "25.0.1")
	}

	_ = first
}

// TestServerCapabilityRepository_GetIsServerScoped proves Get returns each
// server's own latest reading, not the globally newest one.
func TestServerCapabilityRepository_GetIsServerScoped(t *testing.T) {
	f := newCapabilityHarness(t)
	ctx := context.Background()

	f.insert(t, f.serverA, 111, 111, 1, 1, "a-old")
	// Sleep so server B's reading is unambiguously the newer of the two --
	// otherwise a global-newest implementation could pass by accident.
	time.Sleep(10 * time.Millisecond)
	f.insert(t, f.serverB, 222, 222, 2, 2, "b-new")

	gotA, err := f.repo.Get(ctx, f.serverA)
	if err != nil {
		t.Fatalf("Get(serverA): %v", err)
	}
	if gotA.ServerID != f.serverA {
		t.Errorf("Get(serverA) returned a reading for server %d", gotA.ServerID)
	}
	if gotA.DockerVersion != "a-old" {
		t.Errorf("Get(serverA) DockerVersion = %q, want %q", gotA.DockerVersion, "a-old")
	}

	gotB, err := f.repo.Get(ctx, f.serverB)
	if err != nil {
		t.Fatalf("Get(serverB): %v", err)
	}
	if gotB.ServerID != f.serverB {
		t.Errorf("Get(serverB) returned a reading for server %d", gotB.ServerID)
	}
	if gotB.DockerVersion != "b-new" {
		t.Errorf("Get(serverB) DockerVersion = %q, want %q", gotB.DockerVersion, "b-new")
	}
}

// TestServerCapabilityRepository_InsertUnknownServerFails proves the FK from
// server_capabilities.server_id to servers.server_id is enforced.
func TestServerCapabilityRepository_InsertUnknownServerFails(t *testing.T) {
	f := newCapabilityHarness(t)

	err := f.repo.Insert(context.Background(), &manman.ServerCapability{
		ServerID:               999999,
		TotalMemoryMB:          1024,
		AvailableMemoryMB:      512,
		CPUCores:               2,
		AvailableCPUMillicores: 1000,
		DockerVersion:          "24.0.7",
	})
	if err == nil {
		t.Fatal("Insert for a nonexistent server succeeded, want an FK violation")
	}
}
