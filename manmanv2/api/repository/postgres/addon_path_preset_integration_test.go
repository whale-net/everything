//go:build integration

// Real-Postgres coverage for AddonPathPresetRepository. Replays the real
// shipped migrations via //manmanv2/migrate/schema rather than hand-written
// DDL.
//
// This repository differs from its CRUD siblings in the postgres package:
// Update and Delete check RowsAffected and return "preset not found" for a
// missing id, where game.go/gameconfig.go silently succeed. The
// MissingRowErrors tests below pin that difference, and the game test pins
// the opposite behaviour, so neither is changed by accident.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //manmanv2/api/repository/postgres:addon_path_preset_integration_test --test_output=all
package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	manman "github.com/whale-net/everything/manmanv2/models"
)

type presetFixture struct {
	repo  *AddonPathPresetRepository
	pool  *pgxpool.Pool
	gameA int64
	gameB int64
}

func newPresetHarness(t *testing.T) presetFixture {
	t.Helper()
	pool := newMigratedPool(t)
	ctx := context.Background()

	games := NewGameRepository(pool)
	first, err := games.Create(ctx, &manman.Game{Name: "preset-game-a", Metadata: manman.JSONB{}})
	if err != nil {
		t.Fatalf("seed game a: %v", err)
	}
	second, err := games.Create(ctx, &manman.Game{Name: "preset-game-b", Metadata: manman.JSONB{}})
	if err != nil {
		t.Fatalf("seed game b: %v", err)
	}

	return presetFixture{
		repo:  NewAddonPathPresetRepository(pool),
		pool:  pool,
		gameA: first.GameID,
		gameB: second.GameID,
	}
}

func (f presetFixture) seed(t *testing.T, gameID int64, name string) *manman.GameAddonPathPreset {
	t.Helper()
	p, err := f.repo.Create(context.Background(), &manman.GameAddonPathPreset{
		GameID:           gameID,
		Name:             name,
		Description:      strPtr(name + " description"),
		InstallationPath: "/steamapps/common/" + name,
	})
	if err != nil {
		t.Fatalf("seed preset %q: %v", name, err)
	}
	return p
}

