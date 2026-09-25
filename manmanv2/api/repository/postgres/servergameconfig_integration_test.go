//go:build integration

// Real-Postgres coverage for ServerGameConfigRepository -- the join row
// binding a game config to a specific server. Replays the real shipped
// migrations via //manmanv2/migrate/schema rather than hand-written DDL.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //manmanv2/api/repository/postgres:servergameconfig_integration_test --test_output=all
package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	manman "github.com/whale-net/everything/manmanv2/models"
)

// portBindings mirrors handlers.portBindingsToJSONB's on-disk shape:
// {"<containerPort>/<PROTOCOL>": hostPort}.
func portBindings(bindings map[string]float64) manman.JSONB {
	out := manman.JSONB{}
	for k, v := range bindings {
		out[k] = v
	}
	return out
}

func seedSgcServer(t *testing.T, pool *pgxpool.Pool, name string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO servers (name) VALUES ($1) RETURNING server_id`, name,
	).Scan(&id); err != nil {
		t.Fatalf("seed server %q: %v", name, err)
	}
	return id
}

// sgcFixture holds the servers and game configs the SGC tests need.
// server_game_configs FKs to both servers and game_configs, and carries
// UNIQUE(server_id, game_config_id) -- so putting two deployments on one
// server requires two different game configs, not two rows on one config.
type sgcFixture struct {
	repo             *ServerGameConfigRepository
	pool             *pgxpool.Pool
	serverA, serverB int64
	configA, configB int64
}

func newSgcHarness(t *testing.T) sgcFixture {
	t.Helper()
	pool := newMigratedPool(t)
	ctx := context.Background()

	serverA := seedSgcServer(t, pool, "sgc-server-a")
	serverB := seedSgcServer(t, pool, "sgc-server-b")

	game, err := NewGameRepository(pool).Create(ctx,
		&manman.Game{Name: "sgc-game", Metadata: manman.JSONB{}})
	if err != nil {
		t.Fatalf("seed game: %v", err)
	}
	configs := NewGameConfigRepository(pool)
	var configA, configB int64
	for i, name := range []string{"sgc-config-a", "sgc-config-b"} {
		cfg, err := configs.Create(ctx, &manman.GameConfig{
			GameID: game.GameID,
			Name:   name,
			Image:  "example:latest",
		})
		if err != nil {
			t.Fatalf("seed game config %d: %v", i, err)
		}
		if i == 0 {
			configA = cfg.ConfigID
		} else {
			configB = cfg.ConfigID
		}
	}

	return sgcFixture{
		repo:    NewServerGameConfigRepository(pool),
		pool:    pool,
		serverA: serverA,
		serverB: serverB,
		configA: configA,
		configB: configB,
	}
}

func (f sgcFixture) seed(t *testing.T, serverID, configID int64, name string) *manman.ServerGameConfig {
	t.Helper()
	sgc, err := f.repo.Create(context.Background(), &manman.ServerGameConfig{
		ServerID:     serverID,
		GameConfigID: configID,
		PortBindings: portBindings(map[string]float64{"25565/TCP": 25565}),
		Status:       "pending",
	})
	if err != nil {
		t.Fatalf("seed sgc %q: %v", name, err)
	}
	return sgc
}

// TestServerGameConfigRepository_CreateGetRoundTrip proves Create assigns an
// sgc_id and Get returns every stored field, including the JSONB port bindings
// in the "<containerPort>/<PROTOCOL>": hostPort shape.
func TestServerGameConfigRepository_CreateGetRoundTrip(t *testing.T) {
	f := newSgcHarness(t)
	ctx := context.Background()

	created, err := f.repo.Create(ctx, &manman.ServerGameConfig{
		ServerID:     f.serverA,
		GameConfigID: f.configA,
		PortBindings: portBindings(map[string]float64{"25565/TCP": 25565, "19132/UDP": 19132}),
		Status:       "deploying",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.SGCID == 0 {
		t.Fatal("Create did not populate SGCID")
	}

	got, err := f.repo.Get(ctx, created.SGCID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ServerID != f.serverA {
		t.Errorf("ServerID = %d, want %d", got.ServerID, f.serverA)
	}
	if got.GameConfigID != f.configA {
		t.Errorf("GameConfigID = %d, want %d", got.GameConfigID, f.configA)
	}
	if got.Status != "deploying" {
		t.Errorf("Status = %q, want %q", got.Status, "deploying")
	}
	if got.PortBindings["25565/TCP"] != float64(25565) {
		t.Errorf("PortBindings[25565/TCP] = %v, want 25565", got.PortBindings["25565/TCP"])
	}
	if got.PortBindings["19132/UDP"] != float64(19132) {
		t.Errorf("PortBindings[19132/UDP] = %v, want 19132", got.PortBindings["19132/UDP"])
	}
}

// TestServerGameConfigRepository_GetUnknownIDErrors proves Get on a missing
// sgc_id errors rather than returning a zero-valued SGC.
func TestServerGameConfigRepository_GetUnknownIDErrors(t *testing.T) {
	f := newSgcHarness(t)

	if _, err := f.repo.Get(context.Background(), 999999); err == nil {
		t.Fatal("Get on an unknown sgc_id returned no error")
	}
}

// TestServerGameConfigRepository_ListFiltersByServer proves the optional
// serverID argument actually filters -- a missing WHERE here would show one
// server's deployments on another's page.
func TestServerGameConfigRepository_ListFiltersByServer(t *testing.T) {
	f := newSgcHarness(t)
	ctx := context.Background()

	f.seed(t, f.serverA, f.configA, "a1")
	// A second deployment on the same server needs a second game config --
	// (server_id, game_config_id) is unique.
	f.seed(t, f.serverA, f.configB, "a2")
	bSgc := f.seed(t, f.serverB, f.configA, "b1")

	all, err := f.repo.List(ctx, nil, 50, 0)
	if err != nil {
		t.Fatalf("List(nil): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("List(nil) returned %d rows, want 3", len(all))
	}

	filtered, err := f.repo.List(ctx, &f.serverA, 50, 0)
	if err != nil {
		t.Fatalf("List(serverA): %v", err)
	}
	if len(filtered) != 2 {
		t.Fatalf("List(serverA) returned %d rows, want 2", len(filtered))
	}
	for _, s := range filtered {
		if s.ServerID != f.serverA {
			t.Errorf("List(serverA) leaked sgc %d owned by server %d", s.SGCID, s.ServerID)
		}
	}

	other, err := f.repo.List(ctx, &f.serverB, 50, 0)
	if err != nil {
		t.Fatalf("List(serverB): %v", err)
	}
	if len(other) != 1 || other[0].SGCID != bSgc.SGCID {
		t.Errorf("List(serverB) = %v, want just sgc %d", sgcIDs(other), bSgc.SGCID)
	}
}

// TestServerGameConfigRepository_UpdateKeepsForeignKeys proves Update
// rewrites only port_bindings and status -- the SQL's SET list omits server_id
// and game_config_id, so a deployment can be re-statused but not re-homed.
func TestServerGameConfigRepository_UpdateKeepsForeignKeys(t *testing.T) {
	f := newSgcHarness(t)
	ctx := context.Background()

	sgc := f.seed(t, f.serverA, f.configA, "before")

	sgc.PortBindings = portBindings(map[string]float64{"27015/UDP": 27015})
	sgc.Status = "running"
	// Both must be ignored by Update.
	sgc.ServerID = f.serverB
	sgc.GameConfigID = f.configA + 999

	if err := f.repo.Update(ctx, sgc); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := f.repo.Get(ctx, sgc.SGCID)
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	if got.ServerID != f.serverA {
		t.Errorf("ServerID = %d, want %d (Update must not re-home)", got.ServerID, f.serverA)
	}
	if got.GameConfigID != f.configA {
		t.Errorf("GameConfigID = %d, want %d (Update must not re-point)", got.GameConfigID, f.configA)
	}
	if got.Status != "running" {
		t.Errorf("Status = %q, want %q", got.Status, "running")
	}
	if got.PortBindings["27015/UDP"] != float64(27015) {
		t.Errorf("PortBindings[27015/UDP] = %v, want 27015", got.PortBindings["27015/UDP"])
	}
}

// TestServerGameConfigRepository_DeleteRemovesRow proves Delete is a hard
// delete and that a follow-up Get errors.
func TestServerGameConfigRepository_DeleteRemovesRow(t *testing.T) {
	f := newSgcHarness(t)
	ctx := context.Background()

	sgc := f.seed(t, f.serverA, f.configA, "doomed")

	if err := f.repo.Delete(ctx, sgc.SGCID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := f.repo.Get(ctx, sgc.SGCID); err == nil {
		t.Fatal("Get succeeded after Delete, want an error")
	}
}

// TestServerGameConfigRepository_CreateUnknownServerFails proves the real
// migration's FK from server_game_configs.server_id to servers.server_id is
// enforced.
func TestServerGameConfigRepository_CreateUnknownServerFails(t *testing.T) {
	f := newSgcHarness(t)

	_, err := f.repo.Create(context.Background(), &manman.ServerGameConfig{
		ServerID:     999999,
		GameConfigID: f.configA,
		Status:       "pending",
	})
	if err == nil {
		t.Fatal("Create with a nonexistent server_id succeeded, want an FK violation")
	}
}

// TestServerGameConfigRepository_CreateDuplicatePairFails proves the real
// migration's UNIQUE(server_id, game_config_id) is enforced: the same config
// cannot be deployed twice on one server. Caught by running against the real
// migrations -- a hand-written test DDL would have to remember this
// constraint, and would otherwise let a double-deploy through.
func TestServerGameConfigRepository_CreateDuplicatePairFails(t *testing.T) {
	f := newSgcHarness(t)

	f.seed(t, f.serverA, f.configA, "first")

	_, err := f.repo.Create(context.Background(), &manman.ServerGameConfig{
		ServerID:     f.serverA,
		GameConfigID: f.configA,
		Status:       "pending",
	})
	if err == nil {
		t.Fatal("a second SGC for the same (server_id, game_config_id) succeeded, want a unique violation")
	}
}

func sgcIDs(sgcs []*manman.ServerGameConfig) []int64 {
	ids := make([]int64, len(sgcs))
	for i, s := range sgcs {
		ids[i] = s.SGCID
	}
	return ids
}
