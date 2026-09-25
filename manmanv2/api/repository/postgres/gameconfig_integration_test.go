//go:build integration

// Real-Postgres coverage for GameConfigRepository, replaying the real shipped
// migrations via //manmanv2/migrate/schema rather than hand-written DDL.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //manmanv2/api/repository/postgres:gameconfig_integration_test --test_output=all
package postgres

import (
	"context"
	"testing"

	manman "github.com/whale-net/everything/manmanv2/models"
)

func newGameConfigHarness(t *testing.T) (*GameConfigRepository, *manman.Game, *manman.Game) {
	t.Helper()
	pool := newMigratedPool(t)
	games := NewGameRepository(pool)

	// game_configs.game_id is a FK, and List's optional gameID filter needs
	// two distinct parents to tell "filtered" from "unfiltered" apart.
	first := &manman.Game{Name: "game-a", Metadata: manman.JSONB{}}
	if _, err := games.Create(context.Background(), first); err != nil {
		t.Fatalf("seed first game: %v", err)
	}
	second := &manman.Game{Name: "game-b", Metadata: manman.JSONB{}}
	if _, err := games.Create(context.Background(), second); err != nil {
		t.Fatalf("seed second game: %v", err)
	}

	return NewGameConfigRepository(pool), first, second
}

func seedConfig(t *testing.T, repo *GameConfigRepository, gameID int64, name string) *manman.GameConfig {
	t.Helper()
	cfg, err := repo.Create(context.Background(), &manman.GameConfig{
		GameID:       gameID,
		Name:         name,
		Image:        "ghcr.io/example/" + name + ":latest",
		ArgsTemplate: strPtr("--seeded"),
		EnvTemplate:  manman.JSONB{"MODE": "prod"},
		Entrypoint:   jsonStrings("/bin/sh", "-c"),
		Command:      jsonStrings("run.sh"),
	})
	if err != nil {
		t.Fatalf("seed config %q: %v", name, err)
	}
	return cfg
}