// TestAddonPathPresetRepository_CreateGetRoundTrip proves Create assigns a
// preset_id and server-side created_at, and Get returns every stored field.
func TestAddonPathPresetRepository_CreateGetRoundTrip(t *testing.T) {
	f := newPresetHarness(t)
	ctx := context.Background()

	created, err := f.repo.Create(ctx, &manman.GameAddonPathPreset{
		GameID:           f.gameA,
		Name:             "valheim",
		Description:      strPtr("Valheim install root"),
		InstallationPath: "/steamapps/common/Valheim",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.PresetID == 0 {
		t.Fatal("Create did not populate PresetID")
	}
	if created.CreatedAt.IsZero() {
		t.Error("Create did not populate CreatedAt from the RETURNING clause")
	}

	got, err := f.repo.Get(ctx, created.PresetID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.GameID != f.gameA {
		t.Errorf("GameID = %d, want %d", got.GameID, f.gameA)
	}
	if got.Name != "valheim" {
		t.Errorf("Name = %q, want %q", got.Name, "valheim")
	}
	if got.Description == nil || *got.Description != "Valheim install root" {
		t.Errorf("Description = %v, want %q", got.Description, "Valheim install root")
	}
	if got.InstallationPath != "/steamapps/common/Valheim" {
		t.Errorf("InstallationPath = %q, want %q", got.InstallationPath, "/steamapps/common/Valheim")
	}
}

// TestAddonPathPresetRepository_GetUnknownIDErrors proves Get on a missing
// preset_id errors (wrapped by the repository's "failed to get" message).
func TestAddonPathPresetRepository_GetUnknownIDErrors(t *testing.T) {
	f := newPresetHarness(t)

	if _, err := f.repo.Get(context.Background(), 999999); err == nil {
		t.Fatal("Get on an unknown preset_id returned no error")
	}
}

// TestAddonPathPresetRepository_ListByGameOrdersByName proves ListByGame
// filters to one game and sorts by name -- the SQL's ORDER BY is name, not
// preset_id, so this pins that.
func TestAddonPathPresetRepository_ListByGameOrdersByName(t *testing.T) {
	f := newPresetHarness(t)
	ctx := context.Background()

	// Insert out of alphabetical order to prove the ORDER BY does the work.
	third := f.seed(t, f.gameA, "zulu")
	first := f.seed(t, f.gameA, "alpha")
	second := f.seed(t, f.gameA, "mike")
	f.seed(t, f.gameB, "other-game")

	listed, err := f.repo.ListByGame(ctx, f.gameA)
	if err != nil {
		t.Fatalf("ListByGame: %v", err)
	}
	if len(listed) != 3 {
		t.Fatalf("ListByGame returned %d rows, want 3", len(listed))
	}

	want := []int64{first.PresetID, second.PresetID, third.PresetID}
	got := presetIDs(listed)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ListByGame order = %v, want %v (ordered by name)", got, want)
		}
	}
	for _, p := range listed {
		if p.GameID != f.gameA {
			t.Errorf("preset %d belongs to game %d, want %d", p.PresetID, p.GameID, f.gameA)
		}
	}
}

// TestAddonPathPresetRepository_UpdateMutatesColumns proves Update rewrites
// name, description, and installation_path but not game_id.
func TestAddonPathPresetRepository_UpdateMutatesColumns(t *testing.T) {
	f := newPresetHarness(t)
	ctx := context.Background()

	preset := f.seed(t, f.gameA, "before")

	preset.Name = "after"
	preset.Description = strPtr("updated")
	preset.InstallationPath = "/steamapps/common/After"
	preset.GameID = f.gameB // must be ignored

	if err := f.repo.Update(ctx, preset); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := f.repo.Get(ctx, preset.PresetID)
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	if got.GameID != f.gameA {
		t.Errorf("GameID = %d, want %d (Update must not re-parent)", got.GameID, f.gameA)
	}
	if got.Name != "after" {
		t.Errorf("Name = %q, want %q", got.Name, "after")
	}
	if got.Description == nil || *got.Description != "updated" {
		t.Errorf("Description = %v, want %q", got.Description, "updated")
	}
	if got.InstallationPath != "/steamapps/common/After" {
		t.Errorf("InstallationPath = %q, want %q", got.InstallationPath, "/steamapps/common/After")
	}
}

// TestAddonPathPresetRepository_DeleteRemovesRow proves Delete is a hard
// delete and that a follow-up Get errors.
func TestAddonPathPresetRepository_DeleteRemovesRow(t *testing.T) {
	f := newPresetHarness(t)
	ctx := context.Background()

	preset := f.seed(t, f.gameA, "doomed")

	if err := f.repo.Delete(ctx, preset.PresetID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := f.repo.Get(ctx, preset.PresetID); err == nil {
		t.Fatal("Get succeeded after Delete, want an error")
	}
}

// TestAddonPathPresetRepository_MissingRowErrorsOnUpdateAndDelete pins the
// RowsAffected check that distinguishes this repository from the other CRUD
// repositories in the package.
func TestAddonPathPresetRepository_MissingRowErrorsOnUpdateAndDelete(t *testing.T) {
	f := newPresetHarness(t)
	ctx := context.Background()

	t.Run("Update", func(t *testing.T) {
		err := f.repo.Update(ctx, &manman.GameAddonPathPreset{
			PresetID:         999999,
			GameID:           f.gameA,
			Name:             "ghost",
			InstallationPath: "/ghost",
		})
		if err == nil {
			t.Error("Update on an unknown preset_id returned no error, want \"preset not found\"")
		}
	})

	t.Run("Delete", func(t *testing.T) {
		if err := f.repo.Delete(ctx, 999999); err == nil {
			t.Error("Delete on an unknown preset_id returned no error, want \"preset not found\"")
		}
	})
}

// TestAddonPathPresetRepository_CreateUnknownGameFails proves the real
// migration's FK from game_addon_path_presets.game_id to games.game_id is
// enforced.
func TestAddonPathPresetRepository_CreateUnknownGameFails(t *testing.T) {
	f := newPresetHarness(t)

	_, err := f.repo.Create(context.Background(), &manman.GameAddonPathPreset{
		GameID:           999999,
		Name:             "orphan",
		InstallationPath: "/orphan",
	})
	if err == nil {
		t.Fatal("Create with a nonexistent game_id succeeded, want an FK violation")
	}
}

func presetIDs(presets []*manman.GameAddonPathPreset) []int64 {
	ids := make([]int64, len(presets))
	for i, p := range presets {
		ids[i] = p.PresetID
	}
	return ids
}
