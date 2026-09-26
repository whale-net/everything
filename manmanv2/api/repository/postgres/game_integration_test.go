//go:build integration

// Real-Postgres coverage for GameRepository. The sibling tests in this
// package were written before //manmanv2/migrate/schema existed and hand-write
// a const DDL string; this one replays the real shipped migrations, so it also
// catches drift between game.go's SQL and the actual games table.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //manmanv2/api/repository/postgres:game_integration_test --test_output=all
package postgres

import (
	"context"
	"testing"

	manman "github.com/whale-net/everything/manmanv2/models"
)

func newGameHarness(t *testing.T) (*GameRepository, *manman.Game) {
	t.Helper()
	pool := newMigratedPool(t)
	repo := NewGameRepository(pool)

	// Every game_config/volumes row hangs off a game, and List/Update/Delete
	// assertions below need a stable parent, so seed one here.
	game, err := repo.Create(context.Background(), &manman.Game{
		Name:       "fixture-game",
		SteamAppID: strPtr("730"),
		Metadata:   manman.JSONB{"genre": "sandbox"},
	})
	if err != nil {
		t.Fatalf("seed fixture game: %v", err)
	}
	return repo, game
}

// TestGameRepository_CreateGetRoundTrip proves Create assigns a game_id and
// Get returns every stored field, including the nullable steam_app_id and the
// JSONB metadata.
func TestGameRepository_CreateGetRoundTrip(t *testing.T) {
	repo, _ := newGameHarness(t)
	ctx := context.Background()

	created, err := repo.Create(ctx, &manman.Game{
		Name:       "Valheim",
		SteamAppID: strPtr("892970"),
		Metadata:   manman.JSONB{"genre": "survival", "max_players": float64(10)},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.GameID == 0 {
		t.Fatal("Create did not populate GameID")
	}

	got, err := repo.Get(ctx, created.GameID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "Valheim" {
		t.Errorf("Name = %q, want %q", got.Name, "Valheim")
	}
	if got.SteamAppID == nil || *got.SteamAppID != "892970" {
		t.Errorf("SteamAppID = %v, want %q", got.SteamAppID, "892970")
	}
	// JSON round-trips numbers as float64, so compare on the decoded value.
	if got.Metadata["genre"] != "survival" {
		t.Errorf("Metadata[genre] = %v, want %q", got.Metadata["genre"], "survival")
	}
	if got.Metadata["max_players"] != float64(10) {
		t.Errorf("Metadata[max_players] = %v, want 10", got.Metadata["max_players"])
	}
}

// TestGameRepository_GetNilSteamAppID proves a game created without a Steam ID
// round-trips as NULL rather than an empty string -- the column is nullable
// and Get scans it into a *string.
func TestGameRepository_GetNilSteamAppID(t *testing.T) {
	repo, _ := newGameHarness(t)
	ctx := context.Background()

	created, err := repo.Create(ctx, &manman.Game{Name: "NoSteam", Metadata: manman.JSONB{}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.Get(ctx, created.GameID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SteamAppID != nil {
		t.Errorf("SteamAppID = %v, want nil", *got.SteamAppID)
	}
}

// TestGameRepository_GetUnknownIDErrors proves Get on a missing game_id
// returns an error (pgx.ErrNoRows) rather than a zero-valued Game.
func TestGameRepository_GetUnknownIDErrors(t *testing.T) {
	repo, _ := newGameHarness(t)

	if _, err := repo.Get(context.Background(), 999999); err == nil {
		t.Fatal("Get on an unknown game_id returned no error")
	}
}

// TestGameRepository_ListPaginates proves List orders by game_id and honours
// limit/offset, which is how the UI pages through the catalogue.
func TestGameRepository_ListPaginates(t *testing.T) {
	repo, fixture := newGameHarness(t)
	ctx := context.Background()

	var wantIDs []int64
	for _, name := range []string{"alpha", "beta", "gamma"} {
		g, err := repo.Create(ctx, &manman.Game{Name: name, Metadata: manman.JSONB{}})
		if err != nil {
			t.Fatalf("Create %q: %v", name, err)
		}
		wantIDs = append(wantIDs, g.GameID)
	}

	all, err := repo.List(ctx, 50, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// The fixture game plus the three seeded above.
	if len(all) != 4 {
		t.Fatalf("List returned %d rows, want 4", len(all))
	}
	if all[0].GameID != fixture.GameID {
		t.Errorf("List[0].GameID = %d, want %d (ordered by game_id)", all[0].GameID, fixture.GameID)
	}

	// limit=2, offset=1 should return exactly the middle two.
	page, err := repo.List(ctx, 2, 1)
	if err != nil {
		t.Fatalf("List(limit=2, offset=1): %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("paged List returned %d rows, want 2", len(page))
	}
	if page[0].GameID != wantIDs[0] || page[1].GameID != wantIDs[1] {
		t.Errorf("paged List = [%d %d], want [%d %d]", page[0].GameID, page[1].GameID, wantIDs[0], wantIDs[1])
	}

	// A non-positive limit falls back to the package default of 50.
	fallback, err := repo.List(ctx, 0, 0)
	if err != nil {
		t.Fatalf("List(limit=0): %v", err)
	}
	if len(fallback) != 4 {
		t.Errorf("List(limit=0) returned %d rows, want 4 (default limit 50)", len(fallback))
	}
}

// TestGameRepository_UpdateMutatesAllColumns proves Update rewrites name,
// steam_app_id, and metadata together, and leaves the row's identity alone.
func TestGameRepository_UpdateMutatesAllColumns(t *testing.T) {
	repo, _ := newGameHarness(t)
	ctx := context.Background()

	game, err := repo.Create(ctx, &manman.Game{
		Name:       "before",
		SteamAppID: strPtr("1"),
		Metadata:   manman.JSONB{"v": "old"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	game.Name = "after"
	game.SteamAppID = strPtr("2")
	game.Metadata = manman.JSONB{"v": "new"}
	if err := repo.Update(ctx, game); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := repo.Get(ctx, game.GameID)
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	if got.Name != "after" {
		t.Errorf("Name = %q, want %q", got.Name, "after")
	}
	if got.SteamAppID == nil || *got.SteamAppID != "2" {
		t.Errorf("SteamAppID = %v, want %q", got.SteamAppID, "2")
	}
	if got.Metadata["v"] != "new" {
		t.Errorf("Metadata[v] = %v, want %q", got.Metadata["v"], "new")
	}
}

// TestGameRepository_DeleteRemovesRow proves Delete is a hard delete and that
// a follow-up Get errors.
func TestGameRepository_DeleteRemovesRow(t *testing.T) {
	repo, _ := newGameHarness(t)
	ctx := context.Background()

	game, err := repo.Create(ctx, &manman.Game{Name: "doomed", Metadata: manman.JSONB{}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := repo.Delete(ctx, game.GameID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.Get(ctx, game.GameID); err == nil {
		t.Fatal("Get succeeded after Delete, want an error")
	}
}

// TestGameRepository_UpdateUnknownIDIsSilentNoOp documents that Update does
// not check RowsAffected, unlike AddonPathPresetRepository -- an unknown
// game_id succeeds and writes nothing. Worth pinning so a future change to
// either behaviour is a deliberate edit here.
func TestGameRepository_UpdateUnknownIDIsSilentNoOp(t *testing.T) {
	repo, _ := newGameHarness(t)

	err := repo.Update(context.Background(), &manman.Game{
		GameID:   999999,
		Name:     "ghost",
		Metadata: manman.JSONB{},
	})
	if err != nil {
		t.Fatalf("Update on an unknown game_id returned error %v, want nil (no RowsAffected check)", err)
	}
}