// TestGameConfigRepository_CreateGetRoundTrip proves Create assigns a
// config_id and Get returns every stored field -- notably that the []string
// columns entrypoint and command survive the JSONB round-trip in the
// {"items": [...]} shape the handlers write.
func TestGameConfigRepository_CreateGetRoundTrip(t *testing.T) {
	repo, gameA, _ := newGameConfigHarness(t)
	ctx := context.Background()

	created, err := repo.Create(ctx, &manman.GameConfig{
		GameID:       gameA.GameID,
		Name:         "vanilla",
		Image:        "itzg/mc-server:latest",
		ArgsTemplate: strPtr("--memory 4G"),
		EnvTemplate:  manman.JSONB{"EULA": "TRUE"},
		Entrypoint:   jsonStrings("/init"),
		Command:      jsonStrings("server.jar", "--nogui"),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ConfigID == 0 {
		t.Fatal("Create did not populate ConfigID")
	}

	got, err := repo.Get(ctx, created.ConfigID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.GameID != gameA.GameID {
		t.Errorf("GameID = %d, want %d", got.GameID, gameA.GameID)
	}
	if got.Name != "vanilla" {
		t.Errorf("Name = %q, want %q", got.Name, "vanilla")
	}
	if got.Image != "itzg/mc-server:latest" {
		t.Errorf("Image = %q, want %q", got.Image, "itzg/mc-server:latest")
	}
	if got.ArgsTemplate == nil || *got.ArgsTemplate != "--memory 4G" {
		t.Errorf("ArgsTemplate = %v, want %q", got.ArgsTemplate, "--memory 4G")
	}
	if got.EnvTemplate["EULA"] != "TRUE" {
		t.Errorf("EnvTemplate[EULA] = %v, want %q", got.EnvTemplate["EULA"], "TRUE")
	}
	if items, ok := got.Entrypoint["items"].([]interface{}); !ok || len(items) != 1 || items[0] != "/init" {
		t.Errorf("Entrypoint[items] = %v, want [/init]", got.Entrypoint["items"])
	}
	if items, ok := got.Command["items"].([]interface{}); !ok || len(items) != 2 {
		t.Errorf("Command[items] = %v, want 2 items", got.Command["items"])
	}
}

// TestGameConfigRepository_GetUnknownIDErrors proves Get on a missing
// config_id errors rather than returning a zero-valued config.
func TestGameConfigRepository_GetUnknownIDErrors(t *testing.T) {
	repo, _, _ := newGameConfigHarness(t)

	if _, err := repo.Get(context.Background(), 999999); err == nil {
		t.Fatal("Get on an unknown config_id returned no error")
	}
}

// TestGameConfigRepository_ListFiltersByGame proves the optional gameID
// argument actually filters -- a wrong WHERE here would silently show one
// game's configs on another's page.
func TestGameConfigRepository_ListFiltersByGame(t *testing.T) {
	repo, gameA, gameB := newGameConfigHarness(t)
	ctx := context.Background()

	seedConfig(t, repo, gameA.GameID, "a1")
	seedConfig(t, repo, gameA.GameID, "a2")
	bConfig := seedConfig(t, repo, gameB.GameID, "b1")

	all, err := repo.List(ctx, nil, 50, 0)
	if err != nil {
		t.Fatalf("List(nil): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("List(nil) returned %d rows, want 3", len(all))
	}

	filtered, err := repo.List(ctx, &gameA.GameID, 50, 0)
	if err != nil {
		t.Fatalf("List(gameA): %v", err)
	}
	if len(filtered) != 2 {
		t.Fatalf("List(gameA) returned %d rows, want 2", len(filtered))
	}
	for _, c := range filtered {
		if c.GameID != gameA.GameID {
			t.Errorf("List(gameA) leaked config %q owned by game %d", c.Name, c.GameID)
		}
	}

	other, err := repo.List(ctx, &gameB.GameID, 50, 0)
	if err != nil {
		t.Fatalf("List(gameB): %v", err)
	}
	if len(other) != 1 || other[0].ConfigID != bConfig.ConfigID {
		t.Errorf("List(gameB) = %v, want just config %d", configIDs(other), bConfig.ConfigID)
	}
}

// TestGameConfigRepository_UpdateKeepsGameID proves Update rewrites the
// mutable columns but not game_id, which the SQL deliberately omits from its
// SET list -- re-parenting a config is Create's job, not Update's.
func TestGameConfigRepository_UpdateKeepsGameID(t *testing.T) {
	repo, gameA, gameB := newGameConfigHarness(t)
	ctx := context.Background()

	cfg := seedConfig(t, repo, gameA.GameID, "before")

	cfg.Name = "after"
	cfg.Image = "itzg/mc-server:1.20"
	cfg.ArgsTemplate = strPtr("--memory 8G")
	cfg.EnvTemplate = manman.JSONB{"EULA": "FALSE"}
	cfg.Entrypoint = jsonStrings("/bin/sh")
	cfg.Command = jsonStrings("other.sh")
	// Try to re-parent; Update must ignore it.
	cfg.GameID = gameB.GameID

	if err := repo.Update(ctx, cfg); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := repo.Get(ctx, cfg.ConfigID)
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	if got.GameID != gameA.GameID {
		t.Errorf("GameID = %d, want %d (Update must not re-parent)", got.GameID, gameA.GameID)
	}
	if got.Name != "after" || got.Image != "itzg/mc-server:1.20" {
		t.Errorf("Name/Image = %q/%q, want %q/%q", got.Name, got.Image, "after", "itzg/mc-server:1.20")
	}
	if got.ArgsTemplate == nil || *got.ArgsTemplate != "--memory 8G" {
		t.Errorf("ArgsTemplate = %v, want %q", got.ArgsTemplate, "--memory 8G")
	}
	if got.EnvTemplate["EULA"] != "FALSE" {
		t.Errorf("EnvTemplate[EULA] = %v, want %q", got.EnvTemplate["EULA"], "FALSE")
	}
}

// TestGameConfigRepository_DeleteRemovesRow proves Delete is a hard delete and
// that a follow-up Get errors.
func TestGameConfigRepository_DeleteRemovesRow(t *testing.T) {
	repo, gameA, _ := newGameConfigHarness(t)
	ctx := context.Background()

	cfg := seedConfig(t, repo, gameA.GameID, "doomed")

	if err := repo.Delete(ctx, cfg.ConfigID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.Get(ctx, cfg.ConfigID); err == nil {
		t.Fatal("Get succeeded after Delete, want an error")
	}
}

// TestGameConfigRepository_CreateUnknownGameFails proves the real migration's
// FK from game_configs.game_id to games.game_id is enforced -- a constraint a
// hand-written test DDL would have to remember to include.
func TestGameConfigRepository_CreateUnknownGameFails(t *testing.T) {
	repo, _, _ := newGameConfigHarness(t)

	_, err := repo.Create(context.Background(), &manman.GameConfig{
		GameID: 999999,
		Name:   "orphan",
		Image:  "example:latest",
	})
	if err == nil {
		t.Fatal("Create with a nonexistent game_id succeeded, want an FK violation")
	}
}

func configIDs(configs []*manman.GameConfig) []int64 {
	ids := make([]int64, len(configs))
	for i, c := range configs {
		ids[i] = c.ConfigID
	}
	return ids
}
